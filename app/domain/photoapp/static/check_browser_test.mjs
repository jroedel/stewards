// The check queue in a real Chrome against a real server: `make test-browser`.
//
// What only a browser can say is whether check.mjs does what it is for: that
// it runs under the header policy, that Yes, Undo and Skip change the page
// without loading another, and -- the point of it -- that the next photo's
// picture was fetched while the steward looked at the one before, and not
// again when it is shown. The pictures are served no-store, so a second fetch
// would be a second download on the phone. That every press also did what
// the form says is read from the server, not from the page.
//
// The photos are drawn in the page, three plain colours, and given to the
// server as a steward's photos screen gives it one. Set SCREENSHOTS to a
// directory to keep a picture of the queue on a phone.
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { join } from "node:path";

import { launch, skip, stage } from "../../../../scripts/browser.mjs";
import { sessionCookie, startServer } from "../../../../scripts/testserver.mjs";

let server, browser, page, ref, leaf, flower;

before(async () => {
  if (skip()) return;

  server = await startServer("check");
  const { base, cookie } = server;
  const headers = { Cookie: `__Host-session=${cookie}` };
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
  let w = await post("/steward/species", new URLSearchParams({ slug: "brazos-penstemon", common_en: "Brazos penstemon", scientific: "Penstemon tenuis", status: "native" }));
  assert.equal(w.status, 303, `adding the plant: ${w.status}\n${await w.text()}`);

  const list = await (await fetch(base + "/steward/species", { headers })).text();
  const speciesID = /\/steward\/species\/([0-9a-f]{32})\/edit/.exec(list)?.[1];
  assert.ok(speciesID, "the plant is not on the list");

  const add = async (colour, kind, checked) => {
    const form = new FormData();
    form.append("photo", new Blob([Buffer.from(await draw(colour), "base64")], { type: "image/jpeg" }), `${kind}.jpg`);
    for (const [k, v] of Object.entries({ kind, source: "ours", ...(checked ? { checked: "yes" } : {}) })) form.append(k, v);
    const w = await post(`/steward/species/${speciesID}/photos`, form);
    assert.equal(w.status, 303, `adding a photo: ${w.status}\n${await w.text()}`);

    const photos = await (await fetch(`${base}/steward/species/${speciesID}/photos`, { headers })).text();
    return [...photos.matchAll(/\/steward\/photos\/([0-9a-f]{32})\/edit/g)].map((m) => m[1]);
  };

  const before = new Set();
  const added = async (...args) => {
    const id = (await add(...args)).find((x) => !before.has(x));
    before.add(id);
    return id;
  };

  ref = await added("#c00", "leaf", true);
  leaf = await added("#0a0", "leaf", false);
  flower = await added("#00c", "flower", false);

  // Drawing on /healthz, a page of plain text, has Chrome ask for a
  // favicon there and be told 404: not the queue's doing.
  page.errors.length = 0;

  await page.setCookie(sessionCookie(base, cookie));
  stage("ready");
}, { timeout: 300_000 });

after(async () => {
  await browser?.close();
  server?.stop();
});

const opts = { skip: skip() };

// fetched is how many times the page has asked for an address ending in end.
const fetched = (end) => page.evaluate(`performance.getEntriesByType("resource").filter((e) => e.name.endsWith(${JSON.stringify(end)})).length`);

const shown = (id) => `[...document.querySelectorAll("#check img")].some((i) => i.getAttribute("src") === "/photos/${id}/small.jpg" && i.complete && i.naturalWidth > 0)`;

// checked is whether the server says a photo is checked: a signed-out
// request sees only a checked photo.
const checked = async (id) => (await fetch(`${server.base}/photos/${id}/small.jpg`)).status === 200;

test("Yes, Undo and Skip change the queue in place, with the next picture already there", opts, async () => {
  await page.goto(`${server.base}/steward/check`);
  await page.waitFor(shown(leaf));
  await page.waitFor(shown(ref));

  if (process.env.SCREENSHOTS) writeFileSync(join(process.env.SCREENSHOTS, "check-phone.png"), await page.screenshot());

  // The script has fetched the next photo's page and its picture.
  await page.waitFor(`performance.getEntriesByType("resource").some((e) => e.name.endsWith("/photos/${flower}/small.jpg"))`);
  await page.evaluate(`window.sameDocument = true`);

  stage("Yes");
  await page.evaluate(`document.querySelector(".check-yes button").click()`);
  await page.waitFor(`document.querySelector(".check-done")?.textContent.includes("Checked: Brazos penstemon, leaf close-up.")`);
  assert.equal(await page.evaluate(`window.sameDocument`), true, "Yes loaded a page rather than changing this one");
  assert.ok(await page.evaluate(shown(flower)), "the next photo is not showing");
  assert.equal(await fetched(`/photos/${flower}/small.jpg`), 1, "the next picture was fetched again when it was shown");
  assert.match(await page.evaluate(`location.search`), new RegExp(`done=checked&last=${leaf}`), "the address is not the page Yes leads to");
  assert.equal(await page.evaluate(`document.activeElement?.classList.contains("check-done")`), true, "focus is not on what just happened");
  assert.ok(await checked(leaf), "the server does not have the photo checked");

  stage("Undo");
  await page.evaluate(`document.querySelector(".check-done button").click()`);
  await page.waitFor(`document.querySelector(".check-done")?.textContent.includes("Not checked any more.")`);
  assert.equal(await page.evaluate(`window.sameDocument`), true, "Undo loaded a page");
  assert.ok(await page.evaluate(shown(leaf)), "Undo did not come back to the photo");
  assert.equal(await checked(leaf), false, "the server still has the photo checked");

  stage("Skip");
  await page.waitFor(`performance.getEntriesByType("resource").filter((e) => e.name.endsWith("/steward/check?at=${flower}")).length >= 2`);
  const pagesBefore = await fetched(`/steward/check?at=${flower}`);
  await page.evaluate(`document.querySelector("#check a[data-swap]").click()`);
  await page.waitFor(shown(flower));
  assert.equal(await page.evaluate(`window.sameDocument`), true, "Skip loaded a page");
  assert.equal(await fetched(`/steward/check?at=${flower}`), pagesBefore, "Skip fetched the page it had fetched ahead");
  assert.equal(await checked(flower), false, "Skip checked the photo");

  assert.deepEqual(page.errors, [], "the page reported errors, or the header policy refused something");
});
