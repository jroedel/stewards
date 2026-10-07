// Checking many at once in a real Chrome against a real server: `make
// test-browser`.
//
// What only a browser can say is where a tap lands. The first version of the
// page drew each tick as a pill whose hidden box was stretched over the
// nearest positioned ancestor, and with nothing positioned in between that
// was the page's first screen: every box covered all of it, and a tap there
// on a heading, a source link or another photo's tick toggled the page's
// last photo, out of sight. So this taps outside the photos and asserts that
// nothing changed, then taps one photo and asserts that only it did. That the press checked what the
// page says is read from the server, not from the page.
//
// Set SCREENSHOTS to a directory to keep a picture of the page on a phone.
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { join } from "node:path";

import { launch, skip, stage } from "../../../scripts/browser.mjs";
import { sessionCookie, startServer } from "../../../scripts/testserver.mjs";

let server, browser, page, headers, photos;

before(async () => {
  if (skip()) return;

  server = await startServer("many");
  const { base, cookie } = server;
  headers = { Cookie: `__Host-session=${cookie}` };
  const post = (path, body) => fetch(base + path, { method: "POST", body, redirect: "manual", headers, signal: AbortSignal.timeout(60_000) });

  stage("starting Chrome");
  browser = await launch();
  page = await browser.page();
  await page.goto(`${base}/healthz`);

  const draw = (colour) =>
    page.evaluate(`(async () => {
      const c = new OffscreenCanvas(1200, 900);
      const ctx = c.getContext("2d");
      ctx.fillStyle = "${colour}"; ctx.fillRect(0, 0, 1200, 900);
      const b = new Uint8Array(await (await c.convertToBlob({ type: "image/jpeg", quality: 0.9 })).arrayBuffer());
      let s = "";
      for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode(...b.subarray(i, i + 0x8000));
      return btoa(s);
    })()`);

  stage("adding a plant and three photos");
  const w = await post("/steward/species", new URLSearchParams({ slug: "brazos-penstemon", common: "Brazos penstemon", scientific: "Penstemon tenuis", status: "native" }));
  assert.equal(w.status, 303, `adding the plant: ${w.status}\n${await w.text()}`);

  const list = await (await fetch(base + "/steward/species", { headers })).text();
  const speciesID = /\/steward\/species\/([0-9a-f]{32})\/edit/.exec(list)?.[1];
  assert.ok(speciesID, "the plant is not on the list");

  for (const [colour, kind] of [["#c00", "leaf"], ["#0a0", "flower"], ["#00c", "mature"]]) {
    const form = new FormData();
    form.append("photo", new Blob([Buffer.from(await draw(colour), "base64")], { type: "image/jpeg" }), `${kind}.jpg`);
    form.append("kind", kind);
    form.append("source", "ours");
    const w = await post(`/steward/species/${speciesID}/photos`, form);
    assert.equal(w.status, 303, `adding a photo: ${w.status}\n${await w.text()}`);
  }

  const page2 = await (await fetch(`${base}/steward/species/${speciesID}/photos`, { headers })).text();
  photos = [...new Set([...page2.matchAll(/\/steward\/photos\/([0-9a-f]{32})\/edit/g)].map((m) => m[1]))];
  assert.equal(photos.length, 3, "the three photos are not on the plant's page");

  // Drawing on /healthz, a page of plain text, has Chrome ask for a
  // favicon there and be told 404: not this page's doing.
  page.errors.length = 0;

  await page.setCookie(sessionCookie(base, cookie));
  stage("ready");
}, { timeout: 300_000 });

after(async () => {
  await browser?.close();
  server?.stop();
});

const opts = { skip: skip() };

// ticked is the ids whose boxes are ticked. The count on the button is not
// asserted: it is a CSS counter, and Chrome gives a script the counter()
// expression rather than the number it draws. The screenshot shows it.
const ticked = () => page.evaluate(`[...document.querySelectorAll("input[name=id]:checked")].map((i) => i.value).sort().join(",")`);

// checked is whether the server says a photo is checked: a signed-out
// request sees only a checked photo.
const checked = async (id) => (await fetch(`${server.base}/photos/${id}/small.jpg`)).status === 200;

test("a tap outside the photos ticks nothing, a tap on one unticks only it, and the press checks the rest", opts, async () => {
  const [a, b, c] = photos;
  const all = [...photos].sort().join(",");

  await page.goto(`${server.base}/steward/check/many?ids=${photos.map((id) => id.slice(0, 8)).join(",")}`);
  await page.waitFor(`[...document.querySelectorAll(".many-pick img")].every((i) => i.complete && i.naturalWidth > 0)`);
  assert.equal(await ticked(), all, "a photo from the list is not ticked");

  stage("tapping outside the photos");
  for (const outside of ["h1", ".lead", ".photo-kind h2"]) {
    await page.tap(outside);
    assert.equal(await ticked(), all, `a tap on ${outside} changed a tick`);
  }

  stage("tapping one photo");
  await page.tap(`input[name=id][value="${b}"]`);
  assert.equal(await ticked(), [a, c].sort().join(","), "the tap did not untick that photo, and only it");
  assert.equal(await page.evaluate(`getComputedStyle(document.querySelector('input[value="${b}"] ~ img')).opacity`), "0.4", "the photo left out does not fade");
  if (process.env.SCREENSHOTS) writeFileSync(join(process.env.SCREENSHOTS, "check-many-phone.png"), await page.screenshot());

  stage("pressing the button");
  await page.tap(".together-bar .button");
  await page.waitFor(`document.querySelector(".done")?.textContent.includes("Checked 2 photos.")`);
  assert.equal(await ticked(), "", "after the press, the photo left out is ticked again");

  assert.ok(await checked(a), "the server does not have the first photo checked");
  assert.equal(await checked(b), false, "the server checked the photo left out");
  assert.ok(await checked(c), "the server does not have the third photo checked");

  assert.deepEqual(page.errors, [], "the page reported errors, or the header policy refused something");
});
