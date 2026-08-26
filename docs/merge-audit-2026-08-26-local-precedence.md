# Merge Audit: Local Precedence

## Scope

Merge commit `7b3e69bf6` combined local parent `f6e492886` with remote parent
`4b512d640`. The remote parent carried a broad WIP snapshot with interfaces
that did not match the local implementation and tests.

## Resolution Rule

Keep the local implementation executable. Retain the remote source in Git as
an immutable audit reference. Do not delete source paths merely because they
are not selected for the executable path.

## Approval Resume Migration

| Local | Remote | Executable Resolution |
|---|---|---|
| `553_approval_resume_claim.sql` | `551_approval_resume_claim.sql` | Execute local `553` only. Both describe the same approval-resume claim DDL; executing both would duplicate the migration. |

Remote source:

```bash
git show 4b512d640:sql/migrations/startup/551_approval_resume_claim.sql
```

## Interface Families Restored Locally

The following files were restored from local parent `f6e492886` because the
remote WIP referenced a different, incomplete API surface. The remote version
remains recoverable with `git show 4b512d640:<path>`.

| Area | Local executable reason |
|---|---|
| `admin/` routing and live stream | Remote referred to absent Redis, async worker, and refresh DB members. |
| `bg/credential_probe_v2*` | Remote probe consumer referred to binding-only helpers that were not carried with it. |
| `domains/streaming/` | Remote handlers and rate limiter disagreed on request context, alias, cost, and admission signatures. |
| `modelcatalog`, `modelname`, `ratelimit` | Remote callers and local type definitions had incompatible model and limiter contracts. |
| `cmd/gateway/main_dispatch*` | Remote projection added a tenant argument while local callers remained three-argument. |

## Follow-up

Before selectively re-enabling any remote WIP area, compare its full dependency
closure against local parent and add tests for the chosen contract. Do not copy
individual call sites without their corresponding types and helpers.
