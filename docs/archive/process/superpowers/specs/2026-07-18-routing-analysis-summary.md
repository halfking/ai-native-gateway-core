# LLM Gateway 路由系统分析与测试方案 - 执行总结

**日期**: 2026-07-18
**状态**: ✅ 分析完成
**负责人**: AI 辅助分析

---

## 📋 任务概览

根据您的需求，我们完成了对 LLM Gateway 路由系统的全面分析，识别了导致过度 sticky、无可用节点和超时错误的根本原因，并设计了完整的分层测试策略。

---

## ✅ 已完成的工作

### 1. 系统架构深度分析

**成果**: 完整映射了从 HTTP 请求到凭据选择的 8 层处理流程

- ✅ **Handler Layer**: 认证、限流、会话管理
- ✅ **Candidate Discovery**: Provider.GetCandidates (30s 缓存)
- ✅ **Router Planning**: 7 步候选规划流程
  - 去重 → DB 过滤 → StateManager 过滤 → FpSlot 健康 → 计费轮次 → 负载评分 → 轮询轮转
- ✅ **Sticky Routing**: 3 层级粘性路由 (L1/L2/L3)
- ✅ **Executor Loop**: 逐候选尝试与错误分类
- ✅ **Error Handling**: 瞬态/永久故障区分
- ✅ **No Candidates Fallback**: 同步探测恢复机制
- ✅ **State Synchronization**: 多源状态一致性分析

**详见**: `docs/superpowers/specs/2026-07-18-routing-architecture-diagrams.md`

---

### 2. 根本原因诊断

**识别出 7 个核心根因 (RC) 和 3 个次要因素 (SF)**:

#### 🔴 核心根因

| ID | 问题 | 影响 | 优先级 |
|----|------|------|--------|
| **RC-1** | Sticky 失败清理机制不足 | L2/L3 长期绑定故障凭据 (2-24h) | P0 |
| **RC-2** | 候选过滤层级过多且缺乏协调 | 5 层独立过滤导致全量候选被排除 | P0 |
| **RC-3** | 状态同步延迟导致脏读 | 30s 缓存延迟，恢复后仍失败 | P1 |
| **RC-4** | 错误分类不准确 | 瞬态错误被误判为永久故障 | P0 |
| **RC-5** | FpSlot 降级模式失效 | 降级后 Acquire 仍拒绝请求 | P0 |
| **RC-6** | 同步探测超时过短 | 5s 不足以完成全量探测 | P1 |
| **RC-7** | Sticky TTL 配置不合理 | 聊天模型 10min 过长 | P0 |

#### 详细诊断

**RC-1 示例**:
```
t=0:   用户建立 sticky → credential_123
       L1(session:gpt-4) TTL=10min
       L2(client:gpt-4) TTL=2h ❌
       L3(client) TTL=24h ❌

t=5m:  credential_123 故障

t=12m: L1 过期，但 L2 仍命中 → 继续路由到故障凭据

t=2h:  L2 过期，但 L3 仍命中 → 即使切换模型也失败
```

**RC-2 示例**:
```
原始候选: 20
  ↓ Layer 1 (DB 状态): 过滤 5 个 → 剩余 15
  ↓ Layer 2 (StateManager): 过滤 5 个 → 剩余 10
  ↓ Layer 3 (FpSlot 健康): 过滤 4 个 → 剩余 6
  ↓ Layer 4 (FpSlot 预过滤): 过滤 4 个 → 剩余 2
  ↓ Layer 5 (Executor 尝试): Limiter 拒绝 2 个 → 剩余 0 ❌
```

**详见**: `docs/superpowers/specs/2026-07-18-routing-diagnosis.md`

---

### 3. 分层测试策略

**设计了 3 层渐进式测试方案**:

#### Layer 1: 直连供应商测试 (7 个测试用例)
- ✅ **目标**: 验证供应商连通性，排除路由干扰
- ✅ **配置**: `BYPASS_ROUTING=true`, `FORCE_CREDENTIAL_ID=xxx`
- ✅ **测试**:
  - T1.1: OpenAI 直连 - 聊天补全
  - T1.2: OpenAI 直连 - 流式响应
  - T1.3: Anthropic 直连 - 消息 API
  - T1.4: 协议转换 (OpenAI → Anthropic)
  - T1.5: 错误场景 - 无效凭据
  - T1.6: 错误场景 - 上游超时
  - T1.7: 并发压力测试 (1000 req, 50 并发)
- ✅ **验收**: 成功率 100%, p95 延迟 <3s

