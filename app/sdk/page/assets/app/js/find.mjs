// A long list of plants that can be searched: a <select data-find> becomes
// a box to type in, and the list under it shows only the plants with what
// was typed somewhere in their names. "cap" finds Turk's cap, "drum" the
// Turk's caps by their scientific name, "bonnet" Texas bluebonnet, and "tex
// red" Texas redbud and Texas red oak. With a hundred plants a <select> is a
// long scroll on a phone, and it can only be jumped through by a name's
// first letter, which is the part of the name a steward is least sure of.
//
// The <select> stays in the form, hidden, and is what is sent: every choice
// made here is made on it, and a page whose script does not load keeps the
// plain <select>, which works as it always has. The server needs to know
// nothing about this file.
//
// How a name is matched, and in what order the matches come:
//
//   - Case, accents and apostrophes are ignored, and any other punctuation
//     is a space: "turks" finds Turk's cap, and "leaf" Twisted-leaf yucca.
//   - Each word typed must be found in the name, in any order. A name that
//     starts with what was typed comes first, then one with a word that
//     starts with it, then one that only has it inside a word ("bonnet").
//     Within each, the list's own order, which is by name.
//   - data-also on an <option> is more to match by that is not shown, such
//     as the plant's Spanish name. It is matched a little after the name.
//   - An option with no value ("Choose the plant") is never a match. A
//     choice is cleared with the box's clear button instead, where the
//     <select> may be left empty.
//   - In a <select> with <optgroup>s, each group keeps its place and its
//     heading, and is ordered within itself.
//
// data-find holds the box's placeholder. data-find-with names radio buttons
// in the same form that are the same choice made another way -- the sort
// screen's buttons for the plants used last -- so that choosing here clears
// them and choosing one of them clears this, and the form never sends two
// plants.
//
// Like swap.mjs it lives below the apps, served by the page package under an
// address holding its hash, and knows nothing about plants beyond its words.
// The matching is exported for find_test.mjs, under Node; find_browser_test.mjs
// is the rest, in Chrome. A <select data-find> that arrives later, as when
// swap.mjs puts the next photo's form in place, is found by watching the page.

