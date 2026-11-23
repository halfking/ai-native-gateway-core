#!/usr/bin/env bash
# ★★ 2026-11-23（211 配置优化执行报告 §9.24 轮 · N4 基线红收口）：★★ 本脚本在【宿主】上
# ★★ 编译 Go ⇒ ⇒ 子进程会往 Go 的构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】
# ★★ ⇒ ⇒ 必须指向【专用缓存】（★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；
# ★★ 理由与实测读数见 `internal/gocache` 的包注释 ⇒ ★ 实测：★ **单包** `go test ./admin/`
# ★★ 就要 **846 MB** ⇒ ⇒ 体积由【传递依赖闭包】决定 ⇒ ⇒ 按「编译范围」分层不成立）。
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
# 端到端验证脚本：W1-G4 熔断状态可见面
# 
# 目标：验证 /api/health/system 端点是否正确暴露熔断器状态
# 
# 验证方法：
# 1. 检查编译产物
# 2. 验证数据结构（代码层面）
# 3. 模拟验证（单元测试已覆盖）
# 4. 生成验收报告

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GATEWAY_BIN="/tmp/gateway_test"
TEST_OUTPUT="/tmp/w1-g4-verification-$(date +%Y%m%d-%H%M%S).log"

echo "========================================="
echo "W1-G4 熔断状态可见面 - 端到端验证"
echo "========================================="
echo ""
echo "验证时间: $(date '+%Y-%m-%d %H:%M:%S')"
echo "编译产物: $GATEWAY_BIN"
echo "输出日志: $TEST_OUTPUT"
echo ""

# 步骤 1：验证编译产物存在
echo "[1/5] 验证编译产物..."
if [[ ! -f "$GATEWAY_BIN" ]]; then
    echo "❌ 错误：编译产物不存在: $GATEWAY_BIN"
    exit 1
fi

file_info=$(file "$GATEWAY_BIN")
file_size=$(ls -lh "$GATEWAY_BIN" | awk '{print $5}')
echo "✅ 编译产物存在"
echo "   - 文件信息: $file_info"
echo "   - 文件大小: $file_size"
echo ""

# 步骤 2：验证源码中的数据结构
echo "[2/5] 验证数据结构定义..."
cd "$REPO_ROOT/llm-gateway/llm-gateway-go"

# 检查 CircuitBreakerSummary 结构
if grep -q "type CircuitBreakerSummary struct" bg/system_health.go; then
    echo "✅ CircuitBreakerSummary 结构已定义"
    grep -A 6 "type CircuitBreakerSummary struct" bg/system_health.go | head -7
else
    echo "❌ 错误：CircuitBreakerSummary 结构未找到"
    exit 1
fi
echo ""

# 检查 SystemHealthStatus 中的 circuit_breaker 字段
if grep -q "CircuitBreaker.*CircuitBreakerSummary" bg/system_health.go; then
    echo "✅ SystemHealthStatus.CircuitBreaker 字段已添加"
    grep "CircuitBreaker.*CircuitBreakerSummary" bg/system_health.go
else
    echo "❌ 错误：CircuitBreaker 字段未找到"
    exit 1
fi
echo ""

# 步骤 3：验证 aggregateCircuitStats 函数
echo "[3/5] 验证聚合函数..."
if grep -q "func aggregateCircuitStats" bg/system_health.go; then
    echo "✅ aggregateCircuitStats() 函数已实现"
    grep "func aggregateCircuitStats" bg/system_health.go
else
    echo "❌ 错误：aggregateCircuitStats 函数未找到"
    exit 1
fi
echo ""

# 步骤 4：验证 main.go 集成点
echo "[4/5] 验证 main.go 集成..."
if grep -q "bg.NewSystemHealthWorker(dbConn.Pool(), cm)" cmd/gateway/main.go; then
    echo "✅ main.go 已传递 circuitManager 参数"
    grep -n "bg.NewSystemHealthWorker(dbConn.Pool(), cm)" cmd/gateway/main.go
else
    echo "❌ 错误：main.go 集成未完成"
    exit 1
fi
echo ""

# 步骤 5：运行单元测试
echo "[5/5] 运行单元测试..."
echo "运行 bg 包测试..."
if go test -v ./bg/ -run "TestAggregateCircuitStats" > "$TEST_OUTPUT" 2>&1; then
    test_count=$(grep -c "^--- PASS: TestAggregateCircuitStats" "$TEST_OUTPUT" || echo "0")
    echo "✅ 单元测试通过 ($test_count 个测试)"
    echo ""
    echo "测试详情:"
    grep "^=== RUN\|^--- PASS" "$TEST_OUTPUT" | grep "TestAggregateCircuitStats"
else
    echo "❌ 错误：单元测试失败"
    tail -20 "$TEST_OUTPUT"
    exit 1
fi
echo ""

# 生成验收报告
echo "========================================="
echo "验收结果"
echo "========================================="
echo ""
echo "✅ 所有检查通过！"
echo ""
echo "验证项目:"
echo "  [✓] 编译产物存在 ($file_size)"
echo "  [✓] CircuitBreakerSummary 结构已定义"
echo "  [✓] SystemHealthStatus.CircuitBreaker 字段已添加"
echo "  [✓] aggregateCircuitStats() 函数已实现"
echo "  [✓] main.go 集成点已连接"
echo "  [✓] 单元测试全部通过 ($test_count 个)"
echo ""
echo "预期行为:"
echo "  当网关启动并连接到数据库后，/api/health/system 端点将返回:"
echo ""
echo "  {"
echo "    \"status\": \"ok\","
echo "    \"success_rate\": 0.95,"
echo "    \"sample_count\": 1000,"
echo "    \"failure_count\": 50,"
echo "    \"last_check_at\": \"2026-11-19T04:00:00Z\","
echo "    \"circuit_breaker\": {"
echo "      \"open_count\": 0,"
echo "      \"half_open_count\": 0,"
echo "      \"quarantined_count\": 0,"
echo "      \"total_breakers\": 15"
echo "    }"
echo "  }"
echo ""
echo "注意事项:"
echo "  - circuit_breaker 字段在无熔断器或无数据库时不会出现（omitempty）"
echo "  - 需要数据库连接才能启动 SystemHealthWorker"
echo "  - 需要配置 credential.Manager 才能获取熔断器状态"
echo ""
echo "下一步:"
echo "  1. 部署到有数据库的测试环境"
echo "  2. 调用 curl http://<server>:8080/api/health/system"
echo "  3. 验证响应包含 circuit_breaker 字段"
echo ""
echo "完整日志: $TEST_OUTPUT"
echo "========================================="
