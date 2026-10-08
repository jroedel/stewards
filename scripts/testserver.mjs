// A server of this repository's for a browser test: built, started on a free
// port with a database and photo directory of its own, and signed into with
// its one-time bootstrap secret, as the first steward signs in.
//
// Shared by the browser tests that need the real thing -- the send screen's
// script and the header policy it runs under, the zoom pages' pictures as the
// server makes them -- rather than each starting its own the same way.
import { execFileSync, spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { stage } from "./browser.mjs";

const repo = resolve(dirname(fileURLToPath(import.meta.url)), "..");

const freePort = () =>
  new Promise((done) => {
    const s = createServer();
    s.listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close(() => done(port));
    });
  });

// startServer gives back the server's base address, a steward's session
// cookie, its directory (photo-files/ is under it), log() for everything it
// has written so far, and stop().
export async function startServer(name) {
  const dir = mkdtempSync(join(tmpdir(), `stewards-${name}-`));
  let appLog = "";
  let app;

  const stop = () => {
    app?.kill();
    rmSync(dir, { recursive: true, force: true });
  };

  try {
    stage("building the server");
    execFileSync("go", ["build", "-o", join(dir, "stewards"), "./cmd/stewards"], { cwd: repo, stdio: "inherit", timeout: 180_000 });

    const port = await freePort();
    const base = `http://127.0.0.1:${port}`;
    const secret = randomBytes(24).toString("hex");
    writeFileSync(
      join(dir, "config.toml"),
      `[server]\naddr = "127.0.0.1:${port}"\nbase_url = "${base}"\n[db]\npath = "${join(dir, "stewards.db")}"\n[auth]\nbootstrap_secret = "${secret}"\n`,
    );

    stage("starting it");
    app = spawn(join(dir, "stewards"), ["-config", join(dir, "config.toml")], { stdio: ["ignore", "pipe", "pipe"] });
    app.stdout.on("data", (d) => (appLog += d));
    app.stderr.on("data", (d) => (appLog += d));

    for (let i = 0; ; i++) {
      try {
        if ((await fetch(`${base}/healthz`, { signal: AbortSignal.timeout(2000) })).ok) break;
      } catch {
        // not listening yet
      }
      if (i > 100) throw new Error(`the server did not start:\n${appLog}`);
      await new Promise((done) => setTimeout(done, 100));
    }

    stage("signing in");
    const signIn = await fetch(`${base}/sign-in/first`, {
      method: "POST",
      redirect: "manual",
      signal: AbortSignal.timeout(10_000),
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({ email: "steward@example.org", secret }),
    });
    const cookie = signIn.headers.getSetCookie().map((c) => /^__Host-session=([^;]+)/.exec(c)).find(Boolean)?.[1];
    if (!cookie) throw new Error(`the bootstrap sign-in gave no session: ${signIn.status}\n${appLog}`);

    return { base, cookie, dir, log: () => appLog, stop };
  } catch (err) {
    stop();
    throw err;
  }
}

// signedIn is the cookie for page.setCookie, so Chrome is that steward too.
export const sessionCookie = (base, cookie) => ({ name: "__Host-session", value: cookie, url: `${base}/`, secure: true, httpOnly: true, path: "/" });

// translate gives words already in the app their translation, as Claude
// does: through the API, with a key the steward makes for it. pairs maps an
// original, as it was written in English, to its Spanish.
export async function translate({ base, cookie }, pairs) {
  const signal = AbortSignal.timeout(30_000);
  const keys = await fetch(`${base}/steward/keys`, {
    method: "POST",
    signal,
    headers: { Cookie: `__Host-session=${cookie}`, "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ name: "a browser test" }),
  });
  const key = /<p class="key">(stw_[^<]+)<\/p>/.exec(await keys.text())?.[1];
  if (!key) throw new Error(`no key was made: ${keys.status}`);

  const api = { Authorization: `Bearer ${key}`, "Content-Type": "application/json" };
  // The key is worked out here, as translationbus.Key does, rather than
  // looked up in the pending list: that list gives 200 at most, and with
  // every screen's words waiting on a fresh database, which 200 a test's
  // words fall among is luck. An original misspelt here has no translation
  // waiting, and the PUT refuses it.
  const key16 = (text) => createHash("sha256").update(text).digest("hex").slice(0, 16);
  const translations = Object.entries(pairs).map(([en, es]) => ({ key: key16(en), from: "en", text: es }));

  const put = await fetch(`${base}/api/v1/translations`, { method: "PUT", headers: api, signal, body: JSON.stringify({ translations }) });
  const { results } = await put.json();
  if (put.status !== 200 || results.length !== Object.keys(pairs).length || results.some((r) => r.outcome === "refused")) {
    throw new Error(`translating: ${put.status} ${JSON.stringify(results)}`);
  }
}
