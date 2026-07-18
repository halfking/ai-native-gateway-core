# LLM Gateway 路由系统测试套件

本目录包含了完整的路由系统分层测试套件，用于验证路由正确性、稳定性和效率。

## 📁 目录结构

```
tests/routing/
├── README.md                    # 本文件
├── run_all_tests.sh             # 测试运行器
├── lib/
│   └── assert.sh                # 测试断言库
├── layer1_direct/
│   ├── test_openai_direct.sh    # OpenAI 直连测试
│   ├── test_anthropic_direct.sh # Anthropic 直连测试 (待实现)
│   └── test_protocol_conversion.sh # 协议转换测试 (待实现)
├── layer2_components/
│   ├── test_sticky.sh           # Sticky Session 测试
│   ├── test_compression.sh      # 压缩测试 (待实现)
│   └── test_detection.sh        # 检测测试 (待实现)
└── layer3_integration/
    ├── test_full_routing.sh     # 完整路由测试 (待实现)
    ├── test_fault_recovery.sh   # 故障恢复测试 (待实现)
    └── test_performance.sh      # 性能测试 (待实现)
```

## 🚀 快速开始

### 前置要求

1. **必需工具**:
   ```bash
   # macOS
   brew install jq curl
   
   # Linux
   apt-get install jq curl
   ```

2. **Gateway 运行中**:
   ```bash
   # 确保 gateway 在 http://localhost:8080 运行
   curl http://localhost:8080/healthz
   ```

3. **环境变量**:
   ```bash
   export GATEWAY_URL="http://localhost:8080"
   export API_KEY="your-test-api-key"
   ```

### 运行测试

```bash
# 运行所有测试
cd tests/routing
./run_all_tests.sh all

# 只运行 Layer 1 (直连测试)
./run_all_tests.sh layer1

# 只运行 Layer 2 (组件测试)
./run_all_tests.sh layer2

# 只运行 Layer 3 (集成测试)
./run_all_tests.sh layer3
```

### 运行单个测试

```bash
# 运行 OpenAI 直连测试
cd tests/routing
./layer1_direct/test_openai_direct.sh

# 运行 Sticky 测试
./layer2_components/test_sticky.sh
```

## 📊 测试层级

### Layer 1: 直连供应商测试

**目标**: 验证供应商连通性，排除路由干扰

**环境配置**:
```bash
export LLM_GATEWAY_BYPASS_ROUTING=true
export LLM_GATEWAY_FORCE_CREDENTIAL_ID=123
export LLM_GATEWAY_DISABLE_STICKY=true
export LLM_GATEWAY_DISABLE_COMPRESSION=true
```

**测试用例**:
- ✅ T1.1: OpenAI 聊天补全 (非流式)
- ✅ T1.2: OpenAI 流式响应
- ✅ T1.3: 错误处理 (无效模型)
- ✅ T1.4: Tool Calls
- ✅ T1.5: 大上下文处理
- ✅ T1.6: 并发请求 (10 并发)

**验收标准**:
- 成功率: 100%
- P95 延迟: <3s
- 并发成功率: >95%

### Layer 2: 组件隔离测试

**目标**: 逐个启用组件，验证独立功能

**环境配置**:
```bash
export LLM_GATEWAY_BYPASS_ROUTING=false
export LLM_GATEWAY_ENABLE_STICKY=true
# 其他组件按需启用
```

**测试模块**:
- ✅ **Sticky Session** (6 用例):
  - T2.1.1: L1 Sticky (session+model)
  - T2.1.2: L2 Sticky (client+model)
  - T2.1.3: L3 Sticky (client baseline)
  - T2.1.4: 失效清理
  - T2.1.5: TTL 过期
  - T2.1.6: 持久化

- 🔄 **Compression** (待实现):
  - T2.2.1: 上下文窗口溢出
  - T2.2.2: 压缩质量
  - T2.2.3: Memora 集成

- 🔄 **Detection** (待实现):
  - T2.3.1: 提示词注入检测
  - T2.3.2: 敏感信息检测
  - T2.4.1: 有害内容检测
  - T2.4.2: PII 泄露检测

**验收标准**:
- 各组件独立测试通过率: 100%

### Layer 3: 端到端集成测试

**目标**: 完整路由管道，验证多组件协同

**环境配置**:
```bash
# 启用所有功能
export LLM_GATEWAY_BYPASS_ROUTING=false
export LLM_GATEWAY_ENABLE_STICKY=true
export LLM_GATEWAY_ENABLE_COMPRESSION=true
export LLM_GATEWAY_ENABLE_INPUT_DETECTION=true
export LLM_GATEWAY_ENABLE_OUTPUT_COMPLIANCE=true
```

**测试场景** (待实现):
- 🔄 完整路由流程 (5 用例)
- 🔄 故障恢复 (3 用例)
- 🔄 性能稳定性 (3 用例)

**验收标准**:
- 成功率: >99.5%
- P95 延迟: <5s
- 24h 无 panic

## 🛠️ 测试工具

