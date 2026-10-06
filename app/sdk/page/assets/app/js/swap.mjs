// The script that makes the stewards' one-photo-at-a-time screens quick: the
// check queue (photoapp) and sorting the inbox (inboxapp). Pressing the main
// button shows the next photo the moment the server has answered, rather
// than a page load and then a picture later.
//
// Why a script is needed for that at all: a picture nobody has checked or
// sorted yet is served no-store, on purpose -- it is a steward's alone and is
// not kept anywhere -- so a phone cannot be handed the next one from its
// cache, and a <link rel="prefetch"> fetches it for nothing. What can be kept
// is a picture already loaded into this page. So while the steward looks at
// one photo, this fetches the next one's page (data-next on #swap), loads its
// pictures into Image elements, and holds them. Pressing a button in a form
// marked data-swap posts it with fetch, and the page that comes back -- the
// one the form would have gone to -- is put in place of #swap with the held
// pictures moved into it, already decoded. A link marked data-swap (Skip)
// needs no post at all when it goes where the page fetched ahead is.
//
// Everything here improves on forms and links that work without it:
//
//   - An answer that is a page but not one of these screens -- the inbox's
//     list once the last photo is sorted, or the sign-in page when a session
//     has ended -- is gone to, as the browser would have.
//   - No answer at all, or a server error, sends the form the ordinary way.
//     That sends it twice, which each form takes as once: a Yes is
//     photobus.SetChecked, and a sort or a Change sheet sent again the same
//     says what it said the first time.
//   - If this file does not load, each press is a page load and the picture
//     follows it.
//
// It lives below the apps that use it, served by the page package under an
// address holding its hash, as the stylesheet is: two apps need it, and an
// app never imports another (CLAUDE.md). It knows #swap, data-next and
// data-swap, and nothing about photos.
//
// No build step and no library. photoapp's check_browser_test.mjs and
// inboxapp's sort_browser_test.mjs drive it in a real browser against a real
// server.

(() => {
  if (!document.getElementById("swap") || !window.fetch || !window.DOMParser) {
    return;
  }

  // Pictures loaded ahead, by address. Only those of the page shown and the
  // page fetched ahead are kept, so a long sitting holds two pages' worth.
  let held = new Map();

  // The page fetched ahead: its address, and a promise of it.
  let ahead = null;

  const absolute = (url) => new URL(url, location.href).href;

  // Elsewhere is a page that answered but is not one of these screens: the
  // browser is to go there.
  class Elsewhere extends Error {
    constructor(url) {
      super(`${url} is not a page to swap in`);
      this.url = url;
    }
  }

  // load fetches a page. 422 is one of these screens too, saying what to
  // fix. A server error, or no answer, throws plainly.
  async function load(url, init = {}) {
    const res = await fetch(url, { ...init, credentials: "same-origin", headers: { Accept: "text/html" } });
    if (res.status >= 500) {
      throw new Error(`the server answered ${res.status}`);
    }

    if (!res.ok && res.status !== 422) {
      throw new Elsewhere(res.url);
    }

    const doc = new DOMParser().parseFromString(await res.text(), "text/html");
    if (!doc.getElementById("swap")) {
      throw new Elsewhere(res.url);
    }

    return { url: res.url, doc };
  }

  // hold starts loading a page's pictures, and keeps them.
  function hold(doc) {
    for (const img of doc.querySelectorAll("#swap img")) {
      const src = absolute(img.getAttribute("src"));
      if (held.has(src)) continue;

      const picture = new Image();
      picture.src = src;
      picture.decode?.().catch(() => {});
      held.set(src, picture);
    }
  }

  function prefetch() {
    const next = document.getElementById("swap")?.dataset.next;
    if (!next) {
      ahead = null;
      return;
    }

    const url = absolute(next);
    if (ahead?.url === url) return;

    const ready = load(url).then((page) => {
      hold(page.doc);
      return page;
    });
    ready.catch(() => {
      if (ahead?.ready === ready) ahead = null;
    });
    ahead = { url, ready };
  }

  // show puts a fetched page in place of this one.
  function show(page) {
    const fresh = document.adoptNode(page.doc.getElementById("swap"));

    for (const img of fresh.querySelectorAll("img")) {
      const picture = held.get(absolute(img.getAttribute("src")));
      if (!picture) continue;

      for (const { name, value } of img.attributes) picture.setAttribute(name, value);
      img.replaceWith(picture);
    }

    document.getElementById("swap").replaceWith(fresh);
    document.title = page.doc.title;
    history.replaceState(null, "", page.url);

    const kept = new Map();
    for (const img of fresh.querySelectorAll("img")) {
      const src = absolute(img.getAttribute("src"));
      if (held.has(src)) kept.set(src, held.get(src));
    }
    held = kept;

    // Focus where a page load would have left the reader: on what just
    // happened, or what to fix, when there is something, which a screen
    // reader then reads; otherwise at the top.
    window.scrollTo(0, 0);
    (fresh.querySelector("[role=status], [role=alert]") ?? fresh.querySelector("h1"))?.focus({ preventScroll: true });

    prefetch();
  }

  document.addEventListener("submit", async (e) => {
    const form = e.target.closest("form[data-swap]");
    if (!form || !document.getElementById("swap").contains(form)) return;

    e.preventDefault();
    const buttons = [...form.querySelectorAll("button[type=submit], button:not([type])")];
    if (buttons.some((b) => b.disabled)) return;

    // What is sent is read before the buttons are stilled: a disabled
    // button is left out of a form's data, and a form with two -- the
    // Change sheet's "Save and check it" and "Save without checking" --
    // says which by the button's own name and value.
    const body = new URLSearchParams(new FormData(form, e.submitter));
    for (const b of buttons) b.disabled = true;

    try {
      show(await load(form.action, { method: "POST", body }));
    } catch (err) {
      if (err instanceof Elsewhere) {
        location.href = err.url;
        return;
      }

      // Sent the ordinary way, saying which button was pressed as the
      // button would have. Not requestSubmit, which would come back here.
      for (const b of buttons) b.disabled = false;
      if (e.submitter?.name) {
        const pressed = document.createElement("input");
        pressed.type = "hidden";
        pressed.name = e.submitter.name;
        pressed.value = e.submitter.value;
        form.append(pressed);
      }

      form.submit();
    }
  });

  document.addEventListener("click", async (e) => {
    const link = e.target.closest?.("a[data-swap]");
    if (!link || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;

    e.preventDefault();
    const url = absolute(link.href);

    try {
      show(await (ahead?.url === url ? ahead.ready : load(url)));
    } catch (err) {
      location.href = err instanceof Elsewhere ? err.url : url;
    }
  });

  hold(document);
  prefetch();
})();
