// A place card's "+ Add" in a real Chrome against a real server: `make
// test-browser`.
//
// What only a browser can say (issue #94): that tapping "+ Add" puts the
// steward straight in the plant search, with the list open, rather than in
// a popover they then have to tap into. The popover gives the focus to its
// autofocus element as it opens; find.mjs hands the <select>'s autofocus to
// the box it makes. A volunteer never sees "+ Add", so this is a steward's
// page.
//
// Set SCREENSHOTS to a directory to keep a picture of the open popover.
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { join } from "node:path";

import { launch, skip, stage } from "../../../scripts/browser.mjs";
import { sessionCookie, startServer } from "../../../scripts/testserver.mjs";

let server, browser, page;

before(async () => {
  if (skip()) return;

  server = await startServer("add");
  const { base, cookie } = server;
  const auth = { Cookie: `__Host-session=${cookie}` };

  const post = async (path, body) => {
    const w = await fetch(base + path, { method: "POST", body, redirect: "manual", headers: auth, signal: AbortSignal.timeout(60_000) });
    assert.equal(w.status, 303, `${path}: ${w.status}\n${await w.text()}`);
  };

  stage("adding Switchbacks and two plants");
  await post("/steward/places", new URLSearchParams({ slug: "switchbacks", name: "Switchbacks" }));
  await post("/steward/species", new URLSearchParams({ slug: "cherokee-sedge", common: "Cherokee sedge", status: "native" }));
  await post("/steward/species", new URLSearchParams({ slug: "winecup", common: "Winecup", status: "native" }));

  stage("starting Chrome");
  browser = await launch();
  page = await browser.page();
  await page.setCookie(sessionCookie(base, cookie));
  stage("ready");
}, { timeout: 300_000 });

after(async () => {
  await browser?.close();
  server?.stop();
});

test("+ Add puts the steward in the plant search, with the list open", { skip: skip() }, async () => {
  await page.goto(`${server.base}/places/switchbacks`);
  await page.waitFor(`document.querySelector(".add-button") && document.querySelector(".add-popover .find input")`);

  // Nothing is focused before the tap: the popovers' autofocus waits for
  // them to open, rather than pulling the page down to the first one.
  assert.equal(await page.evaluate(`document.activeElement === document.body`), true, "something took the focus as the page loaded");

  stage("tapping the first + Add");
  await page.tap(".add-button");

  const focused = await page.waitFor(`(() => {
    const a = document.activeElement;
    return a?.matches(".add-popover:popover-open .find input") && a.id;
  })()`);
  assert.ok(focused.endsWith("-species"), `the focus is on #${focused}, not the plant search`);

  // Focus opens the whole list, as a tap in the box does.
  await page.waitFor(`!document.activeElement.closest(".find").querySelector(".find-list").hidden`);
  const shown = await page.evaluate(`[...document.activeElement.closest(".find").querySelectorAll("[role=option]")].map((o) => o.textContent)`);
  assert.ok(shown.some((t) => t.includes("Winecup")) && shown.some((t) => t.includes("Cherokee sedge")), `the list shows ${JSON.stringify(shown)}`);

  if (process.env.SCREENSHOTS) writeFileSync(join(process.env.SCREENSHOTS, "card-add.png"), await page.screenshot());

  // Typing goes straight into it.
  await page.type("wine");
  assert.deepEqual(
    await page.evaluate(`[...document.activeElement.closest(".find").querySelectorAll("[role=option]")].map((o) => o.textContent)`),
    ["Winecup"],
  );

  assert.deepEqual(page.errors, [], "the page reported errors, or the header policy refused something");
});
