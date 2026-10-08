// Sorting the inbox in a real Chrome against a real server: `make test-browser`.
//
// The sort screen loads the page package's swap.mjs, as the check queue
// does. What only a browser can say is that it does its job here: a sort
// shows the next photo without loading another page, with its picture fetched
// while the steward looked at the one before and not again; Skip shows the
// photo fetched ahead without asking for it again; the plant just used is a
// button the next time; another outcome's form swaps in the same way; and the
// last sort, which ends on the inbox's list rather than another photo, goes
// there. What each press did is read from the server, not from the page.
//
// The photos are drawn in the page, three plain colours, and sent to the
// inbox as the send screen sends them. Set SCREENSHOTS to a directory to keep
// a picture of the sort screen on a phone.
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { join } from "node:path";

import { launch, skip, stage } from "../../../scripts/browser.mjs";
import { sessionCookie, startServer } from "../../../scripts/testserver.mjs";

let server, browser, page, headers, speciesID, ids;

before(async () => {
  if (skip()) return;

  server = await startServer("sort");
  const { base, cookie } = server;
  headers = { Cookie: `__Host-session=${cookie}` };
  const post = (path, body) => fetch(base + path, { method: "POST", body, redirect: "manual", headers, signal: AbortSignal.timeout(60_000) });

  stage("starting Chrome");
  browser = await launch();
  page = await browser.page();
  await page.goto(`${base}/healthz`);

  stage("adding a plant and sending three photos");
  let w = await post("/steward/species", new URLSearchParams({ slug: "turks-cap", common: "Turk's cap", scientific: "Malvaviscus arboreus var. drummondii", status: "native" }));
  assert.equal(w.status, 303, `adding the plant: ${w.status}\n${await w.text()}`);

  const list = await (await fetch(base + "/steward/species", { headers })).text();
  speciesID = /\/steward\/species\/([0-9a-f]{32})\/edit/.exec(list)?.[1];
  assert.ok(speciesID, "the plant is not on the list");

  ids = await send("#c00", "#0a0", "#00c");

  // Drawing on /healthz, a page of plain text, has Chrome ask for a
  // favicon there and be told 404: not the sort screen's doing.
  page.errors.length = 0;

  await page.setCookie(sessionCookie(base, cookie));
  stage("ready");
}, { timeout: 300_000 });

after(async () => {
  await browser?.close();
  server?.stop();
});

const opts = { skip: skip() };

// draw is a photo of one plain colour, drawn in the page, as base64.
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

// send puts photos of these colours in the inbox, as the send screen does,
// and gives back every photo waiting, in the inbox's order -- the order the
// sort screen goes in.
async function send(...colours) {
  for (const colour of colours) {
    const form = new FormData();
    form.append("photo", new Blob([Buffer.from(await draw(colour), "base64")], { type: "image/jpeg" }), `${colour.slice(1)}.jpg`);
    form.append("at", "property");
    const w = await fetch(`${server.base}/steward/inbox/send`, { method: "POST", body: form, headers, signal: AbortSignal.timeout(60_000) });
    assert.equal(w.status, 201, `sending a photo: ${w.status}\n${await w.text()}`);
  }

  const inbox = await (await fetch(`${server.base}/steward/inbox`, { headers })).text();
  return [...new Set([...inbox.matchAll(/\/steward\/inbox\/([0-9a-f]{32})\/small\.jpg/g)].map((m) => m[1]))];
}

const fetched = (end) => page.evaluate(`performance.getEntriesByType("resource").filter((e) => e.name.endsWith(${JSON.stringify(end)})).length`);

const large = (id) => `/steward/inbox/${id}/large.jpg`;

const shown = (id) => `[...document.querySelectorAll("#swap img")].some((i) => i.getAttribute("src") === "${large(id)}" && i.complete && i.naturalWidth > 0)`;

const fetchedAhead = (id) => page.waitFor(`performance.getEntriesByType("resource").some((e) => e.name.endsWith("${large(id)}"))`);

const click = (selector) => page.evaluate(`document.querySelector(${JSON.stringify(selector)}).click()`);

