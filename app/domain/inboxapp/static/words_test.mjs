// words.mjs under Node's own test runner: make test-js.
import assert from "node:assert/strict";
import { test } from "node:test";

import { fill, sayer } from "./words.mjs";

test("fill puts each value in its place", () => {
  assert.equal(fill("Sending {n} of {count}…", { n: 3, count: 15 }), "Sending 3 of 15…");
  assert.equal(fill("Enviando {n} de {count}…", { count: 15, n: 3 }), "Enviando 3 de 15…");
});

test("fill leaves a placeholder it is not given", () => {
  assert.equal(fill("The server would not take it ({status}).", {}), "The server would not take it ({status}).");
  assert.equal(fill("No placeholders."), "No placeholders.");
});

// A document with only what sayer reads.
const page = (json) => ({ getElementById: (id) => (id === "send-words" && json !== null ? { textContent: json } : null) });

test("sayer says the page's sentences, filled", () => {
  const say = sayer(page(JSON.stringify({ sending: "Enviando {n} de {count}…" })));
  assert.equal(say("sending", { n: 1, count: 2 }), "Enviando 1 de 2…");
});

test("sayer says the name when the page did not give the sentence", () => {
  assert.equal(sayer(page(null))("opening"), "opening");
  assert.equal(sayer(page("not json"))("opening"), "opening");
  assert.equal(sayer(page(JSON.stringify({ opening: 7 })))("opening"), "opening");
});
