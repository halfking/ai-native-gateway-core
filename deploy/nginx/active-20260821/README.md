# Nginx Deployment Archive: 2026-08-21

## Scope

This archive records the SPA fallback fix and the active-passive gateway
failover rule for `llm.kxpms.cn`.

Normal traffic uses the 154 gateway. Nginx uses 245 as a backup gateway after
the primary has failed twice within the configured failure window.

## Backup Rule

| Role | Endpoint | Configuration |
|---|---|---|
| Primary | `<env:HOST_154_INTERNAL>:8781` | `max_fails=2 fail_timeout=30s` |
| Backup | `<env:HOST_245_INTERNAL>:8781` | `backup; max_fails=2 fail_timeout=30s` |

The rule is present in:

- `252 kxpms-on-252.conf` upstream `kxpms_llm_backend`
- `154 llm-kxpms-cn.conf` upstream `llm_local`

Nginx's default `proxy_next_upstream` behavior handles connection errors,
timeouts, and 502/503/504 responses. The 245 gateway remains passive and is
used only after the primary is unavailable.

## SPA Fallback

The catch-all SPA route serves the release web directory and falls back to
`index.html` for Vue history routes such as `/dashboard`, `/keys`, and
`/providers`. API and maintenance locations retain their higher-priority
matching rules.

The public entry path is:

```text
llm.kxpms.cn -> public SNI proxy -> 252 HTTP vhost -> 154 gateway
```

The 252 SPA fallback proxies to the 154 nginx SPA endpoint. It is intentionally
not configured to proxy SPA HTML to 245 because the 245 certificate currently
does not include the public `llm.kxpms.cn` name.

## Verification

- `nginx -t` passed on 252 and 154 after the backup rule was installed.
- `curl https://llm.kxpms.cn/dashboard` returned `HTTP 200 text/html`.
- A temporary nginx `down` flag on the 154 primary caused API requests to reach
  245; the 245 gateway log recorded requests during the failover window.
- Removing the flag and reloading nginx restored 154 as the primary.
- `/v1/chat/completions` continued to return the expected authentication 401
  without credentials.

## Rollback

Timestamped backups are stored on each server under
`/etc/nginx/conf.d/backups/`. Restore the relevant backup, run `nginx -t`, and
reload nginx. Do not use force-push or destructive git commands to roll back
the repository state.

## Follow-ups

- Add `llm.kxpms.cn` to the 245 certificate and server name before enabling SPA
  HTML failover to 245.
- Align the 154 and 245 release SHAs before using 245 for production traffic.
- Replace the hard-coded historical `menu-config.json` release alias on 154
  with the current release path.
- Consider an active health-check endpoint to reduce passive failover latency.