test("a batch is sorted photo after photo without a page load", opts, async () => {
  const [a, b, c] = ids;

  await page.goto(`${server.base}/steward/inbox/${a}`);
  await page.waitFor(shown(a));
  if (process.env.SCREENSHOTS) {
    writeFileSync(join(process.env.SCREENSHOTS, "sort-phone.png"), await page.screenshot());
    await page.evaluate(`document.querySelector("#swap form fieldset:nth-of-type(2)").scrollIntoView()`);
    writeFileSync(join(process.env.SCREENSHOTS, "sort-phone-form.png"), await page.screenshot());
    await page.evaluate(`window.scrollTo(0, 0)`);
  }

  await fetchedAhead(b);
  await page.evaluate(`window.sameDocument = true`);

  stage("the first, from the list, sure");
  assert.equal(await page.evaluate(`document.querySelectorAll("input[name=species]").length`), 0, "a plant is a button before any sort");
  await page.evaluate(`document.querySelector("select[name=species_other]").value = ${JSON.stringify(speciesID)}`);
  await click("input[name=kind][value=leaf]");
  await click("input[name=checked]");
  await click("#swap form button[type=submit]");
  await page.waitFor(`document.querySelector("#swap .done")?.textContent.includes("checked. Volunteers see it now.")`);
  assert.equal(await page.evaluate(`window.sameDocument`), true, "sorting loaded a page");
  assert.ok(await page.evaluate(shown(b)), "the next photo is not showing");
  assert.equal(await fetched(large(b)), 1, "the next picture was fetched again when it was shown");

  stage("Skip");
  await fetchedAhead(c);
  await click("#swap .sort-skip");
  await page.waitFor(shown(c));
  assert.equal(await page.evaluate(`window.sameDocument`), true, "Skip loaded a page");
  assert.equal(await fetched(large(c)), 1, "Skip fetched the picture again");

  stage("the third, by the plant's button");
  await page.waitFor(`document.querySelector("input[name=species][value='${speciesID}']")`);
  await click(`input[name=species][value='${speciesID}']`);
  await click("input[name=kind][value=flower]");
  await click("#swap form button[type=submit]");
  await page.waitFor(`document.querySelector("#swap .done")?.textContent.includes("It is shown to volunteers once a steward checks it.")`);
  assert.ok(await page.evaluate(shown(b)), "the inbox did not go round to the skipped photo");
  assert.equal(await page.evaluate(`window.sameDocument`), true, "sorting loaded a page");

  stage("the last, set aside, ends on the inbox");
  await click(`#swap .sort-else a[href$="as=unsure"]`);
  await page.waitFor(`document.querySelector("#swap input[name=as]")?.value === "unsure"`);
  assert.equal(await page.evaluate(`window.sameDocument`), true, "another outcome's form loaded a page");
  await click("#swap form button[type=submit]");
  await page.waitFor(`location.pathname === "/steward/inbox" && document.body.textContent.includes("Set aside, with the question.")`);

  // What the server made of it: the first checked, the third not, the
  // second set aside, none waiting.
  const plant = await (await fetch(`${server.base}/steward/species/${speciesID}/photos`, { headers })).text();
  assert.match(plant, /Leaf close-up[\s\S]*Shown on the card/, "the leaf sorted as sure is not on the card");
  assert.match(plant, /Flower close-up[\s\S]*Not checked/, "the flower sorted without the tick is checked");
  const inbox = await (await fetch(`${server.base}/steward/inbox`, { headers })).text();
  assert.match(inbox, /Nothing waiting/, "photos are still waiting");
  assert.match(inbox, /Not sure yet/, "the photo set aside is not set aside");

  assert.deepEqual(page.errors, [], "the page reported errors, or the header policy refused something");
});

test("two photos chosen in the inbox are sorted together, the plant named once", opts, async () => {
  const [a, b, c] = await send("#a50", "#5a0", "#05a");

  await page.goto(`${server.base}/steward/inbox`);
  await page.waitFor(`document.querySelectorAll("input[name=photo]").length === 3`);

  // The button's count is the stylesheet's, which Chrome gives a script
  // only as the rule that makes it; the screenshot shows it.
  stage("choosing two");
  await click(`input[name=photo][value='${c}']`);
  await click(`input[name=photo][value='${a}']`);
  assert.equal(await page.evaluate(`document.querySelectorAll("input[name=photo]:checked").length`), 2, "two taps did not choose two photos");
  if (process.env.SCREENSHOTS) writeFileSync(join(process.env.SCREENSHOTS, "inbox-chosen.png"), await page.screenshot());

  await click(".together-bar .button");
  await page.waitFor(`document.querySelector("#swap input[name=group]")?.value === "${a}.${c}" && ${shown(a)}`);
  assert.match(await page.evaluate(`document.querySelector(".sort-where").textContent`), /1 of the 2 chosen/);

  stage("the first, naming the plant");
  await page.evaluate(`window.sameDocument = true`);
  await click(`input[name=species][value='${speciesID}']`);
  await click("input[name=kind][value=leaf]");
  await click("#swap form button[type=submit]");
  await page.waitFor(`${shown(c)} && document.querySelector(".sort-where")?.textContent.includes("2 of the 2 chosen")`);
  assert.equal(await page.evaluate(`window.sameDocument`), true, "sorting one of a group loaded a page");
  assert.equal(await page.evaluate(`document.querySelector("input[name=species][value='${speciesID}']").checked`), true, "the plant is not chosen on the second");

  stage("the second, with the plant already chosen");
  await click("input[name=kind][value=flower]");
  await click("#swap form button[type=submit]");
  await page.waitFor(`location.pathname === "/steward/inbox" && document.querySelectorAll("input[name=photo]").length === 1`);
  assert.equal(await page.evaluate(`document.querySelector("input[name=photo]").value`), b, "the photo not chosen is not the one left");

  assert.deepEqual(page.errors, [], "the page reported errors, or the header policy refused something");
});
