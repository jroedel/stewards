// The offline worker: keeps the public pages a volunteer has opened, and the
// plants of each place they opened, so that the same pages open again under
// the trees where the signal is gone. design.md, "Offline in the woods".
//
// The stewards' choices (2026-10-22): the signal is patchy under the canopy
// and good enough at the parking lot and the house, so what a phone keeps is
// what it was shown with signal, ahead of losing it; and when a place is
// opened, its bed comes with it, lightly -- each plant's Weeding page and the
// first photo on it, the panels' thumbnails -- about 3 MB for Switchbacks.
//
// How each kind of address is answered:
//
//   - A page (pagePaths): from the network first, so whoever has signal sees
//     today's page, and a photo the stewards took down is gone from the page
//     the first time it loads with signal. If the network has not answered in
//     WAIT and there is a kept copy, the kept copy, with its "No signal here"
//     line showing; the network's answer, if it comes, is still kept for next
//     time. With no kept copy it waits for the network as any page would, and
//     if that fails, the page saying this one is not on the phone (/offline).
//   - A photo, a thumbnail, the stylesheet, a font, a script (picturePaths):
//     the kept copy first, as the photos' own stale-while-revalidate does
//     (photoapp), asking again in the background once it is a day old. A
//     photo asked for again and now gone is dropped. With no signal and no
//     copy, a photo's large picture is answered with its small one, which is
//     what the bed kept; a phone with a sharp screen asks for the large.
//   - Anything else -- a steward's screen, a form's post, the API -- is not
//     this worker's, and goes to the network untouched.
//
// What may be kept is the server's to say, not this file's: a page is kept
// only if it was sent "private, no-cache" (page.Keep), which a steward's view
// never is, and a picture only if it was sent public, which a photo nobody has
// checked never is. The paths below only narrow it further.
//
// One address is two pages: the language is a cookie, which a worker cannot
// read. So each page is kept under its address and the language it came in
// (Content-Language), and the language is the one the last page came in, or
// that the EN/ES toggle asked for.
//
// Anything kept and not used for KEEP_DAYS is deleted. Safari on an iPhone
// deletes it all sooner, after seven days of using Safari without opening the
// site (WebKit's own policy); there is nothing this file can do about that,
// and a volunteer back after a month starts again from the parking lot.
//
// A classic script rather than a module, as share-worker.mjs is: it imports
// nothing. It has no words of its own: the "No signal here" line is in every
// kept page, hidden, written by the server in the page's language, and the
// page for an address not kept is the server's /offline, kept like any other.
// The two sentences at the bottom are for a phone that has neither.
"use strict";

const PAGES = "offline-pages-1";
const FILES = "offline-files-1";
const STATE = "offline-state-1";

// WAIT is how long a page waits for the network before showing the copy the
// phone kept: long enough for a slow bar of signal to answer, short enough
// that nobody standing in the sun gives up on the page first.
const WAIT = 4000;

const DAY = 24 * 60 * 60 * 1000;
const KEEP_DAYS = 30;

// KEPT is the header a kept response carries the moment it was kept in.
const KEPT = "X-Kept-At";

const pagePaths = [
  /^\/$/,
  /^\/offline$/,
  /^\/places$/,
  /^\/places\/[a-z0-9-]+$/,
  /^\/plants\/[a-z0-9-]+$/,
  /^\/plants\/[a-z0-9-]+\/photos\/[0-9a-f]{32}$/,
];

const picturePaths = [/^\/photos\/[0-9a-f]{32}\/(thumb|small|large)\.jpg$/, /^\/static\//];

const placePath = /^\/places\/([a-z0-9-]+)$/;
const largePath = /^\/photos\/([0-9a-f]{32})\/large\.jpg$/;

self.addEventListener("install", () => self.skipWaiting());

self.addEventListener("activate", (event) => {
  event.waitUntil(
    (async () => {
      for (const name of await caches.keys()) {
        if (name.startsWith("offline-") && ![PAGES, FILES, STATE].includes(name)) {
          await caches.delete(name);
        }
      }

      await expire();
      await self.clients.claim();
    })(),
  );
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") {
    return;
  }

  const url = new URL(req.url);
  if (url.origin !== self.location.origin) {
    return;
  }

  if (pagePaths.some((p) => p.test(url.pathname))) {
    event.respondWith(page(event, url));
  } else if (picturePaths.some((p) => p.test(url.pathname))) {
    event.respondWith(picture(event, url));
  }
});

// ------------------------------------------------------------------ pages

