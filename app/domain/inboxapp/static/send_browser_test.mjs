// The send screen in a real Chrome against a real server: `make test-browser`.
//
// The server is this repository's, built and started for the test with a
// database of its own, and signed into with its one-time bootstrap secret.
// The photos are drawn in a page and written to files, as a camera roll
// would hold them, and chosen in the file input as a steward chooses them.
//
// What it checks is what a steward would only find out on the phone: that
// the page's script runs under the header policy, that it sends a photo at a
// time, that a large photo arrives shrunk, upright and dated while an
// ordinary one arrives byte for byte, and that sending the batch again is
// recognised. The script failing in any way would still end on the inbox --
// the form posts the batch without it -- which is why the server's log and
// the files it kept are read rather than the page's last word.
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { createServer } from "node:http";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { launch, skip, stage } from "../../../../scripts/browser.mjs";
import { sessionCookie, startServer } from "../../../../scripts/testserver.mjs";
import { exifSegment, jpegSize, orientationOf } from "./jpeg.mjs";
import { has } from "./testjpeg.mjs";

const here = dirname(fileURLToPath(import.meta.url));

let server, tmp, base, fixtures, browser, page;

const sha = (b) => createHash("sha256").update(b).digest("hex");

before(async () => {
  if (skip()) return;

  server = await startServer("send");
  ({ base, dir: tmp } = server);

  // The photos, drawn in a page of their own and written out as files.
  const statics = createServer(async (req, res) => {
    const name = new URL(req.url, "http://x").pathname.slice(1);
    if (name === "") return res.writeHead(200, { "Content-Type": "text/html" }).end('<!doctype html><link rel="icon" href="data:,">');
    if (name !== "testjpeg.mjs") return res.writeHead(404).end();
    res.writeHead(200, { "Content-Type": "text/javascript" }).end(await readFile(join(here, name)));
  });
  await new Promise((done) => statics.listen(0, "127.0.0.1", done));

  stage("starting Chrome");
  browser = await launch();
  const drawing = await browser.page();
  await drawing.goto(`http://127.0.0.1:${statics.address().port}/`);

  const draw = async (name, width, height, orientation) => {
    stage(`drawing ${name}, ${width} by ${height}`);
    const b64 = await drawing.evaluate(`(async () => {
      const TJ = await import("/testjpeg.mjs");
      const c = new OffscreenCanvas(${width}, ${height});
      const ctx = c.getContext("2d");
      ctx.fillStyle = "#fff"; ctx.fillRect(0, 0, ${width}, ${height});
      ctx.fillStyle = "#f00"; ctx.fillRect(0, 0, ${width} / 5, ${height} / 5);
      ctx.fillStyle = "#0a0"; ctx.fillRect(${width / 2}, ${height / 3}, ${width} / 7, ${height} / 7);
      const blob = await c.convertToBlob({ type: "image/jpeg", quality: 0.92 });
      const b = TJ.afterSOI(new Uint8Array(await blob.arrayBuffer()), TJ.exif({ orientation: ${orientation} }));
      let s = "";
      for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode(...b.subarray(i, i + 0x8000));
      return btoa(s);
    })()`);
    const file = join(tmp, name);
    writeFileSync(file, Buffer.from(b64, "base64"));
    return file;
  };

  fixtures = {
    // Taken in portrait on a 50 MP phone: stored sideways, 8160 by 6144.
    large: await draw("PXL_large.jpg", 8160, 6144, 6),
    // An ordinary 12 MP photo, within the limit.
    ordinary: await draw("PXL_ordinary.jpg", 4080, 3072, 1),
    // One taken in a park.
    park: await draw("PXL_park.jpg", 2000, 1500, 1),
  };

  statics.close();

  stage("opening the send screen's tab");
  page = await browser.page();
  await page.setCookie(sessionCookie(base, server.cookie));
  stage("ready");
}, { timeout: 300_000 });

after(async () => {
  await browser?.close();
  server?.stop();
});

const opts = { skip: skip() };

const posts = (path) => server.log().split("\n").filter((l) => l.includes("method=POST") && l.includes(`path=${path} `)).length;

