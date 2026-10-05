// A headless Chrome for the browser tests, driven over the DevTools protocol
// with what Node has built in -- a WebSocket and a child process -- and no
// npm package. Puppeteer and Playwright would each be a dependency tree, a
// download of their own browser, and a lockfile, for the dozen commands
// these tests use.
//
// Chrome is the one on the machine. CHROME names it; otherwise the usual
// names are looked for on PATH. A GitHub runner has google-chrome. A machine
// without one skips the browser tests and says so, except in CI, where a
// missing browser is a failure rather than a quiet pass.
import { spawn } from "node:child_process";
import { accessSync, constants, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";

const names = ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"];

// findChrome is the path of a Chrome to run, or "" when there is none.
export function findChrome() {
  if (process.env.CHROME) {
    return process.env.CHROME;
  }

  for (const dir of (process.env.PATH || "").split(delimiter)) {
    for (const name of names) {
      try {
        accessSync(join(dir, name), constants.X_OK);
        return join(dir, name);
      } catch {
        // not here
      }
    }
  }

  return "";
}

// skip is the reason to skip browser tests on this machine, or false to run
// them: always run in CI, so that there they fail rather than vanish.
export function skip() {
  if (process.env.CI || findChrome()) {
    return false;
  }

  if (!warned) {
    warned = true;
    console.warn("browser tests SKIPPED: no Chrome on this machine. Install one, or set CHROME to its path");
  }

  return "no Chrome on this machine: install one, or set CHROME to its path";
}

let warned = false;

// launch starts Chrome and resolves to {page, close}. page() opens a tab.
export async function launch() {
  const chrome = findChrome();
  if (!chrome) {
    throw new Error("no Chrome found: install one, or set CHROME to its path");
  }

  const profile = mkdtempSync(join(tmpdir(), "stewards-chrome-"));
  const args = [
    "--headless=new",
    "--remote-debugging-port=0",
    `--user-data-dir=${profile}`,
    "--no-first-run",
    "--no-default-browser-check",
    "--disable-extensions",
    "--disable-background-networking",
    "--window-size=412,915",
  ];

  // GitHub's Ubuntu runners do not allow Chrome the user namespaces its
  // sandbox is built from. The pages it opens here are this app's own.
  if (process.env.CI) {
    args.push("--no-sandbox");
  }

  const proc = spawn(chrome, [...args, "about:blank"], { stdio: ["ignore", "ignore", "pipe"] });

  const url = await new Promise((done, fail) => {
    let err = "";
    const timer = setTimeout(() => fail(new Error(`Chrome did not start in 20 s:\n${err}`)), 20_000);
    proc.stderr.on("data", (d) => {
      err += d;
      const m = /DevTools listening on (ws:\/\/\S+)/.exec(err);
      if (m) {
        clearTimeout(timer);
        done(m[1]);
      }
    });
    proc.on("exit", (code) => {
      clearTimeout(timer);
      fail(new Error(`Chrome exited (${code}) before it was ready:\n${err}`));
    });
  });

  const cdp = await connect(url);

  return {
    page: () => openPage(cdp),
    async close() {
      try {
        await cdp.send("Browser.close");
      } catch {
        // already gone
      }
      cdp.close();
      await new Promise((done) => {
        if (proc.exitCode !== null) return done();
        proc.on("exit", done);
        setTimeout(() => {
          proc.kill("SIGKILL");
          done();
        }, 5000);
      });
      rmSync(profile, { recursive: true, force: true });
    },
  };
}

// connect is one WebSocket to the browser, over which every tab is reached
// by its session id.
async function connect(url) {
  const ws = new WebSocket(url);
  await new Promise((done, fail) => {
    ws.addEventListener("open", done, { once: true });
    ws.addEventListener("error", () => fail(new Error(`could not reach Chrome at ${url}`)), { once: true });
  });

  let next = 1;
  const waiting = new Map();
  const listeners = new Set();

  ws.addEventListener("message", (m) => {
    const msg = JSON.parse(m.data);
    if (msg.id && waiting.has(msg.id)) {
      const { done, fail, method } = waiting.get(msg.id);
      waiting.delete(msg.id);
      msg.error ? fail(new Error(`${method}: ${msg.error.message}`)) : done(msg.result);
    } else if (msg.method) {
      for (const fn of listeners) fn(msg);
    }
  });

  return {
    send(method, params = {}, sessionId) {
      const id = next++;
      ws.send(JSON.stringify({ id, method, params, sessionId }));
      return new Promise((done, fail) => waiting.set(id, { done, fail, method }));
    },
    listen(fn) {
      listeners.add(fn);
    },
    close() {
      ws.close();
    },
  };
}

// openPage opens a tab, phone-sized, and gives the few things a test does
// with it. Every console error and uncaught exception is kept in errors, so
// a test can say there were none: a page whose script the header policy
// blocked looks, to everything else, like a page that works.
async function openPage(cdp) {
  const { targetId } = await cdp.send("Target.createTarget", { url: "about:blank" });
  const { sessionId } = await cdp.send("Target.attachToTarget", { targetId, flatten: true });
  const send = (method, params) => cdp.send(method, params, sessionId);

  const errors = [];
  cdp.listen((msg) => {
    if (msg.sessionId !== sessionId) return;
    if (msg.method === "Runtime.exceptionThrown") {
      errors.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
    } else if (msg.method === "Log.entryAdded" && msg.params.entry.level === "error") {
      errors.push(msg.params.entry.text);
    } else if (msg.method === "Runtime.consoleAPICalled" && msg.params.type === "error") {
      errors.push(msg.params.args.map((a) => a.value ?? a.description).join(" "));
    }
  });

  await send("Page.enable");
  await send("Runtime.enable");
  await send("Log.enable");
  await send("Network.enable");
  await send("DOM.enable");
  await send("Emulation.setDeviceMetricsOverride", { width: 412, height: 915, deviceScaleFactor: 2, mobile: true });

  const page = {
    send,
    errors,

    // evaluate runs expr in the page, awaiting it if it is a promise, and
    // gives back its value as JSON would.
    async evaluate(expr) {
      const { result, exceptionDetails } = await send("Runtime.evaluate", {
        expression: expr,
        awaitPromise: true,
        returnByValue: true,
      });
      if (exceptionDetails) {
        throw new Error(`in the page: ${exceptionDetails.exception?.description || exceptionDetails.text}`);
      }
      return result.value;
    },

    // goto loads url and waits for the page to have loaded.
    async goto(url) {
      await send("Page.navigate", { url });
      await page.waitFor(`document.readyState === "complete" && location.href !== "about:blank"`, 15_000);
    },

    // waitFor polls expr in the page until it is truthy, and gives back its
    // value; a navigation in the meantime is waited through.
    async waitFor(expr, timeout = 15_000) {
      const end = Date.now() + timeout;
      let last;
      while (Date.now() < end) {
        try {
          last = await page.evaluate(expr);
          if (last) return last;
        } catch (err) {
          last = err.message; // mid-navigation: try again
        }
        await new Promise((done) => setTimeout(done, 50));
      }
      throw new Error(`waited ${timeout} ms for ${expr}; last: ${JSON.stringify(last)}`);
    },

    // setFiles chooses files, by their paths on this machine, in a file input.
    async setFiles(selector, files) {
      const { root } = await send("DOM.getDocument");
      const { nodeId } = await send("DOM.querySelector", { nodeId: root.nodeId, selector });
      if (!nodeId) throw new Error(`no ${selector} on the page`);
      await send("DOM.setFileInputFiles", { nodeId, files });
    },

    async setCookie(cookie) {
      await send("Network.setCookie", cookie);
    },

    async screenshot() {
      const { data } = await send("Page.captureScreenshot", { format: "png" });
      return Buffer.from(data, "base64");
    },
  };

  return page;
}
