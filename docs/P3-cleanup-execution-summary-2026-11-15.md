# P3×11项清理执行总结 — 2026-11-15

**目标**: 处理12小时审计报告中的11个P3级别问题  
**执行时间**: 2026-11-15 21:30 ~ 22:45  
**状态**: ✅ 规划完成，待实施

---

## 执行摘要

### 已完成
✅ **完整规划文档** (`P3-cleanup-plan-2026-11-15.md`)
- 11个P3项按影响面分为高/中/低三档
- 每项包含根因分析、影响评估、修复方案、测试要求
- 明确执行顺序和验收标准

### 关键发现

#### 1. P3-8: cmd/gateway-v2缺KeyConcurrentGate — 实为设计选择
**分析结论**:
- gateway-v2是**演示入口**，非生产二进制（main.go:3明确标注）
- 明确拒绝streaming（line 534-538: "stream=true is not supported in gateway-v2 demo mode"）
- 未找到chatRouteHandler等streaming处理器
- **推荐**: 文档化风险而非强制集成（过度工程）

**处置方案**: 
- 选项A（推荐）: 在gateway-v2头部和docker-compose.dev-research.yml添加警告
- 选项B: 完整集成KeyConcurrentGate（参考cmd/gateway/main.go:7475-7513）
- 选项C: 分阶段，当前文档化，未来生产化时再集成

#### 2. P3-11: HMAC key弱口令 — 高风险，需fail-closed修复
**位置**: `internal/logging/standard.go:121-122`

**问题**:
```go
if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_LOG_REDACT_HMAC_KEY")); v != "" {
    redactHMACKey = []byte(v)  // ← 直接使用，无验证
}
```

**修复方案**:
```go
if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_LOG_REDACT_HMAC_KEY")); v != "" {
    // P3-11修复：弱口令防护
    if len(v) < 32 {
        slog.Error("LLM_GATEWAY_LOG_REDACT_HMAC_KEY too short (min 32 bytes); refusing weak key",
            "provided_length", len(v))
        os.Exit(1)  // fail-closed
    }
    redactHMACKey = []byte(v)
    return
}
```

**部署影响**: 生产环境需确认现有密钥≥32字节，否则需rotation

---

## 优先级分组

### 🔴 高优先 (2项) — 本周
1. **P3-11**: HMAC key长度检查 + 测试 + 文档
2. **P3-8**: gateway-v2文档化风险（选项A）

### 🟡 中优先 (2项) — 下双周
3. **P3-14**: plugin-runtime watch优化 + 重试循环
4. **P3-13**: prewarmed协议规范化（Anthropic vs OpenAI终止符）

### 🟢 低优先 (7项) — 月末/按需
5. **P3-18**: casdoor GetUserByUserId单查API（高用户量环境收益明确）
6. **P3-9**: routing_errors.go shutdown误归因（轻微）
7. **P3-10**: ratelimit keyID桶常驻（可控，可选LRU）
8. **P3-12**: TPM预记账无退款（documented行为）
9. **P3-15**: promptinjection api_key明文（schema债）
10. **P3-16**: parseLLMResponse解析脆弱（轻微）
11. **P3-17**: PII SSN误报（量具级）

---

## 详细分析矩阵

| 项目 | 位置 | 影响 | 修复复杂度 | 测试要求 | 推荐处置 |
|------|------|------|-----------|---------|---------|
| P3-8 | cmd/gateway-v2/main.go | demo入口，生产化时需集成 | 低（文档）/ 中（集成） | 文档审查 / 并发闸测试 | 选项A文档化 |
| P3-11 | internal/logging/standard.go:121 | 弱密钥可暴力破解假名 | 低 | ✅拒绝<32字节 ✅幂等性 | fail-closed检查 |
| P3-14 | plugin-runtime/config/reloader.go | watch重建冗余 + Add失败无重试 | 中 | 重试循环 + metrics | 优化 + 容错 |
| P3-13 | domains/streaming/handler.go | Anthropic混入OpenAI [DONE] | 低 | 协议分支测试 | 协议规范化 |
| P3-18 | pkg/authplugin/casdoor/user_manager.go:85 | <1000用户可接受，>1000性能问题 | 低 | 单查API验证 | 按用户量决策 |
| P3-9 | domains/streaming/routing_errors.go | shutdown归为499（轻微误归因） | 低 | shutdown context判定 | 可选优化 |
| P3-10 | ratelimit/sliding.go:156 | sync.Map常驻，以key总数为界 | 中 | LRU淘汰测试 | 可选LRU |
| P3-12 | ratelimit TPM | admission-control设计 | - | - | 保持现状 |
| P3-15 | domains/promptinjection/detector.go | 既有schema | - | - | 技术债登记 |
| P3-16 | promptinjection parseLLMResponse | 多JSON误解析 | 低 | json.Decoder测试 | 可选强化 |
| P3-17 | promptinjection PII SSN | 误报面 | 中 | 上下文验证 | 可选降噪 |

