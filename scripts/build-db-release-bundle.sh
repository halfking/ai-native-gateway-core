#!/usr/bin/env bash
# build-db-release-bundle.sh — build database SQL artifacts for llm-gateway-go releases.
#
# Outputs under $OUT_DIR/db/:
#   00-prereqs.sql
#   01-schema.sql
#   02-seed.sql
#   03-current-upgrade.sql
#   MANIFEST.json
#   SHA256SUMS
#
# Usage:
#   bash scripts/build-db-release-bundle.sh v2.4.6 --out /path/to/release
#   SEED_DATABASE_URL=postgresql://... bash scripts/build-db-release-bundle.sh v2.4.6 --out /path/to/release
#   DRY_RUN=1 bash scripts/build-db-release-bundle.sh v2.4.6 --out /tmp/out
#
# Seed policy:
#   - Defaults to the checked-in canonical sql/schema/02-seed.sql.
#   - When SEED_DATABASE_URL or LLM_GATEWAY_DATABASE_URL is set, exports public,
#     non-secret seed/config rows from that live DB.
#   - Credentials/secrets/API keys are never exported by default.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT"

VERSION_ARG=""
OUT_DIR=""
DRY_RUN="${DRY_RUN:-0}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) OUT_DIR="${2:?--out requires a directory}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help)
      sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    -*) echo "unknown arg: $1" >&2; exit 1 ;;
    *) VERSION_ARG="$1"; shift ;;
  esac
done

if [[ -z "$VERSION_ARG" ]]; then
  if [[ -f version.json ]]; then
    VERSION_ARG="v$(python3 -c "import json;print(json.load(open('version.json'))['git_tag'].lstrip('v'))")"
  else
    VERSION_ARG="v$(git describe --tags --always 2>/dev/null || echo dev)"
  fi
fi
VERSION="${VERSION_ARG#v}"
GIT_REF="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
OUT_DIR="${OUT_DIR:-$ROOT/dist/v${VERSION}}"
DB_DIR="$OUT_DIR/db"

mkdir -p "$DB_DIR"

required=("sql/schema/00-prereqs.sql" "sql/schema/01-schema.sql" "sql/schema/02-seed.sql")
for f in "${required[@]}"; do
  [[ -f "$f" ]] || { echo "missing required SQL file: $f" >&2; exit 1; }
done

copy_sql() {
  local src="$1" dst="$2"
  {
    echo "-- Generated for llm-gateway-go v${VERSION} from ${GIT_REF}"
    echo "-- Source: ${src}"
    echo "-- Generated at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo
    cat "$src"
  } >"$dst"
}

copy_sql "sql/schema/00-prereqs.sql" "$DB_DIR/00-prereqs.sql"
copy_sql "sql/schema/01-schema.sql" "$DB_DIR/01-schema.sql"

seed_url="${SEED_DATABASE_URL:-${LLM_GATEWAY_DATABASE_URL:-${DATABASE_URL:-}}}"
if [[ -n "$seed_url" && "$DRY_RUN" != "1" ]]; then
  if ! command -v pg_dump >/dev/null 2>&1; then
    echo "pg_dump not found; cannot export live seed from SEED_DATABASE_URL" >&2
    exit 1
  fi
  # Public system/config metadata only. This intentionally excludes users,
  # credentials, api keys, audits, logs, ledgers and other secrets/runtime data.
  include_tables=(
    applications
    key_applications
    local_models
    local_runtimes
    maas_settings
    model_aliases
    model_credit_rates
    model_families
    model_fingerprints
    model_offers_legacy
    pricing_plans
    provider_catalog
    provider_header_profiles
    provider_settings
    providers
    settings_kv
    standard_model_names
    standard_models
    system_config
    work_type_configs
    work_type_models
    work_types
    tenants
  )
  args=(--data-only --inserts --no-owner --no-privileges --disable-triggers --schema=public)
  for t in "${include_tables[@]}"; do
    args+=(--table="public.${t}")
  done
  {
    echo "-- Generated for llm-gateway-go v${VERSION} from live seed database"
    echo "-- Git ref: ${GIT_REF}"
    echo "-- Generated at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "-- Secret-bearing tables are intentionally excluded."
    echo
    pg_dump "${args[@]}" "$seed_url"
  } >"$DB_DIR/02-seed.sql"
