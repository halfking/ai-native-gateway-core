#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/run-integration-gate.sh
# Purpose:       Run one integration-gated Go test against a freshly created,
#                disposable PostgreSQL database, and — critically — refuse to
#                report success when the suite merely skipped.
#
# Round 43. The existing .github/workflows/integration-testcontainers-ci.yml
# runs `go test -tags=integration ./...` with only TESTCONTAINERS_* in the
# environment. It injects no database URL at all. Measured consequences:
#
#   * 27 of 67 integration test files gate on a DB URL and therefore SKIP,
#     so a green run can contain zero executed database assertions;
#   * the DB URL is read under FIVE different names across the suite
#     (TEST_DATABASE_URL 43 files, TEST_DB_URL 22, TEST_PG_URL 16,
#     LLM_GATEWAY_PG_URL 11, DATABASE_URL 1), so "inject the env var" is
#     ambiguous unless all of them are set;
#   * at least one integration file gates on nothing and never skips —
#     db/db_migration_538_integration_test.go reads no env var and calls no
#     t.Skip — so it hard-fails without a live database instead of skipping.
#
# The convention this script enforces: TEST_PG_URL and friends must point at a
# DISPOSABLE database. A shared database makes integration gates produce false
# red (one test's leftovers are the next test's precondition violation) and
# false green (a skipped suite reads as a pass).
#
# Usage:
#   bash scripts/audit/run-integration-gate.sh ./db
#   bash scripts/audit/run-integration-gate.sh ./internal/dbx TestFoo
#
# Env:
#   PG_CONTAINER  database container (default llm-gateway-pg)
#   PG_USER       database user     (default llm_gateway)
#   PG_PASSWORD   password for TCP auth; the in-container psql uses a local
#                 socket and succeeds without one, so only the Go tests fail
#                 when it is missing
#   PG_CLIENT_IMAGE  multi-arch image used for the DSN precheck when the host
#                 has no psql binary. It only ever runs `SELECT 1`, so a stock
#                 postgres image is enough — and unlike the project's Citus
#                 build it is multi-arch, which is why it is not the same tag.
#   GATE_MIN_RELATIONS  floor for the population assertion (default 400;
#                 measured: baseline only = 328, baseline + startup = 421)
#   GATE_LOG       path for this run's go test output
#   KEEP_GATE_DB=1   keep the database for post-mortem
#   ALLOW_VACUOUS=1  exit 0 even if every test skipped (for auditing; the
#                    summary still reports the skip count loudly)
# -----------------------------------------------------------------------------
set -uo pipefail

PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
PG_USER="${PG_USER:-llm_gateway}"
# Round 43: the first version built the DSN with no password, so every test
# that actually connected failed with "failed SASL auth" — nine misleading
# test failures that looked like product bugs. Set PG_PASSWORD whenever the
# server requires one; the harness now also verifies the DSN works before
# running any test, so this class fails once, up front, with a clear message.
PG_PASSWORD="${PG_PASSWORD-}"
# Only used for `SELECT 1`, so a stock multi-arch postgres image is correct
# here. Do not point this at the project's kx-citus-pg17:*-arm64 tag: that
# image is arm64-only and this fallback exists precisely for hosts that are
# not the maintainer's arm64 laptop.
PG_CLIENT_IMAGE="${PG_CLIENT_IMAGE:-postgres:17-alpine}"
KEEP_GATE_DB="${KEEP_GATE_DB:-0}"
ALLOW_VACUOUS="${ALLOW_VACUOUS:-0}"
# GATE_APPLY_STARTUP=1 also applies the installer's registered startup
# migrations after the baseline. The baseline alone is missing tables that only
# migrations create (session_aggregate_outbox via 630, usage_facts via 537), so
# db.ensure*() tests for those fail with "relation does not exist" — a stale
# artifact, not a product defect. Measured: baseline alone = 328 relations;
# baseline + startup = 421, matching the real installer path.
GATE_APPLY_STARTUP="${GATE_APPLY_STARTUP:-1}"

# GATE_DB_SHAPE — which of the mutually-exclusive fixture families this run
# serves. Round 44 §7 measured that the 68 integration-only files split into
# families that cannot share one database, so a single harness shape was the
# structural reason "integration 全绿" had no single answer.
#
#   installer  prereqs + 01-schema baseline + all 198 registered startup
#              migrations. What the real installer produces. Measured 435
#              relations on 2026-10-01. Serves tests that assume a migrated
#              database and read production-shaped data.
#   baseline   prereqs + 01-schema only, no startup chain. The intermediate
#              shape the db.ensure*() family exercises.
#   prereqs    prereqs only — a nearly empty database. Serves tests that build
#              their own schema; on an installer-shaped database they collide
#              with it (SQLSTATE 42P07 "relation ... already exists").
#
# The shape is READ FROM sql/schema/integration_fixture_shapes.tsv per package
# (see resolve_shape below) so nobody has to remember it; GATE_DB_SHAPE forces
# one for experiments and overrides the manifest.
GATE_DB_SHAPE="${GATE_DB_SHAPE:-}"



