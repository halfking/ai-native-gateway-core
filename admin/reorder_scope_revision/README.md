# Reorder scope revision — persistent version

Status: **DRAFT** (2026-08-19)

## What's here

- `DESIGN.md` — full design document: motivation, schema, trigger, Go-side
  changes, deploy order, risk analysis, test plan, open questions.
- `migration_541_draft.sql` — migration SQL draft matching the design.
  This is **not yet wired into the schema manager**. It needs:
  - review + approval
  - copy into `sql/migrations/startup/541_candidate_binding_scope_revision.{sql,down.sql}`
  - the matching `541_*_test.go` if the project enforces migration tests
  - confirmation that `pgcrypto` (`digest` function) is already in the
    production database — if not, the migration needs `CREATE EXTENSION`.

## Why this directory is here

The handoff at `/tmp/handoff-20260819-030243.md` §5.2 listed the upgrade
"from computed hash → persistent version number" as the next planned step
after the reorder hardening landed in `396d259db`. The integration tests
could not run because `LLM_GATEWAY_PG_URL` is unset in this environment
(see `admin/routing_candidate_binding_test.go:281`), so this session made
concrete progress on the **design** side and left the implementation for
the next session that has PG available.

## Out of scope here

The Go-side code change (`admin/routing.go` — `loadScopeRevision`,
`parseScopeRevision`, the call-site swap in `handleRoutingCandidateBindingReorder`
and `handleRoutingResolve`) is sketched in `DESIGN.md §3.4` but not
committed. It should be implemented alongside the migration in a single
PR, then validated against the integration suite with `LLM_GATEWAY_PG_URL`
set.