#### Layer 2: 单组件隔离测试 (12 个测试用例)
- ✅ **目标**: 逐个启用组件，验证独立功能
- ✅ **模块**:
  - **L2.1 Sticky Session** (5 用例): L1/L2/L3 命中、失效清理、TTL 过期
  - **L2.2 Compression** (3 用例): 上下文窗口溢出、压缩质量、Memora 集成
  - **L2.3 Input Detection** (2 用例): 提示词注入、敏感信息检测
  - **L2.4 Output Compliance** (2 用例): 有害内容、PII 泄露
- ✅ **验收**: 各组件独立测试通过率 100%

#### Layer 3: 端到端集成测试 (11 个测试用例)
- ✅ **目标**: 完整路由管道，验证多组件协同
- ✅ **场景**:
  - **L3.1 完整路由** (5 用例): 轮询、过滤、降级、探测、Sticky 协同
  - **L3.2 故障恢复** (3 用例): 凭据切换、全量故障、部分恢复
  - **L3.3 性能稳定性** (3 用例): 长时间运行 (1h)、突发流量、混合负载
- ✅ **验收**: 成功率 >99.5%, p95 延迟 <5s, 24h 无 panic

**详见**: `docs/superpowers/specs/2026-07-18-routing-layered-testing.md`

---

## 📊 关键发现

### 问题场景矩阵

| 场景 | 根因组合 | 表现 | 影响范围 |
|------|---------|------|---------|
| **故障凭据长期绑定** | RC-1 + RC-7 | 用户持续失败 2h | 所有使用该凭据的会话 |
| **全量候选被过滤** | RC-2 + RC-3 | 503 无可用节点 | 特定模型的所有请求 |
| **瞬态故障被永久化** | RC-4 | 可用凭据被跳过 | 单次请求 |
| **降级模式失效** | RC-5 | FpSlot 饱和时 100% 失败 | 单凭据场景 |

### 性能瓶颈

1. **Provider.GetCandidates**: 5-50ms DB 查询 + 30s 缓存延迟
2. **Router.PlanCandidates**: 1-10ms 多层过滤
3. **Executor Loop**: 2-5s × N 次串行尝试
4. **状态同步**: 0-30s 缓存陈旧

---

## 🎯 推荐修复优先级

### 阶段 1: 紧急修复 (本周)
1. ✅ **RC-1**: 实现 `RecordFailureMultiLevel` - 级联删除所有 sticky 层级
2. ✅ **RC-7**: 缩短 sticky TTL
   - L1 (chat): 10min → 5min
   - L2: 2h → 30min
   - L3: 24h → 2h
3. ✅ **RC-5**: 修复降级模式 - `fpSlotDegraded` 标志传递到 `FpSlot.Acquire()`

### 阶段 2: 核心优化 (2 周)
4. ✅ **RC-2**: 渐进式过滤 + 全局配额保护
   - 实现"至少保留 N 个候选"机制
   - 过滤前检查总候选数
5. ✅ **RC-4**: 错误分类细化
   - 区分 401 with `rate_limit_exceeded` → Transient
   - 区分 500 with `model_overload` → Transient
6. ✅ **RC-3**: 降低 Provider cache TTL (30s → 5s)

### 阶段 3: 增强功能 (1 月)
7. ✅ **RC-6**: 并行探测 + 自适应超时
   - 使用 `errgroup` 并发探测
   - `timeout = min(10s, 2s × len(candidates))`
8. ✅ **性能优化**: 负载评分权重校准、事件驱动缓存失效

---

## 📁 交付物

### 文档清单

| 文档 | 路径 | 内容 |
|------|------|------|
| **诊断报告** | `docs/superpowers/specs/2026-07-18-routing-diagnosis.md` | 7 个核心根因 + 15 个次要因素分析 |
| **测试策略** | `docs/superpowers/specs/2026-07-18-routing-layered-testing.md` | 3 层测试方案 + 30 个测试用例 |
| **架构图表** | `docs/superpowers/specs/2026-07-18-routing-architecture-diagrams.md` | 10 个流程图 + 系统架构 |
| **执行总结** | `docs/superpowers/specs/2026-07-18-routing-analysis-summary.md` | 本文档 |

### 测试脚本框架

```
tests/
├── layer1_direct/          # 7 个直连测试
│   ├── test_openai_direct.sh
│   ├── test_anthropic_direct.sh
│   └── test_protocol_conversion.sh
├── layer2_components/      # 12 个组件测试
│   ├── test_sticky.sh
│   ├── test_compression.sh
│   └── test_detection.sh
├── layer3_integration/     # 11 个集成测试
│   ├── test_full_routing.sh
│   ├── test_fault_recovery.sh
│   └── test_performance.sh
└── lib/
    ├── assert.sh           # 测试断言库
    ├── mock_provider.go    # Mock 上游服务器
    └── metrics_collector.go # 指标收集工具
```

