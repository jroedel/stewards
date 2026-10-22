// Registers the offline worker (offline-worker.js) from every page a phone may
// keep (page.Keep), so that a volunteer who opened a place with signal can
// open it again under the trees without.
//
// Nothing else. The worker does the keeping, and a phone without service
// workers, or a page that is not on https, is simply a page as before.
if (window.isSecureContext && navigator.serviceWorker) {
  navigator.serviceWorker.register("/offline-worker.js", { scope: "/" }).catch(() => {});
}