else
  copy_sql "sql/schema/02-seed.sql" "$DB_DIR/02-seed.sql"
  if [[ -z "$seed_url" ]]; then
    echo "[db] SEED_DATABASE_URL/LLM_GATEWAY_DATABASE_URL not set; using checked-in canonical seed" >&2
  else
    echo "[db] DRY_RUN=1; using checked-in canonical seed" >&2
  fi
fi

upgrade="$DB_DIR/03-current-upgrade.sql"
{
  echo "-- 03-current-upgrade.sql — cumulative forward migrations for llm-gateway-go v${VERSION}"
  echo "-- Git ref: ${GIT_REF}"
  echo "-- Generated at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "--"
  echo "-- Operator note: this bundle concatenates checked-in migrations in lexical order."
  echo "-- For a production upgrade, review the diff from the previous released tag and"
  echo "-- execute only migrations not yet applied in that environment."
  echo
  shopt -s nullglob
  files=(sql/migrations/startup/*.sql sql/migrations/domain/*.sql sql/migrations/manual/*.sql sql/migrations/080-ursm-key-migration-ledger.sql sql/migrations/081-ursm-key-migration-ledger-add-rollback-deadline.sql)
  shopt -u nullglob
  if [[ ${#files[@]} -gt 0 ]]; then
    mapfile -t files < <(printf '%s\n' "${files[@]}" | LC_ALL=C sort -V)
  fi
  if [[ ${#files[@]} -eq 0 ]]; then
    echo "-- No migration SQL files found."
  else
    for f in "${files[@]}"; do
      case "$f" in
        *.down.sql) continue ;;
      esac
      echo
      echo "-- ============================================================================"
      echo "-- Source: ${f}"
      echo "-- ============================================================================"
      cat "$f"
      echo
    done
  fi
} >"$upgrade"

(
  cd "$DB_DIR"
  rm -f SHA256SUMS
  for f in 00-prereqs.sql 01-schema.sql 02-seed.sql 03-current-upgrade.sql; do
    sha=$(shasum -a 256 "$f" | awk '{print $1}')
    echo "$sha  $f" >> SHA256SUMS
  done
)

python3 - <<PY >"$DB_DIR/MANIFEST.json"
import hashlib, json, pathlib
root = pathlib.Path("$DB_DIR")
names = ["00-prereqs.sql", "01-schema.sql", "02-seed.sql", "03-current-upgrade.sql", "SHA256SUMS"]
items = []
for name in names:
    p = root / name
    digest = hashlib.sha256(p.read_bytes()).hexdigest()
    items.append({"name": name, "size_bytes": p.stat().st_size, "sha256": digest})
print(json.dumps({
    "app": "llm-gateway-go",
    "version": "v${VERSION}",
    "git_ref": "${GIT_REF}",
    "seed_source": "live-db" if "${seed_url}" and "${DRY_RUN}" != "1" else "checked-in",
    "files": items,
}, ensure_ascii=False, indent=2))
PY

# The manifest itself is intentionally outside SHA256SUMS to avoid a circular
# checksum. The verifier checks both independently and requires this file.
if [[ -f "$ROOT/../ai-native-maintain/scripts/lib/verify-db-release-bundle.py" ]]; then
  python3 "$ROOT/../ai-native-maintain/scripts/lib/verify-db-release-bundle.py" "$DB_DIR" "${VERSION}"
fi

echo "[db] release database bundle: $DB_DIR"
ls -la "$DB_DIR"
