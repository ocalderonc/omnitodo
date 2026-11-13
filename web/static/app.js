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

  // --- timeline: zoom, drag to move, drag the bottom edge to resize --------
  // Blocks are placed by CSS from --s (start, minutes from the top), --d
  // (length) and --ppm (pixels per minute). A drag only changes those on the
  // page; on release one request saves it and the day comes back re-planned.
  // Moving a block fixes its start; resizing keeps it fixed or floating.
  const SNAP = 5; // minutes
  const KEY_STEP = 15; // minutes per arrow key
  const LONG_PRESS_MS = 350; // phones: hold this long before a drag starts
  const ZOOMS = [0.8, 1.2, 1.5, 2.5, 4]; // pixels per minute
  const ZOOM_KEY = "omnitodo.zoom";
  const saved = (() => { try { return parseInt(localStorage.getItem(ZOOM_KEY), 10); } catch { return NaN; } })();
  let zoom = saved >= 0 && saved < ZOOMS.length ? saved : 2;
  const ppm = () => ZOOMS[zoom];

  const currentDay = () => {
    try { return JSON.parse(document.getElementById("day").getAttribute("hx-vals")).day; } catch { return ""; }
  };
  const hhmm = (m) => String(Math.floor(m / 60)).padStart(2, "0") + ":" + String(m % 60).padStart(2, "0");
  const visibleTimeline = () => {
    const tl = document.querySelector("#day .timeline");
    return tl && tl.offsetParent !== null ? tl : null;
  };

  function showZoom() {
    const main = document.getElementById("main");
    if (!main) return;
    main.style.setProperty("--ppm", ppm() + "px");
    main.classList.toggle("zoomed", ppm() >= 2.5);
    document.querySelectorAll("[data-zoom]").forEach((b) => {
      b.disabled = Number(b.dataset.zoom) < 0 ? zoom === 0 : zoom === ZOOMS.length - 1;
    });
  }
  // setZoom keeps the minute at anchorY (a viewport y) where it is, so
  // zooming does not lose your place.
  function setZoom(next, anchorY) {
    next = Math.max(0, Math.min(ZOOMS.length - 1, next));
    if (next === zoom) return;
    const tl = visibleTimeline();
    const y = anchorY ?? innerHeight / 2;
    const minute = tl ? (y - tl.getBoundingClientRect().top) / ppm() : null;
    zoom = next;
    try { localStorage.setItem(ZOOM_KEY, String(zoom)); } catch {}
    showZoom();
    if (tl && minute !== null) scrollBy(0, tl.getBoundingClientRect().top + minute * ppm() - y);
  }
  document.addEventListener("DOMContentLoaded", showZoom);
  document.addEventListener("htmx:afterSettle", showZoom);
  document.addEventListener("click", (e) => {
    const b = e.target.closest("[data-zoom]");
    if (b) setZoom(zoom + Number(b.dataset.zoom));
  });
  // Ctrl + wheel, which is also a trackpad pinch.
  let wheel = 0;
  document.addEventListener("wheel", (e) => {
    if (!e.ctrlKey || !e.target.closest?.(".tl")) return;
    e.preventDefault();
    wheel += e.deltaY;
    if (Math.abs(wheel) >= 40) {
      setZoom(zoom + (wheel < 0 ? 1 : -1), e.clientY);
      wheel = 0;
    }
  }, { passive: false });

  function openEdit(block) {
    window.htmx?.ajax("GET", `/activities/${block.dataset.id}/edit?day=${encodeURIComponent(currentDay())}`,
      { target: "#modal-body", swap: "innerHTML" });
  }
  let refocus = null;
  function saveSlot(block, values, onFail) {
    block.classList.add("saving");
    const done = () => {
      // Still on the page means #day was not replaced: put it back.
      if (block.isConnected) { block.classList.remove("saving"); onFail(); }
    };
    const req = window.htmx?.ajax("POST", `/activities/${block.dataset.id}/slot`,
      { target: "#day", swap: "outerHTML", values: { day: currentDay(), ...values } });
    if (req) req.then(done, done); else done();
  }
  document.addEventListener("htmx:afterSettle", (e) => {
    if (refocus === null || e.detail.target.id !== "day") return;
    document.querySelector(`.timeline .block[data-id="${refocus}"]`)?.focus();
    refocus = null;
  });

  let drag = null;
  let suppressClick = false;
  function begin(block, resize, clientY) {
    const tl = block.closest(".timeline");
    drag = {
      block, resize, from: Number(tl.dataset.from),
      start: Number(block.dataset.start), dur: Number(block.dataset.dur),
      y0: clientY + scrollY, lastY: clientY, active: false, timer: 0,
      range: block.querySelector(".range"),
    };
    drag.label = drag.range?.textContent;
    drag.newStart = drag.start;
    drag.newDur = drag.dur;
  }
  function activate() {
    drag.active = true;
    drag.block.classList.add("dragging");
    document.body.classList.add("dragging-block");
    navigator.vibrate?.(8);
  }
  function place(d, start, dur) {
    d.block.style.setProperty("--s", start - d.from);
    d.block.style.setProperty("--d", dur);
    if (d.range) d.range.textContent = hhmm(start) + "–" + hhmm(start + dur);
  }
  function follow(clientY) {
    const d = drag;
    d.lastY = clientY;
    const delta = (clientY + scrollY - d.y0) / ppm();
    if (d.resize) {
      let end = Math.round((d.start + d.dur + delta) / SNAP) * SNAP;
      end = Math.max(d.start + SNAP, Math.min(24 * 60, end));
      d.newDur = end - d.start;
    } else {
      const start = Math.round((d.start + delta) / SNAP) * SNAP;
      d.newStart = Math.max(0, Math.min(24 * 60 - d.dur, start));
    }
    place(d, d.newStart, d.newDur);
  }
  // Near the top or bottom of the screen the page scrolls along.
  let scrolling = 0;
  function autoScroll() {
    scrolling = 0;
    if (!drag?.active) return;
    const edge = 56, y = drag.lastY;
    const step = y < edge ? -(edge - y) / 4 : y > innerHeight - edge ? (y - innerHeight + edge) / 4 : 0;
    if (step) {
      scrollBy(0, step);
      follow(y);
      scrolling = requestAnimationFrame(autoScroll);
    }
  }
  function finish(cancel) {
    const d = drag;
    if (!d) return;
    drag = null;
    clearTimeout(d.timer);
    cancelAnimationFrame(scrolling);
    scrolling = 0;
    d.block.classList.remove("dragging", "armed");
    document.body.classList.remove("dragging-block");
    if (!d.active) return;
    suppressClick = true;
    setTimeout(() => (suppressClick = false), 400);
    const restore = () => {
      place(d, d.start, d.dur);
      if (d.range) d.range.textContent = d.label;
    };
    if (cancel || (d.newStart === d.start && d.newDur === d.dur)) return restore();
    saveSlot(d.block, d.resize ? { duration: d.newDur } : { start: hhmm(d.newStart) }, restore);
  }

  // Mouse and pen: a drag starts after a few pixels; less is a click.
  document.addEventListener("pointerdown", (e) => {
    if (e.pointerType === "touch" || e.button !== 0 || drag) return;
    const block = e.target.closest?.(".timeline .block[data-id]");
    if (!block || block.classList.contains("saving")) return;
    begin(block, !!e.target.closest(".grip"), e.clientY);
    block.setPointerCapture?.(e.pointerId);
  });
  document.addEventListener("pointermove", (e) => {
    if (!drag || e.pointerType === "touch") return;
    if (!drag.active && Math.abs(e.clientY + scrollY - drag.y0) > 4) activate();
    if (drag.active) {
      follow(e.clientY);
      if (!scrolling) scrolling = requestAnimationFrame(autoScroll);
    }
  });
  document.addEventListener("pointerup", (e) => e.pointerType !== "touch" && finish(false));
  document.addEventListener("pointercancel", (e) => e.pointerType !== "touch" && finish(true));

  // Touch: hold still on a block, then drag, so a swipe still scrolls. The
  // bottom edge resizes straight away (it does not scroll the page).
  document.addEventListener("touchstart", (e) => {
    if (drag || e.touches.length !== 1) return finish(true);
    const block = e.target.closest?.(".timeline .block[data-id]");
    if (!block || block.classList.contains("saving")) return;
    const y = e.touches[0].clientY;
    begin(block, !!e.target.closest(".grip"), y);
    if (drag.resize) return activate();
    drag.timer = setTimeout(() => {
      if (!drag || drag.active) return;
      drag.block.classList.add("armed");
      activate();
    }, LONG_PRESS_MS);
  }, { passive: true });
  document.addEventListener("touchmove", (e) => {
    if (!drag) return;
    const y = e.touches[0].clientY;
    if (!drag.active) {
      if (Math.abs(y + scrollY - drag.y0) > 8) finish(true); // a swipe: let it scroll
      return;
    }
    e.preventDefault();
    follow(y);
    if (!scrolling) scrolling = requestAnimationFrame(autoScroll);
  }, { passive: false });
  document.addEventListener("keydown", (e) => e.key === "Escape" && drag && finish(true));
  document.addEventListener("touchend", () => finish(false));
  document.addEventListener("touchcancel", () => finish(true));
  addEventListener("scroll", () => drag && !drag.active && finish(true), { passive: true });
  document.addEventListener("contextmenu", (e) => {
    if (e.target.closest?.(".timeline .block[data-id]")) e.preventDefault();
  });

  // A click or tap that was not a drag opens the edit dialog.
  document.addEventListener("click", (e) => {
    const block = e.target.closest(".timeline .block[data-id]");
    if (!block) return;
    if (suppressClick) { e.preventDefault(); return; }
    openEdit(block);
  }, true);

  // Keyboard: Enter edits, arrows move (fixing the start), Shift+arrows resize.
  document.addEventListener("keydown", (e) => {
    const block = e.target.closest?.(".timeline .block[data-id]");
    if (!block || e.altKey || e.ctrlKey || e.metaKey) return;
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      openEdit(block);
      return;
    }
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
    e.preventDefault();
    if (block.classList.contains("saving")) return;
    const step = e.key === "ArrowUp" ? -KEY_STEP : KEY_STEP;
    const start = Number(block.dataset.start), dur = Number(block.dataset.dur);
    refocus = block.dataset.id;
    if (e.shiftKey) {
      saveSlot(block, { duration: Math.max(SNAP, dur + step) }, () => {});
    } else {
      saveSlot(block, { start: hhmm(Math.max(0, Math.min(24 * 60 - dur, start + step))) }, () => {});
    }
  });

  // After a quick add, keep the cursor in the input for the next one.
  document.addEventListener("htmx:afterSettle", (e) => {
    if (e.detail.target.id !== "day" || modal()?.open) return;
    if (e.detail.requestConfig?.elt?.closest(".quickadd")) {
      document.querySelector('.quickadd input[name="q"]')?.focus();
    }
  });
})();
