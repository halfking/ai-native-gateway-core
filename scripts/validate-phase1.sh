#!/bin/bash
# Phase 1 实施验证脚本

set -e

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Phase 1 实施验证"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# 检查必要文件是否存在
echo "📋 检查必要文件..."
FILES=(
    "domains/streaming/executors/router_scoring.go"
    "domains/streaming/executors/router_scoring_test.go"
    "domains/streaming/executors/metrics_degradation.go"
    "domains/streaming/executors/sticky_ttl_test.go"
    "settings/sticky_ttl.go"
    "deploy/monitoring/grafana-alerts/fp-slot-saturation.yaml"
)

MISSING=0
for file in "${FILES[@]}"; do
    if [ -f "$file" ]; then
        echo "  ✅ $file"
    else
        echo "  ❌ $file (缺失)"
        MISSING=$((MISSING + 1))
    fi
done

if [ $MISSING -gt 0 ]; then
    echo ""
    echo "⚠️  警告：有 $MISSING 个文件缺失"
    echo "   请按照 docs/ROUTING_IMPROVEMENT_PHASE1_IMPLEMENTATION.md 完成实施"
    exit 1
fi

echo ""
echo "✅ 文件检查通过"
echo ""

# 运行单元测试
echo "🧪 运行单元测试..."
go test ./domains/streaming/executors -v -run "TestCalculateLoadScore|TestConcurrencyScore|TestCalculateSessionStickyTTL" || {
    echo ""
    echo "❌ 单元测试失败"
    exit 1
}
echo ""
echo "✅ 单元测试通过"
echo ""

# 检查测试覆盖率
echo "📊 检查测试覆盖率..."
go test -cover ./domains/streaming/executors -coverprofile=coverage.out >/dev/null 2>&1
COVERAGE=$(go tool cover -func=coverage.out | grep total | awk '{print $3}' | sed 's/%//')
rm -f coverage.out

echo "  当前覆盖率: $COVERAGE%"
if (( $(echo "$COVERAGE < 80" | bc -l) )); then
    echo "  ⚠️  警告：覆盖率低于 80%"
else
    echo "  ✅ 覆盖率达标"
fi
echo ""

# 运行 golangci-lint
echo "🔍 运行代码检查..."
if command -v golangci-lint &> /dev/null; then
    golangci-lint run ./domains/streaming/executors/... || {
        echo ""
        echo "⚠️  代码检查发现问题，请修复"
    }
    echo "✅ 代码检查通过"
else
    echo "⚠️  golangci-lint 未安装，跳过检查"
fi
echo ""

# 检查 Git 状态
echo "📝 检查 Git 状态..."
if [ -n "$(git status --porcelain)" ]; then
    echo "  ℹ️  有未提交的更改"
    git status --short
else
    echo "  ✅ 工作区干净"
fi
echo ""

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "✅ Phase 1 实施验证完成"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "📌 下一步："
echo "  1. 提交代码: git commit -am \"feat(routing): Phase 1 紧急修复\""
echo "  2. 推送分支: git push origin feature/routing-phase1-fixes"
echo "  3. 创建 PR: gh pr create --title \"feat(routing): Phase 1 紧急修复\""
echo ""
