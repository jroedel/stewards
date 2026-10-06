// find.mjs in a real Chrome against a real server: `make test-browser`.
//
// What only a browser can say: that the list of plants on the sort screen
// becomes a box to type in, that a tap on a plant in the list chooses it --
// a tap moves the focus before it clicks, which is when a list that closes
// on losing the focus would close under the finger -- that the keyboard
// works it too without sending the form, that the form sends what was
// chosen, that the next photo's form swapped in by swap.mjs is searchable as
// well, and that a list a form needs is still needed. find_test.mjs has the
// matching itself, under Node.
//
// Set SCREENSHOTS to a directory to keep a picture of the list on a phone.
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { join } from "node:path";

import { launch, skip, stage } from "../../../../../../scripts/browser.mjs";
import { sessionCookie, startServer } from "../../../../../../scripts/testserver.mjs";

let server, browser, page, headers, ids;
const plant = {};

before(async () => {
  if (skip()) return;

  server = await startServer("find");
  const { base, cookie } = server;
  headers = { Cookie: `__Host-session=${cookie}` };
  const post = (path, body) => fetch(base + path, { method: "POST", body, redirect: "manual", headers, signal: AbortSignal.timeout(60_000) });

  stage("starting Chrome");
  browser = await launch();
  page = await browser.page();
  await page.goto(`${base}/healthz`);

  stage("adding five plants and sending two photos");
  for (const [slug, common_en, scientific, common_es] of [
    ["turks-cap", "Turk's cap", "Malvaviscus arboreus var. drummondii", "Monacillo"],
    ["turks-cap-pink", "Turk's cap 'Pink'", "Malvaviscus arboreus var. drummondii", ""],
    ["winecup", "Winecup", "Callirhoe involucrata", "Copa de vino"],
    ["texas-redbud", "Texas redbud", "Cercis canadensis var. texensis", ""],
    ["texas-red-oak", "Texas red oak", "Quercus buckleyi", ""],
  ]) {
    const before = await speciesIDs();
    const w = await post("/steward/species", new URLSearchParams({ slug, common_en, common_es, scientific, status: "native" }));
    assert.equal(w.status, 303, `adding ${common_en}: ${w.status}\n${await w.text()}`);
    plant[slug] = (await speciesIDs()).find((id) => !before.includes(id));
    assert.ok(plant[slug], `${common_en} is not on the list`);
  }

  ids = await send("#c60", "#6c0");

  // Drawing on /healthz, a page of plain text, has Chrome ask for a
  // favicon there and be told 404: not this script's doing.
  page.errors.length = 0;

  await page.setCookie(sessionCookie(base, cookie));
  stage("ready");
}, { timeout: 300_000 });

after(async () => {
  await browser?.close();
  server?.stop();
});

const opts = { skip: skip() };

async function speciesIDs() {
  const list = await (await fetch(`${server.base}/steward/species`, { headers })).text();
  return [...list.matchAll(/\/steward\/species\/([0-9a-f]{32})\/edit/g)].map((m) => m[1]);
}

// send puts photos of these colours in the inbox, drawn in the page, as the
// send screen sends them, and gives back the photos waiting in the inbox's
// order.
async function send(...colours) {
  for (const colour of colours) {
    const jpeg = await page.evaluate(`(async () => {
      const c = new OffscreenCanvas(800, 600);
      const ctx = c.getContext("2d");
      ctx.fillStyle = "${colour}"; ctx.fillRect(0, 0, 800, 600);
      const b = new Uint8Array(await (await c.convertToBlob({ type: "image/jpeg", quality: 0.9 })).arrayBuffer());
      let s = "";
      for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode(...b.subarray(i, i + 0x8000));
      return btoa(s);
    })()`);

    const form = new FormData();
    form.append("photo", new Blob([Buffer.from(jpeg, "base64")], { type: "image/jpeg" }), `${colour.slice(1)}.jpg`);
    form.append("at", "property");
    const w = await fetch(`${server.base}/steward/inbox/send`, { method: "POST", body: form, headers, signal: AbortSignal.timeout(60_000) });
    assert.equal(w.status, 201, `sending a photo: ${w.status}\n${await w.text()}`);
  }

  const inbox = await (await fetch(`${server.base}/steward/inbox`, { headers })).text();
  return [...new Set([...inbox.matchAll(/\/steward\/inbox\/([0-9a-f]{32})\/small\.jpg/g)].map((m) => m[1]))];
}

