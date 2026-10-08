// The send screen's sentences, for its scripts to say.
//
// They are the app's copy like any other, translated by Claude into the
// page's language, so they are not written here: the server puts them in
// the page as JSON, under #send-words (inboxapp's scriptWords), with their
// {placeholders} left for the script to fill, since "Sending 3 of 15" is a
// different sentence every few seconds. inboxapp's
// TestTheScriptsAskForWordsThatAreThere checks that every name a script
// asks for here is one the server gives.

// fill puts values into text's {placeholders}, leaving any it is not given
// as they are, so that a missing value shows rather than vanishing.
export function fill(text, values = {}) {
  return text.replace(/\{([a-zA-Z]+)\}/g, (all, name) => (name in values ? String(values[name]) : all));
}

// sayer is the page's sentences as a function: sayer(doc)("sending", {n: 3,
// count: 15}). A name the page did not give comes back as the name, which is
// wrong to read and easy to see, rather than as nothing at all.
export function sayer(doc = globalThis.document) {
  let words = {};
  try {
    words = JSON.parse(doc.getElementById("send-words")?.textContent || "{}") || {};
  } catch {
    words = {};
  }

  return (name, values) => fill(typeof words[name] === "string" ? words[name] : name, values);
}