### assert.sh - 断言库

提供丰富的测试断言函数：

```bash
# HTTP 状态断言
assert_http_status 200

# JSON 字段断言
assert_json_field_exists "$response" ".id"
assert_json_field_equals "$response" ".model" "gpt-4"

# 日志断言
assert_log_contains "credential_id=123"

# 性能断言
assert_response_time_under 2000 "$response_time_ms"

# 通用断言
assert_equals "expected" "actual" "description"
assert_gt 10 5 "value"
assert_contains "$string" "substring"
```

### 测试帮助函数

```bash
# HTTP 请求
response_time=$(http_request "POST" "$url" "$payload" "$headers")

# 日志捕获
capture_logs 100  # 最近 100 行

# 服务等待
wait_for_service "$url/healthz" 30  # 30 秒超时

# 测试组织
test_section "Section Title"
test_case "Test Case Name"
skip_test "Reason for skipping"
```

## 📝 编写新测试

### 测试模板

```bash
#!/bin/bash
set -euo pipefail

# Load assertion library
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../lib/assert.sh"

# Configuration
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"

# Test case
test_my_feature() {
    test_case "My Feature Test"
    
    local payload='{"model": "gpt-4", "messages": [...]}'
    
    local response_time
    response_time=$(http_request "POST" "$GATEWAY_URL/v1/chat/completions" \
        "$payload" "Authorization: Bearer $API_KEY")
    
    # Assertions
    assert_http_status 200
    assert_json_field_exists "$LAST_RESPONSE" ".id"
    assert_response_time_under 3000 "$response_time"
}

# Main
main() {
    init_test_run
    test_section "My Test Suite"
    
    wait_for_service "$GATEWAY_URL/healthz" 30
    
    test_my_feature
    
    finalize_test_run
}

main "$@"
```

### 最佳实践

1. **独立性**: 每个测试独立运行，不依赖其他测试的状态
2. **清理**: 使用 `trap` 确保资源清理
3. **超时**: 设置合理的超时避免测试挂起
4. **日志**: 记录足够的上下文信息用于调试
5. **幂等性**: 测试可重复运行

## 🔍 故障排查

### 测试失败

1. **查看详细日志**:
   ```bash
   ./run_all_tests.sh layer1 2>&1 | tee test.log
   ```

2. **检查 Gateway 状态**:
   ```bash
   curl http://localhost:8080/healthz
   kubectl logs -l app=llm-gateway --tail=100
   ```

3. **验证环境变量**:
   ```bash
   echo $GATEWAY_URL
   echo $API_KEY
   ```

### 常见问题

**Q: 测试无法连接到 Gateway**
```bash
# 检查 Gateway 是否运行
curl http://localhost:8080/healthz

# 检查端口
lsof -i :8080
```

**Q: jq 命令未找到**
```bash
# macOS
brew install jq

# Linux
sudo apt-get install jq
```

**Q: 权限拒绝**
```bash
# 添加执行权限
chmod +x tests/routing/**/*.sh
```

## 📈 监控与指标

### 测试指标

测试运行器会输出：
- 总测试数
- 通过数
- 失败数
- 成功率

### 集成 CI/CD

```yaml
# GitHub Actions 示例
- name: Run routing tests
  run: |
    cd tests/routing
    ./run_all_tests.sh all
  env:
    GATEWAY_URL: http://localhost:8080
    API_KEY: ${{ secrets.TEST_API_KEY }}
```

## 🎯 当前状态

| 层级 | 测试套件数 | 实现状态 | 通过率 |
|------|-----------|---------|--------|
| **Layer 1** | 3 | 1/3 实现 | - |
| **Layer 2** | 4 | 1/4 实现 | - |
| **Layer 3** | 3 | 0/3 实现 | - |
| **总计** | 10 | 2/10 实现 | - |

### 已实现
- ✅ 断言库 (lib/assert.sh)
- ✅ 测试运行器 (run_all_tests.sh)
- ✅ Layer 1: OpenAI 直连测试
- ✅ Layer 2: Sticky Session 测试

### 待实现
- 🔄 Layer 1: Anthropic 直连测试
- 🔄 Layer 1: 协议转换测试
- 🔄 Layer 2: Compression 测试
- 🔄 Layer 2: Detection 测试
- 🔄 Layer 3: 完整路由测试
- 🔄 Layer 3: 故障恢复测试
- 🔄 Layer 3: 性能测试

## 📚 参考文档

- [诊断报告](../../docs/superpowers/specs/2026-07-18-routing-diagnosis.md)
- [测试策略](../../docs/superpowers/specs/2026-07-18-routing-layered-testing.md)
- [架构图表](../../docs/superpowers/specs/2026-07-18-routing-architecture-diagrams.md)
- [执行总结](../../docs/superpowers/specs/2026-07-18-routing-analysis-summary.md)

## 🤝 贡献

欢迎贡献新的测试用例！请遵循现有的测试模板和最佳实践。

---

**最后更新**: 2026-07-19  
**维护者**: LLM Gateway Team
