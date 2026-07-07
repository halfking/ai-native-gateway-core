#!/usr/bin/env bash
# ====================================================================
# 本地数据库初始化 — 按 sql/README.md 权威流程
# ====================================================================
# 解决问题：sql/scripts/init.sh 依赖不存在的 db-init-lib.sh 已损坏；
# db/db.go 的 ensure* 函数不创建 provider_catalog/approval_*/promote_* 等对象，
# 导致 gateway 启动报 "relation does not exist"。
#
# 本脚本按 SSOT 顺序应用：schema 快照 → startup 迁移 → domain 迁移。
# 所有迁移对象都使用 IF NOT EXISTS，可安全重复执行（幂等）。
#
# 用法：
#   ./scripts/init-local-db.sh                # 从 .env 读 DATABASE_URL
#   DATABASE_URL=postgres://... ./scripts/init-local-db.sh
#   ./scripts/init-local-db.sh --skip-schema  # 跳过 schema 快照，只跑迁移
# ====================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
SQL_DIR="$ROOT_DIR/sql"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
log()  { echo -e "${BLUE}[init-db]${NC} $*"; }
ok()   { echo -e "${GREEN}[init-db]✓${NC} $*"; }
warn() { echo -e "${YELLOW}[init-db]⚠${NC} $*"; }
die()  { echo -e "${RED}[init-db]✗${NC} $*" >&2; exit 1; }

# ── 解析 DATABASE_URL ──
if [[ -z "${DATABASE_URL:-}" && -f "$ROOT_DIR/.env" ]]; then
    # shellcheck disable=SC1090
    DATABASE_URL=$(grep -E '^DATABASE_URL=' "$ROOT_DIR/.env" | head -1 | cut -d= -f2- | tr -d '"' "'")
    export DATABASE_URL
fi
if [[ -z "${DATABASE_URL:-}" ]]; then
    # 回退：用 DB_* 变量拼一个
    DB_HOST="${DB_HOST:-localhost}"; DB_PORT="${DB_PORT:-5432}"
    DB_NAME="${DB_NAME:-llm_gateway}"; DB_USER="${DB_USER:-xutaohuang}"
    export DATABASE_URL="postgres://${DB_USER}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable"
fi
log "DATABASE_URL=${DATABASE_URL}"

# psql 包装：统一用 DATABASE_URL，失败不中断整个脚本（单个迁移失败记录后继续）
PSQL=(psql -v ON_ERROR_STOP=0 -q)
run_sql_file() {
    local f="$1" label="$2"
    if "${PSQL[@]}" "$DATABASE_URL" -f "$f" > "/tmp/init-db-$(basename "$f").log" 2>&1; then
        return 0
    else
        # 检查日志里是否有真实错误（忽略 "already exists" 这类幂等警告）
        if grep -qiE "error|fatal" "/tmp/init-db-$(basename "$f").log" 2>/dev/null \
           && ! grep -qiE "already exists|duplicate key|exists, skipping" "/tmp/init-db-$(basename "$f").log"; then
            warn "$label 失败（见 /tmp/init-db-$(basename "$f").log）"
            return 1
        fi
        return 0
    fi
}

SKIP_SCHEMA=false
[[ "${1:-}" == "--skip-schema" ]] && SKIP_SCHEMA=true

# ── 1. Schema 快照 ──
if ! $SKIP_SCHEMA; then
    log "步骤 1/3: 应用 schema 快照 (00-prereqs → 01-schema → 02-seed)..."

    # 关键：01-schema.sql 是 pg_dump 导出的"全新建库"快照，用普通 CREATE TABLE（非 IF NOT EXISTS）。
    # 若 public schema 里已有旧表（列更少），CREATE TABLE 会报 already exists 而跳过，
    # 导致表结构停留在旧版（缺 health_status/canonical_id 等列），视图随后引用这些列时
    # 又因 "column does not exist" 失败 → v_routable_credential_models 等视图缺失 → 网关路由报错。
    # 修复：应用快照前 DROP SCHEMA public CASCADE 重建。本地测试库可安全重建。
    log "  清理旧 public schema（DROP CASCADE + CREATE）..."
    if ! "${PSQL[@]}" "$DATABASE_URL" -c "DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;" > /tmp/init-db-drop.log 2>&1; then
        die "DROP SCHEMA 失败（见 /tmp/init-db-drop.log）"
    fi

    run_sql_file "$SQL_DIR/schema/00-prereqs.sql" "00-prereqs" || die "00-prereqs 失败"
    run_sql_file "$SQL_DIR/schema/01-schema.sql" "01-schema"   || die "01-schema 失败"
    run_sql_file "$SQL_DIR/schema/02-seed.sql"   "02-seed"     || warn "02-seed 有警告（通常可忽略）"
    ok "Schema 快照应用完成"
else
    log "跳过 schema 快照（--skip-schema）"
fi

# ── 2. Startup 迁移 ──
log "步骤 2/3: 应用 startup 迁移 ($(ls "$SQL_DIR/migrations/startup"/[0-9]*.sql 2>/dev/null | wc -l | tr -d ' ') 个文件)..."
startup_fail=0
for f in "$SQL_DIR/migrations/startup"/[0-9]*.sql; do
    [[ -f "$f" ]] || continue
    run_sql_file "$f" "startup/$(basename "$f")" || startup_fail=$((startup_fail + 1))
done
ok "Startup 迁移应用完成（失败 $startup_fail 个，幂等警告已忽略）"

# ── 3. Domain 迁移（只跑正向 .sql，跳过 .down.sql）──
log "步骤 3/3: 应用 domain 迁移 ($(ls "$SQL_DIR/migrations/domain"/[0-9]*.sql 2>/dev/null | grep -v '\.down\.sql$' | wc -l | tr -d ' ') 个文件，跳过 .down.sql)..."
domain_fail=0
for f in "$SQL_DIR/migrations/domain"/[0-9]*.sql; do
    [[ -f "$f" ]] || continue
    [[ "$f" == *.down.sql ]] && continue
    run_sql_file "$f" "domain/$(basename "$f")" || domain_fail=$((domain_fail + 1))
done
ok "Domain 迁移应用完成（失败 $domain_fail 个）"

# ── 验证关键对象存在 ──
log "验证关键数据库对象..."
for obj in \
    "SELECT to_regclass('public.providers') AS providers" \
    "SELECT to_regclass('public.credentials') AS credentials" \
    "SELECT to_regclass('public.provider_catalog') AS provider_catalog" \
    "SELECT to_regclass('public.approval_routing_rules') AS approval_routing_rules"; do
    result=$("${PSQL[@]}" "$DATABASE_URL" -tAc "$obj" 2>/dev/null | tr -d ' ')
    if [[ -n "$result" && "$result" != "null" ]]; then
        echo -e "  ${GREEN}✓${NC} ${obj##*public.} → $result"
    else
        echo -e "  ${YELLOW}⚠${NC} ${obj##*public.} → 缺失（可能需要手动检查迁移）"
    fi
done

ok "数据库初始化完成。gateway 现在应该能完整启动（无 'relation does not exist' 错误）。"
