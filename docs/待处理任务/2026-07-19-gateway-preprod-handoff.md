# LLM Gateway Pre-Production Handoff

## Status

- Repository: `llm-gateway-go`
- Branch: `main`
- Starting commit for the next task: `4008a6b80`
- Local worktree was clean when this handoff was written.

The local code and unit-test gate are complete. The remaining work is to
validate the release in pre-production before any production promotion.

## Completed Work

- `83a0bbbfe fix(ops): harden gateway deployment defaults`
  - Docker builds the real `cmd/gateway` entrypoint.
  - Docker and Compose use port `8781` and `/healthz`.
  - Compose requires gateway and Grafana secrets from environment variables.
  - Monitoring image tags are pinned.
  - `V1000` captures provider-model rows before changing them and includes a
    companion down migration.
- `4008a6b80 fix(credentialfpslot): restore nodes after cooldown`
  - Node recovery after cooldown is covered by the latest committed fix.
- The diagnostic-run drawer display work is not present in the final Git
  history. Reconfirm its product requirement before reimplementing it.

## Remaining Task

Deploy the current `main` revision to the 245 pre-production environment and
run the release gate. Do not deploy to production as part of this task.

Success criteria:

1. The 245 deployment completes through the repository deployment workflow.
2. The migration `V1000__fix_volcano_glm_outbound_mapping.sql` is applied only
   after a database backup/snapshot is confirmed.
3. L1 through L4 validation passes: service health, dependencies, functional
   request path, and real provider credential behavior.
4. The V1000 provider-model mapping is verified for both Volcano provider
   codes and the rollback file is ready if validation fails.
5. A deployment report records the release commit, validation evidence, and
   rollback result if one was required.

## Required Guardrails

- Load `env-injector` before using any environment-specific command.
- Load `deploy-245` and `llm-gateway-deploy-test` before deployment work.
- Production deployment is human-only. Stop after successful 245 validation
  and request explicit approval for any 154/production promotion.
- Never read, print, or commit real secret values. Use existing environment
  injection and placeholders such as `<env:LLM_GATEWAY_DATABASE_URL>`.
- Before applying V1000, verify the companion rollback file exists:
  `deploy/sql/migrations/V1000__fix_volcano_glm_outbound_mapping.down.sql`.
- If deployment validation fails, use the service rollback workflow before
  attempting another change.

## Verification Already Completed

- `go test ./...`
- `go build ./cmd/gateway`
- `npm run build` in `web/`
- `npx vitest run src/composables/useRouteIncidents.test.ts` in `web/`
- `docker compose -f docker-compose.yml config` with placeholder environment
  values
- `git diff --check` and commit-time Go/SQL checks

Known local tooling gaps:

- `gitleaks` was unavailable.
- `govulncheck` was unavailable.

## Suggested Commands

Run these after loading the required skills and injecting the target
environment. Replace no secret values manually.

```bash
git status --short --branch
git fetch origin main
git rev-list --left-right --count HEAD...origin/main
git log -1 --oneline
```

Then follow the deployment skill's documented 245 command. After deployment,
run its L1-L4 validation and record the results in a dated deployment report.

## Suggested Skills

- `env-injector`: inject approved 245 environment configuration.
- `deploy-245`: execute the pre-production deployment workflow.
- `llm-gateway-deploy-test`: run environment promotion and regression gates.
- `database-migrations`: review V1000 backup and rollback behavior.
- `verification-loop`: collect final command evidence before reporting.
- `security-review`: rerun secret and dependency checks when tooling is
  available.

## Continuation Prompt

```text
Continue the LLM Gateway 245 pre-production release gate from
docs/待处理任务/2026-07-19-gateway-preprod-handoff.md.

Start by reading the handoff, checking Git status and origin/main divergence,
then load env-injector, deploy-245, llm-gateway-deploy-test, and
verification-loop. Deploy only to 245, validate L1-L4 including V1000's
Volcano mapping, capture evidence, and stop before any production promotion.
Do not expose secrets. If any validation fails, use the documented rollback
workflow and report the exact failing layer.
```
