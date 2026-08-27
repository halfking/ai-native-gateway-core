# Deploy Upgrade Page Status

## Scope

This change updates the `deploy-154.sh` and `deploy-245.sh` deployment flow through the shared `deploy-seamless.sh` and `deploy-lib/host.sh` implementation. The wrapper scripts remain unchanged because they delegate to the shared orchestrator.

## Implementation

- `scripts/deploy-lib/maintenance-template.html` is the self-contained Chinese upgrade page. It displays the current version, target version, build time, and automatic five-second refresh.
- `host_show_upgrade_banner` renders the page locally, uploads it through the existing SSH command, atomically renames it into place, and creates `maintenance/UPGRADING` only after the page is complete.
- `host_hide_upgrade_banner` removes the marker and page after the deployment verification gates pass.
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

These nginx files must be installed and validated before using the new deployment flow. The repository does not have access to the target hosts in this session, so production `nginx -t`, reload, and live deployment were not executed here.

## Verification

- `bash -n scripts/deploy-lib/host.sh scripts/deploy-seamless.sh tests/deploy_host_test.sh` passed.
- `bash tests/deploy_host_test.sh all` passed: **31 passed, 0 failed**.
- Static nginx checks passed: balanced braces, one upgrade error route, one internal upgrade location, and one exact location for each health probe in every modified vhost source.
- `git diff --check` passed.

## Operational Rollout

1. Install the relevant 154/245/252 nginx source configuration on the corresponding hosts.
2. Run `nginx -t` on each host and reload nginx before the first deployment using the marker.
3. Run a 245 deployment and verify that the marker page appears during restart and disappears only after all gates pass.
4. Run a 154 deployment and verify both the target-host and 252 public ingress paths.
5. If a deployment and rollback both fail, investigate the retained `maintenance/UPGRADING` marker before manually removing it.

## Audit Notes

- No temporary HTTP server, extra port, or background process is used.
- The page is written before the marker, preventing partial-page responses.
- Existing unrelated working-tree changes were intentionally excluded from this snapshot.
