#!/bin/bash
# P0 修复自动应用脚本
# 用于应用 Ready Gate Fail-Open + LRU 优化 + 降级模式改进

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKUP_DIR="$SCRIPT_DIR/.backup_$(date +%Y%m%d_%H%M%S)"

echo "============================================"
echo "路由节点状态 P0 修复自动应用"
echo "时间: $(date)"
echo "============================================"

# 创建备份目录
mkdir -p "$BACKUP_DIR"
echo "✅ 备份目录: $BACKUP_DIR"

# ====================================
# 修复 1: Ready Gate Fail-Open
# ====================================
echo ""
echo "📝 应用修复 1: Ready Gate Fail-Open"
FILE1="$SCRIPT_DIR/domains/ursm/v2/manager.go"

if [ ! -f "$FILE1" ]; then
    echo "❌ 文件不存在: $FILE1"
    exit 1
fi

# 备份
cp "$FILE1" "$BACKUP_DIR/manager.go.backup"
echo "✅ 已备份: manager.go"

# 查找修改位置
LINE_NUM=$(grep -n "if !ready {" "$FILE1" | grep -v "//" | head -1 | cut -d: -f1)
if [ -z "$LINE_NUM" ]; then
    echo "❌ 未找到 'if !ready {' 行"
    exit 1
fi

echo "📍 找到修改位置: 第 $LINE_NUM 行"
echo ""
echo "⚠️  需要手动修改 $FILE1"
echo "   位置: 第 $LINE_NUM 行附近"
echo ""
echo "   修改前:"
echo "   -------"
echo "   if !ready {"
echo "       return nil, \"\", fmt.Errorf(\"ursm.v2: not ready\")"
echo "   }"
echo "   if len(missIndices) == 0 {"
echo "       scoreAndSort(views, seeds, m.cfg.ScoringWeights)"
echo "       return views, statesource.StateSourceNodeMirrorHit, nil"
echo "   }"
echo ""
echo "   修改后:"
echo "   -------"
echo "   // M2 Fail-Open Enhancement (2026-08-13): Allow LRU-only requests to"
echo "   // succeed even when Ready=false, as long as EVERY seed resolved from"
echo "   // the mirror."
echo "   if len(missIndices) == 0 {"
echo "       scoreAndSort(views, seeds, m.cfg.ScoringWeights)"
echo "       src := statesource.StateSourceNodeMirrorHit"
echo "       if !ready {"
echo "           src = statesource.StateSourceNodeMirrorFallback"
echo "           m.log.Warn(\"ursm.v2: serving from LRU cache while Redis unavailable\","
echo "               \"seed_count\", len(seeds),"
echo "           )"
echo "       }"
echo "       return views, src, nil"
echo "   }"
echo ""
echo "   // Only reject when we NEED Redis but it's not ready"
echo "   if !ready {"
echo "       return nil, \"\", fmt.Errorf(\"ursm.v2: not ready (cache miss requires Redis)\")"
echo "   }"
echo ""
read -p "请手动修改后按 Enter 继续..."

# ====================================
# 修复 2: LRU 缓存优化
# ====================================
echo ""
echo "📝 应用修复 2: LRU 缓存优化"
FILE2="$SCRIPT_DIR/domains/ursm/v2/config.go"

if [ ! -f "$FILE2" ]; then
    echo "❌ 文件不存在: $FILE2"
    exit 1
fi

# 备份
cp "$FILE2" "$BACKUP_DIR/config.go.backup"
echo "✅ 已备份: config.go"

# 自动修改
sed -i.bak 's/LRUMirrorSize:.*100000/LRUMirrorSize:    300_000,  \/\/ 2026-08-13: 10万 → 30万/' "$FILE2"
sed -i.bak 's/LRUMirrorSoftTTL:.*30 \* time.Second/LRUMirrorSoftTTL: 60 * time.Second,  \/\/ 2026-08-13: 30s → 60s/' "$FILE2"

echo "✅ 已修改: config.go"
grep -A 1 "LRUMirrorSize\|LRUMirrorSoftTTL" "$FILE2" | head -4

# ====================================
# 修复 3: 降级模式改进
# ====================================
echo ""
echo "📝 应用修复 3: 降级模式改进"
FILE3="$SCRIPT_DIR/domains/streaming/executors/router.go"

if [ ! -f "$FILE3" ]; then
    echo "❌ 文件不存在: $FILE3"
    exit 1
fi

# 备份
cp "$FILE3" "$BACKUP_DIR/router.go.backup"
echo "✅ 已备份: router.go"

# 查找修改位置
LINE_NUM=$(grep -n "len(candidates) <= 2" "$FILE3" | head -1 | cut -d: -f1)
if [ -z "$LINE_NUM" ]; then
    echo "❌ 未找到 'len(candidates) <= 2' 行"
    exit 1
fi

echo "📍 找到修改位置: 第 $LINE_NUM 行"
echo ""
echo "⚠️  需要手动修改 $FILE3"
echo "   位置: 第 $LINE_NUM 行"
echo ""
echo "   修改前:"
echo "   -------"
echo "   if len(candidates) <= 2 {"
echo ""
echo "   修改后:"
echo "   -------"
echo "   // 2026-08-13: Always try degraded mode when no candidates are available"
echo "   if len(available) == 0 {"
echo ""
read -p "请手动修改后按 Enter 继续..."

# ====================================
# 编译验证
# ====================================
echo ""
echo "🔨 编译验证"
if command -v go &> /dev/null; then
    echo "正在编译..."
    if go build -o gateway cmd/gateway/main.go; then
        echo "✅ 编译成功!"
        ls -lh gateway
    else
        echo "❌ 编译失败! 请检查修改"
        exit 1
    fi
else
    echo "⚠️  未找到 go 命令，跳过编译"
fi

# ====================================
# 总结
# ====================================
echo ""
echo "============================================"
echo "✅ P0 修复应用完成"
echo "============================================"
echo ""
echo "📦 备份位置: $BACKUP_DIR"
echo ""
echo "🧪 下一步:"
echo "  1. 运行单元测试:"
echo "     go test ./domains/ursm/v2/... -v"
echo "     go test ./domains/streaming/executors/... -v"
echo ""
echo "  2. 本地验证后部署到 154:"
echo "     bash deploy-154.sh"
echo ""
echo "  3. 监控 Prometheus 指标:"
echo "     - llmgw_routing_state_source_total{source=\"node_mirror_fallback\"}"
echo "     - llmgw_ursm_v2_cache_hit_rate"
echo ""
echo "🔄 回滚方法:"
echo "  cp $BACKUP_DIR/*.backup domains/ursm/v2/"
echo "  cp $BACKUP_DIR/router.go.backup domains/streaming/executors/"
echo ""
