# PostgreSQL instance sync

Date: 2026-09-08

## Scope

- Inventory and deterministic manifest generation for local and 252 PostgreSQL.
- Verified backup and missing-database bootstrap.
- Additive-only schema synchronization with conflict review.
- Insert-only data merge for non-`llm_gateway` databases.

## Safety decisions

- `llm_gateway` data merge and full bootstrap are permanently blocked.
- Manifest execution requires matching inventory and policy hashes.
- The allowlist hash comparison is covered through the executable schema
  entry point, preventing Bash conditional parsing regressions.
- Data apply uses bounded statement and lock timeouts.
- Generated constraints and indexes are safe to retry.
- Keyless-table signatures use exact identifiers, including metacharacters.

## Schema ownership

- This repository's SQL SSOT owns `llm_gateway.public`.
- The public allowlist is SSOT-grounded, but additive DDL is read from the
  reviewed local database snapshot.
- `llm_gateway.maintain` is comparison-only here; its DDL is owned and applied
  by `ai-native-maintain/internal/migrations`.

## Verification

- ShellCheck passed for all changed sync scripts.
- Sync CLI, command, guardrail, allowlist, and inventory tests passed.
- Generated constraint and index DDL was executed twice safely in a rollback
  transaction against the local PostgreSQL instance.
- A fresh completion pass found no missing included databases, applied
  insert-only convergence to all 24 eligible pairs, and returned zero FK
  orphans and zero disabled user triggers on every target.
- Follow-up audit wired the dispatcher, added `verify`/`restore`, split the
  env/write libraries, and distilled the reusable skill.
- Pre-merge audit against latest `main`: `main` was already an ancestor of
  this branch (zero divergent commits, no conflicts). ShellCheck 0.11.0
  reported zero findings at warning severity across all 14 changed scripts,
  and all seven sync/tunnel test suites passed on the merged tree. No code
  corrections were required; the fail-closed guardrails (manifest hash and
  provenance, `llm_gateway` three-layer data protection, allowlist hash
  enforcement, ownership-safe tunnel teardown) were re-verified by test.