PKG="${1:-}"
TEST_NAME="${2:-}"
die() { echo "ERROR: $*" >&2; exit 2; }

[[ -n "$PKG" ]] || die "usage: $0 <package> [test-name]"
command -v docker >/dev/null 2>&1 || die "docker not found"
docker ps --format '{{.Names}}' | grep -qx "$PG_CONTAINER" \
  || die "container '$PG_CONTAINER' not running (override with PG_CONTAINER=)"
docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -tAc 'SELECT 1' >/dev/null 2>&1 \
  || die "cannot connect to '$PG_CONTAINER' as '$PG_USER' (override with PG_USER=)"

# Short, unique, and well under PostgreSQL's 63-byte identifier cap. A name
# that exceeds the cap makes CREATE DATABASE fail, after which every statement
# errors and the run looks like "everything failed" for the wrong reason.
GATE_DB="itgate_${$}_${RANDOM}"
GATE_DB="${GATE_DB:0:30}"

PGPORT=$(docker port "$PG_CONTAINER" 5432/tcp 2>/dev/null | head -1 | sed 's/.*://')
[[ -n "$PGPORT" ]] || PGPORT=5432
if [[ -n "$PG_PASSWORD" ]]; then
  GATE_URL="postgresql://${PG_USER}:${PG_PASSWORD}@127.0.0.1:${PGPORT}/${GATE_DB}"
else
  GATE_URL="postgresql://${PG_USER}@127.0.0.1:${PGPORT}/${GATE_DB}"
fi

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

