// A share from the phone's photos app, in a real Chrome against a real
// server: `make test-browser`.
//
// Android's share sheet cannot be driven from here, but what it does to the
// app can: Chrome posts the photos to the manifest's share target as one
// multipart form, a navigation of the browser's own. A form posted from the
// page to the same address is that navigation, and goes through the same
// service worker. What this checks is the part only a browser can show: that
// the worker registers under the header policy, takes the post before the
// server sees it, and that the photos then sit in the send screen's file
// input and are sent a photo at a time from there.
import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import { launch, skip, stage } from "../../../../scripts/browser.mjs";
import { sessionCookie, startServer } from "../../../../scripts/testserver.mjs";

let server, base, browser, page;

before(async () => {
  if (skip()) return;

  server = await startServer("share");
  ({ base } = server);

  stage("starting Chrome");
  browser = await launch();
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

const workerReady = `navigator.serviceWorker.getRegistration("/steward/inbox/shared").then((r) => !!(r && r.active))`;

// share posts photos to the share target as Chrome does for a share: drawn
// here, invented, each a colour of its own so none is taken for another.
async function share(photos) {
  stage(`sharing ${photos.length} photos`);
  await page.evaluate(`(async () => {
    const chosen = new DataTransfer();
    for (const [name, colour] of ${JSON.stringify(photos)}) {
      const c = new OffscreenCanvas(1200, 900);
      const ctx = c.getContext("2d");
      ctx.fillStyle = colour; ctx.fillRect(0, 0, 1200, 900);
      ctx.fillStyle = "#fff"; ctx.fillRect(100, 100, 300, 200);
      const blob = await c.convertToBlob({ type: "image/jpeg", quality: 0.9 });
      chosen.items.add(new File([blob], name, { type: "image/jpeg" }));
    }

    const form = document.createElement("form");
    form.method = "post";
    form.action = "/steward/inbox/shared";
    form.enctype = "multipart/form-data";
    const input = document.createElement("input");
    input.type = "file";
    input.name = "photo";
    input.multiple = true;
    input.files = chosen.files;
    form.append(input);
    document.body.append(form);
    form.submit();
    return true;
  })()`);
}

test("photos shared to the app wait in the send screen, and are sent from there", opts, async () => {
  stage("opening the inbox, which registers the worker");
  await page.goto(`${base}/steward/inbox`);
  await page.waitFor(workerReady);

  await share([["PXL_20261006_093000.jpg", "#b03030"], ["Señora's yard.jpg", "#30a040"]]);

  await page.waitFor(`location.pathname === "/steward/inbox/new" && document.querySelector("#photo").files.length === 2`);
  assert.equal(await page.evaluate(`location.search`), "?shared=2");
  assert.equal(posts("/steward/inbox/shared"), 0, "the worker took the share, and the server never saw it");

  assert.deepEqual(
    await page.evaluate(`Array.from(document.querySelector("#photo").files, (f) => [f.name, f.type])`),
    [["PXL_20261006_093000.jpg", "image/jpeg"], ["Señora's yard.jpg", "image/jpeg"]],
    "in the order shared, with their names",
  );
  assert.match(await page.evaluate(`document.querySelector("#shared").innerText`), /2 photos from your phone, ready to send/);

  // Reloaded, as Android does to a tab it put away: still there.
  await page.goto(`${base}/steward/inbox/new?shared=2`);
  await page.waitFor(`document.querySelector("#photo").files.length === 2`);

  await page.evaluate(`document.querySelector('input[name="at"][value="nursery"]').click(), true`);
  await page.evaluate(`document.querySelector("form[data-send] button[type=submit]").click(), true`);
  assert.equal(await page.waitFor(`location.pathname === "/steward/inbox" && location.search`, 60_000), "?done=sent&n=2&d=0");
  assert.equal(posts("/steward/inbox/send"), 2, "a photo at a time");
  assert.match(await page.evaluate(`document.body.innerText`), /At a nursery/);

  // Sent, and the inbox has opened: the phone keeps no copy.
  await page.waitFor(`caches.has("shared-photos").then((has) => !has)`);
});

test("a share already sent says so rather than showing an empty form", opts, async () => {
  await page.goto(`${base}/steward/inbox/new?shared=2`);
  await page.waitFor(`!document.querySelector("#shared").hidden`);

  assert.match(await page.evaluate(`document.querySelector("#shared").innerText`), /not on this page any more/);
  assert.equal(await page.evaluate(`document.querySelector("#photo").files.length`), 0);
});

test("a share the worker missed is asked for again, and the next one is caught", opts, async () => {
  stage("unregistering the worker");
  await page.evaluate(`navigator.serviceWorker.getRegistration("/steward/inbox/shared").then((r) => r.unregister())`);

  await share([["PXL_20261006_094500.jpg", "#3050c0"]]);
  await page.waitFor(`location.search === "?shared=again"`);
  assert.equal(posts("/steward/inbox/shared"), 1, "the share reached the server");
  assert.match(await page.evaluate(`document.querySelector("#shared").innerText`), /Share them again from your photos app/);

  // The send screen registered the worker again.
  await page.waitFor(workerReady);
  await share([["PXL_20261006_094500.jpg", "#3050c0"]]);
  await page.waitFor(`location.search === "?shared=1" && document.querySelector("#photo").files.length === 1`);
  assert.equal(posts("/steward/inbox/shared"), 1, "and the second did not");
});

test("the pages ran with nothing blocked and nothing thrown", opts, () => {
  assert.deepEqual(page.errors, []);
  assert.doesNotMatch(server.log(), /level=ERROR/);
});
