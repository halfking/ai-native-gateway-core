#!/bin/bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# Phase 1 路由改进实施启动脚本

set -e

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  路由系统 Phase 1 改进实施启动"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# 检查是否在正确的目录
if [ ! -f "go.mod" ]; then
    echo "❌ 错误：请在项目根目录运行此脚本"
    exit 1
fi

# 检查文档是否存在
if [ ! -f "docs/ROUTING_IMPROVEMENT_PHASE1_IMPLEMENTATION.md" ]; then
    echo "❌ 错误：找不到实施方案文档"
    exit 1
fi

echo "✅ 环境检查通过"
echo ""

# 创建特性分支
BRANCH_NAME="feature/routing-phase1-fixes"
echo "📌 创建特性分支: $BRANCH_NAME"

if git rev-parse --verify "$BRANCH_NAME" >/dev/null 2>&1; then
    echo "⚠️  分支已存在，切换到该分支"
    git checkout "$BRANCH_NAME"
else
    git checkout -b "$BRANCH_NAME"
    echo "✅ 分支创建成功"
fi
echo ""

# 创建必要的目录
echo "📁 创建实施文件目录结构"
mkdir -p domains/streaming/executors
mkdir -p settings
mkdir -p deploy/prometheus/alerts
echo "✅ 目录创建完成"
echo ""

# 显示待实施的改进项
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Phase 1 改进清单"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "改进 1: 修复 loadScore 权重失衡"
echo "  → 创建: domains/streaming/executors/router_scoring.go"
echo "  → 创建: domains/streaming/executors/router_scoring_test.go"
echo "  → 修改: domains/streaming/executors/router.go"
echo ""
echo "改进 2: 添加降级模式监控"
echo "  → 创建: domains/streaming/executors/metrics_degradation.go"
echo "  → 修改: domains/streaming/executors/executor.go"
echo "  → 创建: deploy/prometheus/alerts/fp-slot-saturation.yaml"
echo ""
echo "改进 3: 优化 Sticky TTL"
echo "  → 修改: domains/streaming/executors/sticky.go"
echo "  → 创建: domains/streaming/executors/sticky_ttl_test.go"
echo "  → 创建: settings/sticky_ttl.go"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# 提示下一步
echo "🚀 下一步操作："
echo ""
echo "1. 阅读实施方案："
echo "   cat docs/ROUTING_IMPROVEMENT_PHASE1_IMPLEMENTATION.md | less"
echo ""
echo "2. 开始实施代码（按照文档复制代码）"
echo ""
echo "3. 运行测试："
echo "   go test ./domains/streaming/executors -v"
echo "   go test -cover ./domains/streaming/executors"
echo ""
echo "4. 本地验证："
echo "   docker-compose up -d"
echo "   ./scripts/loadtest-gateway.sh"
echo ""
echo "5. 提交代码："
echo "   git add ."
echo "   git commit -m \"feat(routing): Phase 1 紧急修复\""
echo "   git push origin $BRANCH_NAME"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "✅ 启动脚本执行完成"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