# Resolve the shape from the manifest when the caller did not force one.
#
# The manifest is sql/schema/integration_fixture_shapes.tsv: one row per
# integration package, columns <package>\t<shape>\t<measured fail count>\t<reason>.
# It is the measured record, NOT a claim that the shape makes the package green
# — measured 2026-10-01, no package is green on either shape, because the
# self-building and already-migrated fixtures are interleaved INSIDE packages.
# Recording the fail count per shape is what stops the next reader from
# assuming a shape label is a green light.
#
# A package with no row keeps the historical behaviour (installer), so adding
# the manifest cannot change any existing run until someone fills a row in.
SHAPE_MANIFEST="$REPO_ROOT/sql/schema/integration_fixture_shapes.tsv"
if [[ -z "$GATE_DB_SHAPE" && -f "$SHAPE_MANIFEST" ]]; then
  _rel="${PKG#./}"
  GATE_DB_SHAPE=$(awk -F'\t' -v p="$_rel" '
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    $1 == p { print $2; exit }' "$SHAPE_MANIFEST")
  if [[ -n "$GATE_DB_SHAPE" ]]; then
    echo "  [shape] ${_rel} -> ${GATE_DB_SHAPE}（来自 integration_fixture_shapes.tsv）"
  fi
fi
GATE_DB_SHAPE="${GATE_DB_SHAPE:-installer}"
cd "$REPO_ROOT" || die "cannot enter repo root: $REPO_ROOT"

# Refuse before touching the database if the target package has no
# integration-tagged tests at all. A package with none reports PASS from its
# ordinary unit tests, so the `NPASS==0` vacuity check below never fires and the
# run reads as a green gate.
# Measured: internal/trace has 0 files with `//go:build integration`, yet
# `go test -tags=integration ./internal/trace` yields 31 PASS — every one an
# ordinary unit test. A gate that said GATE OK for that package would have
# proven nothing about integration.
# Derive the answer from `go list` rather than by re-parsing build tags in the
# shell. `go list -tags=integration` has already evaluated the constraints, so
# the set difference against the untagged list is exactly "the test files that
# only exist because the integration tag is on". Two earlier attempts at this
# were wrong and are recorded here so they are not re-tried:
#   * grepping the source for `//go:build integration` mis-handles negated
#     tags — `//go:build !nintegration` also contains the substring;
#   * go list reports test file names relative to the PACKAGE, so a bare
#     `-f "$tf"` test from the repo root finds nothing and this guard rejects
#     every package, including ones that do have integration tests.
# `.Dir` is prepended to make the paths absolute and directly testable.
gatelist_files() {
  go list "$@" -f '{{$d := .Dir}}{{range .TestGoFiles}}{{$d}}/{{.}}
{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}
{{end}}' "$PKG" 2>/dev/null | sort
}
GATE_ITEST_COUNT=$(comm -13 \
  <(gatelist_files) \
  <(gatelist_files -tags=integration) | grep -c . || true)
GATE_ITEST_COUNT="${GATE_ITEST_COUNT:-0}"
if (( GATE_ITEST_COUNT == 0 )); then
  die "$PKG 下没有任何仅由 integration build tag 引入的测试文件。" \
"该包只会跑普通单测并报 PASS，看起来像门禁通过，实际零 integration 覆盖。 " \
"请把它从 CI 包列表里去掉，或给它补上真正的 integration 测试。"
fi
echo "  [coverage] integration-only test files=$GATE_ITEST_COUNT"

cleanup() {
  if [[ "$KEEP_GATE_DB" == "1" ]]; then
    echo "  keeping $GATE_DB"
  else
    # Drop the tenant role's grants before the role, or DROP ROLE fails.
    if [[ -n "${TENANT_ROLE:-}" ]]; then
      docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$GATE_DB" -q \
        -c "DROP OWNED BY $TENANT_ROLE" >/dev/null 2>&1 || true
    fi
    docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
      -c "DROP ROLE IF EXISTS ${TENANT_ROLE:-itgate_tenant}" >/dev/null 2>&1 || true
    docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
      -c "DROP DATABASE IF EXISTS $GATE_DB" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

echo "═══ integration gate: $PKG ${TEST_NAME:+(run=$TEST_NAME)} ═══"
echo "═══ disposable db=$GATE_DB container=$PG_CONTAINER ═══"

docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
  -c "DROP DATABASE IF EXISTS $GATE_DB" >/dev/null 2>&1
if ! docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
     -c "CREATE DATABASE $GATE_DB" >/dev/null 2>/tmp/itgate-create.err; then
  echo "--- stderr ---" >&2; cat /tmp/itgate-create.err >&2
  die "CREATE DATABASE $GATE_DB failed — refusing to run tests against a database that does not exist"
fi
docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$GATE_DB" -q \
  -c "CREATE EXTENSION IF NOT EXISTS citus" >/dev/null 2>&1

# Apply prereqs so citus_columnar exists; without it every
# `SET default_table_access_method = columnar` in a migration under test fails.
PREREQ=sql/schema/00-prereqs.sql
if [[ -f "$PREREQ" ]]; then
  cat "$PREREQ" | docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$GATE_DB" \
    -q -v ON_ERROR_STOP=1 --single-transaction >/dev/null 2>/tmp/itgate-prereq.err
  if [[ $? -ne 0 ]]; then
    echo "  ✗ 00-prereqs.sql failed:"; head -4 /tmp/itgate-prereq.err | sed 's/^/      /'
    die "prereqs must apply cleanly; without extensions the gate proves nothing"
  fi
fi

# Apply the baseline as well. Round 43 established that schema_migrations is
# created by NO migration and exists only in the pg_dump baseline — and the
# db.ensure*() family stamps its migration number into that table. With prereqs
# alone, nine ./db tests failed at `relation "public.schema_migrations" does not
# exist`, which reads like a product defect but is a bootstrap hole. Applying
# 01-schema.sql gives the gate a realistic starting schema.
#
# Round 44 closure made this conditional on the DB SHAPE, because the two
# families of integration fixtures are mutually exclusive and one harness can
# only build one kind of database. See GATE_DB_SHAPE above.
case "$GATE_DB_SHAPE" in
  installer|baseline)
    BASELINE=sql/schema/01-schema.sql
    if [[ -f "$BASELINE" ]]; then
      cat "$BASELINE" | docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$GATE_DB" \
        -q -v ON_ERROR_STOP=1 --single-transaction >/dev/null 2>/tmp/itgate-baseline.err
      if [[ $? -ne 0 ]]; then
        echo "  ✗ 01-schema.sql failed:"; head -4 /tmp/itgate-baseline.err | sed 's/^/      /'
        die "baseline must apply cleanly; the db.ensure*() family needs schema_migrations"
      fi
      echo "  ✓ baseline applied"
    fi
    ;;
  prereqs)
    echo "  ── shape=prereqs: 故意不应用基线（自建表族需要近乎空的库）"
    ;;
esac

