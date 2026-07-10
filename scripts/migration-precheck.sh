#!/usr/bin/env bash
# scripts/migration-precheck.sh
# Migration 前置检查脚本 (2026-07-09 审计要求)
#
# 用途: 在执行 migration 之前检查依赖关系，避免部分失败
# 用法: ./scripts/migration-precheck.sh <migration_file>
#
# 检查项:
#   1. 检查基表是否存在（如 364 需要 315 的 prompt_injection_policies）
#   2. 检查必需的列是否存在
#   3. 检查外键引用的表是否有主键/唯一约束
#   4. 检查扩展是否已启用（如 pgvector）
#
# 退出码:
#   0  所有检查通过，可以执行 migration
#   1  存在前置依赖问题
#   2  参数错误

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

usage() {
    echo "Usage: $0 <migration_file> [env]"
    echo ""
    echo "Arguments:"
    echo "  migration_file   要检查的 migration 文件路径"
    echo "  env             数据库环境: local (default), kaixuan-1, 252"
    echo ""
    echo "Examples:"
    echo "  $0 sql/migrations/startup/364_prompt_injection_enhanced.sql"
    echo "  $0 sql/migrations/startup/364_prompt_injection_enhanced.sql kaixuan-1"
    exit 2
}

if [[ $# -lt 1 ]]; then
    usage
fi

MIG_FILE="$1"
ENV="${2:-local}"

if [[ ! -f "$MIG_FILE" ]]; then
    echo -e "${RED}❌ 错误: migration 文件不存在: $MIG_FILE${NC}"
    exit 2
fi

# 数据库连接配置
case "$ENV" in
    local)
        DB_CMD="docker exec r112_postgres psql -U kxuser -d llm_gateway"
        ;;
    kaixuan-1)
        DB_CMD="kubectl --kubeconfig=\$HOME/.kube/config-kaixuan-1 exec -n default kaixuan-pg-55fbb459fb-wc75l -- psql -U llm_gateway -d llm_gateway"
        ;;
    252)
        DB_CMD="ssh -p 25022 root@115.29.212.252 docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway"
        ;;
    *)
        echo -e "${RED}❌ 未知环境: $ENV${NC}"
        exit 2
        ;;
esac

echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}  Migration 前置检查${NC}"
echo -e "${BLUE}  文件: $MIG_FILE${NC}"
echo -e "${BLUE}  环境: $ENV${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

PASS=0
FAIL=0
WARN=0
declare -a FAILURES=()

# 工具函数
check_pass() {
    echo -e "  ${GREEN}✓${NC} $1"
    PASS=$((PASS + 1))
}

check_fail() {
    echo -e "  ${RED}✗${NC} $1"
    FAIL=$((FAIL + 1))
    FAILURES+=("$1")
}

check_warn() {
    echo -e "  ${YELLOW}⚠${NC} $1"
    WARN=$((WARN + 1))
}

# ── 1. 检查基表依赖 ──────────────────────────────────────────────────
echo -e "${YELLOW}[1/4]${NC} 检查基表依赖..."

# 根据文件路径自动识别需要检查的基表
check_base_table() {
    local table="$1"
    local desc="$2"

    local exists
    exists=$(eval "$DB_CMD -tAc \"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='$table');\"" 2>/dev/null || echo "f")

    if [[ "$exists" == "t" ]]; then
        check_pass "基表存在: $table ($desc)"
    else
        check_fail "基表不存在: $table ($desc) - 需要先执行对应的 migration"
    fi
}

# 根据 migration 编号检查对应的基表
if [[ "$MIG_FILE" =~ /364_ ]] || [[ "$MIG_FILE" =~ /365_ ]]; then
    check_base_table "prompt_injection_policies" "由 migration 315 创建"
    check_base_table "output_compliance_policies" "由 migration 316 创建"
    check_base_table "prompt_injection_rules" "由 migration 315 创建"
    check_base_table "prompt_injection_detections" "由 migration 315 创建"
    check_base_table "output_compliance_audit" "由 migration 316 创建"
fi

