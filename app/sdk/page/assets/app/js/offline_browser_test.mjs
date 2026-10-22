// The offline worker in a real Chrome against a real server: `make
// test-browser`.
//
// A volunteer opens Switchbacks with signal, and then loses it, which here is
// the server going away: every request after that fails at once, as it does
// on a phone with no bars. What only a browser can say is whether the worker
// (../offline-worker.js) kept what the stewards decided it should, and shows
// it:
//
//   - the place card, again, with its thumbnails and the line saying this is
//     the copy kept on the phone;
//   - a plant on it, at Weeding as its row links it, which was never opened
//     but came with the bed, with its first photo -- answered with the small
//     picture when the screen asks for the large one;
//   - its back link still leading to Switchbacks;
//   - and for an address nobody opened, the page saying it is not on this
//     phone, rather than Chrome's own.
//
// The photos are drawn in the page, never taken (CLAUDE.md). Set SCREENSHOTS
// to a directory to keep a picture of each page.
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { join } from "node:path";

import { launch, skip, stage } from "../../../../../../scripts/browser.mjs";
import { startServer } from "../../../../../../scripts/testserver.mjs";

let server, browser, page, base;

before(async () => {
  if (skip()) return;

  server = await startServer("offline");
  base = server.base;
  const auth = { Cookie: `__Host-session=${server.cookie}` };

  const post = async (path, body) => {
    const w = await fetch(base + path, { method: "POST", body, redirect: "manual", headers: auth, signal: AbortSignal.timeout(60_000) });
    assert.equal(w.status, 303, `${path}: ${w.status}\n${await w.text()}`);
  };
  const read = async (path) => (await fetch(base + path, { headers: auth })).text();

  stage("starting Chrome");
  browser = await launch();
  page = await browser.page();

  // Two photos of one colour each, drawn on the server's own page so that
  // nothing else needs serving.
  await page.goto(base + "/offline");
  const drawn = async (colour) =>
    Buffer.from(
      await page.evaluate(`(async () => {
        const c = new OffscreenCanvas(900, 600);
        const ctx = c.getContext("2d");
        ctx.fillStyle = "${colour}"; ctx.fillRect(0, 0, 900, 600);
        const b = new Uint8Array(await (await c.convertToBlob({ type: "image/jpeg", quality: 0.9 })).arrayBuffer());
        let s = "";
        for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode(...b.subarray(i, i + 0x8000));
        return btoa(s);
      })()`),
      "base64",
    );

  stage("adding Switchbacks and a plant with its photos");
  await post("/steward/places", new URLSearchParams({ slug: "switchbacks", name: "Switchbacks" }));
  const placeID = /\/steward\/places\/([0-9a-f]{32})\/edit/.exec(await read("/steward"))?.[1];
  assert.ok(placeID, "the place is not on the stewards' page");

  await post("/steward/species", new URLSearchParams({ slug: "cherokee-sedge", common: "Cherokee sedge", status: "native" }));
  const speciesID = /\/steward\/species\/([0-9a-f]{32})\/edit/.exec(await read("/steward/species"))?.[1];
  assert.ok(speciesID, "the plant is not on the list");

  await post(`/steward/places/${placeID}/plants`, new URLSearchParams({ species: speciesID, action: "protect" }));

  for (const [kind, colour] of [["leaf", "#2e7d32"], ["young", "#9ccc65"]]) {
    const form = new FormData();
    form.append("photo", new Blob([await drawn(colour)], { type: "image/jpeg" }), `${kind}.jpg`);
    for (const [k, v] of Object.entries({ kind, source: "ours", checked: "yes" })) form.append(k, v);
    await post(`/steward/species/${speciesID}/photos`, form);
  }

  stage("ready");
}, { timeout: 300_000 });

after(async () => {
  await browser?.close();
  server?.stop();
});

const opts = { skip: skip() };

const shot = async (name) => {
  if (!process.env.SCREENSHOTS) return;
  writeFileSync(join(process.env.SCREENSHOTS, `offline-${name}.png`), await page.screenshot());
};

// loaded waits for every picture on the page to have arrived or failed, and
// says how many arrived.
const loaded = () =>
  page.evaluate(`Promise.all([...document.images].map((i) => i.complete ? 0 : new Promise((r) => { i.onload = i.onerror = r; })))
    .then(() => [...document.images].filter((i) => i.naturalWidth > 0).length)`);

const kept = (cache) => page.evaluate(`caches.open("${cache}").then((c) => c.keys()).then((k) => k.map((r) => r.url))`);

test("a place opened with signal opens again without, with its bed", opts, async () => {
  stage("opening Switchbacks with signal, once to install the worker and once through it");
  await page.goto(base + "/places/switchbacks");
  await page.waitFor("navigator.serviceWorker.controller !== null", 30_000);
  await page.goto(base + "/places/switchbacks");

  stage("waiting for the bed to be kept");
  await page.waitFor(
    `caches.open("offline-pages-1").then((c) => c.keys()).then((k) => {
      const urls = k.map((r) => r.url);
      return urls.some((u) => u.includes("/plants/cherokee-sedge?from=switchbacks")) && urls.some((u) => u.includes("/offline?"));
    })`,
    30_000,
  );
  await page.waitFor(`caches.open("offline-files-1").then((c) => c.keys()).then((k) => k.filter((r) => /small\\.jpg$/.test(r.url)).length === 1)`, 30_000);

  const pictures = await kept("offline-files-1");
  assert.equal(pictures.filter((u) => u.endsWith("/thumb.jpg")).length, 1, `the thumbnail: ${pictures}`);

  stage("losing the signal");
  server.stop();

  await page.goto(base + "/places/switchbacks");
  assert.equal(await page.evaluate(`document.querySelector("h1")?.textContent`), "Switchbacks");
  const note = await page.evaluate(`(() => { const n = document.querySelector(".offline-note"); return n && !n.hidden ? n.textContent : null; })()`);
  assert.match(note || "", /^No signal here\. This is the copy saved on this phone on \w+day, \w+ \d+/, `the line over the kept card: ${note}`);
  assert.equal(await loaded(), await page.evaluate("document.images.length"), "a picture on the kept card did not load");
  await shot("card");

  stage("opening a plant that came with the bed");
  const row = await page.evaluate(`document.querySelector(".plant-row")?.getAttribute("href")`);
  assert.equal(row, "/plants/cherokee-sedge?from=switchbacks&view=weeding");
  await page.goto(base + row);
  assert.equal(await page.evaluate(`document.querySelector("h1")?.textContent`), "Cherokee sedge");
  assert.equal(await page.evaluate(`document.querySelector(".back")?.textContent.trim()`), "‹ Switchbacks");

  // The first photo on its card came with the bed, at its small size. The
  // second did not, and is a plain drawn leaf rather than a broken picture.
  assert.equal(await loaded(), 2, "a picture on the plant's card is broken");
  const [first, second] = await page.evaluate(`[...document.querySelectorAll("figure.photo img")].map((i) => i.currentSrc)`);
  assert.equal(await page.evaluate(`fetch(${JSON.stringify(first)}).then((r) => r.headers.get("Content-Type"))`), "image/jpeg", "the first photo is not the photo");
  assert.equal(await page.evaluate(`fetch(${JSON.stringify(second)}).then((r) => r.headers.get("Content-Type"))`), "image/svg+xml", "the photo not kept is not the drawn leaf");
  await shot("plant");

  stage("an address never opened");
  await page.goto(base + "/plants/nothing-here");
  assert.equal(await page.evaluate(`document.querySelector("h1")?.textContent`), "No signal here");
  await shot("not-kept");
});
