# Incident 2026-07-09 — Request Logging 252

This runbook archive documents the incident response and resolution for the
request-logging failure on host `192.168.0.252` that began on 2026-07-09.

## Canonical record

See `docs/INCIDENT-2026-07-09-REQUEST-LOGGING-252.md` for the consolidated
timeline, root cause, fix, verification, and lessons-learned.

## Files preserved here

### root-runbooks/ — command-and-copy snippets

These were copy-pasteable instructions issued to operators during the incident
response. They are kept verbatim for historical reference but should NOT be
re-executed (target hosts and timestamps are baked in).

- `COPY_AND_RUN.md` — copy-paste runbook for terminal
- `EXECUTE_NOW.md` — immediate-execution commands
- `FIX_ONE_COMMAND.sh` — single-command fix attempt (ssh into 192.168.0.252)
- `READY_TO_FIX.md` — pre-fix readiness checklist
- `START_FIX_NOW.md` — start-of-fix prompt
- `i18n-fix-plan.md` — design plan for i18n TODO cleanup (related but distinct)

### scripts/ — one-off shell scripts

- `apply-fix-252.sh` — applies the SQL fix to host 252
- `complete-fix-252.sh` — wraps apply + verify for host 252
- `fix-252-local.sh` — local-machine variant of the fix
- `diagnose-request-logging.sh` — diagnostic queries (REUSABLE — kept for future incidents)
- `verify-request-logging-252.sh` — verification queries (REUSABLE — kept for future incidents)

### web-scripts/ — i18n TODO cleanup iteration history

These are incremental iterations of the i18n TODO cleanup. The final
algorithm lives in `i18n-cleanup.mjs` and `i18n-fix-flat.mjs` (both REUSABLE).
Other entries are intermediate one-offs retained as historical artefacts.

- `i18n-cleanup.mjs` — REUSABLE: dry-run/apply flags, multi-locale diff
- `i18n-fix-flat.mjs` — REUSABLE: per-file/locale scoping with --dry-run
- `clean-todos.mjs` — initial TODO-only sweep
- `final-cleanup.mjs` — superseded by i18n-cleanup.mjs
- `fix-chat-flat-keys.mjs`, `fix-chat-todos.mjs` — per-file iterations on chat.ts
- `fix-flat-keys.mjs`, `fix-flat-keys-all.mjs` — flat-key iteration
- `fix-models-todos.mjs` — per-file iteration on models.ts
- `fix-parity.mjs` — locale parity verification
- `fix-provider-detail.mjs`, `fix-provider-detail-all.mjs` — per-file iterations on providerDetail.ts
- `fix-remaining-todos.mjs` — final-pass iteration
- `remove-redundant-todos.mjs` — narrow redundant-marker sweep

## What is NOT here

The actual code fixes are tracked in regular git history, not in this
runbook archive:

- `web/src/**` destructure + non-null-assertion fixes — commit `631eecc6`
- `sql/migrations/startup/341_hot_table_independence.sql` — commits `cd80e4f3`, `1567cfd5`
- `sql/migrations/startup/345_request_wal_hot_independence.sql` — commits `1567cfd5`
- `domains/hooks/observability/telemetry/client.go` (affinity_hit) — commit `956f6b7f`
- `web/src/api/_core.ts` + `LoginView.vue` (avoid login loop) — commit `5911f1c9`