// fold is text as it is matched: lower case, without accents or apostrophes,
// anything not a letter or a digit a space. from is, for each character of
// it, where in the text it came from, so that a match can be marked on the
// name as it is shown.
export function fold(text) {
  let folded = "";
  const from = [];

  for (let i = 0; i < text.length; ) {
    const ch = String.fromCodePoint(text.codePointAt(i));

    let f = "";
    if (!/['’‘ʼ`]/u.test(ch)) {
      f = ch.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();
      if (!/^[\p{L}\p{N}]+$/u.test(f)) f = " ";
    }

    for (const c of f) {
      folded += c;
      from.push(i);
    }

    i += ch.length;
  }

  return { text: folded, from };
}

// words is what was typed, as the words to look for.
export const words = (query) => fold(query).text.split(" ").filter(Boolean);

// place is where a word is found best in folded text, and how well: 0 at the
// very start, 1 at the start of a word, 2 inside one. null if it is not.
function place(word, text) {
  let best = null;

  for (let at = text.indexOf(word); at !== -1; at = text.indexOf(word, at + 1)) {
    const tier = at === 0 ? 0 : text[at - 1] === " " ? 1 : 2;
    if (!best || tier < best.tier) best = { at, tier };
    if (tier < 2) break;
  }

  return best;
}

// find is the options matching what was typed, best first, each with the
// stretches of its label to mark: [start, end) in the label as written.
// options are { value, label, also, group }, in the list's order; group is
// the heading an option is under, or "".
export function find(options, query) {
  const want = words(query);
  const groups = [...new Set(options.map((o) => o.group))];

  if (want.length === 0) {
    return options.filter((o) => o.value !== "").map((option) => ({ option, marks: [] }));
  }

  const found = [];

  options.forEach((option, index) => {
    if (option.value === "") return;

    const label = fold(option.label);
    const also = fold(option.also ?? "").text;
    const tiers = [];
    const marks = [];

    for (const word of want) {
      const inLabel = place(word, label.text);
      const inAlso = place(word, also);

      if (inLabel && (!inAlso || inLabel.tier <= inAlso.tier + 0.5)) {
        tiers.push(inLabel.tier);

        const last = label.from[inLabel.at + word.length - 1];
        marks.push([label.from[inLabel.at], last + String.fromCodePoint(option.label.codePointAt(last)).length]);
      } else if (inAlso) {
        tiers.push(inAlso.tier + 0.5);
      } else {
        return;
      }
    }

    found.push({ option, marks: merge(marks), key: [groups.indexOf(option.group), Math.max(...tiers), tiers.reduce((a, b) => a + b), index] });
  });

  found.sort((x, y) => {
    for (let i = 0; i < x.key.length; i++) {
      if (x.key[i] !== y.key[i]) return x.key[i] - y.key[i];
    }

    return 0;
  });

  return found.map(({ option, marks }) => ({ option, marks }));
}

// merge puts stretches in order and joins those that overlap or touch.
function merge(marks) {
  const out = [];

  for (const [start, end] of marks.toSorted((x, y) => x[0] - y[0])) {
    const last = out.at(-1);
    if (last && start <= last[1]) last[1] = Math.max(last[1], end);
    else out.push([start, end]);
  }

  return out;
}

// parts splits "Turk's cap (Malvaviscus arboreus)" into the name and what
// is in brackets after it, shown under it, smaller. at is where the second
// starts in the label, for the marks; a label with no brackets is all name.
export function parts(label) {
  const m = /^(.*\S)\s*\(([^()]+)\)$/su.exec(label);
  if (!m) return { name: label, sub: "", at: -1 };

  return { name: m[1], sub: m[2], at: label.lastIndexOf("(") + 1 };
}

// ---------------------------------------------------------------- the page

// marked is text as nodes, with the stretches given in <mark>. offset is
// where the text starts in the label the marks are of.
function marked(text, offset, marks) {
  const nodes = [];
  let at = 0;

  for (const [start, end] of marks) {
    const s = Math.max(start - offset, at);
    const e = Math.min(end - offset, text.length);
    if (e <= s) continue;

    if (s > at) nodes.push(document.createTextNode(text.slice(at, s)));

    const m = document.createElement("mark");
    m.textContent = text.slice(s, e);
    nodes.push(m);
    at = e;
  }

  if (at < text.length) nodes.push(document.createTextNode(text.slice(at)));

  return nodes;
}

let made = 0;

function enhance(select) {
  if (select.closest(".find")) return;

  const n = ++made;
  const form = select.form;
  const required = select.required;
  const options = [...select.options].map((o) => ({
    value: o.value,
    label: o.text.trim(),
    also: o.dataset.also ?? "",
    group: o.parentElement.tagName === "OPTGROUP" ? o.parentElement.label : "",
  }));
  const hint = select.dataset.find || "Type any part of the name";
  const twin = select.dataset.findWith;

  const box = document.createElement("div");
  box.className = "find";

  // The box takes the <select>'s id, so that its <label for> names the box
  // now, and its description.
  const input = document.createElement("input");
  input.type = "text";
  input.id = select.id || `find-${n}`;
  select.removeAttribute("id");
  input.placeholder = hint;
  input.autocomplete = "off";
  input.spellcheck = false;
  input.setAttribute("autocapitalize", "none");
  input.setAttribute("autocorrect", "off");
  input.enterKeyHint = "done";
  input.setAttribute("role", "combobox");
  input.setAttribute("aria-autocomplete", "list");
  input.setAttribute("aria-expanded", "false");
  input.setAttribute("aria-controls", `find-${n}-list`);
  if (select.hasAttribute("aria-describedby")) input.setAttribute("aria-describedby", select.getAttribute("aria-describedby"));

  const clear = document.createElement("button");
  clear.type = "button";
  clear.className = "find-clear";
  clear.setAttribute("aria-label", "Clear the choice");
  clear.hidden = true;

  const list = document.createElement("ul");
  list.id = `find-${n}-list`;
  list.className = "find-list";
  list.setAttribute("role", "listbox");
  list.hidden = true;

  const none = document.createElement("p");
  none.className = "find-none";
  none.setAttribute("role", "status");

  // A hidden <select> that is required would stop the form with a message
  // nobody can see. The box asks instead.
  select.required = false;
  select.hidden = true;
  select.tabIndex = -1;
  select.before(box);
  box.append(input, clear, list, none, select);

  const chosen = () => options.find((o) => o.value === select.value && o.value !== "");

  // active is the option Enter would choose, by its index in what is shown.
  let shown = [];
  let active = -1;

  function settle() {
    const c = chosen();
    input.value = c?.label ?? "";
    input.placeholder = hint;
    clear.hidden = !c || required;
    input.setCustomValidity(required && !c ? "Choose one from the list." : "");
  }

  function setActive(i) {
    list.querySelector("[aria-current]")?.removeAttribute("aria-current");
    active = i;

    const li = shown[i]?.li;
    if (!li) {
      input.removeAttribute("aria-activedescendant");
      return;
    }

    li.setAttribute("aria-current", "true");
    input.setAttribute("aria-activedescendant", li.id);
    li.scrollIntoView({ block: "nearest" });
  }

  function show() {
    const query = input.value;
    const results = find(options, query);
    const c = chosen();

    list.replaceChildren();
    shown = [];

    let heading = null;
    results.forEach(({ option, marks }, i) => {
      if (option.group && option.group !== heading) {
        heading = option.group;
        const h = document.createElement("li");
        h.className = "find-group";
        h.setAttribute("role", "presentation");
        h.textContent = heading;
        list.append(h);
      }

      const li = document.createElement("li");
      li.id = `find-${n}-${i}`;
      li.setAttribute("role", "option");
      li.dataset.value = option.value;
      li.setAttribute("aria-selected", String(option === c));

      const { name, sub, at } = parts(option.label);
      const strong = document.createElement("span");
      strong.className = "find-name";
      strong.append(...marked(name, 0, marks));
      li.append(strong);

      if (sub) {
        const small = document.createElement("span");
        small.className = "find-sub";
        small.append(...marked(sub, at, marks));
        li.append(small);
      }

      list.append(li);
      shown.push({ option, li });
    });

    list.hidden = false;
    input.setAttribute("aria-expanded", "true");
    none.textContent = results.length === 0 ? `Nothing has “${query.trim()}” in its name.` : "";

    // Typing puts the best match under Enter. With nothing typed, the
    // plant chosen already is where the list opens.
    if (query.trim()) {
      setActive(results.length ? 0 : -1);
    } else {
      setActive(-1);
      shown.find((s) => s.option === c)?.li.scrollIntoView({ block: "nearest" });
    }
  }

  function close() {
    list.hidden = true;
    list.replaceChildren();
    none.textContent = "";
    shown = [];
    active = -1;
    input.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-activedescendant");
  }

  function choose(value) {
    const changed = select.value !== value;
    select.value = value;
    close();
    settle();

    if (value && twin) {
      for (const radio of form?.querySelectorAll(`input[type=radio][name="${CSS.escape(twin)}"]`) ?? []) radio.checked = false;
    }

    if (changed) select.dispatchEvent(new Event("change", { bubbles: true }));
  }

  // Opened, the box is emptied for typing, and what was chosen is the
  // placeholder meanwhile; leaving it without choosing puts it back. It
  // opens on the focus, and again on a tap or a key in a box that still has
  // the focus after Escape -- or what is typed would go on the end of the
  // plant's name.
  function begin() {
    const c = chosen();
    if (c && input.value === c.label) {
      input.value = "";
      input.placeholder = c.label;
    }

    show();
  }

  input.addEventListener("focus", () => {
    // On a phone the keyboard takes the lower half of the screen: the box
    // goes to the top, so the list has the half that is left. At once, not
    // smoothly: a page still gliding is one where the next tap lands on
    // whatever has slid under the finger, such as the link below the box.
    if (matchMedia("(pointer: coarse)").matches) box.scrollIntoView({ block: "start" });

    begin();
  });

  input.addEventListener("click", () => {
    if (list.hidden) begin();
  });

  input.addEventListener("beforeinput", () => {
    if (list.hidden) begin();
  });

  input.addEventListener("input", show);

  input.addEventListener("blur", () => {
    close();
    settle();
  });

  input.addEventListener("keydown", (e) => {
    const open = !list.hidden;

    switch (e.key) {
      case "ArrowDown":
      case "ArrowUp":
        e.preventDefault();
        if (!open) show();
        if (shown.length) {
          const step = e.key === "ArrowDown" ? 1 : -1;
          setActive(active === -1 ? (step === 1 ? 0 : shown.length - 1) : (active + step + shown.length) % shown.length);
        }
        break;
      case "Enter":
        // Enter in the box chooses, and never sends the form half done.
        if (open && (active !== -1 || input.value.trim())) {
          e.preventDefault();
          if (active !== -1) choose(shown[active].option.value);
        }
        break;
      case "Escape":
        if (open) {
          e.preventDefault();
          close();
          settle();
        }
        break;
    }
  });

  // A press on the list must not take the focus from the box first, or the
  // list would close before the tap landed.
  list.addEventListener("pointerdown", (e) => e.preventDefault());
  list.addEventListener("mousedown", (e) => e.preventDefault());
  list.addEventListener("click", (e) => {
    const li = e.target.closest("[role=option]");
    if (!li) return;

    choose(li.dataset.value);
    input.blur();
  });

  clear.addEventListener("click", () => {
    choose("");
    input.focus();
  });

  if (twin && form) {
    form.addEventListener("change", (e) => {
      if (e.target.type === "radio" && e.target.name === twin && e.target.checked && select.value !== "") {
        select.value = "";
        settle();
      }
    });
  }

  settle();
}

function enhanceAll(root) {
  if (root.matches?.("select[data-find]")) enhance(root);
  for (const select of root.querySelectorAll?.("select[data-find]") ?? []) enhance(select);
}

if (typeof document !== "undefined") {
  enhanceAll(document);

  new MutationObserver((records) => {
    for (const r of records) {
      for (const node of r.addedNodes) {
        if (node.nodeType === Node.ELEMENT_NODE) enhanceAll(node);
      }
    }
  }).observe(document.documentElement, { childList: true, subtree: true });
}