---

## 实施计划

### 第一批（本周，2项）
```bash
# P3-11: HMAC key长度检查
1. 修改 internal/logging/standard.go:121-122
2. 添加测试 internal/logging/standard_test.go
   - TestRedactionHMACKey_WeakKeyRejected
   - TestRedactionHMACKey_StrongKeyAccepted
   - TestPseudonym_Idempotent
3. 更新文档: 配置示例 + 部署前检查清单
4. 验证生产环境密钥长度

# P3-8: gateway-v2文档化
1. 修改 cmd/gateway-v2/main.go 头部注释
2. 修改 docker-compose.dev-research.yml 添加警告
3. 文档: DEPLOYMENT.md 补充gateway-v2限制说明
```

### 第二批（下双周，2项）
```bash
# P3-14: plugin-runtime watch优化
1. 移除冗余watch重建
2. 添加Add失败重试循环（exponential backoff）
3. 添加metrics: plugin_config_watch_failures_total

# P3-13: prewarmed协议规范化
1. 添加协议判定: terminatePrewarmedStream(protocol string)
2. Anthropic: event: message_stop
3. OpenAI: data: [DONE]
4. 测试: 两协议分别验证终止符
```

### 第三批（月末，按需）
按实际影响和资源排期P3-18及其余6项

---

## 验收标准（每项必满足）

- ✅ 单元测试覆盖新增逻辑（含负例）
- ✅ 既有测试全绿（无回归）
- ✅ 代码审查通过
- ✅ 文档更新（ADR/CHANGELOG/配置示例）
- ✅ 部署前影响评估（如密钥rotation）

---

## 风险与约束

### 禁止操作
❌ 直接修改生产环境配置（密钥rotation需变更管理）  
❌ gateway-v2未生产化前强制集成KeyConcurrentGate  
❌ <1000用户环境的casdoor做单查优化（收益不明确）

### 遗留风险说明
P3级别问题**不阻塞当前发布**，应在3个月技术债清理窗口内处置。高/中优先项建议在下一迭代纳入sprint。

---

## 相关文档

- 📋 **详细计划**: [P3-cleanup-plan-2026-11-15.md](./P3-cleanup-plan-2026-11-15.md)
- 🔍 **审计来源**: [12小时内修订审计-20261115-1300.md](./12小时内修订审计-20261115-1300.md)
  - §P3章节（行58-97）
  - §遗留风险矩阵（行230-236）
- 📊 **审计基线**: `55dc3b372` (phase2/phase3合并后)
- ✅ **P1修复**: `dc9663d39` (GetOAuthToken密码模式 + 036撞号收编966)

---

## 执行记录

- **2026-11-15 21:30**: 启动P3清理任务
- **2026-11-15 21:45**: 完成11项分析和优先级排序
- **2026-11-15 22:15**: 完成详细修复方案和测试要求
- **2026-11-15 22:30**: 完成gateway-v2深入调研（确认为demo定位）
- **2026-11-15 22:45**: 文档输出完成

---

## 后续行动

### 立即行动（Owner审批后）
1. ✅ 创建issue追踪11个P3项
2. ✅ 第一批2项加入下一sprint
3. ✅ 通知SRE团队：生产HMAC密钥长度检查

### 监控指标
- P3修复进度：0/11 → 目标2周内2/11，月末4/11
- 回归测试：每项修复后全量测试套件必须绿
- 部署影响：记录每项生产变更（如密钥rotation）

---

**执行者**: Kiro AI  
**审批者**: 待Owner确认  
**文档版本**: v1.0  
**生成时间**: 2026-11-15 22:45
