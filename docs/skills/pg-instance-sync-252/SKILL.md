---
name: pg-instance-sync-252
description: Safely align local Docker llm-gateway-pg with 252 pg-252-pg17. Use when inventorying both instances, filling missing databases, applying additive schema, insert-only merging data, verifying FK integrity, or rolling back from a verified dump. Never copies llm_gateway data.
---

# PostgreSQL instance sync (local ↔ 252)

## When to use

- Local `llm-gateway-pg` and 252 `pg-252-pg17` have missing databases or
  schema/data drift.
- The user asks to sync instance structure, merge local-new rows to 252, or
  reuse the instance-sync scripts.

## Hard gates

- Wait for explicit confirmation before any write.
- `llm_gateway` is `SCHEMA_ONLY`. Never merge data or fully bootstrap it.
- Data writes are `INSERT ... ON CONFLICT DO NOTHING` only.
- Additive schema never applies `CONFLICT_LOCAL_WINS` or owner/GRANT.
- `llm_gateway.public` additive objects need a hashed allowlist.
- `llm_gateway.maintain` DDL is owned by `ai-native-maintain`.
- Rollback creates a **new** database. Never DROP/overwrite an existing one.

## Pipeline

Use `scripts/pg-instance-sync.sh` as the dispatcher:

1. `inventory --output-dir <dir>`
2. `plan --local-inventory ... --remote-inventory ... --policy configs/pg-sync-policy.conf --manifest ...`
3. `scripts/pg-instance-impact.sh` and review the matrix
4. `backup` then `scripts/pg-instance-bootstrap.sh` for missing DBs
5. `apply-schema` then `apply-data` (both need `--yes --manifest-hash --freshness-check --impact-matrix --work-dir`)
6. `verify --output-dir ...` (FK orphans and disabled triggers must be 0)
7. `restore` only into a new database name

`all` is intentionally not auto-run.

## Policy

`configs/pg-sync-policy.conf`:

- Exclude `test|e2e|bench|dev` and `llm_gateway_sync_*`
- Alias `smm_data=smm`
- Schema allowlist `llm_gateway=public,maintain` is comparison scope only

## Do not

- Replay `sql/schema/01-schema.sql` on a populated `llm_gateway`
- Use `pg-instance-schema-additive.sh` as the deterministic gateway migration
  path; prefer `scripts/apply-db-revision-sequence.sh`
- Treat owner/GRANT or deparse-only CHECK/function whitespace as semantic drift
- Ask the user for passwords; load them with `envs/loader.sh`

## Tests

```bash
bash scripts/test-pg-instance-sync.sh
bash scripts/test-pg-instance-sync-commands.sh
bash scripts/test-pg-instance-sync-guardrails.sh
bash scripts/test-pg-instance-allowlist.sh
bash scripts/test-pg-instance-inventory.sh
bash scripts/test-pg-instance-verify.sh
bash tests/db252_tunnel_test.sh
```
