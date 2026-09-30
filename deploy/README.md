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
substituted, and never edited by hand on the server. Until `deploy.sh` exists
to do that, a person installs it the same way:

```sh
sed 's/__APP_PORT__/8451/' deploy/htaccess.template > .htaccess
# then copy .htaccess into public_html/stewards.schoenstatt-fathers.us/public/
```

Until the binary is running on the server, the subdomain answers with a proxy
error (502 or 503) rather than the app. That is expected.

## Still to come

`deploy.sh`, the supervisor and the deploy workflow, adapted from
mass-intentions. When they arrive they will read the docroot and port from the
GitHub variables `APP_DOCROOT` and `APP_PORT`, which a person sets.
