// The send screen's script. It sends the photos chosen one request at a time,
// each shrunk first if it is larger than it needs to be (shrink.mjs), says how
// far it has got, and keeps the form still while it does.
//
// Without it the form posts the whole batch at once. That works, but it shows
// nothing for the minutes it takes, it can be changed underneath, and the
// first batch of fifteen sent that way ended on an error page from the proxy
// although every photo had arrived. Everything here improves on a form that
// works without it: if this file does not load -- or the browser knows no
// modules, which is how it is loaded -- only the progress is lost.
//
// Sending again is always safe. A photo already in the inbox is recognised by
// its contents and counted as "there already", never added twice, so every
// way this can fail ends in the same advice: press Send again. That holds for
// a shrunk photo too: the same photo shrinks to the same bytes in the same
// browser, which shrink_browser_test.mjs checks rather than assumes.
//
// No build step and no library: modules served by inboxapp as they are here,
// and written for the browsers on the stewards' phones. send_browser_test.mjs
// drives this page in a real browser against a real server.
import { shrink } from "./shrink.mjs";

// Photos shared from the phone's photos app arrive in the file input by way
// of share.mjs, which every steward's page loads (ShareScript), before
// anybody presses Send; from there they are sent as though they had been
// chosen.

(() => {
  const form = document.querySelector("form[data-send]");
  if (!form || !window.fetch || !window.FormData) {
    return;
  }

  const input = form.querySelector('input[type="file"]');
  const button = form.querySelector('button[type="submit"]');
  const status = document.getElementById("sending");
  const words = document.getElementById("sending-words");
  const bar = document.getElementById("sending-bar");
  const result = document.getElementById("sent");
  const max = Number(form.dataset.max) || 20;
  const label = button.textContent;

  // How long to wait before trying a photo again, each time it fails for
  // want of a signal or a server: about five minutes in all, and then it
  // stops and says so.
  //
  // It was twenty seconds, sized for a phone walking out from under the
  // canopy. The first share from the photos app met something else: a weak
  // signal whose uploads broke off ten and twenty seconds in, for three
  // minutes, after which the screen gave up -- and the steward pressed Send
  // a minute later and all seven went through. Five minutes outlasts a
  // signal like that, and the supervisor's restart of the app as well.
  const waits = [2000, 5000, 10000, 20000, 30000, 30000, 30000, 30000, 30000, 30000, 30000, 30000];

  // The photos this page has already had an answer for, kept or there
  // already, so that pressing Send again after a failure sends only the ones
  // that did not arrive. Sending one again would be safe -- the inbox knows
  // it -- but not free: on that same signal the first photo went up again in
  // full, two minutes, to be told it was there. By the File itself, which is
  // the same object for as long as it sits in the input; choosing the photos
  // again makes new ones, and those are sent and recognised as before.
  const delivered = new WeakSet();

  // A little longer than the server gives one photo to arrive, so a request
  // that has stalled for good is given up on rather than waited for forever.
  const patience = 6 * 60 * 1000;

  let busy = false;
  let awake = null;

  // Leaving the page part-way stops the batch where it is, so ask first.
  const stay = (ev) => {
    ev.preventDefault();
    ev.returnValue = "";
  };

  form.addEventListener("submit", async (ev) => {
    if (busy) {
      ev.preventDefault();
      return;
    }

    const chosen = Array.from(input.files || []);
    if (chosen.length === 0) {
      return; // the browser's own "required" says what to do
    }

    ev.preventDefault();
    clear(result);

    if (chosen.length > max) {
      say(result, "problem", `That is ${chosen.length} photos. Send up to ${max} at a time.`);
      return;
    }

    const files = chosen.filter((f) => !delivered.has(f));

    // The batch's where, place and note, read before the form is locked:
    // a disabled field is left out of a FormData.
    const fields = new FormData(form);
    fields.delete("photo");

    lock(true);

    let kept = 0;
    let already = chosen.length - files.length;
    const refused = [];
    let stop = null;

    for (let i = 0; i < files.length; i++) {
      progress(i, files.length, `Sending ${i + 1} of ${files.length}…`);

      const answer = await send(fields, files[i], i, files.length);

      if (answer.outcome === "kept") {
        kept++;
        delivered.add(files[i]);
      } else if (answer.outcome === "already") {
        already++;
        delivered.add(files[i]);
      } else if (answer.photo) {
        refused.push({ name: files[i].name, problem: answer.photo });
      } else {
        stop = answer.stop;
        break;
      }
    }

    if (!stop && refused.length === 0) {
      // All of them: the inbox says how many, as it does after a batch
      // sent without this script.
      progress(files.length, files.length, "Sent. Opening the inbox…");
      window.removeEventListener("beforeunload", stay);
      window.location.assign(`${form.dataset.done}?done=sent&n=${kept}&d=${already}`);
      return;
    }

    lock(false);

    if (kept + already > 0) {
      say(result, "done", sentWords(kept, already));
    }

    if (refused.length > 0) {
      const box = say(result, "problem", `${refused.length} not kept:`);
      const list = document.createElement("ul");
      for (const r of refused) {
        const item = document.createElement("li");
        const name = document.createElement("strong");
        name.textContent = r.name;
        item.append(name, `: ${r.problem}`);
        list.append(item);
      }
      box.append(list);
    }

    if (stop) {
      say(result, "problem", stop);
    } else {
      // Every photo had its answer, so choosing them again would only send
      // the refused ones to be refused again.
      input.value = "";
    }

    result.scrollIntoView({ block: "start" });
  });

  // send is one photo, tried again after a pause when the signal or the
  // server fails, which is what a phone at the far end of the trail meets.
  // It answers {outcome}, {photo: why it was not kept}, or {stop: why the
  // batch cannot go on}.
  async function send(fields, file, i, n) {
    // Once, before any try: shrinking the same photo again gives the same
    // bytes, but there is no need to spend the phone's time finding out.
    const photo = await shrink(file);

    const body = new FormData();
    for (const [k, v] of fields) {
      body.append(k, v);
    }
    body.append("photo", photo, file.name);

    for (let attempt = 0; ; attempt++) {
      let answer = null;

      try {
        const res = await fetch(form.dataset.send, {
          method: "POST",
          body,
          credentials: "same-origin",
          headers: { Accept: "application/json" },
          signal: AbortSignal.timeout ? AbortSignal.timeout(patience) : undefined,
        });
        answer = await read(res);
      } catch {
        answer = null; // no signal, or no answer in time
      }

      if (answer) {
        return answer;
      }

      if (attempt >= waits.length) {
        return {
          stop: `The connection is gone. Press Send again when you have a signal: ` +
            `the photos already sent are in the inbox, and only the rest will be sent.`,
        };
      }

      progress(i, n, attempt < 3
        ? `The signal dropped on ${i + 1} of ${n}. Trying again…`
        : `The signal keeps dropping on ${i + 1} of ${n}. Still trying; keep this page open…`);
      await pause(waits[attempt]);
    }
  }

  // pause waits ms, or less when the phone says it is back online after
  // saying it was not: there is no point waiting out thirty seconds of a
  // signal that has already come back.
  function pause(ms) {
    return new Promise((done) => {
      const back = () => {
        clearTimeout(timer);
        done();
      };
      const timer = setTimeout(() => {
        window.removeEventListener("online", back);
        done();
      }, ms);
      window.addEventListener("online", back, { once: true });
    });
  }

  // read turns an answer into one of send's, or null for "try again".
  async function read(res) {
    const json = (res.headers.get("Content-Type") || "").startsWith("application/json");

    if (res.redirected || (res.ok && !json) || (res.status === 403 && !json)) {
      // The session ran out while sending. A write without one is a bare
      // 403 (mid.Require); a redirect to the sign-in page is here in case
      // that ever changes.
      return {
        stop: `Your sign-in has run out. Sign in again in a new tab, then come back and press Send: ` +
          `the photos already sent will not be added twice.`,
      };
    }

    if (!json) {
      if (res.status === 413) {
        return { photo: "Too large to send. Send it as the camera saved it, not a video." };
      }
      if (res.status >= 500) {
        return null; // the proxy in front of the app, busy or restarting
      }
      return { stop: `The server would not take it (${res.status}). Open this page again and send once more.` };
    }

    const body = await res.json();

    if (res.ok) {
      return { outcome: body.outcome };
    }
    if (res.status >= 500) {
      return null;
    }

    const problem = (body.error && body.error.problem) || "It could not be kept.";
    if (body.error && body.error.field === "photo") {
      return { photo: problem };
    }

    // Not about one photo -- the place, the note -- so the same for every
    // one after it.
    return { stop: `${problem} Nothing more was sent; fix it and press Send again.` };
  }

  function lock(on) {
    busy = on;
    form.setAttribute("aria-busy", on ? "true" : "false");

    for (const el of form.elements) {
      el.disabled = on;
    }

    button.textContent = on ? "Sending…" : label;
    status.hidden = !on;

    if (on) {
      // Under the button that was pressed, which on a phone can be the
      // bottom edge of the screen: bring it up to where it will be read.
      status.scrollIntoView({ block: "center", behavior: "smooth" });
      window.addEventListener("beforeunload", stay);
      keepAwake();
    } else {
      window.removeEventListener("beforeunload", stay);
      if (awake) {
        awake.release().catch(() => {});
        awake = null;
      }
    }
  }

  // keepAwake asks the phone not to dim its screen while sending: a phone
  // that locks itself pauses the page, and the batch with it.
  function keepAwake() {
    if (navigator.wakeLock) {
      navigator.wakeLock.request("screen").then((lock) => {
        // A batch of one can finish before the phone answers.
        if (busy) {
          awake = lock;
        } else {
          lock.release().catch(() => {});
        }
      }, () => {});
    }
  }

  function progress(done, n, text) {
    words.textContent = text;
    bar.max = n;
    bar.value = done;
  }

  function say(where, kind, text) {
    const p = document.createElement("div");
    p.className = kind;
    p.setAttribute("role", kind === "problem" ? "alert" : "status");
    p.textContent = text;
    where.append(p);
    where.hidden = false;
    return p;
  }

  function clear(where) {
    where.replaceChildren();
    where.hidden = true;
  }

  // sentWords is inboxapp's sentWords, for a batch that ends on this page.
  function sentWords(kept, already) {
    let s;
    if (kept === 0) {
      s = "No new photos.";
    } else if (kept === 1) {
      s = "1 photo is in the inbox.";
    } else {
      s = `${kept} photos are in the inbox.`;
    }

    if (already === 1) {
      s += " 1 was there already.";
    } else if (already > 1) {
      s += ` ${already} were there already.`;
    }

    return s;
  }
})();
