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

  // After a quick add, keep the cursor in the input for the next one.
  document.addEventListener("htmx:afterSettle", (e) => {
    if (e.detail.target.id !== "day" || modal()?.open) return;
    if (e.detail.requestConfig?.elt?.closest(".quickadd")) {
      document.querySelector('.quickadd input[name="q"]')?.focus();
    }
  });
})();
