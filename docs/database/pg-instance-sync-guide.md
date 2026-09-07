# PostgreSQL Instance Sync Tool Guide

## Overview

The PostgreSQL instance sync tool (`scripts/pg-instance-sync.sh`) provides safe, controlled synchronization of database schemas and data between PostgreSQL instances, with special protections for production data.

Current status:
- Implemented: inventory, deterministic plan, impact matrix, backup,
  missing-database bootstrap, additive schema, insert-only merge, verify,
  and restore-into-new-database.
- Fail-closed: unified `all` (steps have different risk gates) and automatic
  `CONFLICT_LOCAL_WINS` application.
- `llm_gateway` data is never copied; its schema scope is `public,maintain`.

## Commands

### Read-Only Commands

#### `inventory`
Collect database lists from local and remote instances.
```bash
pg-instance-sync.sh inventory --yes
```

**Safety Requirements:**
- Requires `--yes` flag for explicit confirmation
- Does not modify any databases
- Outputs database lists to timestamped files in `tmp/`

#### `plan`  
Generate deterministic sync manifest from database inventories.
```bash
pg-instance-sync.sh plan \
  --local-inventory local.tsv \
  --remote-inventory remote.tsv \
  --policy policy.conf \
  --manifest manifest.tsv
```

#### `verify`
Verify sync results and validate database states.
```bash
pg-instance-sync.sh verify --manifest manifest.tsv \
  --policy configs/pg-sync-policy.conf --output-dir verify/ \
  --impact-matrix impact/impact-matrix.tsv
```
It fails if any included remote database has FK orphans or disabled user
triggers. Remaining non-owner/non-GRANT signature rows are written to
`deferred-semantic.tsv` for review.

### Write Commands

All write commands require:
- `--yes` flag for explicit confirmation
- `--manifest-hash <hash>` for integrity verification
- `--freshness-check` to validate manifest age

#### `backup`
Create backup before sync operations.
```bash
pg-instance-sync.sh backup --yes --manifest manifest.tsv \
  --manifest-hash <hash> --freshness-check \
  --policy configs/pg-sync-policy.conf --output-dir <backup-dir>
```

#### `apply-schema`
Apply schema changes (DDL operations).
```bash
pg-instance-sync.sh apply-schema --yes --manifest-hash <hash> --freshness-check --manifest manifest.tsv
```
```bash
pg-instance-sync.sh apply-schema --yes --freshness-check \
  --manifest manifest.tsv --manifest-hash <hash> \
  --policy configs/pg-sync-policy.conf \
  --impact-matrix impact/impact-matrix.tsv --work-dir <work-dir>
```
The dispatcher calls `scripts/pg-instance-schema-additive.sh`. The standalone
executor remains available:
It applies only `ADD_LOCAL`/`ADD_REMOTE` objects. Relations are restored in
`pre-data` and `post-data` transactions with functions between the two
sections. It never applies `CONFLICT_LOCAL_WINS` and always skips
`llm_gateway` unless a hashed, reviewed public-object allowlist is supplied:
```bash
ALLOWLIST=configs/pg-sync-llm-ssot-allowlist.txt
scripts/pg-instance-schema-additive.sh --yes --freshness-check \
  --database llm_gateway --llm-ssot-allowlist "$ALLOWLIST" \
  --llm-ssot-allowlist-hash "$(shasum -a 256 "$ALLOWLIST" | awk '{print $1}')" \
  --manifest manifest.tsv --manifest-hash <hash> \
  --policy configs/pg-sync-policy.conf \
  --impact-matrix impact/impact-matrix.tsv --work-dir <work-dir>
```
The allowlist anchors durable `public` object names to this repository's SQL,
but the executor still obtains DDL from the reviewed live local database. It
is therefore SSOT-grounded, not a deterministic SQL-to-DDL generator. For the
curated deterministic `public` migration path, use
`scripts/apply-db-revision-sequence.sh`.

This repository does not own `llm_gateway.maintain` DDL. That schema must be
migrated by `ai-native-maintain/internal/migrations`; the policy includes it
for comparison only. Local integration objects and expired time partitions
remain excluded from the public allowlist.

#### `apply-data`
Apply data sync (DML operations with strict limitations).
```bash
pg-instance-sync.sh apply-data --yes --manifest-hash <hash> --freshness-check --manifest manifest.tsv
```
The dispatcher calls `scripts/pg-instance-data-merge.sh`. The standalone
executor remains available:
```bash
scripts/pg-instance-data-merge.sh --yes --freshness-check \
  --manifest manifest.tsv --manifest-hash <hash> \
  --policy configs/pg-sync-policy.conf \
  --impact-matrix impact/impact-matrix.tsv --work-dir <work-dir>
```
It requires identical table/column write contracts, uses explicit-column
`INSERT ... ON CONFLICT DO NOTHING`, blocks drifting keyless tables, and only
raises sequence floors.

### Impact and missing-database bootstrap

```bash
scripts/pg-instance-impact.sh --manifest manifest.tsv \
  --policy configs/pg-sync-policy.conf --output-dir impact/

scripts/pg-instance-bootstrap.sh --yes --freshness-check \
  --manifest manifest.tsv --manifest-hash <hash> \
  --policy configs/pg-sync-policy.conf \
  --backup-index <backup-dir>/backup-index.tsv --output-dir bootstrap/
```

Bootstrap refuses to run if the target database already exists and validates
the selected backup checksum before creating the database.

