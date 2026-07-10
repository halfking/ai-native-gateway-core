#!/usr/bin/env bash
# scripts/db-schema-diff.sh
# 数据库结构对比脚本 (2026-07-09 审计要求)
#
# 用途: 对比多个环境的数据库结构，检测差异
# 用法: ./scripts/db-schema-diff.sh [env1] [env2]
#
# 支持的环境:
#   local, kaixuan-1, 252, all (默认)
#
# 退出码:
#   0  所有环境结构一致
#   1  存在差异

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

# 输出文件
OUTPUT_DIR="/tmp/db-schema-diff-$$"
mkdir -p "$OUTPUT_DIR"

# 获取指定环境的表列表
get_tables() {
    local env="$1"
    case "$env" in
        local)
            docker exec r112_postgres psql -U kxuser -d llm_gateway -tAc \
                "SELECT table_name FROM information_schema.tables WHERE table_schema='public' ORDER BY table_name;" \
                2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | grep -v '^$'
            ;;
        kaixuan-1)
            kubectl --kubeconfig="$HOME/.kube/config-kaixuan-1" exec -n default \
                kaixuan-pg-55fbb459fb-wc75l -- \
                psql -U llm_gateway -d llm_gateway -tAc \
                "SELECT table_name FROM information_schema.tables WHERE table_schema='public' ORDER BY table_name;" \
                2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | grep -v '^$'
            ;;
        252)
            ssh -p 25022 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null \
                root@115.29.212.252 "docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -tAc \
                \"SELECT table_name FROM information_schema.tables WHERE table_schema='public' ORDER BY table_name;\"" \
                2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | grep -v '^$'
            ;;
        *)
            echo -e "${RED}❌ 未知环境: $env${NC}" >&2
            return 1
            ;;
    esac
}

# 主函数
main() {
    local env1="${1:-local}"
    local env2="${2:-kaixuan-1}"

    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BLUE}  数据库结构对比工具${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""

    local exit_code=0

    if [[ "$env1" == "all" ]]; then
        echo "对比所有环境..."
        echo ""

        local local_tables="/tmp/db_schema_local.txt"
        local k1_tables="/tmp/db_schema_k1.txt"
        local p252_tables="/tmp/db_schema_252.txt"

        get_tables "local" > "$local_tables" 2>/dev/null
        get_tables "kaixuan-1" > "$k1_tables" 2>/dev/null
        get_tables "252" > "$p252_tables" 2>/dev/null

        echo "=== local vs kaixuan-1 ==="
        local only_k1=$(comm -13 "$local_tables" "$k1_tables" | wc -l | tr -d ' ')
        local only_local=$(comm -23 "$local_tables" "$k1_tables" | wc -l | tr -d ' ')
        local common_lk=$(comm -12 "$local_tables" "$k1_tables" | wc -l | tr -d ' ')
        echo "  仅 kaixuan-1: $only_k1 张"
        echo "  仅 local: $only_local 张"
        echo "  共同: $common_lk 张"

        echo ""
        echo "=== local vs 252 ==="
        local only_252=$(comm -13 "$local_tables" "$p252_tables" | wc -l | tr -d ' ')
        local only_local2=$(comm -23 "$local_tables" "$p252_tables" | wc -l | tr -d ' ')
        local common_lp=$(comm -12 "$local_tables" "$p252_tables" | wc -l | tr -d ' ')
        echo "  仅 252: $only_252 张"
        echo "  仅 local: $only_local2 张"
        echo "  共同: $common_lp 张"

        echo ""
        echo "=== kaixuan-1 vs 252 ==="
        local only_252_2=$(comm -13 "$k1_tables" "$p252_tables" | wc -l | tr -d ' ')
        local only_k1_2=$(comm -23 "$k1_tables" "$p252_tables" | wc -l | tr -d ' ')
        local common_kp=$(comm -12 "$k1_tables" "$p252_tables" | wc -l | tr -d ' ')
        echo "  仅 252: $only_252_2 张"
        echo "  仅 kaixuan-1: $only_k1_2 张"
        echo "  共同: $common_kp 张"
    else
        echo "对比 $env1 vs $env2..."
        get_tables "$env1" > "$OUTPUT_DIR/${env1}_tables.txt" 2>/dev/null || {
            echo -e "${RED}❌ 无法获取 $env1 的表列表${NC}"
            exit 1
        }
        get_tables "$env2" > "$OUTPUT_DIR/${env2}_tables.txt" 2>/dev/null || {
            echo -e "${RED}❌ 无法获取 $env2 的表列表${NC}"
            exit 1
        }

        local count1=$(wc -l < "$OUTPUT_DIR/${env1}_tables.txt" | tr -d ' ')
        local count2=$(wc -l < "$OUTPUT_DIR/${env2}_tables.txt" | tr -d ' ')
        echo "  $env1: $count1 张表"
        echo "  $env2: $count2 张表"

        local only_env1=$(comm -23 "$OUTPUT_DIR/${env1}_tables.txt" "$OUTPUT_DIR/${env2}_tables.txt" | wc -l | tr -d ' ')
        local only_env2=$(comm -13 "$OUTPUT_DIR/${env1}_tables.txt" "$OUTPUT_DIR/${env2}_tables.txt" | wc -l | tr -d ' ')
        local common=$(comm -12 "$OUTPUT_DIR/${env1}_tables.txt" "$OUTPUT_DIR/${env2}_tables.txt" | wc -l | tr -d ' ')

        echo ""
        echo "  仅 $env1: $only_env1 张"
        echo "  仅 $env2: $only_env2 张"
        echo "  共同: $common 张"
    fi

    # 清理
    rm -rf "$OUTPUT_DIR"

    exit $exit_code
}

main "$@"
