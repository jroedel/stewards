// Photos shared to the stewards app from the phone's photos app: this
// registers the worker that catches them (share-worker.mjs), and on the send
// screen puts them in the file input, as though they had been chosen there.
//
// It is loaded by every page a steward is signed in on, with the manifest
// that offers "Install app" (page.Renderer's OfferApp), so that the worker is
// in place before the first share whichever page the app was installed from.
// A share that comes before the worker does reaches the server instead,
// which asks for it again (inboxapp's shared handler).
//
// The photos wait in Cache Storage, under STASH, from the share until the
// inbox opens again, which is where a batch that was sent ends up. So a
// shared batch survives Android closing the tab while the steward looks
// something up, and is gone once it has been sent. A batch that was shared
// and never sent waits until the next share replaces it, or the inbox is
// next opened.
//
// Everything here is an improvement on a send screen that works without it,
// so every failure is quiet but one: photos that were shared and cannot be
// put in the form, which the screen says, so the steward chooses them
// instead.
const WORKER = "/steward/inbox/static/share-worker.mjs";
const SHARED = "/steward/inbox/shared";
const STASH = "shared-photos";
const INBOX = "/steward/inbox";

(async () => {
  if (!window.isSecureContext) {
    return;
  }

  if (navigator.serviceWorker) {
    navigator.serviceWorker.register(WORKER, { scope: SHARED }).catch(() => {});
  }

  if (!window.caches) {
    return;
  }

  if (location.pathname === INBOX) {
    // Where a sent batch ends up: whatever was shared has been sent, or
    // was left. Only here, not on every page: a steward who looks
    // something up on another page between the share and Send comes back
    // to the photos still there.
    await caches.delete(STASH).catch(() => {});
    return;
  }

  const form = document.querySelector("form[data-send]");
  const shared = new URLSearchParams(location.search).get("shared");
  if (form && shared && /^[0-9]+$/.test(shared) && Number(shared) > 0) {
    await fill(form);
  }
})();

// fill puts the stashed photos in the send screen's file input, and says so,
// in the page's language, from the page (words.mjs). Imported here rather
// than at the top: this file is loaded by every steward's page, and only the
// send screen, after a share, has anything to say.
async function fill(form) {
  const input = form.querySelector('input[type="file"]');
  const max = Number(form.dataset.max) || 30;
  const { sayer } = await import("./words.mjs");
  const t = sayer(document);

  let files = [];
  try {
    files = await take();
  } catch {
    files = [];
  }

  if (files.length === 0) {
    say("problem", t("sharedGone"));
    return;
  }

  if (files.length > max) {
    say("problem", t("sharedTooMany", { count: files.length, max }));
    await caches.delete(STASH).catch(() => {});
    return;
  }

  try {
    const chosen = new DataTransfer();
    for (const f of files) {
      chosen.items.add(f);
    }
    input.files = chosen.files;
  } catch {
    say("problem", files.length === 1 ? t("sharedOneStuck") : t("sharedManyStuck", { count: files.length }));
    return;
  }

  say("done", files.length === 1 ? t("sharedOne") : t("sharedMany", { count: files.length }));
}

// take is the stashed photos, in the order they were shared.
async function take() {
  if (!(await caches.has(STASH))) {
    return [];
  }

  const cache = await caches.open(STASH);
  const keys = await cache.keys();
  const order = (req) => Number(new URL(req.url).pathname.split("/").pop());
  keys.sort((a, b) => order(a) - order(b));

  const files = [];
  for (const req of keys) {
    const res = await cache.match(req);
    if (!res) {
      continue;
    }

    const blob = await res.blob();
    let name = "photo.jpg";
    try {
      name = decodeURIComponent(res.headers.get("X-Name") || name);
    } catch {
      // keep the plain name
    }

    files.push(new File([blob], name, {
      type: blob.type,
      lastModified: Number(res.headers.get("X-Modified")) || Date.now(),
    }));
  }

  return files;
}

// say writes the one sentence about the share above the form, where the
// server writes its own about a share that did not arrive.
function say(kind, text) {
  const where = document.getElementById("shared");
  if (!where) {
    return;
  }

  const p = document.createElement("p");
  p.className = kind;
  p.setAttribute("role", kind === "problem" ? "alert" : "status");
  p.textContent = text;
  where.replaceChildren(p);
  where.hidden = false;
}