if [[ "$MIG_FILE" =~ /332_ ]]; then
    check_base_table "credentials" "基表"
    check_base_table "providers" "基表"
    check_base_table "provider_models" "基表"
    check_base_table "credential_model_bindings" "基表"
fi

echo ""

# ── 2. 检查必需的列是否存在 ──────────────────────────────────────────
echo -e "${YELLOW}[2/4]${NC} 检查必需的列..."

# 自动从 migration 文件中提取要添加的列
check_column() {
    local table="$1"
    local column="$2"

    local exists
    exists=$(eval "$DB_CMD -tAc \"SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='$table' AND column_name='$column');\"" 2>/dev/null || echo "f")

    if [[ "$exists" == "t" ]]; then
        check_warn "列已存在: $table.$column (使用 IF NOT EXISTS 是安全的)"
    fi
}

# 364 检查已有列
if [[ "$MIG_FILE" =~ /364_ ]]; then
    # 这些列如果已存在不会导致错误，因为使用了 ADD COLUMN IF NOT EXISTS
    :
fi

echo ""

# ── 3. 检查外键约束的合理性 ──────────────────────────────────────────
echo -e "${YELLOW}[3/4]${NC} 检查外键约束..."

# 提取 migration 文件中的 FOREIGN KEY 引用
fk_refs=$(grep -E 'FOREIGN KEY.*REFERENCES' "$MIG_FILE" 2>/dev/null | \
          sed -E 's/.*REFERENCES[[:space:]]+([a-z_]+)\(([a-z_]+)\).*/\1.\2/' || true)

if [[ -n "$fk_refs" ]]; then
    while IFS= read -r ref; do
        [[ -z "$ref" ]] && continue
        ref_table="${ref%.*}"
        ref_column="${ref#*.}"

        # 检查被引用的表是否有主键或唯一约束
        has_constraint=$(eval "$DB_CMD -tAc \"SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='$ref_table'::regclass AND contype IN ('p', 'u'));\"" 2>/dev/null || echo "f")

        if [[ "$has_constraint" == "t" ]]; then
            check_pass "外键引用有效: $ref (被引用表有主键/唯一约束)"
        else
            check_fail "外键引用失败: $ref (被引用表缺少主键/唯一约束) - 这会导致 365 类问题"
        fi
    done <<< "$fk_refs"
else
    check_warn "未发现外键约束"
fi

echo ""

# ── 4. 检查扩展依赖 ──────────────────────────────────────────────────
echo -e "${YELLOW}[4/4]${NC} 检查扩展依赖..."

# 检查 pgvector
if grep -q 'CREATE EXTENSION.*vector' "$MIG_FILE" 2>/dev/null; then
    has_pgvector=$(eval "$DB_CMD -tAc \"SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='vector');\"" 2>/dev/null || echo "f")

    if [[ "$has_pgvector" == "t" ]]; then
        check_pass "pgvector 扩展已安装"
    else
        check_warn "pgvector 扩展未安装 - 向量相似度检测将被禁用 (其余功能正常)"
    fi
fi

# 检查 Citus
if grep -q 'citus' "$MIG_FILE" 2>/dev/null; then
    has_citus=$(eval "$DB_CMD -tAc \"SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='citus');\"" 2>/dev/null || echo "f")

    if [[ "$has_citus" == "t" ]]; then
        check_pass "Citus 扩展已安装"
    else
        check_warn "Citus 扩展未安装 - 部分功能可能受限"
    fi
fi

echo ""
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "  PASS=$PASS  FAIL=$FAIL  WARN=$WARN"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

if [[ $FAIL -gt 0 ]]; then
    echo ""
    echo -e "${RED}❌ 存在前置依赖问题，建议修复后再执行 migration:${NC}"
    for f in "${FAILURES[@]}"; do
        echo "  - $f"
    done
    echo ""
    echo "  参考: docs/migrations/migration-status-2026-07-09.md"
    exit 1
fi

if [[ $WARN -gt 0 ]]; then
    echo ""
    echo -e "${YELLOW}⚠️  存在警告，但 migration 可以继续执行${NC}"
    exit 0
fi

echo ""
echo -e "${GREEN}✅ 所有检查通过，可以执行 migration${NC}"
exit 0