# Apply the registered startup migrations so the gate database matches what the
# installer actually produces (421 relations, not the baseline's 328).
#
# Some migrations carry `-- dbinit:no-transaction` and must not run inside the
# per-file transaction (DROP INDEX CONCURRENTLY cannot). The installer honours
# that marker in applySQL; this loop must honour it too, or it reports two
# failures the installer would not have.
if [[ "$GATE_APPLY_STARTUP" == "1" && "$GATE_DB_SHAPE" == "installer" ]]; then
  echo "  ── applying registered startup migrations ──"
  SF_DIR="$REPO_ROOT/installer/cmd/llm-gw-installer/embeddata/startup"
  sf_ok=0; sf_fail=0; sf_missing=0
  declare -a sf_failed=()
  while read -r f; do
    [[ -n "$f" ]] || continue
    p="$SF_DIR/$f"
    if [[ ! -f "$p" ]]; then
      sf_missing=$((sf_missing+1))
      sf_failed+=("$f (no embeddata file)")
      continue
    fi
    args=(-q -v ON_ERROR_STOP=1)
    # Same rule as dbinit.requiresNoTransaction: marker in a comment line.
    if ! grep -qE '^[[:space:]]*--.*dbinit:no-transaction' "$p"; then
      args+=(--single-transaction)
    fi
    if docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$GATE_DB" \
         "${args[@]}" < "$p" >/dev/null 2>/tmp/itgate-sf.err; then
      sf_ok=$((sf_ok+1))
    else
      sf_fail=$((sf_fail+1))
      reason=$(grep -i '^ERROR' /tmp/itgate-sf.err | head -1 | cut -c1-140)
      [[ -z "$reason" ]] && reason=$(head -1 /tmp/itgate-sf.err | cut -c1-140)
      sf_failed+=("$f :: $reason")
    fi
  done < <(sed -n '/StartupFiles: \[\]string{/,/^\t}/p' \
             "$REPO_ROOT/installer/internal/dbinit/runner.go" \
           | grep -oE '"[0-9a-zA-Z_]+\.sql"' | tr -d '"')
  echo "  startup: applied=$sf_ok failed=$sf_fail missing=$sf_missing"
  # The sed above extracts the file list by matching a Go source literal. If
  # runner.go is ever reformatted — the declaration moves inside a type block,
  # the anchor text changes, the map literal is generated — the match returns
  # ZERO lines and prints no error. The loop then does nothing, the gate
  # database silently falls back to the stale baseline (328 relations instead
  # of 421), and db.ensure*() tests start failing with "relation does not
  # exist" — a self-inflicted artifact that reads like a product defect.
  # Verified: breaking the anchor alone yields 0 rows with exit status 0.
  sf_total=$((sf_ok + sf_fail + sf_missing))
  if (( sf_total == 0 )); then
    die "从 installer/internal/dbinit/runner.go 的 StartupFiles 里解析出 0 条迁移。 " \
"Go 源码的声明形态已变，sed 锚点失配。这不是「没有启动迁移」，是解析失败—— " \
"门禁库会静默退回 328 relations 的陈旧基线。请更新本脚本的 sed 锚点， " \
"并把 sf_total 的地板值与实测的注册迁移条数一起校准。"
  fi
  if (( sf_total < 100 )); then
    die "只从 StartupFiles 解析出 $sf_total 条迁移，明显少于 installer 实际注册的 " \
"条数（173）。解析多半是部分失配；继续跑等于拿半个起始库当门禁库。"
  fi
  # Known fresh-install gaps are a RATCHET, not a disclaimer.
  #
  # Round 43 collected this list and printed it, which made it neither a gate nor
  # a record: the run stayed green no matter how many entries there were, so the
  # list could grow silently and nobody would learn about it from a red build.
  #
  # Round 44 measures the same 19 failures on the installer's own path
  # (embeddata 00-prereqs -> 01-schema -> 02-seed + all 173 registered startup
  # files), so they are real fresh-install gaps rather than an artifact of this
  # harness. They are now enumerated in sql/schema/startup_known_gaps.tsv with a
  # reason each, and:
  #
  #   * a failure NOT in that file  -> die. A 20th gap turns the gate red until
  #     someone adds the line with a reason.
  #   * a line that did NOT fail   -> reported as stale, so a fixed migration
  #     gets its entry retired instead of the list quietly becoming a blanket
  #     exemption. (Not fatal: a genuine fix must not be blocked on paperwork.)
  #
  # Note the asymmetry that makes the list unable to rot silently: exemptions are
  # enumerated, so removing the whole file turns every gap fatal rather than
  # permissive. The guard tests also assert the file is non-empty.
  GAP_MANIFEST="$REPO_ROOT/sql/schema/startup_known_gaps.tsv"
  if (( sf_fail > 0 )); then
    if [[ ! -f "$GAP_MANIFEST" ]]; then
      die "有 ${sf_fail} 条启动迁移未应用，但找不到已知缺口清单 ${GAP_MANIFEST}。" \