async function page(event, url) {
  const asked = url.searchParams.get("lang");
  if (asked === "en" || asked === "es") {
    await setLang(asked);
  }

  const kept = await keptPage(url);

  const network = fetch(event.request).then(async (res) => {
    if (keepablePage(res)) {
      await keepPage(res.clone(), event);
    }

    return res;
  });

  // Whatever the page shows, the network's answer is still waited for and
  // kept, so a slow answer is there next time.
  event.waitUntil(network.catch(() => {}));

  if (!kept) {
    return network.catch(() => notKept());
  }

  return new Promise((resolve) => {
    let done = false;
    const answer = (res) => {
      if (!done) {
        done = true;
        clearTimeout(timer);
        resolve(res);
      }
    };

    const timer = setTimeout(() => answer(shown(kept)), WAIT);

    // A server that is down behind a working signal is no better than no
    // signal: the kept copy says more than Apache's error page.
    network.then((res) => answer(res.status >= 500 ? shown(kept) : res)).catch(() => answer(shown(kept)));
  });
}

// keepablePage is whether the server said this page may be kept: page.Keep.
function keepablePage(res) {
  return res.ok && res.type === "basic" && res.headers.get("Cache-Control") === "private, no-cache";
}

async function keepPage(res, event) {
  const lang = res.headers.get("Content-Language") === "es" ? "es" : "en";
  const url = new URL(res.url);

  const known = await getLang();
  await setLang(lang);

  const cache = await caches.open(PAGES);
  await cache.put(key(url, lang), await stamped(res));

  const place = placePath.exec(url.pathname);
  if (place && event) {
    event.waitUntil(keepBed(place[1]).catch(() => {}));
  }

  // The page for an address not kept, in each language a page has come in.
  if (event && (lang !== known || !(await cache.match(key(new URL("/offline", url), lang))))) {
    event.waitUntil(keepAddress("/offline").catch(() => {}));
  }
}

// keptPage is the copy kept of an address, in the language last used, or in
// the other if that is all there is: a Spanish page beats no page.
async function keptPage(url) {
  const cache = await caches.open(PAGES);
  const lang = await getLang();

  return (await cache.match(key(url, lang))) || (await cache.match(key(url, lang === "es" ? "en" : "es")));
}

// key is the address a page is kept under: its own, without ?lang=, with the
// language it is in.
function key(url, lang) {
  const q = new URLSearchParams(url.search);
  q.delete("lang");
  q.sort();
  q.set("kept-lang", lang);

  return url.origin + url.pathname + "?" + q.toString();
}

// shown is a kept page as it is shown: with the "No signal here" line no
// longer hidden, and the moment it was kept in that line.
async function shown(kept) {
  const lang = kept.headers.get("Content-Language") === "es" ? "es" : "en";
  const at = Number(kept.headers.get(KEPT)) || Date.now();

  const html = (await kept.clone().text())
    .replace('<p class="offline-note" hidden>', '<p class="offline-note">')
    .replace("{when}", when(at, lang));

  // Its own headers, the page's policy among them, but not its length,
  // which the line just changed.
  const headers = new Headers(kept.headers);
  headers.delete("Content-Length");

  return new Response(html, { status: 200, headers });
}

