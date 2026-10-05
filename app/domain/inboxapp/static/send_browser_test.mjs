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
import { execFileSync, spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { createServer } from "node:http";
import { createServer as createNetServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { launch, skip, stage } from "../../../../scripts/browser.mjs";
import { exifSegment, jpegSize, orientationOf } from "./jpeg.mjs";
import { has } from "./testjpeg.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, "../../../..");

let tmp, app, appLog = "", base, cookie, fixtures, browser, page;

const sha = (b) => createHash("sha256").update(b).digest("hex");

const freePort = () =>
  new Promise((done) => {
    const s = createNetServer();
    s.listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close(() => done(port));
    });
  });

before(async () => {
  if (skip()) return;

  tmp = mkdtempSync(join(tmpdir(), "stewards-send-"));

  // The server, as it ships.
  stage("building the server");
  execFileSync("go", ["build", "-o", join(tmp, "stewards"), "./cmd/stewards"], { cwd: repo, stdio: "inherit", timeout: 180_000 });

  const port = await freePort();
  base = `http://127.0.0.1:${port}`;
  const secret = randomBytes(24).toString("hex");
  writeFileSync(
    join(tmp, "config.toml"),
    `[server]\naddr = "127.0.0.1:${port}"\nbase_url = "${base}"\n[db]\npath = "${join(tmp, "stewards.db")}"\n[auth]\nbootstrap_secret = "${secret}"\n`,
  );

  stage("starting it");
  app = spawn(join(tmp, "stewards"), ["-config", join(tmp, "config.toml")], { stdio: ["ignore", "pipe", "pipe"] });
  app.stdout.on("data", (d) => (appLog += d));
  app.stderr.on("data", (d) => (appLog += d));

  for (let i = 0; ; i++) {
    try {
      if ((await fetch(`${base}/healthz`, { signal: AbortSignal.timeout(2000) })).ok) break;
    } catch {
      // not listening yet
    }
    if (i > 100) throw new Error(`the server did not start:\n${appLog}`);
    await new Promise((done) => setTimeout(done, 100));
  }

  stage("signing in");
  const signIn = await fetch(`${base}/sign-in/first`, {
    method: "POST",
    redirect: "manual",
    signal: AbortSignal.timeout(10_000),
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ email: "steward@example.org", secret }),
  });
  cookie = signIn.headers.getSetCookie().map((c) => /^__Host-session=([^;]+)/.exec(c)).find(Boolean)?.[1];
  if (!cookie) throw new Error(`the bootstrap sign-in gave no session: ${signIn.status}\n${appLog}`);

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
  };

  statics.close();

  stage("opening the send screen's tab");
  page = await browser.page();
  await page.setCookie({ name: "__Host-session", value: cookie, url: `${base}/`, secure: true, httpOnly: true, path: "/" });
  stage("ready");
}, { timeout: 300_000 });

after(async () => {
  await browser?.close();
  app?.kill();
  if (tmp) rmSync(tmp, { recursive: true, force: true });
});

const opts = { skip: skip() };

const posts = (path) => appLog.split("\n").filter((l) => l.includes("method=POST") && l.includes(`path=${path} `)).length;

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

test("the page ran with nothing blocked and nothing thrown", opts, () => {
  assert.deepEqual(page.errors, []);
  assert.doesNotMatch(appLog, /level=ERROR/);
});
