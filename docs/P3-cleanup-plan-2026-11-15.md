# P3×11项清理计划 — 2026-11-15

**来源**: [12小时内修订审计-20261115-1300.md](./12小时内修订审计-20261115-1300.md) §P3章节  
**优先级**: P3（可接受遗留，3个月内技术债清理轮）  
**状态**: 规划中，待Owner审批

---

## 执行摘要

本计划覆盖审计文档中识别的11个P3级别问题，按影响面排序为：
- **高优先** (2项): P3-8 gateway-v2执法真空、P3-11 HMAC key弱口令
- **中优先** (2项): P3-14 plugin-runtime watch冗余、P3-13 prewarmed跨协议混入
- **低优先** (7项): 其余归因/资源管理/数据质量问题

---

## 高优先修复 (2项)

### P3-8: cmd/gateway-v2缺KeyConcurrentGate执法真空

**位置**: `cmd/gateway-v2/main.go`  
**根因**: gateway-v2是Pipeline架构演示入口，未集成KeyConcurrentGate并发闸  
**影响**: 
- docker-compose.dev-research.yml引用该二进制
- 若上生产，显式帽key(ConfiguredConcurrent)不受执法
- NULL key的executors纵深软告警消失（82个key失去观察面）
- tierDefaults第二列(default=6/free=3...)现为全链死值

**当前状态分析**:
- gateway-v2明确标注为"并行的演示入口"（main.go:3）
- line 534-538显式拒绝stream=true："stream=true is not supported in gateway-v2 demo mode"
- 未找到chatRouteHandler/messagesRouteHandler/responsesRouteHandler等streaming handlers
- 主要用途：展示Hook Pipeline架构，供集成测试参考

**建议处置**:
1. **选项A（最小化）**: 在gateway-v2文档头部和docker-compose.dev-research.yml添加警告注释："仅供开发测试，禁止生产部署；无并发执法"
2. **选项B（完整集成）**: 参考cmd/gateway/main.go:7475-7513，集成KeyConcurrentGate：
   - 添加`keyConcurrentGate := streaming.NewKeyConcurrentGate()`
   - 为未来streaming支持预留Wrap点
   - 添加KeyConcurrencyStatsWriter（需dbConn）
3. **选项C（分阶段）**: 当前A方案文档化风险，未来若gateway-v2需生产化再执行B方案

**推荐**: 选项A（文档化），理由：gateway-v2当前定位明确为demo，增加执法成本收益比低

---

### P3-11: security/sanitize HMAC key弱口令防护

**位置**: `internal/logging/standard.go:121-122`  
**根因**: `LLM_GATEWAY_LOG_REDACT_HMAC_KEY` 环境变量原样作HMAC key，无长度检查/KDF  
**影响**: 弱口令可暴力对拍还原假名（确定性HMAC ⇒ 候选值重算即可验证）

**代码片段**:
```go
// line 121-122
if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_LOG_REDACT_HMAC_KEY")); v != "" {
    redactHMACKey = []byte(v)  // ← 直接使用，无验证
    return
}
```

**修复方案**:
```go
func redactionHMACKey() []byte {
    redactHMACKeyOnce.Do(func() {
        if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_LOG_REDACT_HMAC_KEY")); v != "" {
            // P3-11修复：弱口令防护
            if len(v) < 32 {
                slog.Error("LLM_GATEWAY_LOG_REDACT_HMAC_KEY too short (min 32 bytes); refusing weak key",
                    "provided_length", len(v))
                os.Exit(1)  // fail-closed: 弱密钥不可用
            }
            // 可选：KDF派生（HKDF-SHA256）增强熵
            // salt := []byte("llm-gateway-log-redact-v1")
            // kdf := hkdf.New(sha256.New, []byte(v), salt, nil)
            // key := make([]byte, 32)
            // if _, err := io.ReadFull(kdf, key); err != nil { panic }
            // redactHMACKey = key
            redactHMACKey = []byte(v)
            return
        }
        // ... 原有随机生成逻辑 ...
    })
    return redactHMACKey
}
```

**测试要求**:
- ✅ 拒绝<32字节密钥（exit 1）
- ✅ 接受≥32字节密钥
- ✅ 未配置时生成随机密钥（既有行为保留）
- ✅ 同密钥产出同假名（Pseudonym幂等性）

**部署影响**:
- 生产环境需确认现有`LLM_GATEWAY_LOG_REDACT_HMAC_KEY`长度≥32
- 若<32，需rotation到强密钥（假名会全量刷新，历史关联断裂）

---

## 中优先优化 (2项)

### P3-14: plugin-runtime watch重建冗余优化

**位置**: `plugin-runtime/config/reloader.go:101`  
**根因**: reloader已watch**目录**(`Add(Dir)`)，atomic replace不丢dir watch；重建冗余但无害  
**真实风险**: Remove→Add窗口丢事件、Add失败仅log无重试循环

