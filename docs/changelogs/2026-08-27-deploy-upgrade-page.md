# Deploy Upgrade Page Status

## Scope

This change updates the `deploy-154.sh` and `deploy-245.sh` deployment flow through the shared `deploy-seamless.sh` and `deploy-lib/host.sh` implementation. The wrapper scripts remain unchanged because they delegate to the shared orchestrator.

## Implementation

- `scripts/deploy-lib/maintenance-template.html` is the self-contained Chinese upgrade page. It displays the current version, target version, build time, and automatic five-second refresh.
- `host_show_upgrade_banner` renders the page locally, uploads it through the existing SSH command, atomically renames it into place, and creates `maintenance/UPGRADING` only after the page is complete.
- `host_hide_upgrade_banner` removes the page first, verifies it is gone, and removes the marker last; a failed cleanup leaves the marker protecting traffic.
- `deploy-seamless.sh` enables the page immediately before the stop/restart boundary. It keeps the marker through health, local nginx, database, admin-login, and running-release checks.
- A failed deployment keeps the marker when rollback cannot be verified, so an unverified release is not exposed. A successful deployment or successful verified rollback removes it.
- For target `154`, the same lifecycle also protects the public `llm.kxpms.cn` ingress on `252`, which terminates TLS and proxies to the gateway. Target `245` uses its own vhost.

## Nginx Contract

The 154, 245, and 252 vhost sources define:

- `error_page 503 =200 /__llm_gateway_upgrade.html`;
- an internal static location backed by the maintenance page;
- an `UPGRADING` marker check on gateway-facing routes and the public root;
- exact `/healthz` and `/readyz` locations that continue to proxy to the real gateway probes;
- unchanged independent Maintain and license-authority routes.

These nginx files have since been installed and validated on the real hosts (see Live Rollout Results below); the 252 variant additionally required a root-path `error_page` fix, tracked in its own section.

## Verification

- `bash -n scripts/deploy-lib/host.sh scripts/deploy-seamless.sh tests/deploy_host_test.sh` passed.
- `bash tests/deploy_host_test.sh all` passed: **32 passed, 0 failed**.
- Audit fixes: direct rollback now rejects missing, unverified, and already-active releases; upgrade-page cleanup deletes the marker only after page removal succeeds.
- Static nginx checks passed: balanced braces, one upgrade error route, one internal upgrade location, and one exact location for each health probe in every modified vhost source.
- `git diff --check` passed.

## Operational Rollout

1. Install the relevant 154/245/252 nginx source configuration on the corresponding hosts.
2. Run `nginx -t` on each host and reload nginx before the first deployment using the marker.
3. Run a 245 deployment and verify that the marker page appears during restart and disappears only after all gates pass.
4. Run a 154 deployment and verify both the target-host and 252 public ingress paths.
5. If a deployment and rollback both fail, investigate the retained `maintenance/UPGRADING` marker before manually removing it.

## Live Rollout Results (2026-08-27 21:13–21:38 CST)

All steps above were executed on the real hosts.

- Config install: 245 `llmgo.kxpms.cn`, 154 `llm-kxpms-cn`, and 252 `kxpms-on-252` vhosts installed with per-host backups, `nginx -t` clean, and reloaded. `/opt/llm-gateway-go/maintenance/` (245/154) and `/var/www/llm-gateway-maintenance/` (252, newly created, 755 root) are in place.
- Manual marker drill (245): with `UPGRADING` present, `/`, `/api/admin/models`, and `/v1/models` returned the upgrade page (HTTP 200, `x-llm-gateway-upgrade: in-progress`, `cache-control: no-store`); `/healthz` stayed a real gateway probe and `/readyz` returned the gateway's own 401. After the hide order (page first, marker second) the real SPA and API returned immediately.
- 245 deployment (`1771-80a52caf`, built from `80a52caff` in a clean worktree): public watcher captured the full lifecycle — real SPA until the switch, upgrade page for ~18 s spanning the restart (`healthz` 502 then 200 while the page persisted), real SPA restored only after admin-login and release-identity gates. Total 48 s, switch 0 s, release marked verified.
- 154 production deployment (`1772-80a52caf`): dual-point banners enabled on 154 and 252 before stop, all gates passed, banners removed in reverse order (252 first). DNS-path watcher: upgrade page 21:36:06–21:37:49 (~103 s, including a 93 s restart through the jump host); 252-direct watcher: upgrade page 21:36:05–21:37:48. Both `healthz` probes stayed real throughout (502/timeout during restart, 200 after). Post-deploy: both maintenance directories empty, release verified, service active.
- DNS note: `llm.kxpms.cn` currently resolves to 47.97.111.154 directly, so the "public" path is 154's own vhost; the 252 vhost was verified via `--resolve` and keeps dual-ingress protection valid for future DNS flips back to 252.

## 252 Root-Path Fix (commit 6fa0e3173)

The first live drill exposed a bug specific to the 252 vhost: `location = /` defines a local `error_page 418` (the `/?login=*` gateway entry), which blocks inheritance of the server-level `error_page 503` mapping; with `recursive_error_pages` off, the named-location fallback cannot re-trigger error handling either. Result: with the marker present, `/` and `/?login=*` served nginx's default 503 body instead of the upgrade page. The fix restates `error_page 503 =200 /__llm_gateway_upgrade.html` inside `location = /` (both active-20260821 variants); because the marker `if` precedes the 418 check, both paths are covered. Verified live on 252: `/`, `/?login=*`, `/v1/*`, `/api/admin/*` return the upgrade page; `/healthz` stays a real probe; `/maintain/` and its static assets are unaffected.

## Coordination Note

A parallel task hardened the same vhosts with static-asset long-cache locations (commit `bfe748dc3`) and redistributed them at 21:28 after the initial template installs here had overwritten earlier hand-edits. The final live state on all three hosts matches the committed templates including both changes (md5-verified for 245).

## Audit Notes

- No temporary HTTP server, extra port, or background process is used.
- The page is written before the marker, preventing partial-page responses.
- Existing unrelated working-tree changes were intentionally excluded from this snapshot.