// sendBatch chooses files on the send screen, presses Send, and gives back
// where the page went once it had finished.
async function sendBatch(files) {
  stage(`sending ${files.length} photos`);
  await page.goto(`${base}/steward/inbox/new`);
  await page.waitFor(`!!document.querySelector("form[data-send]")`);
  await page.setFiles("#photo", files);
  await page.evaluate(`document.querySelector("form[data-send] button[type=submit]").click(), true`);

  return page.waitFor(`location.pathname === "/steward/inbox" && location.search`, 60_000);
}

const originals = () => {
  const dir = join(tmp, "photo-files", "inbox");
  return readdirSync(dir)
    .filter((n) => n.endsWith("-original.jpg"))
    .map((n) => readFileSync(join(dir, n)));
};

test("a batch goes a photo at a time: the large one shrunk, upright and dated, the ordinary one as it was", opts, async () => {
  const sendsBefore = posts("/steward/inbox/send");

  assert.equal(await sendBatch([fixtures.large, fixtures.ordinary]), "?done=sent&n=2&d=0");

  assert.equal(posts("/steward/inbox/send") - sendsBefore, 2, "two posts to the one-photo route");
  assert.equal(posts("/steward/inbox"), 0, "and none of the whole batch at once");

  const kept = originals();
  assert.equal(kept.length, 2);

  const ordinary = readFileSync(fixtures.ordinary);
  const asItWas = kept.find((b) => sha(b) === sha(ordinary));
  assert.ok(asItWas, "the ordinary photo is kept byte for byte");

  const large = kept.find((b) => b !== asItWas);
  const app1 = exifSegment(new Uint8Array(large));
  assert.deepEqual(jpegSize(new Uint8Array(large)), { width: 3084, height: 4096 }, "the large one, 12 MP and stored upright");
  assert.equal(orientationOf(app1), 1, "and saying so");
  assert.ok(has(new Uint8Array(large), "2026:05:14 09:30:00"), "with the camera's date");
  assert.ok(large.length < readFileSync(fixtures.large).length, "and smaller than it was");

  // The server read that date: the inbox shows both on the day and at the
  // hour the camera gave, in the garden's time.
  const shown = await page.evaluate(`document.body.innerText`);
  assert.match(shown, /Thursday 14 May/);
  assert.equal(shown.match(/9:30 am/g)?.length, 2, shown);
});

test("the same batch sent again is recognised, the shrunk photo too", opts, async () => {
  assert.equal(await sendBatch([fixtures.large, fixtures.ordinary]), "?done=sent&n=0&d=2");
  assert.equal(originals().length, 2);
});

// Each choice of where shows only its own field, by CSS alone; and a batch
// the script sends from a park says so, and where.
test("somewhere else shows its own field, and the batch says where it was", opts, async () => {
  await page.goto(`${base}/steward/inbox/new`);
  await page.waitFor(`!!document.querySelector("form[data-send]")`);

  const shown = () =>
    page.evaluate(`[".for-property", ".for-nursery", ".for-elsewhere"].filter((c) => getComputedStyle(document.querySelector(c)).display !== "none")`);
  const choose = (at) => page.evaluate(`document.querySelector('input[name="at"][value="${at}"]').click(), true`);

  assert.deepEqual(await shown(), [".for-property"], "on the property: the place");
  await choose("nursery");
  assert.deepEqual(await shown(), [".for-nursery"], "at a nursery: which one");
  await choose("elsewhere");
  assert.deepEqual(await shown(), [".for-elsewhere"], "somewhere else: where");

  await page.evaluate(`document.querySelector("#where").value = "Pedernales Falls State Park", true`);
  await page.setFiles("#photo", [fixtures.park]);
  await page.evaluate(`document.querySelector("form[data-send] button[type=submit]").click(), true`);
  assert.equal(await page.waitFor(`location.pathname === "/steward/inbox" && location.search`, 60_000), "?done=sent&n=1&d=0");

  assert.match(await page.evaluate(`document.body.innerText`), /Somewhere else: Pedernales Falls State Park/);
});

test("the page ran with nothing blocked and nothing thrown", opts, () => {
  assert.deepEqual(page.errors, []);
  assert.doesNotMatch(server.log(), /level=ERROR/);
});