**建议修复**:
1. 移除冗余watch重建（降噪）
2. 添加Add失败后的重试循环（with backoff）
3. 添加metrics: `plugin_config_watch_failures_total`

---

### P3-13: prewarmed跨协议混入规范化

**位置**: `domains/streaming/handler.go writePrewarmedStreamError*`  
**根因**: 对Anthropic/messages族也补OpenAI风格裸`data: [DONE]`  
**影响**: Anthropic SSE客户端按`event:`类型解析，多数实现忽略无名data行，风险小但属跨协议语义混入

**建议修复**:
```go
// 根据协议类型选择终止符
func terminatePrewarmedStream(w http.ResponseWriter, protocol string) {
    if protocol == "anthropic" {
        // Anthropic协议：使用event: message_stop
        fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
    } else {
        // OpenAI协议：使用data: [DONE]
        fmt.Fprintf(w, "data: [DONE]\n\n")
    }
}
```

---

## 低优先项 (7项)

### P3-9: routing_errors.go网关shutdown归为client_canceled
- **位置**: `domains/streaming/routing_errors.go`
- **影响**: 轻微误归因（shutdown的ctx cancel → 499）
- **修复**: 添加shutdown context判定分支

### P3-10: ratelimit/sliding.go keyID桶常驻
- **位置**: `ratelimit/sliding.go:156`、`anomaly/request_rate.go:40`
- **影响**: sync.Map条目常驻，但以API key总数为界，可控
- **原状态**: 改为keyID后比原sessionID更收敛
- **建议**: 添加LRU淘汰策略（可选）

### P3-12: ratelimit TPM预记账无完成侧退款
- **位置**: `ratelimit` TPM admission-control
- **状态**: documented行为（设计如此）
- **处置**: 保持现状，在文档中明确说明

### P3-15: promptinjection api_key明文读出
- **位置**: `domains/promptinjection/detector.go`
- **状态**: 既有schema设计
- **处置**: 技术债登记，未来schema v2加密

### P3-16: promptinjection parseLLMResponse解析脆弱
- **位置**: `domains/promptinjection/detector.go parseLLMResponse`
- **影响**: 首尾大括号截取在多JSON输出下误解析（轻微）
- **修复**: 使用json.Decoder.Token()遍历取第一个完整对象

### P3-17: promptinjection PII SSN误报
- **位置**: `domains/promptinjection/detector.go`
- **影响**: PII纯SSN `\d{3}-\d{2}-\d{4}` 有误报面（量具级）
- **处置**: 添加上下文验证（如"SSN:"前缀）降低误报

### P3-18: casdoor GetUsers全量拉取
- **位置**: `pkg/authplugin/casdoor/user_manager.go:85`
- **根因**: `GetUser(userID)`内部调`client.GetUsers()`全量拉取再遍历匹配
- **SDK有**: `GetUserByUserId(userId)`单查API未用
- **影响**: 用户数<1000时可接受，>1000时性能问题
- **修复**: 
```go
func (m *UserManager) GetUser(ctx context.Context, userID string) (*User, error) {
    // 直接使用单查API
    sdkUser, err := m.client.GetUserByUserId(userID)
    if err != nil {
        return nil, err
    }
    return convertSDKUser(sdkUser), nil
}
```

---

## 执行计划

### 第一批（本周，高优先2项）
1. **P3-11 HMAC key**: 添加长度检查 + 测试 + 文档更新
2. **P3-8 gateway-v2**: 文档化风险（选项A）

### 第二批（下双周，中优先2项）
3. **P3-14 plugin-runtime**: watch重建优化 + 重试循环
4. **P3-13 prewarmed**: 协议规范化

### 第三批（月末，低优先7项按需）
5. P3-18 casdoor优先（高用户量环境性能收益明确）
6. 其余6项按技术债清理排期

---

## 验收标准

每项修复需满足：
- ✅ 单元测试覆盖新增逻辑（含负例）
- ✅ 既有测试全绿（无回归）
- ✅ 代码审查通过
- ✅ 文档更新（ADR/CHANGELOG/配置示例）
- ✅ 部署前影响评估（如密钥rotation）

---

## 遗留风险说明

P3级别问题不阻塞当前发布，但应在3个月技术债清理窗口内处置。高/中优先项建议在下一迭代纳入sprint；低优先项可按实际影响排期。

**禁止操作**:
- 直接修改生产环境配置（如弱HMAC key轮换需变更管理流程）
- 在gateway-v2未生产化前强制集成KeyConcurrentGate（过度工程）
- 为<1000用户环境的casdoor做单查优化（性能收益不明确）

---

## 附录：审计文档引用

- [12小时内修订审计-20261115-1300.md](./12小时内修订审计-20261115-1300.md)
  - §P3（可接受遗留）：行58-97
  - §遗留风险矩阵：行230-236
- 审计基线：`55dc3b372`（phase2/phase3合并后）
- 修复提交：`dc9663d39`（P1×2根修，P3待本计划）
