#!/bin/bash
# 统计标准化验证脚本 - Maestro全面测试
# 验证所有统计相关页面在211部署后的功能

set -euo pipefail

BASE_URL="https://llm.itestu.cn"
MAESTRO_DIR="maestro"
RESULTS_DIR="/tmp/statistics-verification-$(date +%Y%m%d-%H%M%S)"

mkdir -p "$RESULTS_DIR"

echo "=== 统计页面Maestro验证 ==="
echo "目标: $BASE_URL"
echo "结果目录: $RESULTS_DIR"
echo ""

# 测试流程列表
declare -a FLOWS=(
    "02-overview-live:Dashboard概览页"
    "12-live-stage-tree:Live实时拓扑"
    "13-session-analytics-users:会话分析-用户"
    "16-session-analytics-system:会话分析系统"
)

PASSED=0
FAILED=0
TOTAL=${#FLOWS[@]}

echo "共 $TOTAL 个测试流程"
echo ""

for flow_info in "${FLOWS[@]}"; do
    IFS=':' read -r flow_name flow_desc <<< "$flow_info"
    flow_file="$MAESTRO_DIR/flows/${flow_name}.yaml"
    
    if [ ! -f "$flow_file" ]; then
        echo "⚠️  跳过: $flow_desc (文件不存在: $flow_file)"
        continue
    fi
    
    echo "▶️  运行: $flow_desc"
    echo "   文件: $flow_file"
    
    result_file="$RESULTS_DIR/${flow_name}-result.txt"
    
    if maestro test "$flow_file" -e BASE_URL="$BASE_URL" > "$result_file" 2>&1; then
        echo "   ✅ 通过"
        ((PASSED++))
    else
        echo "   ❌ 失败 (详见: $result_file)"
        ((FAILED++))
        # 显示失败详情的最后20行
        echo "   --- 失败详情 ---"
        tail -20 "$result_file" | sed 's/^/   /'
        echo "   ----------------"
    fi
    echo ""
done

echo "=== 验证结果汇总 ==="
echo "总计: $TOTAL"
echo "通过: $PASSED"
echo "失败: $FAILED"
echo ""

if [ $FAILED -eq 0 ]; then
    echo "✅ 所有统计页面验证通过"
    exit 0
else
    echo "❌ 有 $FAILED 个测试失败"
    echo "详细结果: $RESULTS_DIR"
    exit 1
fi