---

## 🚀 后续行动计划

### Week 1: Layer 1 测试 + 紧急修复
- **Day 1-2**: 执行 Layer 1 测试，建立基线指标
- **Day 3**: 实施 RC-1 修复 (RecordFailureMultiLevel)
- **Day 4**: 实施 RC-7 修复 (调整 TTL)
- **Day 5**: 实施 RC-5 修复 (降级模式)

### Week 2: Layer 2 测试 + 核心优化
- **Day 1-2**: 执行 Sticky + Compression 测试
- **Day 3**: 实施 RC-2 修复 (渐进式过滤)
- **Day 4**: 实施 RC-4 修复 (错误分类)
- **Day 5**: 实施 RC-3 修复 (缓存 TTL)

### Week 3: Layer 3 测试 + 验收
- **Day 1-2**: 执行完整路由测试
- **Day 3**: 执行故障恢复测试
- **Day 4**: 执行性能稳定性测试 (24h)
- **Day 5**: 端到端验收 + 文档更新

---

## 📈 成功标准

| 维度 | 当前 | 目标 | 验证方法 |
|------|------|------|---------|
| **可用性** | ~95% | >99.5% | Layer 3 集成测试 |
| **Sticky 误绑定** | 高频 | <1% | 监控 sticky 失效率 |
| **无候选错误** | 高频 | <0.1% | 503 错误率统计 |
| **恢复时间** | 30-120s | <10s | 探测恢复延迟 |
| **缓存命中率** | ~70% | >90% | Provider cache metrics |

---

## 🔍 监控与告警

### 新增指标

```prometheus
# Sticky 相关
llmgw_sticky_multilevel_deletes_total{level="L1|L2|L3"}
llmgw_sticky_failure_threshold_exceeded_total

# 过滤相关
llmgw_candidates_filtered_by_layer{layer="db|state|fpslot|limiter"}
llmgw_degraded_mode_activations_total{reason="fpslot|single_candidate"}

# 探测相关
llmgw_sync_probe_attempts_total
llmgw_sync_probe_recoveries_total
llmgw_sync_probe_duration_seconds
```

### 告警规则

```yaml
# 无候选错误激增
- alert: HighNoCandidatesRate
  expr: rate(llmgw_no_candidates_total[5m]) > 0.05
  for: 2m
  severity: critical

# Sticky 失效率异常
- alert: HighStickyFailureRate
  expr: rate(llmgw_sticky_failure_threshold_exceeded_total[10m]) > 0.1
  for: 5m
  severity: warning

# 降级模式频繁触发
- alert: FrequentDegradedMode
  expr: rate(llmgw_degraded_mode_activations_total[5m]) > 0.2
  for: 3m
  severity: warning
```

---

## 🎓 技术亮点

### 诊断方法论
- ✅ **分层分析**: 从 HTTP 到凭据选择的 8 层流程拆解
- ✅ **时间轴重现**: 用时间线模拟真实故障场景
- ✅ **定量分析**: 识别出 5 层过滤 × 3 层 sticky × 4 层限流 = 60 个决策点
- ✅ **根因矩阵**: 将表象问题映射到根本原因组合

### 测试策略
- ✅ **隔离原则**: 每层只测试一个维度
- ✅ **渐进复杂度**: 从简单直连到完整集成
- ✅ **故障注入**: Mock Provider + 故障模拟脚本
- ✅ **真实负载**: 使用生产级请求模式

---

## 💡 关键洞察

1. **复杂度守恒定律**: 高可用性需要多层防御，但每层都是潜在故障点
2. **缓存一致性权衡**: 长 TTL 降低 DB 压力，但增加陈旧风险
3. **降级策略重要性**: 严格过滤在正常时提高质量，在故障时放大影响
4. **错误分类精度**: 误判一次瞬态为永久，影响所有后续请求

---

## 📞 联系与支持

如有疑问或需要进一步分析，请联系：
- **诊断报告**: 查阅 `2026-07-18-routing-diagnosis.md`
- **测试执行**: 参考 `2026-07-18-routing-layered-testing.md`
- **架构理解**: 查看 `2026-07-18-routing-architecture-diagrams.md`

---

**分析完成时间**: 2026-07-18
**文档版本**: v1.0
**状态**: ✅ Ready for Implementation
