// find.mjs's matching under Node's own test runner: `make test-js`. What
// only a browser can show -- the box, the list, a tap choosing, the form
// sending the choice -- is in find_browser_test.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";

import { find, fold, parts, words } from "./find.mjs";

// The plants as the sort screen lists them, by name.
const plants = [
  "Choose the plant",
  "Snake herb (Dyschoriste linearis)",
  "Texas bluebonnet (Lupinus texensis)",
  "Texas red oak (Quercus buckleyi)",
  "Texas redbud (Cercis canadensis var. texensis)",
  "Tree of heaven (Ailanthus altissima)",
  "Turk's cap (Malvaviscus arboreus var. drummondii)",
  "Turk's cap 'Pink' (Malvaviscus arboreus var. drummondii)",
  "Twisted-leaf yucca (Yucca rupicola)",
  "Winecup (Callirhoe involucrata)",
].map((label, i) => ({ value: i === 0 ? "" : `id${i}`, label, also: "", group: "" }));

const names = (query, options = plants) => find(options, query).map((r) => r.option.label.replace(/ \(.*/, ""));

test("a name is found by any word in it, not only its first", () => {
  assert.deepEqual(names("cap"), ["Turk's cap", "Turk's cap 'Pink'"]);
  assert.deepEqual(names("yucca"), ["Twisted-leaf yucca"]);
  assert.deepEqual(names("heaven"), ["Tree of heaven"]);
});

test("and by its scientific name", () => {
  assert.deepEqual(names("drum"), ["Turk's cap", "Turk's cap 'Pink'"]);
  assert.deepEqual(names("quercus"), ["Texas red oak"]);
});

test("case, accents, apostrophes and hyphens are not in the way", () => {
  assert.deepEqual(names("TURKS"), ["Turk's cap", "Turk's cap 'Pink'"]);
  assert.deepEqual(names("turk’s"), ["Turk's cap", "Turk's cap 'Pink'"]);
  assert.deepEqual(names("leaf"), ["Twisted-leaf yucca"]);
  assert.deepEqual(names("twisted-leaf"), ["Twisted-leaf yucca"]);
  assert.deepEqual(names("pink"), ["Turk's cap 'Pink'"]);

  const accented = [{ value: "a", label: "Ñandú (Café)", also: "", group: "" }];
  assert.deepEqual(names("nandu", accented), ["Ñandú"]);
  assert.deepEqual(names("cafe", accented), ["Ñandú"]);
});

test("every word typed must be there, in any order", () => {
  assert.deepEqual(names("tex red"), ["Texas red oak", "Texas redbud"]);
  assert.deepEqual(names("red tex"), ["Texas red oak", "Texas redbud"]);
  assert.deepEqual(names("tex oak"), ["Texas red oak"]);
  assert.deepEqual(names("tex pine"), []);
});

test("the start of the name first, then of a word, then inside one", () => {
  // "t": Texas... and Tree and Turk's and Twisted start with it; Snake
  // herb's linearis and Winecup's involucrata only have one inside.
  const t = names("t");
  assert.deepEqual(t.slice(0, 7), ["Texas bluebonnet", "Texas red oak", "Texas redbud", "Tree of heaven", "Turk's cap", "Turk's cap 'Pink'", "Twisted-leaf yucca"]);
  assert.deepEqual(t.slice(7), ["Snake herb", "Winecup"]);

  // "bonnet" only inside bluebonnet; "cup" only inside Winecup.
  assert.deepEqual(names("bonnet"), ["Texas bluebonnet"]);
  assert.deepEqual(names("cup"), ["Winecup"]);

  // "ru" starts rupicola, a word, and is only inside drummondii: the yucca
  // comes before the Turk's caps it would follow by name.
  assert.deepEqual(names("ru"), ["Twisted-leaf yucca", "Turk's cap", "Turk's cap 'Pink'"]);
});

test("nothing typed is the whole list, less the empty choice", () => {
  assert.equal(find(plants, "").length, plants.length - 1);
  assert.equal(find(plants, "  ").length, plants.length - 1);
  assert.ok(!find(plants, "").some((r) => r.option.value === ""));
  assert.deepEqual(names("choose"), [], "the empty choice is never a match");
});

test("data-also is matched but never marked, a little after the name", () => {
  const options = [
    { value: "a", label: "Copper canyon daisy (Tagetes lemmonii)", also: "", group: "" },
    { value: "b", label: "Winecup (Callirhoe involucrata)", also: "Copa de vino", group: "" },
  ];

  const got = find(options, "copa");
  assert.deepEqual(got.map((r) => r.option.value), ["b"]);
  assert.deepEqual(got[0].marks, []);

  // A word starting the name beats the same word starting the hidden one.
  assert.deepEqual(find(options, "co").map((r) => r.option.value), ["a", "b"]);
});

test("the stretches to mark are in the label as written", () => {
  const [cap] = find(plants, "turks drum");
  const label = cap.option.label;
  assert.deepEqual(cap.marks.map(([s, e]) => label.slice(s, e)), ["Turk's", "drum"]);

  const [twisted] = find(plants, "leaf twis");
  assert.deepEqual(twisted.marks.map(([s, e]) => twisted.option.label.slice(s, e)), ["Twis", "leaf"]);

  // Two words over the same letters are one mark.
  const [oak] = find(plants, "red re");
  assert.deepEqual(oak.marks.map(([s, e]) => oak.option.label.slice(s, e)), ["red"]);
});

test("a group keeps its place, and is ordered within itself", () => {
  const options = [
    { value: "a", label: "Big muhly", also: "", group: "Not here yet" },
    { value: "b", label: "Lindheimer's muhly", also: "", group: "Not here yet" },
    { value: "c", label: "Gulf muhly", also: "", group: "Already growing here" },
    { value: "d", label: "Muhly 'Regal Mist'", also: "", group: "Already growing here" },
  ];

  assert.deepEqual(find(options, "muh").map((r) => r.option.value), ["a", "b", "d", "c"]);
});

test("a label is split into its name and what is in brackets", () => {
  assert.deepEqual(parts("Turk's cap (Malvaviscus arboreus)"), { name: "Turk's cap", sub: "Malvaviscus arboreus", at: 12 });
  assert.equal("Turk's cap (Malvaviscus arboreus)".slice(12, 23), "Malvaviscus");
  assert.deepEqual(parts("Winecup"), { name: "Winecup", sub: "", at: -1 });
  assert.equal(parts("Turk's cap 'Pink' (Malvaviscus)").name, "Turk's cap 'Pink'");
});

test("folding keeps where each character came from", () => {
  const f = fold("Turk’s Ñ-1");
  assert.equal(f.text, "turks n 1");
  assert.deepEqual(f.from, [0, 1, 2, 3, 5, 6, 7, 8, 9]);
  assert.deepEqual(words("  Tex,  RED "), ["tex", "red"]);
});
