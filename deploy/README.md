# Deploying

Nothing here is run by an agent (see `CLAUDE.md` §6). This directory holds
what a person, or CI, uses to put the app on the konsoleH account it shares
with mass-intentions.

## Where things live on the server

| What | Where | Why |
|---|---|---|
| Docroot for `stewards.schoenstatt-fathers.us` | `public_html/stewards.schoenstatt-fathers.us/public/` | Holds `.htaccess` and nothing else |
| The binary, `config.toml`, `stewards.db` | A directory **outside** `public_html` | So Apache can never serve the database or the config |
| The Go server | `127.0.0.1:8451` | Loopback only. Apache proxies to it; TLS is Apache's job |

## The front end

`htaccess.template` is the whole of Apache's configuration for this app: it
proxies every request to the loopback port with `mod_rewrite`'s `[P]` flag,
because shared hosting does not allow `ProxyPass` in `.htaccess`.
`scripts/htaccess-test.sh` pins the rules that matter and their order.

It is written into the docroot from this template, with `__APP_PORT__`
substituted, and never edited by hand on the server:

- **On every push to `main`**, by the `ship` job in
  `.github/workflows/deploy.yml`.
- **By hand**, with `make deploy-htaccess`.

Either way `deploy/deploy.sh htaccess` then checks from outside that Apache
is reading it: `http://` must redirect to `https://`, and `/healthz` must be
the app (200) or the proxy with nothing behind it yet (502/503). An Apache
403 or 404 there means the file was written to a directory the subdomain
does not serve -- check the docroot in konsoleH against `APP_DOCROOT`.

## Deploying

A push to `main` deploys. The `ship` job in `.github/workflows/deploy.yml`
runs `deploy/deploy.sh deploy`, which in order:

1. refuses an `APP_DIR` inside `public_html`, and checks over HTTPS that
   `config.toml`, the database, the log and the scripts are not served;
2. builds the static binary and uploads it beside the running one;
3. runs the **new** binary's `-check` against the **live** `config.toml`, and
   confirms it listens where Apache proxies -- so a config it would refuse is
   a deploy that changes nothing;
4. stops if a newer commit has reached `main` meanwhile (its own deploy
   follows);
5. reinstalls `.htaccess` and the cron watchdog;
6. under the supervisor's lock: stops the app, backs up `stewards.db`
   (keeping `KEEP_BACKUPS`), and swaps the binary;
7. starts it, and waits for `/healthz` on the loopback. If it does not start
   or does not answer, it **rolls back** to the last binary that did;
8. checks from outside that `/healthz` is 200 and `/` is answered by the app.

There is no separate install step: every deploy creates what it needs, so the
first push is the install. What it cannot create is `config.toml`, which a
person puts there with `make deploy-send-secrets`.

On the server, in `APP_DIR`:

| File | What |
|---|---|
| `stewards` | the binary. `.prev` is the one before, `.last-good` the latest that started and answered, `.failed` the latest that did not |
| `config.toml` | from `make deploy-send-secrets`; never edited on the server |
| `stewards.db` | the database, mode 0600 |
| `photo-files/` | the photos: each plant photo's large, small and original, and the inbox's in `inbox/`. Mode 0700 |
| `backups/` | per deploy, a copy of the database and a snapshot of `photo-files/`, the newest `KEEP_BACKUPS` of each kept. The snapshots are hard links, so a photo is stored once however many hold it; they keep a photo removed later, but are on the same disk, so a copy off the server is still a person's to take |
| `run.sh`, `supervise.sh` | start the binary, and keep it running from cron |
| `stewards.log` | the server's log |
| `deployed-commit.txt` | the commit that is live |

The watchdog is two crontab lines ending in `# stewards`. The crontab is
shared with mass-intentions, and each project's deploy leaves the other's
lines alone -- `scripts/deploy-test.sh` holds that.

## By hand

| Command | What |
|---|---|
| `make prod-status` | the process, the public checks, what is live, backups, the photos and their snapshots, the log |
| `make prod-logs N=200` | the tail of the log |
| `make prod-backup` | a backup now, database and photos (stops the app for a second or two) |
| `make prod-restart` | restart |
| `make deploy` | deploy from your machine, from a clean `main` |
| `make deploy-htaccess` | the front end only |
