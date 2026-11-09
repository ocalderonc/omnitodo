// Small glue for HTMX: the edit dialog and the phone tabs. No inline scripts
// anywhere, so the CSP can stay at script-src 'self'.
(() => {
  const modal = () => document.getElementById("modal");

  // A form loaded into the dialog opens it.
  document.addEventListener("htmx:afterSwap", (e) => {
    const m = modal();
    if (e.detail.target.id === "modal-body" && m && !m.open) m.showModal();
  });

  // The server sends HX-Trigger: closeModal after a successful save.
  document.addEventListener("closeModal", () => {
    const m = modal();
    if (m && m.open) m.close();
  });

  document.addEventListener("click", (e) => {
    if (e.target.closest("[data-close]")) e.target.closest("dialog")?.close();
    const tab = e.target.closest("[data-tab-btn]");
    if (tab) document.getElementById("main").dataset.tab = tab.dataset.tabBtn;
  });

  // Installable app: the service worker keeps the shell and the last copy of
  // each day for offline viewing (see internal/server/pwa.go).
  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.register("/sw.js").catch(() => {});
  }
  // The login page means no session: drop cached days so a logged-out
  // device does not keep showing them offline.
  if (location.pathname === "/login" && "caches" in window) {
    caches.delete("pages");
  }
  const markOffline = () => document.body.classList.toggle("offline", !navigator.onLine);
  addEventListener("online", markOffline);
  addEventListener("offline", markOffline);
  document.addEventListener("DOMContentLoaded", markOffline);

  // --- voice entry ---------------------------------------------------------
  // The phone records one sentence; the server transcribes and reads it and
  // answers with the New activity form filled in, which opens in the dialog.
  // Nothing is saved until Save is tapped. Ported from Moneta's recorder.
  const canRecord = () =>
    "MediaRecorder" in window && !!navigator.mediaDevices?.getUserMedia;
  const showMics = () => {
    if (canRecord()) document.querySelectorAll("[data-voice]").forEach((b) => (b.hidden = false));
  };
  document.addEventListener("DOMContentLoaded", showMics);
  document.addEventListener("htmx:afterSettle", showMics);

  const SPEAKING = 0.04; // RMS level that counts as someone talking
  const SILENCE_MS = 2500; // quiet after speech that ends the recording
  const FIRST_WORD_MS = 7000; // quiet from the start that gives up
  const CAP_MS = 30000; // one sentence, not a dictation
  let recording = null;

  const status = (text, isError) => {
    const el = document.querySelector("#day .voice-status");
    if (!el) return;
    el.textContent = text || "";
    el.hidden = !text;
    el.classList.toggle("error", !!isError);
  };
  const micButtons = (state) =>
    document.querySelectorAll("[data-voice]").forEach((b) => {
      b.classList.toggle("recording", state === "recording");
      b.classList.toggle("busy", state === "busy");
      b.disabled = state === "busy";
      b.setAttribute("aria-pressed", state === "recording" ? "true" : "false");
    });

  function preferredFormat() {
    for (const mimeType of ["audio/webm;codecs=opus", "audio/mp4", "audio/webm"]) {
      if (MediaRecorder.isTypeSupported?.(mimeType)) return { mimeType };
    }
    return undefined;
  }
  function filenameFor(type) {
    if (type.includes("mp4") || type.includes("aac")) return "voice.m4a";
    if (type.includes("ogg")) return "voice.ogg";
    return "voice.webm";
  }
  function levelMeter(stream) {
    const Ctx = window.AudioContext || window.webkitAudioContext;
    if (!Ctx) return null;
    try {
      const ctx = new Ctx();
      ctx.resume?.().catch(() => {}); // iOS may start it suspended
      const analyser = ctx.createAnalyser();
      analyser.fftSize = 512;
      ctx.createMediaStreamSource(stream).connect(analyser);
      const samples = new Float32Array(analyser.fftSize);
      return {
        level() {
          analyser.getFloatTimeDomainData(samples);
          let sum = 0;
          for (const s of samples) sum += s * s;
          return Math.sqrt(sum / samples.length);
        },
        close: () => ctx.close().catch(() => {}),
      };
    } catch {
      return null;
    }
  }

  async function startRecording() {
    let stream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    } catch (e) {
      const denied = e && (e.name === "NotAllowedError" || e.name === "SecurityError");
      status(denied ? "Microphone blocked: allow it for this site in the browser settings." : "Could not open the microphone.", true);
      return;
    }
    const recorder = new MediaRecorder(stream, preferredFormat());
    const chunks = [];
    recorder.ondataavailable = (e) => e.data.size > 0 && chunks.push(e.data);
    const meter = levelMeter(stream);
    const startedAt = performance.now();
    let lastSpoke = startedAt, spoke = false, cancelled = false;
    // A meter that never reads above exactly 0 is not working (a suspended
    // AudioContext; a real room is never digital silence). Then the level is
    // not trusted: the recording ends on a tap or at the cap, and is sent.
    let alive = false;
    const end = () => recorder.state !== "inactive" && recorder.stop();
    const watchdog = setInterval(() => {
      const now = performance.now();
      const level = meter ? meter.level() : 0;
      if (level > 0) alive = true;
      if (level > SPEAKING) { spoke = true; lastSpoke = now; }
      if (now - startedAt >= CAP_MS) end();
      else if (alive && spoke && now - lastSpoke >= SILENCE_MS) end();
      else if (alive && !spoke && now - startedAt >= FIRST_WORD_MS) end();
    }, 100);
    recorder.onstop = () => {
      clearInterval(watchdog);
      meter?.close();
      stream.getTracks().forEach((t) => t.stop());
      recording = null;
      // Silence is not sent: it costs a call and comes back as "Thank you."
      if (cancelled || (alive && !spoke) || chunks.length === 0) {
        micButtons("idle");
        status(cancelled ? "" : "Nothing heard. Tap the microphone and speak.", !cancelled);
        return;
      }
      const type = recorder.mimeType || chunks[0].type || "audio/webm";
      send(new Blob(chunks, { type }), filenameFor(type));
    };
    recorder.start();
    recording = { stop: end, cancel: () => { cancelled = true; end(); } };
    micButtons("recording");
    status("Listening… say the activity, then pause. Tap the microphone to finish.");
  }

  async function send(blob, filename) {
    micButtons("busy");
    status("Reading what you said…");
    const day = (() => {
      try { return JSON.parse(document.getElementById("day").getAttribute("hx-vals")).day; } catch { return ""; }
    })();
    const body = new FormData();
    body.append("audio", blob, filename);
    body.append("day", day);
    try {
      const res = await fetch("/voice?day=" + encodeURIComponent(day), {
        method: "POST", body, credentials: "same-origin", headers: { "HX-Request": "true" },
      });
      if (res.status === 401) { location.href = "/login"; return; }
      if (!res.ok) throw new Error(String(res.status));
      const target = document.getElementById("modal-body");
      target.innerHTML = await res.text();
      window.htmx?.process(target);
      const m = modal();
      if (m && !m.open) m.showModal();
      status("");
    } catch {
      status(navigator.onLine ? "Could not send the recording. Try again." : "Offline: voice needs a connection.", true);
    } finally {
      micButtons("idle");
    }
  }

  document.addEventListener("click", (e) => {
    if (!e.target.closest("[data-voice]")) return;
    if (recording) recording.stop();
    else startRecording();
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && recording) recording.cancel();
  });

  // After a quick add, keep the cursor in the input for the next one.
  document.addEventListener("htmx:afterSettle", (e) => {
    if (e.detail.target.id !== "day" || modal()?.open) return;
    if (e.detail.requestConfig?.elt?.closest(".quickadd")) {
      document.querySelector('.quickadd input[name="q"]')?.focus();
    }
  });
})();
