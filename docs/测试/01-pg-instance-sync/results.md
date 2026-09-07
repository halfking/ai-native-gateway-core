# PostgreSQL Instance Sync Results

## 2026-09-08 real-instance result

### Inventory and backups

- Local `llm-gateway-pg` and remote 252 `pg-252-pg17` were inventoried through
  the managed tunnel.
- 45 custom-format backups (375 MB) passed SHA-256 and `pg_restore -l`
  validation.
- Missing databases were restored from verified backups: local `kxmemory`;
  remote `acc_swarm_db`, `ai_alyy`, `identity_shadow`, and `maintain_db`.
- The 02:50 completion inventory classified every included database as
  `COMMON` or the approved `smm_data` → `smm` alias. No missing included
  database remained.

### Schema result

- Additive schema restore completed for `acc_db`, `kaixuan`, `maintain`,
  `pocket`, `postgres`, `memora`, and `redclaw`.
- The approved empty-table `acc_db` column conflicts, dependent view, and
  tenant policy were aligned to local in one transaction.
- The only non-`llm_gateway` signature differences are deparse-only:
  whitespace in `acc_db.public.orchestration_active_tenants` and equivalent
  PostgreSQL casts in two `redclaw` CHECK constraints.
- The owner/GRANT dimension remains intentionally deferred under the approved
  `semantic_only` conflict scope.

### `llm_gateway` protection and SSOT scope

- Every data pass reported `SKIPPED_SCHEMA_ONLY` for `llm_gateway`.
- The hashed public-object allowlist added 12 relations and four required
  parent-table columns to 252. The new base tables contained zero rows.
- The final additive pass added five SSOT columns and the independent rule FK
  to local.
- The remaining allowlisted semantic difference is intentionally gated:
  `prompt_injection_detections.risk_level` is varchar locally and integer in
  SQL SSOT/252, so its dependent numeric CHECK was not applied.
- `llm_gateway.maintain` is comparison-only here. Its authoritative migrations
  belong to `ai-native-maintain/internal/migrations`.

### Data result

- Initial insert-only passes converged all write-compatible databases after
  schema alignment.
- A fresh completion dry-run found all 24 non-gateway pairs eligible.
- The completion apply succeeded for all 24 pairs using explicit-column
  `INSERT ... ON CONFLICT DO NOTHING`; conflicts retained 252 rows and no
  update or delete statements were generated.
- Post-apply checks returned zero FK orphans and zero disabled user triggers
  for every target database.

### Guardrails and validation

- Manifest freshness binds generation time, local/remote inventory SHA-256,
  and policy SHA-256.
- Full `llm_gateway` bootstrap and all gateway data modes are blocked.
- Schema and data writes require `--yes`, a matching manifest hash, and a
  freshness check.
- Keyless non-empty tables block on data drift.
- ShellCheck passed for all instance-sync scripts.
- CLI, command, guardrail, allowlist, inventory, and tunnel tests passed.
- Generated constraint/index DDL was validated transactionally against local
  PostgreSQL.
- The allowlist hash regression test invokes the real schema executor.

## Current implementation boundary

- `pg-instance-sync.sh` now dispatches `apply-schema`, `apply-data`, `verify`,
  and `restore`. Unified `all` remains fail-closed.
- `verify` checks remote FK orphans and disabled user triggers.
- `restore` creates a new database from a checksummed dump and refuses
  `llm_gateway` plus any existing target.
