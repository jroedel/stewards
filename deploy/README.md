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

## Still to come

Shipping the binary: the `deploy` command in `deploy.sh` (swap, supervisor,
cron, backups, rollback), adapted from mass-intentions, as a second step of
the `ship` job. Until it lands, `make deploy-status` says "not ready" and
names it.
