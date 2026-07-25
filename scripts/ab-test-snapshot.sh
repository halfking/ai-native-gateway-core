#!/usr/bin/env bash
# =====================================================================
# scripts/ab-test-snapshot.sh - A/B 测试基线/实验数据快照
#
# 用法:
#   bash scripts/ab-test-snapshot.sh baseline > baseline.json
#   bash scripts/ab-test-snapshot.sh experiment > experiment.json
#
# 用于 A/B 测试期间生成对比数据快照
# =====================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER="root@8.136.114.245"
METRICS_URL="http://localhost:8781/metrics"

# 颜色
BLUE='\033[0;34m'
GREEN='\033[0;32m'
NC='\033[0m'

log_info()    { echo -e "${BLUE}[INFO]${NC} $1"; }
log_ok()      { echo -e "${GREEN}[OK]${NC} $1"; }

# ----------------------------------------------------------------------------
# 通过 SSH 获取指标
# ----------------------------------------------------------------------------
get_metric() {
    local pattern="$1"
    ssh "$SERVER" "curl -s --max-time 10 $METRICS_URL" 2>/dev/null | \
        grep -E "^$pattern" | \
        head -50
}

# ----------------------------------------------------------------------------
# 获取服务状态
# ----------------------------------------------------------------------------
get_service_status() {
    ssh "$SERVER" "
        echo '--- Service Status ---'
        systemctl is-active llm-gateway 2>/dev/null || echo 'unknown'
        echo ''
        echo '--- Feature Flag ---'
        grep -E '^PRESSURE_AWARE_ROUTING' /opt/llm-gateway-go/.env 2>/dev/null || echo 'not set'
        echo ''
        echo '--- Version ---'
        cat /opt/llm-gateway-go/version.json 2>/dev/null
    "
}

# ----------------------------------------------------------------------------
# 生成快照
# ----------------------------------------------------------------------------
generate_snapshot() {
    local mode="$1"
    local timestamp=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    cat << EOF
{
  "snapshot": {
    "mode": "$mode",
    "timestamp": "$timestamp",
    "source": "245-server"
  },
  "service": $(get_service_status | sed 's/`/\\`/g' | tr '\n' ' ' | sed 's/"/\\"/g' | sed 's/^/[ /' | sed 's/$/ ]/' || echo '{}'),
  "pressure_metrics": [
EOF

    # 获取关键指标
    local metrics_found=0
    get_metric 'llmgw_pressure_aware_routing_enabled' | while read -r line; do
        if [ $metrics_found -gt 0 ]; then echo ","; fi
        local name=$(echo "$line" | awk '{print $1}')
        local value=$(echo "$line" | awk '{print $2}')
        echo "    {\"name\": \"$name\", \"value\": $value}"
        metrics_found=$((metrics_found + 1))
    done

    echo "  ],"
    echo "  \"penalty_counter\": ["
    get_metric 'llmgw_pressure_penalty_applied_total' | head -20 | while read -r line; do
        if [ $metrics_found -gt 0 ]; then echo ","; fi
        echo "    \"$line\""
    done
    echo "  ],"
    echo "  \"penalty_histogram\": ["
    get_metric 'llmgw_pressure_penalty_value_bucket' | head -20 | while read -r line; do
        if [ $metrics_found -gt 0 ]; then echo ","; fi
        echo "    \"$line\""
    done
    echo "  ],"
    echo "  \"pressure_signal\": ["
    get_metric 'llmgw_pressure_signal' | head -20 | while read -r line; do
        if [ $metrics_found -gt 0 ]; then echo ","; fi
        echo "    \"$line\""
    done
    echo "  ]"
    echo "}"
}

# ----------------------------------------------------------------------------
# 主入口
# ----------------------------------------------------------------------------
case "${1:-}" in
    baseline)
        log_info "生成基线数据快照（Feature flag = false）..."
        generate_snapshot "baseline"
        ;;
    experiment)
        log_info "生成实验数据快照（Feature flag = true）..."
        generate_snapshot "experiment"
        ;;
    *)
        echo "用法: $0 <baseline|experiment>"
        echo ""
        echo "示例:"
        echo "  $0 baseline    # 在 Feature flag 关闭时采集基线"
        echo "  $0 experiment  # 在 Feature flag 开启时采集实验数据"
        exit 1
        ;;
esac
