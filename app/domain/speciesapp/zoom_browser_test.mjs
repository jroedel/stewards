// The zoom pages in a real Chrome against a real server: `make test-browser`.
//
// A plant card's photo opens on a page of its own (card.go, photo), and an
// inbox photo the same way (inboxapp, zoom). Both put the large picture and
// the full one in one box, the full one on top, and leave the zooming to the
// phone's own pinch. What only a browser can say is whether that box holds:
//
//   - The large picture was turned upright by the server, and the full one
//     is the original's bytes, still stored sideways, with an EXIF saying so.
//     They show the same way up only if the browser turns the full one by
//     its EXIF, as the stripped file must still let it. A photo taken in
//     portrait is the case that would show it.
//   - The two pictures sit exactly over each other, at phone and laptop
//     widths, with the page not scrolling sideways.
//   - Nothing the page uses is refused by the header policy -- the card's
//     magnifying glass is a data: image in the stylesheet.
//
// The photo is drawn in a page and written to a file, and given to the
// server as a steward's screens and the send screen's script give it one.
// Set SCREENSHOTS to a directory to keep a picture of each page.
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { createServer } from "node:http";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { launch, skip, stage } from "../../../scripts/browser.mjs";
import { sessionCookie, startServer } from "../../../scripts/testserver.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const testjpeg = join(here, "../inboxapp/static/testjpeg.mjs");

let server, browser, page, plantPhoto, inboxPhoto;

// Stored 4000 by 3000, red in its top-left quarter, with an EXIF saying it is
// to be turned a quarter clockwise (orientation 6): a portrait photo, as a
// phone keeps one. Upright, it is 3000 by 4000 with the red at the top right.
const STORED = { width: 4000, height: 3000 };

before(async () => {
  if (skip()) return;

  server = await startServer("zoom");
  const { base, cookie, dir } = server;

  const statics = createServer(async (req, res) => {
    const name = new URL(req.url, "http://x").pathname.slice(1);
    if (name === "") return res.writeHead(200, { "Content-Type": "text/html" }).end('<!doctype html><link rel="icon" href="data:,">');
    if (name !== "testjpeg.mjs") return res.writeHead(404).end();
    res.writeHead(200, { "Content-Type": "text/javascript" }).end(await readFile(testjpeg));
  });
  await new Promise((done) => statics.listen(0, "127.0.0.1", done));

  stage("starting Chrome");
  browser = await launch();
  const drawing = await browser.page();
  await drawing.goto(`http://127.0.0.1:${statics.address().port}/`);

  stage("drawing a photo taken in portrait");
  const b64 = await drawing.evaluate(`(async () => {
    const TJ = await import("/testjpeg.mjs");
    const c = new OffscreenCanvas(${STORED.width}, ${STORED.height});
    const ctx = c.getContext("2d");
    ctx.fillStyle = "#fff"; ctx.fillRect(0, 0, ${STORED.width}, ${STORED.height});
    ctx.fillStyle = "#f00"; ctx.fillRect(0, 0, ${STORED.width / 2}, ${STORED.height / 2});
    const blob = await c.convertToBlob({ type: "image/jpeg", quality: 0.9 });
    const b = TJ.afterSOI(new Uint8Array(await blob.arrayBuffer()), TJ.exif({ orientation: 6 }));
    let s = "";
    for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode(...b.subarray(i, i + 0x8000));
    return btoa(s);
  })()`);
  statics.close();

  const file = join(dir, "PXL_portrait.jpg");
  writeFileSync(file, Buffer.from(b64, "base64"));
  const photo = () => new Blob([readFileSync(file)], { type: "image/jpeg" });

  const post = (path, body) =>
    fetch(base + path, { method: "POST", body, redirect: "manual", headers: { Cookie: `__Host-session=${cookie}` }, signal: AbortSignal.timeout(60_000) });

  stage("adding a plant and its photo");
  let w = await post(
    "/steward/species",
    new URLSearchParams({ slug: "brazos-penstemon", common_en: "Brazos penstemon", scientific: "Penstemon tenuis", status: "native" }),
  );
  assert.equal(w.status, 303, `adding the plant: ${w.status}\n${await w.text()}`);

  const list = await (await fetch(base + "/steward/species", { headers: { Cookie: `__Host-session=${cookie}` } })).text();
  const speciesID = /\/steward\/species\/([0-9a-f]{32})\/edit/.exec(list)?.[1];
  assert.ok(speciesID, "the plant is not on the list");

  let form = new FormData();
  form.append("photo", photo(), "PXL_portrait.jpg");
  for (const [k, v] of Object.entries({ kind: "leaf", source: "ours", checked: "yes" })) form.append(k, v);
  w = await post(`/steward/species/${speciesID}/photos`, form);
  assert.equal(w.status, 303, `adding the photo: ${w.status}\n${await w.text()}`);

  const photos = await (await fetch(`${base}/steward/species/${speciesID}/photos`, { headers: { Cookie: `__Host-session=${cookie}` } })).text();
  plantPhoto = /\/steward\/photos\/([0-9a-f]{32})\/edit/.exec(photos)?.[1];
  assert.ok(plantPhoto, "the photo is not on the plant's photos");

  stage("sending it to the inbox");
  form = new FormData();
  form.append("photo", photo(), "PXL_portrait.jpg");
  form.append("at", "property");
  w = await post("/steward/inbox/send", form);
  assert.equal(w.status, 201, `sending it: ${w.status}\n${await w.text()}`);

  const inbox = await (await fetch(`${base}/steward/inbox`, { headers: { Cookie: `__Host-session=${cookie}` } })).text();
  inboxPhoto = /\/steward\/inbox\/([0-9a-f]{32})\/small\.jpg/.exec(inbox)?.[1];
  assert.ok(inboxPhoto, "the photo is not in the inbox");

  page = await browser.page();
  await page.setCookie(sessionCookie(base, cookie));
  stage("ready");
}, { timeout: 300_000 });

