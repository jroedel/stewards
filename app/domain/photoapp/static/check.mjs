// The check queue's script. It makes the next photo appear the moment Yes or
// Skip is pressed, rather than a page load and then a picture later.
//
// Why a script is needed for that at all: an unchecked picture is served
// no-store (photoapp, file), on purpose -- it is a steward's alone and is not
// kept anywhere -- so a phone cannot be handed the next one from its cache,
// and a <link rel="prefetch"> fetches it for nothing. What can be kept is a
// picture already loaded into this page. So while the steward looks at one
// photo, this fetches the next one's page (data-next on #check), loads its
// pictures into Image elements, and holds them. Pressing Yes posts the form
// with fetch, and the page that comes back -- the same one the form would
// have gone to, with "Checked" and Undo at the top -- is put in place of
// #check with the held pictures moved into it, already decoded. Skip needs
// no post at all: the page fetched ahead is that page.
//
// Everything here improves on forms and links that work without it. Any
// answer this cannot use -- an error, a page with no #check because the
// session ended -- sends the form or follows the link the ordinary way,
// and Yes sent twice is harmless (photobus.SetChecked). If this file does
// not load, each press is a page load and the picture follows it.
//
// No build step and no library, as for the inbox's scripts.
// check_browser_test.mjs drives it in a real browser against a real server.

(() => {
  if (!document.getElementById("check") || !window.fetch || !window.DOMParser) {
    return;
  }

  // Pictures loaded ahead, by address. Only those of the page shown and the
  // page fetched ahead are kept, so a long sitting holds two pages' worth.
  let held = new Map();

  // The page fetched ahead: its address, and a promise of it.
  let ahead = null;

  const absolute = (url) => new URL(url, location.href).href;

  // load fetches a page of the queue. 422 is the queue too, saying why a
  // photo could not be checked. Anything else, or a page that is not the
  // queue, is for the browser to show the ordinary way.
  async function load(url, init = {}) {
    const res = await fetch(url, { ...init, credentials: "same-origin", headers: { Accept: "text/html" } });
    if (!res.ok && res.status !== 422) {
      throw new Error(`the queue answered ${res.status}`);
    }

    const doc = new DOMParser().parseFromString(await res.text(), "text/html");
    if (!doc.getElementById("check")) {
      throw new Error("that was not the queue");
    }

    return { url: res.url, doc };
  }

  // hold starts loading a page's pictures, and keeps them.
  function hold(doc) {
    for (const img of doc.querySelectorAll("#check img")) {
      const src = absolute(img.getAttribute("src"));
      if (held.has(src)) continue;

      const picture = new Image();
      picture.src = src;
      picture.decode?.().catch(() => {});
      held.set(src, picture);
    }
  }

  function prefetch() {
    const next = document.getElementById("check")?.dataset.next;
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
    const fresh = document.adoptNode(page.doc.getElementById("check"));

    for (const img of fresh.querySelectorAll("img")) {
      const picture = held.get(absolute(img.getAttribute("src")));
      if (!picture) continue;

      for (const { name, value } of img.attributes) picture.setAttribute(name, value);
      img.replaceWith(picture);
    }

    document.getElementById("check").replaceWith(fresh);
    document.title = page.doc.title;
    history.replaceState(null, "", page.url);

    const kept = new Map();
    for (const img of fresh.querySelectorAll("img")) {
      const src = absolute(img.getAttribute("src"));
      if (held.has(src)) kept.set(src, held.get(src));
    }
    held = kept;

    // Focus where a page load would have left the reader: on what just
    // happened when something did, which a screen reader then reads, and
    // otherwise at the top.
    window.scrollTo(0, 0);
    (fresh.querySelector("[role=status], [role=alert]") ?? fresh.querySelector("h1"))?.focus({ preventScroll: true });

    prefetch();
  }

  document.addEventListener("submit", async (e) => {
    const form = e.target.closest("form[data-swap]");
    if (!form || !document.getElementById("check").contains(form)) return;

    e.preventDefault();
    const button = form.querySelector("button");
    if (button.disabled) return;
    button.disabled = true;

    try {
      show(await load(form.action, { method: "POST", body: new URLSearchParams(new FormData(form)) }));
    } catch {
      button.disabled = false;
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
    } catch {
      location.href = url;
    }
  });

  hold(document);
  prefetch();
})();