#### `restore`
Restore a checksummed dump into a **new** database name. Existing targets and
`llm_gateway` are refused.

#### `all`
Intentionally not auto-run. Use inventory → plan → impact → backup →
bootstrap → apply-schema → apply-data → verify.

## Policy Configuration

The sync policy is defined in `configs/pg-sync-policy.conf`:

### Database Classification Rules

- **EXCLUDE_DB_REGEX**: Databases matching this pattern are excluded
- **SCHEMA_ONLY_DBS**: These databases sync schema only, never data
- **DB_ALIASES**: Local to remote database name mappings
- **LOCAL_ONLY_ALLOWLIST**: Databases that can be created on remote if missing

### Safety Controls

- **REQUIRE_MANIFEST_HASH**: Ensures manifest integrity
- **REQUIRE_FRESHNESS_CHECK**: Validates manifest age
- **MAX_MANIFEST_AGE_HOURS**: Maximum allowed manifest age
- **DESTRUCTIVE_OPERATIONS_REQUIRE_CONFIRMATION**: Manual review for DROP operations

## Database Classifications

| Classification | Description | Schema Sync | Data Sync |
|---------------|-------------|-------------|-----------|
| COMMON | Exists on both instances | ✓ | Conditional |
| LOCAL_ONLY | Only on local instance | ✓ | Insert-only |
| REMOTE_ONLY | Only on remote instance | Bootstrap | Full copy |
| ALIAS | Different names, same data | ✓ | Conditional |
| EXCLUDED | Skipped by policy | ✗ | ✗ |

## Critical Safety Features

### LLM Gateway Protection
- **llm_gateway** databases are permanently protected from data operations
- Only `SCHEMA_ONLY` mode is permitted
- Any attempt to modify llm_gateway data is blocked

### Hash Verification
- All write operations require manifest hash verification
- Prevents execution with stale or modified manifests
- Detects concurrent modifications

### Freshness Checks
- Manifests older than 1 hour trigger warnings
- Prevents execution with outdated database state assumptions

### Destructive Operation Guards
- DROP TABLE/INDEX/CONSTRAINT operations require manual confirmation
- Generated as separate "destructive plan" for review
- Never executed automatically

## Environment Configuration

### Local Environment (`configs/env-local.sh`)
- Docker container: `llm-gateway-pg`
- Connection via local Docker daemon
- User: `llm_gateway` (superuser role)

### Remote Environment (`configs/env-252.sh`)
- SSH tunnel to remote host
- Container: `pg-252-pg17`
- Credentials from `~/workspace/ai-native-tools/envs/`
- Local tunnel port: `15432`

## Library Functions

### `scripts/lib/pg-instance-inventory.sh`
- `get_database_list()`: Collect database names
- `get_database_info()`: Detailed database metadata

### `scripts/lib/pg-instance-guardrails.sh`
- `validate_manifest_freshness()`: Hash and age validation
- `validate_llm_gateway_protection()`: LLM data protection
- `validate_database_names()`: Name pattern validation

### `scripts/lib/252-db-tunnel.sh`
- SSH tunnel management for remote database access

## Usage Examples

### Basic Workflow

1. **Collect inventories:**
```bash
pg-instance-sync.sh inventory --yes
```

2. **Generate sync plan:**
```bash
pg-instance-sync.sh plan \
  --local-inventory tmp/local-inventory-*.tsv \
  --remote-inventory tmp/remote-inventory-*.tsv \
  --policy configs/pg-sync-policy.conf \
  --manifest manifest.tsv
```

3. **Get manifest hash:**
```bash
HASH=$(shasum -a 256 manifest.tsv | awk '{print $1}')
```

4. **Apply schema and data, then verify:**
```bash
pg-instance-sync.sh apply-schema --yes --manifest manifest.tsv \
  --manifest-hash $HASH --freshness-check --policy configs/pg-sync-policy.conf \
  --impact-matrix impact/impact-matrix.tsv --work-dir work/schema
pg-instance-sync.sh apply-data --yes --manifest manifest.tsv \
  --manifest-hash $HASH --freshness-check --policy configs/pg-sync-policy.conf \
  --impact-matrix impact/impact-matrix.tsv --work-dir work/data
pg-instance-sync.sh verify --manifest manifest.tsv \
  --policy configs/pg-sync-policy.conf --output-dir work/verify
```

### Policy Customization

Edit `configs/pg-sync-policy.conf` to adjust:
- Exclusion patterns
- Schema-only databases
- Database aliases
- Safety thresholds

## Troubleshooting

### Common Issues

1. **"Hash mismatch" error**
   - Manifest file was modified after generation
   - Regenerate manifest or use correct hash

2. **"Manifest too old" warning**
   - Database state may have changed
   - Collect fresh inventory and regenerate manifest

3. **"llm_gateway data operation blocked"**
   - By design - llm_gateway data is protected
   - Use SCHEMA_ONLY mode for llm_gateway

4. **SSH tunnel connection failed**
   - Check SSH configuration and keys
   - Verify remote container is running

### Debug Mode

Set `LOG_LEVEL=DEBUG` in policy file for detailed logging.

## Testing

Run the test suite:
```bash
bash scripts/test-pg-instance-sync.sh                    # Basic CLI
bash scripts/test-pg-instance-sync-commands.sh           # Command validation  
bash scripts/test-pg-instance-sync-guardrails.sh         # Safety checks
bash scripts/test-pg-instance-inventory.sh               # Inventory collection
bash scripts/test-pg-instance-verify.sh                  # verify/restore guards
```