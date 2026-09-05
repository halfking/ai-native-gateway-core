# Database Environment Separation

**Version**: 2.0
**Updated**: 2026-08-27
**Status**: Active

## 1. Current Environments

| Environment | Role | Access | Data Policy |
|---|---|---|---|
| RDS production | Authoritative production database | DBA-approved access only | Never copy data out |
| 252 test | Integration and pre-release database | Controlled SSH tunnel | May sync to local |
| local | Docker container `llm-gateway-pg` on `127.0.0.1:5432` | Developer-owned | May be overwritten by 252 sync |

Current environment references are limited to RDS, 252, and local. Historical server topology belongs only in archived documentation.

## 2. Access Rules

### RDS Production

- Do not connect, migrate, or deploy without explicit DBA approval.
- Do not export or sync RDS data to another environment.
- Use environment injection and placeholders. Do not hardcode endpoints, users, or passwords.
- Require an approval ID, a rollback plan, and a post-change verification before an approved migration.

### 252 Test

- Use 252 for integration testing and local database refreshes.
- Load credentials through the envs loader before opening a tunnel.
- Treat test data as sensitive. Do not publish dumps, endpoints, or credentials in repository files.
- Test migrations here before a production approval request.

### Local Docker

- The canonical local database is `llm-gateway-pg`.
- Its `llm_gateway` user credentials must match the value exposed by `envs/common/database.yaml`.
- Recreate the container with `scripts/local-dev/recreate-llm-gateway-pg.sh` when the password environment variable drifts.
- Sync guidance: `docs/06-deployment/02-database/local-pg-sync-from-252.md`.

## 3. Allowed Synchronization

```text
RDS production  -- prohibited -->  other environments
252 test         -- allowed ---->  local Docker
local Docker     -- prohibited --> 252 or RDS
```

Run a full refresh only when local data can be overwritten:

```bash
source ~/workspace/ai-native-tools/envs/loader.sh --project llm-gateway-go
export PGOPTIONS='-c statement_timeout=0'
bash scripts/pg-table-copy.sh \
  --source configs/env-252.sh --target configs/env-local.sh
```

`PGOPTIONS` disables the 252-side statement timeout for large-table inspection. It affects only the invoking client session.

## 4. Local-Only Tables

Tables whose names begin with `_` are local-only pending-deletion objects. They are retained for audit and must not be referenced by application code, tests, migrations, or views.

Removal requires the database destructive-change process: impact analysis, independent audit, human approval, backup, then deletion.

## 5. Verification

After a 252-to-local refresh:

1. Confirm the Docker container is healthy.
2. Verify `llm_gateway` can connect with credentials from the envs loader.
3. Compare 252 and local public-table inventories, excluding local `_` tables.
4. Check key business tables such as `credentials`, `providers`, `users`, `api_keys`, and `applications` for matching schema and row counts.
5. Record the result in the database sync changelog.

## 6. Security Requirements

- Use `<env:KEY>` in committed documentation and configuration.
- Do not expose connection strings, server addresses, SSH destinations, or passwords.
- Keep local credential sources out of Git.
- Do not substitute static passwords into command history or generated reports.

## 7. References

- `docs/06-deployment/02-database/local-pg-sync-from-252.md`
- `scripts/pg-table-copy.sh`
- `scripts/local-dev/recreate-llm-gateway-pg.sh`
- `.agents/skills/db-sync-252-local/SKILL.md`