after(async () => {
  await browser?.close();
  server?.stop();
});

const opts = { skip: skip() };

const SCREENS = {
  phone: { width: 390, height: 844, deviceScaleFactor: 3, mobile: true },
  laptop: { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false },
};

// measure waits for the full picture to arrive and gives back what the page
// made of both: their natural sizes as the browser turned them, where they
// sit, which way up each one is, and whether the page scrolls sideways.
const measure = () =>
  page.waitFor(`(() => {
    const full = document.querySelector(".zoom-full");
    const large = document.querySelector(".zoom-frame img:not(.zoom-full)");
    if (!full?.complete || !full.naturalWidth || !large?.complete || !large.naturalWidth) return null;

    // Which corner is red, read from the picture as the browser draws it.
    const redAt = (img) => {
      const c = document.createElement("canvas");
      c.width = img.naturalWidth; c.height = img.naturalHeight;
      const ctx = c.getContext("2d");
      ctx.drawImage(img, 0, 0);
      const red = (x, y) => { const [r, g, b] = ctx.getImageData(x * c.width, y * c.height, 1, 1).data; return r > 200 && g < 80 && b < 80; };
      return [red(0.1, 0.1) && "top-left", red(0.9, 0.1) && "top-right", red(0.1, 0.9) && "bottom-left", red(0.9, 0.9) && "bottom-right"].filter(Boolean);
    };

    const box = (el) => { const r = el.getBoundingClientRect(); return [r.left, r.top, r.width, r.height].map(Math.round); };

    return {
      full: [full.naturalWidth, full.naturalHeight], large: [large.naturalWidth, large.naturalHeight],
      fullRed: redAt(full), largeRed: redAt(large),
      fullBox: box(full), largeBox: box(large),
      innerWidth, innerHeight, scrollWidth: document.documentElement.scrollWidth,
    };
  })()`, 30_000);

async function checkZoomPage(name, screen, m) {
  assert.deepEqual(m.full, [3000, 4000], "the full picture is the photo at its own size, upright");
  assert.deepEqual(m.large, [1200, 1600], "the large one too");
  assert.deepEqual(m.fullRed, ["top-right"], "the full picture shows the way it was taken");
  assert.deepEqual(m.largeRed, ["top-right"], "and the large one the same way");
  assert.deepEqual(m.fullBox, m.largeBox, "the two sit exactly over each other");
  assert.equal(m.fullBox[2], screen.width - 32, "as wide as the screen, less its edges");
  assert.ok(m.fullBox[3] <= Math.ceil(screen.height * 0.85), `no taller than most of the screen: ${m.fullBox[3]}`);
  assert.ok(m.scrollWidth <= m.innerWidth, `the page does not scroll sideways: ${m.scrollWidth} > ${m.innerWidth}`);

  if (process.env.SCREENSHOTS) writeFileSync(join(process.env.SCREENSHOTS, `${name}.png`), await page.screenshot());
}

for (const [name, screen] of Object.entries(SCREENS)) {
  test(`on a ${name}, a card's photo opens on a page of its own, upright and whole`, opts, async () => {
    await page.send("Emulation.setDeviceMetricsOverride", screen);

    await page.goto(`${server.base}/plants/brazos-penstemon?view=weeding`);
    const badge = await page.evaluate(`getComputedStyle(document.querySelector(".photo-zoom"), "::after").backgroundImage`);
    assert.match(badge, /^url\("data:image\/svg\+xml/, "the magnifying glass is drawn");
    if (process.env.SCREENSHOTS) writeFileSync(join(process.env.SCREENSHOTS, `card-${name}.png`), await page.screenshot());

    await page.evaluate(`document.querySelector(".photo-zoom").click(), true`);
    await page.waitFor(`location.pathname === "/plants/brazos-penstemon/photos/${plantPhoto}"`);

    await checkZoomPage(`plant-${name}`, screen, await measure());
  });

  test(`on a ${name}, an inbox photo zooms the same way`, opts, async () => {
    await page.send("Emulation.setDeviceMetricsOverride", screen);

    await page.goto(`${server.base}/steward/inbox/${inboxPhoto}`);
    await page.evaluate(`document.querySelector(".photo-zoom").click(), true`);
    await page.waitFor(`location.pathname === "/steward/inbox/${inboxPhoto}/zoom"`);

    await checkZoomPage(`inbox-${name}`, screen, await measure());
  });
}

test("the pages ran with nothing blocked and nothing thrown", opts, () => {
  assert.deepEqual(page.errors, []);
  assert.doesNotMatch(server.log(), /level=ERROR/);
});