"清单缺失时无法区分「已知缺口」与「新回归」，而放行等于把新回归当已知缺口吞掉。"
    fi
    mapfile -t gap_known < <(sed -e 's/#.*$//' -e '/^[[:space:]]*$/d' \
                              -e 's/[[:space:]].*$//' "$GAP_MANIFEST" | sort -u)
    declare -a gap_unlisted=()
    declare -a gap_hit=()
    for entry in "${sf_failed[@]}"; do
      f="${entry%% :: *}"
      if printf '%s\n' "${gap_known[@]}" | grep -qxF "$f"; then
        gap_hit+=("$entry")
      else
        gap_unlisted+=("$entry")
      fi
    done
    echo "  startup: ${#gap_hit[@]} 条已知缺口（见 sql/schema/startup_known_gaps.tsv）"
    printf '    - %s\n' "${gap_hit[@]}"
    if (( ${#gap_unlisted[@]} > 0 )); then
      echo "  ✗ 新增未登记的启动迁移失败（${#gap_unlisted[@]} 条），这不在已知缺口清单里："
      printf '    - %s\n' "${gap_unlisted[@]}"
      # An EMPTY manifest is the normal, healthy state as of 2026-10-01 (all 19
      # historical gaps were fixed and retired), so it must NOT be treated as a
      # corrupt file. The first version of this block died on a zero-entry
      # manifest BEFORE reporting which migrations failed, so a genuinely new
      # gap surfaced as "the manifest is broken" — pointing the reader at the
      # paperwork instead of at the actual breakage. Report the names first;
      # the empty-manifest case is just "every failure here is unlisted", and
      # the same fatal exit and the same remediation apply.
      if (( ${#gap_known[@]} == 0 )); then
        die "有 ${#gap_unlisted[@]} 条启动迁移在全新安装路径上失败，且已知缺口清单当前为空" \
"（这是 2026-10-01 之后的正常状态：历史 19 条缺口已全部修复并退场）。" \
"上面点名的这些失败全部是未登记缺口。确认是真实缺口后，把文件与原因补进 " \
"sql/schema/startup_known_gaps.tsv 再重跑；不要为了让门禁变绿而放宽这里的判据。"
      fi
      die "有 ${#gap_unlisted[@]} 条启动迁移在全新安装路径上失败且未登记为已知缺口。" \
"这意味着又出现了一批真实的新鲜安装缺口，或某条已修迁移回归了。" \
"确认是真实缺口后，把文件与原因补进 sql/schema/startup_known_gaps.tsv 再重跑；" \
"不要为了让门禁变绿而放宽这里的判据。"
    fi
    # Stale entries: a listed migration that applied fine this run. Surfaced, not
    # fatal, so a real fix is not blocked — but it must not be forgotten either.
    declare -a gap_stale=()
    mapfile -t gap_actual < <(for e in "${sf_failed[@]}"; do echo "${e%% :: *}"; done | sort -u)
    for known in "${gap_known[@]}"; do
      printf '%s\n' "${gap_actual[@]}" | grep -qxF "$known" || gap_stale+=("$known")
    done
    if (( ${#gap_stale[@]} > 0 )); then
      echo "  ⚠ 已知缺口清单里有 ${#gap_stale[@]} 条本轮未复现（可能已修复），请从清单中删除："
      printf '    - %s\n' "${gap_stale[@]}"
    fi
  fi
fi

# Create a genuinely non-bypass role for TEST_TENANT_DATABASE_URL.
#
# Why this exists. domains/requestjourney/observation_outbox_integration_test.go
# asserts tenant isolation by connecting through a "non-bypass pool", and its own
# comment states the requirement: "The validator superuser pool above bypasses
# RLS even with FORCE; the non-bypass pool is required to assert RLS isolation."
#
# The harness used to hand it TEST_TENANT_DATABASE_URL pointing at the very same
# URL — same database, same role. That role is `llm_gateway`, which this cluster
# reports as rolsuper=t AND rolbypassrls=t, so the probe read the other tenant's
# row and the test failed with:
#
#     alpha scope saw 1 beta rows, want 0 (RLS leak)
#
# That message points at a tenant-isolation vulnerability. It is not one: the
# policies in 552 are correct and the startup chain applies them cleanly. The
# test was measuring a superuser. A false red on a security assertion is worse
# than no assertion, because it trains readers to dismiss RLS failures.
#
# A separate, non-superuser, non-BYPASSRLS role makes the assertion real.
TENANT_ROLE="${TENANT_ROLE:-itgate_tenant}"
TENANT_PASSWORD="${TENANT_PASSWORD:-itgate-tenant-pw}"
TENANT_DSN=""
# Verify the DSN the tests will actually use. Without this, a wrong DSN
# surfaces as N unrelated per-test failures instead of one clear error.
#
# Note the in-container psql above only proves a local socket login. TCP auth
# is a separate path and is the one the Go tests take, so it gets its own check.
#
# The host may have no psql binary at all (a stock CI image is not guaranteed to
# carry postgresql-client). Do not blame the password for a missing binary:
# probe the binary first, and fall back to the container, which certainly has
# one and can reach the published port via --network host.
if command -v psql >/dev/null 2>&1; then
  dsn_runner() { psql "$1" -tAc 'SELECT 1' >/dev/null 2>&1; }
  dsn_where="host psql"
else
  dsn_runner() {
    docker run --rm --network host -e PGPASSWORD="${PG_PASSWORD:-}" \
      "$PG_CLIENT_IMAGE" psql "$1" -tAc 'SELECT 1' >/dev/null 2>&1
  }
  dsn_where="containerised psql (no host psql found; using $PG_CLIENT_IMAGE)"
fi
echo "  [dsn] verifying via $dsn_where"
if ! dsn_runner "$GATE_URL"; then
  # Distinguish the two failure causes instead of guessing.
  if [[ -z "$PG_PASSWORD" ]]; then
    die "生成的 DSN 认证失败：$dsn_where 连不上 ${GATE_DB}。 " \
"该 DSN 未带密码，而本机容器要求密码（实测无密码 TCP 连接报  " \
"fe_sendauth: no password supplied）。请设置 PG_PASSWORD 后重跑。"
  fi
  die "生成的 DSN 认证失败：$dsn_where 连不上 ${GATE_DB}（已带密码）。 " \
"请核对 PG_PASSWORD / PG_USER / 端口 $PGPORT 是否与容器实际配置一致。"
fi

# Population assertion, in two tiers. "relations > 0" is the weak form: it is
# satisfied by a database holding a single stray table, and — worse — it is
# still satisfied when the startup-migration loop above silently parsed zero
# files. The floor is what actually distinguishes a real installer-shaped
# database from a nearly-empty one.
#
# Measured round 43 on this harness: baseline only = 328 relations;
# baseline + registered startup migrations = 421.
# Materialise the non-bypass role now that the gate database is fully populated
# (grants are issued over whatever exists, so this must run AFTER the baseline
# and the startup chain, not before).
#
# The role is deliberately NOT superuser and NOT BYPASSRLS — that is the entire
# point. If role creation or grant fails, TENANT_DSN stays empty and the harness
# exports an empty TEST_TENANT_DATABASE_URL, which makes RLS tests skip loudly
# instead of silently measuring a superuser.
if docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$GATE_DB" -q -v ON_ERROR_STOP=1 >/dev/null 2>/tmp/itgate-tenant.err <<SQL
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$TENANT_ROLE') THEN
    EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L', '$TENANT_ROLE', '$TENANT_PASSWORD');
  END IF;
END
\$\$;
GRANT CONNECT ON DATABASE $GATE_DB TO $TENANT_ROLE;
GRANT USAGE ON SCHEMA public TO $TENANT_ROLE;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO $TENANT_ROLE;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO $TENANT_ROLE;
SQL
then
  TENANT_DSN="postgresql://${TENANT_ROLE}:${TENANT_PASSWORD}@127.0.0.1:${PGPORT}/${GATE_DB}"
  # Prove the role really is non-bypass before exporting it. Asserting the
  # premise is the difference between a real isolation test and a false red:
  # if this ever reports true, the DSN is handed out with a clear warning
  # rather than producing an "RLS leak" that means nothing.
  BYPASS_STATE=$(docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$GATE_DB" -tAc \
    "SELECT bool_or(rolsuper OR rolbypassrls) FROM pg_roles WHERE rolname = '$TENANT_ROLE'" \
    2>/dev/null || echo "")
  if [[ "$BYPASS_STATE" == "t" ]]; then
    echo "  ✗ 租户角色 $TENANT_ROLE 具备 superuser/BYPASSRLS，RLS 隔离断言将失真" >&2
    TENANT_DSN=""
  else
    echo "  [tenant] non-bypass role $TENANT_ROLE ready (rolsuper=off, bypassrls=off)"
  fi
else
  echo "  ⚠ 建租户角色失败，TEST_TENANT_DATABASE_URL 置空（RLS 断言将被跳过而非误报）:" >&2
  head -3 /tmp/itgate-tenant.err | sed 's/^/      /' >&2
  TENANT_DSN=""
fi

# The population floor is per-shape. Applying the installer floor to a
# deliberately-nearly-empty prereqs database would make the self-building
# family ungateable for a reason that has nothing to do with the code under
# test — the exact "残缺环境被当合格环境 / 合格环境被当残缺" confusion in
# both directions.
#
# Measured 2026-10-01 on the real harness:
#   installer -> 435 relations   baseline -> 328 (round 43 measurement)
#   prereqs   -> small; extensions only, and the tests under this shape
#                build their own tables by design.
case "$GATE_DB_SHAPE" in
  installer) GATE_MIN_RELATIONS="${GATE_MIN_RELATIONS:-400}" ;;
  baseline)  GATE_MIN_RELATIONS="${GATE_MIN_RELATIONS:-300}" ;;
  prereqs)   GATE_MIN_RELATIONS="${GATE_MIN_RELATIONS:-0}" ;;
  *) die "未知 GATE_DB_SHAPE='$GATE_DB_SHAPE'（可选 installer|baseline|prereqs）" ;;
esac
RELS=$(docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$GATE_DB" -tAc \
  "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m')" 2>/dev/null || echo 0)
echo "  [populated] shape=$GATE_DB_SHAPE relations=${RELS:-0} (floor=$GATE_MIN_RELATIONS)"
if [[ "$GATE_DB_SHAPE" != "prereqs" ]]; then
  # Only the "must be populated" shapes are checked for emptiness. A prereqs
  # database is SUPPOSED to be nearly empty — that is the point of the shape
  # for the self-building family.
  if ! [[ "${RELS:-0}" =~ ^[0-9]+$ ]] || (( RELS <= 0 )); then
    die "disposable database is EMPTY after prereqs — the run would be vacuous"
  fi
  if (( RELS < GATE_MIN_RELATIONS )); then
    die "门禁库只有 $RELS 个 relations，低于 $GATE_DB_SHAPE 形态的地板 ${GATE_MIN_RELATIONS}。 " \
"该形态实测应为 435（installer）/ 328（baseline）；低于地板说明起始库没建全， " \
"而基线层的 RELS>0 仍会通过 —— 那正是把残缺环境当合格环境的形状。 " \
"若确实应下调地板，请改 GATE_MIN_RELATIONS 并在提交信息里写明实测依据。"
  fi
fi

# A package with no integration-tagged test files reports PASS from its plain
# unit tests, so `NPASS==0` never fires and the run reads as a green gate.
# That case is now rejected up front, before the database is touched.

# Inject every name the suite reads. A one-name injection silently skips files
# that read another name.
#
# The list below is NOT hand-maintained from memory — sql/schema/integration_gate_test.go
# re-derives it from the repository on every run (TestGateInjectsEveryDBCredentialName),
# so adding a sixth name in some test file without adding it here turns that guard
# red. Round 43 shipped a five-name list that was already incomplete: five more
# names were in use, and one file (internal/dbx/vacuum_mutex_test.go) additionally
# has NO build tag and silently t.Skipf's when it cannot connect, so it had never
# run anywhere until this harness gave it a URL — at which point it failed.
#
# Redis names are deliberately absent: pointing TEST_REDIS_URL at a PostgreSQL
# DSN would be worse than leaving it unset.
echo ""
echo "── running ──"
RUN_LOG="${GATE_LOG:-/tmp/itgate-run-$(echo "$PKG" | tr -c 'a-zA-Z0-9' '-').log}"
echo "  [log] $RUN_LOG"
env \
  AI_SESSION_MANAGER_DATABASE_URL="$GATE_URL" \
  D07_S01_PG_URL="$GATE_URL" \
  DATABASE_URL="$GATE_URL" \
  LICENSE_AUTHORITY_DATABASE_URL="$GATE_URL" \
  LLM_GATEWAY_PG_URL="$GATE_URL" \
  OMNIFREE_TEST_DB_URL="$GATE_URL" \
  TEST_AUDIT_FRESH_SCHEMA_DB_URL="$GATE_URL" \
  TEST_AUDIT_ISOLATED_DB_URL="$GATE_URL" \
  TEST_DATABASE_URL="$GATE_URL" \
  TEST_DB_URL="$GATE_URL" \
  TEST_INSTALLER_FRESH_DB_URL="$GATE_URL" \
  TEST_PG_DSN="$GATE_URL" \
  TEST_PG_URL="$GATE_URL" \
  TEST_RESOLVE_INVARIANT_DB_URL="$GATE_URL" \
  TEST_TENANT_DATABASE_URL="$TENANT_DSN" \
  go test -tags=integration -count=1 -v -timeout 20m ${TEST_NAME:+-run "$TEST_NAME"} "$PKG" \
  > "$RUN_LOG" 2>&1
TEST_RC=$?

NPASS=$(grep -cE "^\s*--- PASS" "$RUN_LOG")
NSKIP=$(grep -cE "^\s*--- SKIP" "$RUN_LOG")
NFAIL=$(grep -cE "^\s*--- FAIL" "$RUN_LOG")

echo ""
echo "═══ summary ═══"
echo "  exit=$TEST_RC  PASS=$NPASS  SKIP=$NSKIP  FAIL=$NFAIL"
grep -E "^\s*--- FAIL" "$RUN_LOG" | head -10
if (( NFAIL > 0 )); then
  # In `go test -v` the assertion detail is printed BEFORE the "--- FAIL:"
  # marker. Grabbing context after it yields an empty report, which makes a
  # gate that says FAIL without saying why.
  echo "  failing test output (context before each --- FAIL marker):"
  grep -B 14 "^\s*--- FAIL" "$RUN_LOG" | grep -vE "testcontainers-go -|Server Version|API Version|^--$" | tail -40
fi

# Name the shape mismatch instead of leaving 42P07 to be read as a product bug.
#
# "relation X already exists" on a database the harness fully populated means
# the test builds its own schema and collided with ours. That is a fixture-shape
# fact, not a defect in the code under test — but reported raw it reads exactly
# like one, which is how R44 §7's family split stayed invisible for a month.
#
# Measured 2026-10-01, this turned out to be rarer and messier than "two clean
# families": no package is green on either shape (see
# sql/schema/integration_fixture_shapes.tsv). So this block DIAGNOSES and
# POINTS, and deliberately does not re-run or claim the other shape would pass —
# doing that would repeat the exact overclaim this project keeps having to undo.
N_ALREADY=$(grep -c "already exists" "$RUN_LOG" 2>/dev/null || true)
N_MISSING=$(grep -cE 'relation "[^"]+" does not exist' "$RUN_LOG" 2>/dev/null || true)
if (( ${N_ALREADY:-0} > 0 )) && [[ "$GATE_DB_SHAPE" != "prereqs" ]]; then
  cat >&2 <<DIAG

── 形态不匹配诊断 ──
  本形态 shape=${GATE_DB_SHAPE} 已被本包自己的 CREATE TABLE 撞了 ${N_ALREADY} 次
  （SQLSTATE 42P07 relation ... already exists）。这说明该测试自建 schema，
  而门禁库是满的。它不是产品缺陷。
  想看该包在空库上的表现：
      GATE_DB_SHAPE=prereqs bash scripts/audit/run-integration-gate.sh $PKG
  ⚠ 实测（2026-10-01）没有任何包在两种形态下都全绿，本包内两种夹具是混着的；
    换形态通常只是把失败挪个位置，不是修复。长期解法是给自建表族做
    per-test schema 隔离，而不是换一个整库形态。
DIAG
fi
if (( ${N_MISSING:-0} > 0 )) && [[ "$GATE_DB_SHAPE" == "prereqs" ]]; then
  cat >&2 <<DIAG

── 形态不匹配诊断 ──
  本形态 shape=prereqs 下有 ${N_MISSING} 处 relation ... does not exist：
  该测试假定一个已迁移的库，而本形态刻意不应用基线与启动迁移。
  它不是产品缺陷。改用满库形态：
      GATE_DB_SHAPE=installer bash scripts/audit/run-integration-gate.sh $PKG
DIAG
fi

# The core distinction the repo's discipline demands: "all green" is not the
# same claim as "it actually ran".
if (( NPASS == 0 )); then
  echo ""
  echo "✗ VACUOUS RUN: 0 tests passed."
  if (( NSKIP > 0 )); then
    echo "  $NSKIP test(s) skipped. This is NOT evidence of correctness."
  fi
  if [[ "$ALLOW_VACUOUS" == "1" ]]; then
    echo "  ALLOW_VACUOUS=1 — continuing anyway."
  else
    exit 3
  fi
elif (( NSKIP > 0 )); then
  echo ""
  echo "⚠ $NSKIP test(s) skipped alongside $NPASS passing. Report these as"
  echo "  'green with skips', not as full green."
fi

exit "$TEST_RC"