// when is a moment as the "No signal here" line says it: "Saturday, October
// 24 at 9:40 AM", "sábado, 24 de octubre, 9:40 a.m.".
function when(ms, lang) {
  return new Intl.DateTimeFormat(lang === "es" ? "es-US" : "en-US", {
    weekday: "long",
    month: "long",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(new Date(ms));
}

// notKept is the answer for an address with no signal and no copy: the
// server's /offline page, kept like any other, or the two sentences below on a
// phone that has not kept even that.
async function notKept() {
  const kept = await keptPage(new URL("/offline", self.location.origin));
  if (kept) {
    return kept;
  }

  return new Response(
    '<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">' +
      '<p lang="en">No signal here, and this page is not saved on this phone.</p>' +
      '<p lang="es">Aquí no hay señal, y esta página no está guardada en este teléfono.</p>',
    { status: 503, headers: { "Content-Type": "text/html; charset=utf-8" } },
  );
}

// ------------------------------------------------------------------ a bed

// keepBed keeps what a place's plants need with no signal: the list the
// server gives at /places/{slug}/keep (placeapp), a page or picture at a
// time, skipping what was kept in the last day. Once an hour at most for a
// place, so a volunteer opening the same card all morning fetches it once.
async function keepBed(slug) {
  const state = await caches.open(STATE);
  const mark = new URL("/kept-bed/" + slug, self.location.origin).href;

  const last = await state.match(mark);
  if (last && Date.now() - Number(await last.text()) < DAY / 24) {
    return;
  }

  const res = await fetch("/places/" + slug + "/keep", { credentials: "same-origin" });
  if (!res.ok) {
    return;
  }

  const list = await res.json();
  await state.put(mark, new Response(String(Date.now())));

  for (const address of list.pictures || []) {
    await keepPicture(address);
  }

  for (const address of list.pages || []) {
    await keepAddress(address);
  }

  await expire();
}

// keepAddress fetches a page and keeps it, unless a copy kept in the last
// day is already there.
async function keepAddress(address) {
  const url = new URL(address, self.location.origin);
  const kept = await keptPage(url);
  if (kept && fresh(kept)) {
    return;
  }

  const res = await fetch(url.href, { credentials: "same-origin" });
  if (keepablePage(res)) {
    await keepPage(res, null);
  }
}

async function keepPicture(address) {
  const url = new URL(address, self.location.origin);
  const cache = await caches.open(FILES);

  const kept = await cache.match(url.href);
  if (kept && fresh(kept)) {
    return;
  }

  const res = await fetch(url.href);
  if (keepablePicture(res)) {
    await cache.put(url.href, await stamped(res));
  }
}

// ------------------------------------------------------------------ pictures

async function picture(event, url) {
  const cache = await caches.open(FILES);

  const kept = await cache.match(url.href);
  if (kept) {
    if (!fresh(kept)) {
      event.waitUntil(again(url).catch(() => {}));
    }

    return kept;
  }

  try {
    const res = await fetch(event.request);
    if (keepablePicture(res)) {
      await cache.put(url.href, await stamped(res.clone()));
    }

    return res;
  } catch {
    const large = largePath.exec(url.pathname);
    if (large) {
      const small = await cache.match(new URL("/photos/" + large[1] + "/small.jpg", url).href);
      if (small) {
        return small;
      }
    }

    // A photo the bed did not keep -- the second and later on a plant's card
    // -- is a leaf drawn on grey, as a plant with no photo is in a panel,
    // rather than the browser's broken picture. Its caption still says what
    // it would have shown.
    if (url.pathname.startsWith("/photos/")) {
      return new Response(NOT_KEPT, { headers: { "Content-Type": "image/svg+xml" } });
    }

    return Response.error();
  }
}

const NOT_KEPT =
  '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 300">' +
  '<rect width="400" height="300" fill="#E6E6E6"/>' +
  '<g transform="translate(176 126) scale(2)" fill="none" stroke="#6B6B6B" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
  '<path d="M5 19c0-8 5-13 14-14-1 9-6 14-14 14z"/><path d="M5 19l7-7"/></g></svg>';

// again asks for a kept picture once more: kept anew if it is still there,
// dropped if the stewards took it down.
async function again(url) {
  const res = await fetch(url.href);
  const cache = await caches.open(FILES);

  if (res.status === 404) {
    await cache.delete(url.href);
  } else if (keepablePicture(res)) {
    await cache.put(url.href, await stamped(res));
  }
}

// keepablePicture is whether the server sent this as anybody's to keep: a
// checked photo, or one of the site's own files. A photo nobody has checked is
// a steward's and sent private, no-store.
function keepablePicture(res) {
  return res.ok && res.type === "basic" && /\bpublic\b/.test(res.headers.get("Cache-Control") || "");
}

// ------------------------------------------------------------------ keeping

// stamped is a response with the moment it was kept added to its headers.
async function stamped(res) {
  const headers = new Headers(res.headers);
  headers.set(KEPT, String(Date.now()));

  return new Response(await res.blob(), { status: res.status, statusText: res.statusText, headers });
}

function fresh(kept) {
  return Date.now() - Number(kept.headers.get(KEPT) || 0) < DAY;
}

// expire deletes everything kept more than KEEP_DAYS ago and not kept again
// since.
async function expire() {
  for (const name of [PAGES, FILES]) {
    const cache = await caches.open(name);

    for (const req of await cache.keys()) {
      const res = await cache.match(req);
      if (!res || Date.now() - Number(res.headers.get(KEPT) || 0) > KEEP_DAYS * DAY) {
        await cache.delete(req);
      }
    }
  }
}

// The language the pages are being read in: the one the last page came in,
// or the toggle asked for; at first, the phone's own.
async function getLang() {
  const res = await (await caches.open(STATE)).match(new URL("/kept-lang", self.location.origin).href);
  if (res) {
    return await res.text();
  }

  return (self.navigator.language || "").toLowerCase().startsWith("es") ? "es" : "en";
}

async function setLang(lang) {
  await (await caches.open(STATE)).put(new URL("/kept-lang", self.location.origin).href, new Response(lang));
}
