// The service worker that catches photos shared to the stewards app from the
// phone's own photos app, and nothing else.
//
// A steward who has installed the app from Chrome ("Install app", which the
// inbox's pages offer through their manifest) finds "Garden stewards" in
// Android's share sheet. Choosing it makes Chrome post the photos, as one
// multipart form, to the manifest's share_target: SharePattern. This worker's
// scope is that one address, so the post comes here instead of to the
// server, and the photos are put in the phone's own Cache Storage. The worker
// then sends the steward on to the send screen, whose script (share.mjs) puts
// them in the file input as though they had been chosen there. From there it
// is the send screen as always: where they were taken, a place and a note,
// then Send, a photo a request.
//
// Why not let the post through to the server? It would be the whole batch in
// one request, which is what the send screen's script was written to stop
// (see send.mjs), and it would arrive before anybody had said where the
// photos were taken. So the server's handler for that address only ever sees
// a share this worker missed -- the phone had not opened the inbox since the
// app was installed, or Chrome cleared the worker -- and asks for the share
// again.
//
// A classic script rather than a module, though it is served beside them:
// it imports nothing, and a classic worker registers in every Chrome that
// can install an app at all.
"use strict";

// SHARED is the share target, and the scope this worker is registered with.
// STASH is where the photos wait; share.mjs reads it by the same names.
const SHARED = "/steward/inbox/shared";
const STASH = "shared-photos";
const NEW = "/steward/inbox/new";

// A new version takes over at once. It controls no page -- its only address
// is a post that is always answered with a redirect -- so there is nothing
// open that a change could break underneath.
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (ev) => ev.waitUntil(self.clients.claim()));

self.addEventListener("fetch", (ev) => {
  const url = new URL(ev.request.url);
  if (ev.request.method !== "POST" || url.pathname !== SHARED) {
    return; // to the network, as though there were no worker
  }

  ev.respondWith(keep(ev.request));
});

// keep puts the shared photos in the stash, replacing whatever an earlier
// share left there, and answers with the send screen. ?shared= carries how
// many, so the screen can tell a share that arrived empty from one whose
// photos have since been sent.
async function keep(req) {
  let n = 0;

  try {
    const form = await req.formData();
    const files = form.getAll("photo").filter((f) => typeof f !== "string");

    await caches.delete(STASH);
    const cache = await caches.open(STASH);

    for (const [i, f] of files.entries()) {
      // The name goes in a header, so it is escaped: a header holds no
      // more than Latin-1, and a phone names a photo in any script.
      await cache.put(`${SHARED}/${i}`, new Response(f, {
        headers: {
          "Content-Type": f.type || "application/octet-stream",
          "X-Name": encodeURIComponent(f.name || `photo-${i + 1}.jpg`),
          "X-Modified": String(f.lastModified || Date.now()),
        },
      }));
    }

    n = files.length;
  } catch {
    // The phone is out of room, or the post was not a form. The screen
    // says the photos did not arrive, which is true.
    return Response.redirect(new URL(`${NEW}?shared=lost`, self.location.origin), 303);
  }

  return Response.redirect(new URL(`${NEW}?shared=${n}`, self.location.origin), 303);
}
