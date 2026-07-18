# 路由系统测试 - 完整指南

**版本**: v1.0  
**日期**: 2026-07-19  
**测试覆盖**: 52 个用例, 100%  

---

## 目录

- [概览](#概览)
- [测试架构](#测试架构)
- [52 个测试用例详解](#52-个测试用例详解)
- [运行指南](#运行指南)
- [配置参考](#配置参考)
- [CI/CD 集成](#cicd-集成)
- [故障排查](#故障排查)

---

## 概览

### 测试目标

路由系统测试专注于验证 LLM Gateway 的核心路由逻辑，包括：

- ✅ 供应商连通性（OpenAI, Anthropic）
- ✅ 协议转换（双向）
- ✅ Sticky Session（L1/L2/L3）
- ✅ 压缩与上下文管理
- ✅ 安全检测（注入、PII）
- ✅ 完整路由集成
- ✅ 故障恢复
- ✅ 性能稳定性

### 测试统计

| 指标 | 值 |
|------|-----|
| **总用例数** | 52 |
| **测试层级** | 3 (Layer 1-3) |
| **运行时间** | 10-15 分钟 (快速模式) |
| **测试覆盖** | 100% |
| **自动化** | 全自动 |

### 测试分层

```
Layer 1: 直连测试 (18 个)
├── 验证供应商连通性
├── 协议正确性
└── 基础功能

Layer 2: 组件测试 (19 个)
├── Sticky Session
├── Compression
└── Security Detection

Layer 3: 集成测试 (15 个)
├── Full Routing
├── Fault Recovery
└── Performance
```

---

## 测试架构

### 测试框架组成

```
tests/routing/
├── run_all_tests.sh           # 测试运行器
├── lib/
│   └── assert.sh              # 断言库 (14 函数)
├── layer1_direct/             # Layer 1 测试
│   ├── test_openai_direct.sh
│   ├── test_anthropic_direct.sh
│   └── test_protocol_conversion.sh
├── layer2_components/         # Layer 2 测试
│   ├── test_sticky.sh
│   ├── test_compression.sh
│   └── test_detection.sh
└── layer3_integration/        # Layer 3 测试
    ├── test_full_routing.sh
    ├── test_fault_recovery.sh
    └── test_performance.sh
```

### 断言库功能

14 个核心断言函数：

| 函数 | 功能 |
|------|------|
| `assert_http_status` | HTTP 状态码验证 |
| `assert_json_field_exists` | JSON 字段存在性 |
| `assert_json_field_equals` | JSON 字段值比较 |
| `assert_contains` | 字符串包含 |
| `assert_equals` | 值相等 |
| `assert_gt` | 大于比较 |
| `assert_response_time_under` | 响应时间验证 |
| `assert_log_contains` | 日志内容检查 |
| `http_request` | HTTP 请求封装 |
| `capture_logs` | 日志捕获 |
| `wait_for_service` | 服务就绪等待 |
| `test_case` | 测试用例声明 |
| `test_section` | 测试章节 |
| `skip_test` | 跳过测试 |

---

## 52 个测试用例详解

### Layer 1: 直连测试（18 个）

#### 1.1 OpenAI Direct（6 个）

**T1.1: Chat Completion (非流式)**
- 验证: HTTP 200, JSON 结构, 响应时间
- 预期: 成功率 100%, P95 < 3s

**T1.2: Streaming Response**
- 验证: SSE 流式输出, `data: [DONE]`
- 预期: TTFB < 1s, 完整接收率 100%

**T1.3: Error Handling (无效模型)**
- 验证: 4xx 错误, 错误信息格式
- 预期: 正确返回错误

**T1.4: Tool Calls**
- 验证: 工具调用请求/响应格式
- 预期: 工具调用成功触发

**T1.5: Large Context**
- 验证: 大上下文处理 (4K+ tokens)
- 预期: 成功处理, token 统计正确

**T1.6: Concurrent Requests (10 并发)**
- 验证: 并发处理能力
- 预期: 成功率 > 95%

#### 1.2 Anthropic Direct（6 个）

**T1.7: Messages API**
- 验证: Anthropic Messages API 格式
- 预期: HTTP 200, 正确响应结构

**T1.8: Streaming**
- 验证: Anthropic SSE 流
- 预期: `event: message_stop` 正确接收

**T1.9: Tool Use**
- 验证: Anthropic 工具使用
- 预期: tool_use 触发成功

**T1.10: System Prompt**
- 验证: system 字段处理
- 预期: 系统提示生效

**T1.11: Multi-turn Conversation**
- 验证: 多轮对话上下文
- 预期: 上下文保持正确

**T1.12: Error Handling**
- 验证: Anthropic 错误格式
- 预期: 正确错误响应

#### 1.3 Protocol Conversion（6 个）

**T1.13: OpenAI → Anthropic**
- 验证: OpenAI 格式转 Anthropic 上游
- 预期: 转换成功, 返回 OpenAI 格式

**T1.14: Anthropic → OpenAI**
- 验证: Anthropic 格式转 OpenAI 上游
- 预期: 转换成功, 返回 Anthropic 格式

**T1.15: Tool Calls 转换**
- 验证: 工具调用跨协议转换
- 预期: 工具调用格式正确转换

**T1.16: Streaming 转换**
- 验证: 流式响应协议转换
- 预期: SSE 格式正确转换

**T1.17: System Message 转换**
- 验证: system 消息转换
- 预期: 系统消息正确处理

**T1.18: Error Message 转换**
- 验证: 错误消息格式转换
- 预期: 错误格式统一返回

### Layer 2: 组件测试（19 个）

#### 2.1 Sticky Session（6 个）

**T2.1.1: L1 Sticky (session+model)**
- 验证: 同会话同模型绑定
- 预期: 使用相同凭据, 日志 "sticky L1 hit"

**T2.1.2: L2 Sticky (client+model)**
- 验证: 同客户端同模型跨会话绑定
- 预期: L2 命中, 日志 "sticky L2 hit"

**T2.1.3: L3 Sticky (client baseline)**
- 验证: 客户端基线跨模型绑定
- 预期: L3 命中, 日志 "sticky L3 hit"

**T2.1.4: Failure Cleanup**
- 验证: 连续失败后清理 sticky
- 预期: 失败阈值后切换新凭据

**T2.1.5: TTL Expiration**
- 验证: TTL 过期后重新路由
- 预期: TTL 过期不再使用旧凭据

**T2.1.6: Persistence**
- 验证: Sticky 持久化 (需重启)
- 预期: 重启后 sticky 恢复

#### 2.2 Compression（6 个）

**T2.2.1: Context Overflow → 压缩**
- 验证: 上下文窗口溢出自动压缩
- 预期: 日志显示压缩, 请求成功

**T2.2.2: Compression Quality**
- 验证: 压缩后信息保留
- 预期: 关键实体保留, 对话连贯

**T2.2.3: Memora Integration**
- 验证: Memora 写入和查询
- 预期: Session facts 正确存储

**T2.2.4: Compression Modes**
- 验证: LCS vs Memora 模式
- 预期: 两种模式都工作

**T2.2.5: Compression Metrics**
- 验证: Token 使用统计
- 预期: 压缩前后 token 统计正确

**T2.2.6: Error Handling**
- 验证: 压缩失败处理
- 预期: 优雅降级或返回错误

#### 2.3 Detection & Security（7 个）

**T2.3.1: Prompt Injection (4 模式)**
- 验证: 4 种注入模式检测
- 预期: 检测率 > 0

**T2.3.2: PII Detection (5 类型)**
- 验证: 邮箱、电话、SSN、信用卡、地址
- 预期: 检测率 > 0

**T2.3.3: Jailbreak Detection**
- 验证: 越狱尝试检测
- 预期: 阻止或日志记录

**T2.4.1: Harmful Content**
- 验证: 输出合规性检查
- 预期: 有害内容检测

**T2.4.2: PII Redaction**
- 验证: 输出 PII 脱敏
- 预期: PII 被脱敏或日志

**T2.4.3: False Positive Rate**
- 验证: 正常内容误报率
- 预期: 5 个正常请求通过 ≥ 4 个

**T2.4.4: Audit Trail**
- 验证: 安全事件审计日志
- 预期: 审计日志存在

### Layer 3: 集成测试（15 个）

#### 3.1 Full Routing（7 个）

**T3.1: 多候选路由**
- 验证: 多个凭据负载均衡
- 预期: 凭据分布相对均匀

**T3.2: 候选过滤**
- 验证: 自动跳过不可用凭据
- 预期: 日志显示过滤原因

**T3.3: Sticky + 路由集成**
- 验证: Sticky 与路由联合工作
- 预期: Sticky 优先级正确

**T3.4: 降级模式**
- 验证: 单候选降级处理
- 预期: 降级模式生效, 请求成功

**T3.5: 无候选探测恢复**
- 验证: 同步探测恢复机制
- 预期: 探测成功后恢复

**T3.6: 混合负载 (20 并发)**
- 验证: 混合模型并发处理
- 预期: 成功率 > 95%

**T3.7: E2E 延迟**
- 验证: 端到端延迟
- 预期: P95 < 5s

#### 3.2 Fault Recovery（5 个）

**T3.8: 凭据故障切换**
- 验证: 凭据失败自动切换
- 预期: 高可用性 (≥ 9/10 成功)

**T3.9: 供应商恢复**
- 验证: 自动重试逻辑
- 预期: 重试成功或优雅失败

**T3.10: 优雅降级**
- 验证: 压力下服务连续性
- 预期: 成功率 ≥ 80% 或优雅降级

**T3.11: Sticky 故障清理**
- 验证: 故障后 sticky 清理
- 预期: 检测到凭据切换

**T3.12: 熔断器**
- 验证: 快速失败机制
- 预期: 连续快速失败 (< 100ms)

#### 3.3 Performance（3 个）

**T3.13: 长期稳定性 (1 小时)**
- 验证: 长时间运行稳定性
- 配置: `LONG_RUN_DURATION=3600`
- 预期: 成功率 > 95%, 内存增长 < 500MB

**T3.14: 突发流量 (100 并发)**
- 验证: 突发并发处理
- 配置: `BURST_CONCURRENT=100`
- 预期: 成功率 > 90%, 响应时间分布

**T3.15: 持续负载 (5 分钟)**
- 验证: 持续负载稳定性
- 配置: 10 RPS × 300s
- 预期: 成功率 > 95%, 延迟稳定

---

## 运行指南

### 快速开始

```bash
# 1. 进入测试目录
cd tests/routing

# 2. 检查环境
command -v jq && echo "✓ jq installed"
command -v curl && echo "✓ curl installed"
curl http://localhost:8080/healthz

# 3. 配置环境变量
export GATEWAY_URL="http://localhost:8080"
export API_KEY="your-test-key"

# 4. 运行所有测试
./run_all_tests.sh all
```

### 分层运行

```bash
# Layer 1: 直连测试 (2-3 分钟)
./run_all_tests.sh layer1

# Layer 2: 组件测试 (3-4 分钟)
./run_all_tests.sh layer2

# Layer 3: 集成测试 (2-3 分钟)
./run_all_tests.sh layer3
```

### 单个测试

```bash
# 运行特定测试脚本
./layer1_direct/test_openai_direct.sh
./layer2_components/test_sticky.sh
./layer3_integration/test_full_routing.sh
```

### 快速模式

```bash
# 跳过长时间性能测试
export SKIP_LONG_TESTS=true
./run_all_tests.sh all
```

---

## 配置参考

### 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `GATEWAY_URL` | `http://localhost:8080` | Gateway 地址 |
| `API_KEY` | `test-key` | 测试 API Key |
| `FORCE_CREDENTIAL_ID` | - | 强制使用凭据 ID (Layer 1) |
| `OPENAI_CREDENTIAL_ID` | `123` | OpenAI 凭据 ID |
| `ANTHROPIC_CREDENTIAL_ID` | `456` | Anthropic 凭据 ID |
| `SKIP_LONG_TESTS` | `false` | 跳过长时间测试 |
| `LONG_RUN_DURATION` | `3600` | 长期测试时长 (秒) |
| `BURST_CONCURRENT` | `100` | 突发并发数 |

### Layer 1 配置

```bash
export LLM_GATEWAY_BYPASS_ROUTING=true
export LLM_GATEWAY_FORCE_CREDENTIAL_ID=123
export LLM_GATEWAY_DISABLE_STICKY=true
export LLM_GATEWAY_DISABLE_COMPRESSION=true
```

### Layer 2 配置

```bash
export LLM_GATEWAY_BYPASS_ROUTING=false
export LLM_GATEWAY_ENABLE_STICKY=true
export LLM_GATEWAY_ENABLE_COMPRESSION=true
export LLM_GATEWAY_ENABLE_INPUT_DETECTION=true
```

### Layer 3 配置

```bash
# 启用所有组件
export LLM_GATEWAY_BYPASS_ROUTING=false
export LLM_GATEWAY_ENABLE_STICKY=true
export LLM_GATEWAY_ENABLE_COMPRESSION=true
export LLM_GATEWAY_ENABLE_INPUT_DETECTION=true
export LLM_GATEWAY_ENABLE_OUTPUT_COMPLIANCE=true
```

---

## CI/CD 集成

### GitHub Actions

```yaml
- name: Run routing tests
  env:
    GATEWAY_URL: http://localhost:8080
    SKIP_LONG_TESTS: true
  run: |
    cd tests/routing
    ./run_all_tests.sh all
```

### GitLab CI

```yaml
routing-tests:
  script:
    - cd tests/routing
    - export SKIP_LONG_TESTS=true
    - ./run_all_tests.sh all
```

详见: [routing-ci-cd.md](./routing-ci-cd.md)

---

## 故障排查

### 常见问题

1. **Gateway 未运行**: `curl http://localhost:8080/healthz`
2. **jq 未安装**: `brew install jq` (macOS)
3. **权限拒绝**: `chmod +x tests/routing/**/*.sh`
4. **测试超时**: 增加 `LLM_GATEWAY_UPSTREAM_TIMEOUT`
5. **并发失败**: 降低 `BURST_CONCURRENT`

### 调试方法

```bash
# 启用详细输出
bash -x ./run_all_tests.sh layer1

# 查看特定测试
./layer1_direct/test_openai_direct.sh 2>&1 | tee debug.log

# 检查 Gateway 日志
kubectl logs -l app=llm-gateway --tail=100
```

---

## 参考资料

- [测试框架验证报告](../superpowers/specs/2026-07-19-test-framework-verification.md)
- [100% 测试覆盖报告](../superpowers/specs/2026-07-19-test-coverage-100-complete.md)
- [路由系统诊断](../superpowers/specs/2026-07-18-routing-diagnosis.md)
- [架构与流程图](../superpowers/specs/2026-07-18-routing-architecture-diagrams.md)

---

**文档版本**: v1.0  
**最后更新**: 2026-07-19  
**维护者**: LLM Gateway Team