// listed is the plants the list shows, by their common names.
const listed = () => page.evaluate(`[...document.querySelectorAll(".find-list [role=option] .find-name")].map((e) => e.textContent)`);

const value = (name) => page.evaluate(`document.querySelector("select[name=${name}]").value`);

const box = "#species_other";

test("a plant on the sort screen is found by any part of its name, and chosen with a tap", opts, async () => {
  const [a, b] = ids;

  await page.goto(`${server.base}/steward/inbox/${a}`);
  await page.waitFor(`document.querySelector("${box}[role=combobox]")`);
  assert.equal(await page.evaluate(`getComputedStyle(document.querySelector("select[name=species_other]")).display`), "none", "the plain list is still showing");
  await page.evaluate(`window.sameDocument = true`);

  stage("the whole list on a tap, then a word from the scientific name");
  await page.tap(box);
  await page.waitFor(`!document.querySelector(".find-list").hidden`);
  assert.equal((await listed()).length, 5, "a tap in the empty box does not show every plant");

  await page.type("drum");
  await page.waitFor(`document.querySelectorAll(".find-list [role=option]").length === 2`);
  assert.deepEqual(await listed(), ["Turk's cap", "Turk's cap 'Pink'"]);
  assert.equal(await page.evaluate(`document.querySelector(".find-list mark").textContent`), "drum", "what was typed is not marked");
  if (process.env.SCREENSHOTS) writeFileSync(join(process.env.SCREENSHOTS, "find-phone.png"), await page.screenshot());

  stage("a tap on the second");
  await page.tap(".find-list [role=option]:nth-child(2)");
  await page.waitFor(`document.querySelector(".find-list").hidden`);
  assert.equal(await value("species_other"), plant["turks-cap-pink"], "the tap did not choose the plant");
  assert.equal(await page.evaluate(`document.querySelector("${box}").value`), "Turk's cap 'Pink' (Malvaviscus arboreus var. drummondii)");
  assert.notEqual(await page.evaluate(`document.activeElement?.id`), "species_other", "the keyboard stays up after choosing");

  stage("by its Spanish name, then left without choosing");
  await page.tap(box);
  await page.waitFor(`document.querySelector("${box}").value === ""`);
  assert.equal(await page.evaluate(`document.querySelector("${box}").placeholder`), "Turk's cap 'Pink' (Malvaviscus arboreus var. drummondii)", "what was chosen is not shown while typing");
  await page.type("copa");
  await page.waitFor(`document.querySelectorAll(".find-list [role=option]").length === 1`);
  assert.deepEqual(await listed(), ["Winecup"]);
  await page.press("Escape");
  await page.waitFor(`document.querySelector(".find-list").hidden`);
  assert.equal(await value("species_other"), plant["turks-cap-pink"], "Escape changed the choice");
  assert.match(await page.evaluate(`document.querySelector("${box}").value`), /^Turk's cap 'Pink'/, "Escape did not put the choice back");

  stage("by the keyboard, two words, without sending the form");
  await page.tap(box);
  await page.type("tex red");
  await page.waitFor(`document.querySelectorAll(".find-list [role=option]").length === 2`);
  assert.deepEqual(await listed(), ["Texas red oak", "Texas redbud"]);
  await page.press("ArrowDown");
  await page.press("Enter");
  await page.waitFor(`document.querySelector(".find-list").hidden`);
  assert.equal(await value("species_other"), plant["texas-redbud"], "Enter did not choose the plant under it");
  assert.equal(await page.evaluate(`window.sameDocument && !document.querySelector("#swap .done")`), true, "Enter in the box sent the form");

  stage("nothing matching");
  await page.tap(box);
  await page.type("zinnia");
  await page.waitFor(`document.querySelector(".find-none").textContent.includes("zinnia")`);
  await page.press("Escape");

  stage("sent");
  await page.evaluate(`document.querySelector("input[name=kind][value=leaf]").click()`);
  await page.evaluate(`document.querySelector("#swap form button[type=submit]").click()`);
  await page.waitFor(`document.querySelector("#swap .done") && [...document.querySelectorAll("#swap img")].some((i) => i.getAttribute("src") === "/steward/inbox/${b}/large.jpg")`);
  const redbud = await (await fetch(`${server.base}/steward/species/${plant["texas-redbud"]}/photos`, { headers })).text();
  assert.match(redbud, /Leaf close-up/, "the photo was not filed under the plant chosen");

  stage("the next photo's form, swapped in, and its button for the plant just used");
  await page.waitFor(`document.querySelector("${box}[role=combobox]")`);
  assert.equal(await page.evaluate(`window.sameDocument`), true, "saving loaded a page");
  const chip = `input[name=species][value='${plant["texas-redbud"]}']`;
  await page.waitFor(`document.querySelector("${chip}")`);
  await page.evaluate(`document.querySelector("${chip}").click()`);

  // The button and the list are one choice: choosing in the list lets go
  // of the button, and the button empties the list, so the form never
  // sends two plants.
  await page.tap(box);
  await page.type("wine");
  await page.waitFor(`document.querySelectorAll(".find-list [role=option]").length === 1`);
  await page.tap(".find-list [role=option]");
  await page.waitFor(`document.querySelector(".find-list").hidden`);
  assert.equal(await value("species_other"), plant.winecup);
  assert.equal(await page.evaluate(`document.querySelector("input[name=species]:checked")`), null, "the button is still chosen as well");

  await page.evaluate(`document.querySelector("${chip}").click()`);
  assert.equal(await value("species_other"), "", "the list kept its plant when the button was chosen");
  assert.equal(await page.evaluate(`document.querySelector("${box}").value`), "");

  assert.deepEqual(page.errors, [], "the page reported errors, or the header policy refused something");
});

test("a list the form needs is still needed, and asked for by the box", opts, async () => {
  const w = await fetch(`${server.base}/steward/places`, {
    method: "POST",
    body: new URLSearchParams({ slug: "rain-garden", name_en: "Rain garden" }),
    redirect: "manual",
    headers,
  });
  assert.equal(w.status, 303, `adding the place: ${w.status}\n${await w.text()}`);
  const places = await (await fetch(`${server.base}/steward`, { headers })).text();
  const place = /\/steward\/places\/([0-9a-f]{32})/.exec(places)?.[1];
  assert.ok(place, "the place is not on the list");

  await page.goto(`${server.base}/steward/places/${place}/plants`);
  await page.waitFor(`document.querySelector("#species[role=combobox]")`);

  const form = `document.querySelector("#species").form`;
  assert.equal(await page.evaluate(`${form}.checkValidity()`), false, "the form can be sent with no plant");
  assert.equal(await page.evaluate(`document.querySelector("#species").validationMessage`), "Choose one from the list.");
  assert.equal(await page.evaluate(`document.querySelector(".find-clear").hidden`), true, "a choice the form needs can be cleared");

  await page.tap("#species");
  await page.type("oak");
  await page.press("Enter");
  await page.waitFor(`document.querySelector(".find-list").hidden`);
  assert.equal(await page.evaluate(`${form}.checkValidity()`), true, "a plant was chosen and the form still asks for one");

  await page.evaluate(`${form}.requestSubmit()`);
  await page.waitFor(`document.readyState === "complete" && document.querySelector("#species[role=combobox]") && ![...document.querySelectorAll("select[name=species] option")].some((o) => o.value === "${plant["texas-red-oak"]}")`);

  assert.deepEqual(page.errors, [], "the page reported errors, or the header policy refused something");
});
