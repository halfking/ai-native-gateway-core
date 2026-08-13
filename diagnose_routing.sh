#!/bin/bash
# 路由节点状态诊断脚本
# 用于分析 "No available provider. All 0 candidates" 问题

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPORT_FILE="$SCRIPT_DIR/routing_diagnosis_$(date +%Y%m%d_%H%M%S).log"

echo "==================================="
echo "路由节点状态诊断"
echo "时间: $(date)"
echo "报告: $REPORT_FILE"
echo "==================================="

# 输出函数
log() {
    echo "[$(date +%H:%M:%S)] $*" | tee -a "$REPORT_FILE"
}

section() {
    echo "" | tee -a "$REPORT_FILE"
    echo "==== $* ====" | tee -a "$REPORT_FILE"
}

# 1. 检查代码中的关键配置
section "1. URSM v2 配置检查"
if [ -f "$SCRIPT_DIR/domains/ursm/v2/config.go" ]; then
    log "LRU 配置:"
    grep -A 2 "LRUMirrorSize\|LRUMirrorSoftTTL" "$SCRIPT_DIR/domains/ursm/v2/config.go" | tee -a "$REPORT_FILE"
else
    log "⚠️  配置文件不存在: domains/ursm/v2/config.go"
fi

# 2. 检查 Ready Gate 逻辑
section "2. Ready Gate 逻辑检查"
if [ -f "$SCRIPT_DIR/domains/ursm/v2/manager.go" ]; then
    log "查找 Ready Gate 检查逻辑:"
    grep -n "not ready" "$SCRIPT_DIR/domains/ursm/v2/manager.go" | head -5 | tee -a "$REPORT_FILE"
    
    log ""
    log "查找 LRU 缓存命中逻辑:"
    grep -n "len(missIndices) == 0" "$SCRIPT_DIR/domains/ursm/v2/manager.go" | tee -a "$REPORT_FILE"
else
    log "⚠️  文件不存在: domains/ursm/v2/manager.go"
fi

# 3. 检查降级模式触发条件
section "3. 降级模式触发条件检查"
if [ -f "$SCRIPT_DIR/domains/streaming/executors/router.go" ]; then
    log "查找降级模式触发条件:"
    grep -n -B 2 -A 5 "tryDegradedMode" "$SCRIPT_DIR/domains/streaming/executors/router.go" | head -20 | tee -a "$REPORT_FILE"
else
    log "⚠️  文件不存在: domains/streaming/executors/router.go"
fi

# 4. 检查错误消息生成逻辑
section "4. 错误消息生成检查"
if [ -f "$SCRIPT_DIR/domains/streaming/handler.go" ]; then
    log "查找 'No available provider' 生成位置:"
    grep -n "No available provider" "$SCRIPT_DIR/domains/streaming/handler.go" | tee -a "$REPORT_FILE"
else
    log "⚠️  文件不存在: domains/streaming/handler.go"
fi

# 5. 统计相关代码位置
section "5. 关键代码位置统计"
log "PlanCandidatesWithContext 调用:"
find "$SCRIPT_DIR" -name "*.go" -type f -exec grep -l "PlanCandidatesWithContext" {} \; 2>/dev/null | grep -v vendor | wc -l | tee -a "$REPORT_FILE"

log "FilterAndScore 调用:"
find "$SCRIPT_DIR" -name "*.go" -type f -exec grep -l "FilterAndScore" {} \; 2>/dev/null | grep -v vendor | wc -l | tee -a "$REPORT_FILE"

log "StateBackend 使用:"
find "$SCRIPT_DIR" -name "*.go" -type f -exec grep -l "StateBackend" {} \; 2>/dev/null | grep -v vendor | wc -l | tee -a "$REPORT_FILE"

# 6. 检查是否有现有的修复
section "6. 已有修复检查"
log "查找 'fail-open' 相关注释:"
grep -r "fail.open\|fail-open\|failopen" "$SCRIPT_DIR" --include="*.go" | grep -v vendor | head -5 | tee -a "$REPORT_FILE"

log ""
log "查找 'NodeMirrorFallback' 引用:"
grep -r "NodeMirrorFallback" "$SCRIPT_DIR" --include="*.go" | grep -v vendor | wc -l | tee -a "$REPORT_FILE"

# 7. 分析候选节点过滤流程
section "7. 候选节点过滤流程分析"
if [ -f "$SCRIPT_DIR/domains/streaming/executors/router.go" ]; then
    log "查找 FilterAvailable 调用:"
    grep -n "FilterAvailable" "$SCRIPT_DIR/domains/streaming/executors/router.go" | tee -a "$REPORT_FILE"
    
    log ""
    log "查找 filterHealthyNodes 调用:"
    grep -n "filterHealthyNodes" "$SCRIPT_DIR/domains/streaming/executors/router.go" | tee -a "$REPORT_FILE"
fi

# 8. 检查冷却逻辑
section "8. 冷却逻辑检查"
log "查找冷却相关文件:"
find "$SCRIPT_DIR/domains/ursm/v2" -name "*.go" -type f | grep -i "cool\|reduc" | tee -a "$REPORT_FILE"

if [ -f "$SCRIPT_DIR/domains/ursm/v2/reducer/reducer.go" ]; then
    log ""
    log "查找冷却时间设置:"
    grep -n "CoolUntil\|cooling\|time.Minute" "$SCRIPT_DIR/domains/ursm/v2/reducer/reducer.go" | head -10 | tee -a "$REPORT_FILE"
fi

# 9. 生成诊断总结
section "9. 诊断总结"
log "潜在问题点:"
log "  1. Ready Gate 在 Redis 不可达时阻止 LRU 缓存使用"
log "  2. LRU 容量可能不足 (当前: 100000)"
log "  3. 降级模式触发条件可能过严 (len(candidates) <= 2)"
log "  4. 冷却策略可能过于激进"
log "  5. 错误消息不够明确 (统一显示 'No available provider')"

section "10. 建议修复优先级"
log "P0 - 紧急修复:"
log "  1. Ready Gate Fail-Open: 允许 LRU 缓存在 Redis 不可达时提供服务"
log "  2. 提升 LRU 容量: 100000 -> 300000, TTL: 30s -> 60s"
log "  3. 降级模式: 改为 len(available)==0 时触发"
log ""
log "P1 - 重要修复:"
log "  4. 分级冷却策略: 偶发故障 30s, 持续故障 5min, 致命故障 1hour"
log "  5. 错误消息细化: 区分 cooling / rate_limit / quota_exceeded"

echo ""
echo "诊断完成! 报告已保存到: $REPORT_FILE"
echo ""
echo "下一步:"
echo "  1. 查看详细报告: cat $REPORT_FILE"
echo "  2. 查看修复方案: cat FIX_ROUTING_NODE_STATUS.md"
echo "  3. 查看审计报告: cat ROUTING_NODE_STATUS_AUDIT_20260813.md"
