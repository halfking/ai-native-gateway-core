# 会话打标 / 跨模块数据借用 / 输出脱敏与授权 —— 架构方案

> 日期：2026-07-09（v2，修正 hook 接入方式）
> 范围：每会话（非每轮次）打标分析、模块间数据借用、输出合规脱敏、owner==caller 授权规则
> 状态：**方向已确认，文档已落地，待实现**

---

## 0. v2 修正说明

v1 方案误把 `cmd/gateway/main.go:1901` 的 `nil, nil, nil` 当作接入点。**重读代码后纠正**：

- 生产网关跑的是 **V1 ChatHandler**，不是 V4 pipeline（V4 默认关闭，`v2UsePipeline()` 返回 false）。
- 响应侧 hook 通过 **`ResponseInterceptor` 接口 + `InterceptorChain`** 链式接入，**不是**直接改 main.go 的 nil 参数。
- 因此 output_compliance 的正确接入是：新增一个实现 `ResponseInterceptor` 的拦截器，并入既有 `InterceptorChain`（装配于 `cmd/gateway/goal_control.go:201`）。

本文档为权威版本，v1 描述作废。

---

## 1. 现状审计

### 1.1 两个身份面（决定脱敏作用层）

| 面 | 认证 | 身份字段 | 有真实 user？ |
|---|---|---|---|
| **控制面**（admin UI/API） | JWT `admin/jwt.go` | `UserID`(int)/`Username`/`Role`/`TenantID` → `AuthContext` | ✅ |
| **数据面**（`/v1/chat/*`） | `sk-` key `domains/authentication/verifier.go` | `KeyInfo{ID, TenantID, OwnerUser *string}` | ❌ 仅 key，`OwnerUser` 是创建 key 时填的自由文本 |

**决策**：脱敏作用于**数据面响应**（调用方只有 key）。admin 按既有三层可见性看明文。

### 1.2 V1 hook 链式插件模式（真实的接入机制）

生产网关有两类 hook 接入点：

**响应侧 — `ResponseInterceptor` + `InterceptorChain`**
- 接口 `domains/hooks/response/types.go:71`：`InterceptNonStream` / `InterceptStreamChunk` / `InterceptStreamEnd`，返回 `InterceptResult{ShouldBlock, ModifiedBody, InjectFollowUp, ...}`。
- 链 `domains/hooks/response/chain.go:14`：`NewInterceptorChain(hooks...)`，按序执行，`ModifiedBody` 在拦截器间传递（chain.go:50-58）。
- 装配 `cmd/gateway/goal_control.go:201`：`chain := response.NewInterceptorChain(goalHook, auditHook); chatHandler.SetResponseInterceptor(chain)`。
- 调用 `domains/streaming/handler.go:2008-2083`：流式走 `InterceptStreamEnd`，非流式走 `InterceptNonStream`。
- `*sql.DB` 桥接已存在：`dbConn.Stdlib()`（main.go:1128,1585）。

**请求侧 — `CheckV1` 扁平接口**（sessionaudit 模板）
- `domains/hooks/sessionaudit/hook.go:172` `CheckV1(ctx, sessionID, tenantID, model, content, ua, ip)` → `CheckV1Result{StatusCode: 0/403/202}`。
- 装配 `cmd/gateway/main.go:1082,1094` `NewSessionAuditHookV1(...)` → `chatHandler.SetSessionAuditHook(...)`。
- 调用 `handler.go:1098-1136`。

### 1.3 四个真实缺陷

1. **output_compliance 无 V1 实现**：`domains/hooks/outputcompliance/hook.go` 只实现 V4 `pipeline.Hook`（`var _ pipeline.Hook`，hook.go:190），注册于 `main_pipeline.go:431` 但仅当 V4 开启 + checker 非 nil 时生效——两者默认都不满足。**生产路径完全不跑**。
2. **`ModifiedBody` 被忽略**：`handler.go:2070-2082` 的非流式路径只处理 `ShouldBlock`/`InjectFollowUp`，**不应用 `ModifiedBody`** → 即便接入拦截器，脱敏后的 body 也不写回客户端。这是断点。
3. **`redactOutput` bug**：`domains/outputcompliance/checker.go:492`，`issue.Content` 在 line 213 已被赋为 mask 后的值，`strings.ReplaceAll(output, maskedValue, "[已脱敏]")` 永不命中。
4. **`pii_stripped` 契约悬空**：`cache_update_hook.go:169` 读 `env.Metadata["pii_stripped"]` 写 `SessionState.PIIStripped`，但全仓库唯一生产写者是测试。

### 1.4 三个会话标签存储（互不同步）

| 存储 | 位置 | 内容 | 持久？ | v6 落 PG？ |
|---|---|---|---|---|
| `SessionState` v6 | Redis(L1/L2)+PG(L3) | AuditScore/SecurityScore/PIIStripped/ApprovalStatus/OptimizationApplied | ❌ 30min TTL | ❌ `loadFromDB` 只恢复 v1-v3 |
| `session_summaries` | PG | compliance_status/pii_detected（**列存在但默认值，无人写**） | ✅ | — |
| `session_tags` | PG 长表 | task/client/llm/topic/intent/quality（纯 summaries 投影，无独立信号） | ✅ | — |

---

## 2. 目标架构

### 2.1 三原则
1. **脱敏走 output_compliance 引擎**，作为 `ResponseInterceptor` 入链，不新建引擎。
2. **owner==caller 文本匹配**：复用 `api_keys.owner_user` + `ownerScopeClause` 约定；空 owner 按"非自有"脱敏。
3. **`session_tags` 为规范标签存储**：`SessionState`（热路径）与 `session_summaries`（OLAP）都向其投影。

### 2.2 每会话打标统一层

```
热路径(每请求)  cache_update_hook (PostRouting) ─ MarkAudited ─┐
SessionState v6                                                ├→ 投影 → session_tags(PG,规范)
OLAP(异步)       session_analytics hook ─ tagger.TagSession ───┘
                                                              │
                                              SessionTagReader 接口（新增）
                                                              │
                              ┌────────────┬─────────┬────────┴────┬──────────┐
                              ▼            ▼         ▼             ▼          ▼
                          审批/安全    routing    panorama UI   会话详情    脱敏决策
                                                              (本轮 chips) (读 pii/compliance)
```

**新 tag_key 词汇表**（`tag_source='auto'`，投影自 v6）：
`security`(risk:low/med/high) · `compliance`(compliant/pii/toxic/bias) · `pii`(detected/stripped/none) · `approval`(pending/approved/rejected/timeout) · `optimization`(strip_tools/...) · `risk_action`(log/warn/sanitize/block/approval)。既有 task/client/llm/topic/intent/quality 不变。

### 2.3 输出脱敏管线（ResponseInterceptor 入链）

```
LLM 响应 → handler.go:2008 responseInterceptor 链
           ├─ goalHook.InterceptNonStream（既有）
           ├─ auditHook.InterceptNonStream（既有）
           └─ OutputComplianceInterceptor.InterceptNonStream（新增，最后）
                ├─ Checker.Check(tenant, output) → 检测 PII/toxic
                ├─ OwnerAllowsSensitive(callerOwner, dataOwner)? → 决定脱敏/放行
                ├─ redactOutput（修复后）→ ModifiedBody
                └─ 返回 InterceptResult{ModifiedBody: 脱敏后body}
handler.go:2070（修复后）应用 ModifiedBody → 写回客户端
```

**owner==caller 规则**（`domains/outputcompliance/owner.go` 新增）：
```go
func OwnerAllowsSensitive(callerOwner, dataOwner string) bool {
    if callerOwner == "" { return false }   // 调用方无身份 → 脱敏
    if dataOwner == ""  { return false }    // 数据无主 → 非"自有"→ 脱敏
    return callerOwner == dataOwner         // 文本匹配
}
```
- `callerOwner` 来自 `KeyInfo.OwnerUser`；`dataOwner` 来自 `session_dim.owner_user`。
- 新设置 `output_compliance.redaction_mode`：`off`/`always`/`owner_mismatch`（默认 `owner_mismatch`）。
- admin 三层始终看明文（脱敏只在 data plane 响应链）。

**为什么不用双因子/授权表**（用户已选文本匹配）：双因子需定义 sensitivity 语义；授权表要新建表+UI，违背"不重复"。文本匹配零新基础设施。

### 2.4 点亮悬空契约
OutputComplianceInterceptor 脱敏发生时 → 通过 telemetry 或直接 SessionCache 设 `pii_stripped=true` → `cache_update_hook` 自动持久化 `SessionState.PIIStripped` → 投影 `session_tags(pii=stripped)`。

---

## 3. 实施分解

### 阶段 A：输出脱敏接线（ResponseInterceptor）

| # | 文件 | 改动 | 复用 |
|---|---|---|---|
| A1 | `domains/hooks/outputcompliance/interceptor.go`（新） | `OutputComplianceInterceptor` 实现 `response.ResponseInterceptor` | response.InterceptorChain |
| A2 | `domains/streaming/handler.go:2070` | 修复：非流式路径应用 `ModifiedBody`（写回响应体） | — |
| A3 | `domains/outputcompliance/checker.go:492` | 修复 `redactOutput`（位置替换或委托 `approval.SensitiveDetector.Redact`） | approval.SensitiveDetector.Redact |
| A4 | `domains/outputcompliance/owner.go`（新） | `OwnerAllowsSensitive` + redaction_mode 判定 | — |
| A5 | `settings/` | 新增 `output_compliance.redaction_mode`（off/always/owner_mismatch） | settings 自动注册 |
| A6 | `cmd/gateway/main.go`（goal_control 邻近） | `initOutputCompliance(dbConn.Stdlib(), chatHandler)`；并入 InterceptorChain | goal_control.go 模板 |
| A7 | interceptor | 脱敏发生时设 `pii_stripped` 标记（点亮契约） | telemetry/SessionCache |

**验收**：data plane 调用方收到脱敏响应；admin 看明文；`SessionState.PIIStripped` 被置位。

### 阶段 B：每会话打标统一层

| # | 文件 | 改动 |
|---|---|---|
| B1 | `domains/hooks/sessionaudit/cache_update_hook.go` | `MarkAudited` 后投影 v6 → `session_tags` |
| B2 | `domains/hooks/compression/session_cache.go` | `loadFromDB` 恢复 v6（从 session_tags 反查） |
| B3 | `domains/analysis/tagger.go` | 扩展词汇表 + `TagFromSessionState`（热路径投影，与 OLAP `TagSession` 共写一张表） |
| B4 | `admin/session_panorama_handler.go` | 统一 `SessionTagReader` 替代 5 个手写查询 |

### 阶段 C+D：详情展示 + 契约文档化
- C1 `admin/session_compare.go` `TurnView` 加会话级 tags；C2 `SessionTurnsPanel.vue` 顶部 chips（接前轮组件）。
- D 在 `domain/request_envelope.go` Metadata 注释登记共享 key 契约（`audit_result`/`pii_stripped`/`output_compliance_result`/`optimization_applied`）。

---

## 4. 关键文件索引

**接入点（V1 链式插件）**
- `domains/hooks/response/types.go:71` — `ResponseInterceptor` 接口
- `domains/hooks/response/chain.go:14` — `InterceptorChain`
- `cmd/gateway/goal_control.go:201` — 链装配模板
- `domains/streaming/handler.go:575` — `SetResponseInterceptor` setter
- `domains/streaming/handler.go:2008-2083` — 调用点（**2070 需修 ModifiedBody**）
- `cmd/gateway/main.go:1082,1094` — 请求侧 CheckV1 装配模板

**脱敏引擎（复用）**
- `domains/outputcompliance/checker.go:492` — buggy redactOutput（待修）
- `domains/approval/sensitive_detector.go:311` — 位置精确 `Redact`（成熟候选）
- `domains/outputcompliance/checker.go:451` — `maskPII`

**owner 授权（复用约定）**
- `admin/session_tenant.go:78,110` — `IsRegularUser`/`ownerScopeClause`
- `sql/objects/tables/api_keys.sql:11` — `owner_user`/`data_sensitivity`
- `sql/migrations/startup/358_session_ownership.sql` — `session_dim.owner_user`

**打标（规范层）**
- `sql/migrations/startup/351_session_analytics_tables.sql:18` — `session_tags`
- `domains/analysis/tagger.go` — 既有 tagger
- `domains/hooks/compression/session_cache.go:73` — SessionState v6
- `domains/hooks/sessionaudit/cache_update_hook.go:169` — `pii_stripped` 读者

**共享 bus**
- `domain/request_envelope.go` — `env.Metadata`/`env.Governance`

---

## 5. 不做（排除）
- ❌ 不改 V4 pipeline / main.go:1901 nil（V4 默认关闭，死路径）。
- ❌ 不新建脱敏引擎（复用 output_compliance.Checker + approval.SensitiveDetector.Redact）。
- ❌ 不新建第四标签存储（统一 session_tags）/ 不新建授权表（owner 文本匹配）/ 不改 L1-L3。
- ❌ admin 侧不脱敏（看明文），仅 data plane 响应脱敏。

---

## 6. 风险登记

| 风险 | 影响 | 缓解 |
|---|---|---|
| ModifiedBody 应用改变非流式响应语义 | 中 | 最小改动：仅当 `len(ModifiedBody)>0` 时覆盖；保留 usage block 合并逻辑 |
| owner 约定破裂（key owner 填错） | 中 | 脱敏独立于可见性兜底；文档化约定 |
| 流式响应脱敏复杂（chunk 拼接） | 中 | 本轮先覆盖非流式 + `InterceptStreamEnd`（结束时已重组 body）；chunk 级脱敏为未来增强 |
| checker 接线后误杀 | 高 | 默认 `enforcement_mode=observe`+`redaction_mode=owner_mismatch`；故障降级 allow |

---

## 7. 实现时发现的关键限制（write-time vs post-response）

实现阶段 A 时精读 `domains/streaming/executors/executor_chat.go:83-92` 发现：**非流式响应体在 `executor.Execute` 内部就已经 `w.Write(body)` 写给客户端**（流式则在 `StreamChat` 边写边发）。因此 `handler.go:2008` 的 `ResponseInterceptor` 在响应已发出后才运行——**事后无法回改已发给客户端的字节**。

这决定了 OutputComplianceInterceptor 的真实能力边界：

| 能力 | 可行？ | 机制 |
|---|---|---|
| 检测 PII/敏感 | ✅ | `Checker.Check`（纯函数，对 captured body） |
| 点亮 `pii_stripped` 标记 → session_tags | ✅ | interceptor 设 Metadata → handler 写回 `result.ResponseBody`（供 telemetry/日志/缓存用） |
| 阻断（严重/enforce） | ⚠️ 部分 | `ShouldBlock` 在非流式已晚（字节已发）；流式可在 chunk 间断开连接 |
| **回改客户端已收到的字节** | ❌ | 需 write-time transform（见下） |

**真正的"客户端可见"字节级脱敏**必须走 **write-time transform**，即 `ChatExecutor.StripMinimaxFields`（executor_chat.go:37，在 `w.Write` 前对 body 做变换的函数指针模式）。这是既有且验证过的字节改写通道（main.go:566 已用 `streaming.StripMinimaxFieldsBody` 接入）。

因此本架构分两层：
- **A 阶段（本轮，已完成）**：`ResponseInterceptor` 做**检测 + 打标 + 阻断决策 + telemetry/日志/cache 一致性**。这立即点亮 `pii_stripped` 契约、让 session_tags 准确、让 admin 观测到合规结果。
- **未来增强（write-time 脱敏）**：新增一个 `RedactBodyFn func([]byte) []byte` 函数指针，挂在 `ChatExecutor`（与 `StripMinimaxFields` 同点，executor_chat.go:84），在 `w.Write` 前对非流式 body 做位置精确脱敏；流式则挂到 `StreamChatWithCapture` 的 `stripFn`（stream.go:71）。这样客户端才真正收到脱敏后内容。

此限制已在 `handler.go:2076-2087` 的注释中记录，避免后续误以为 post-response 改写能影响客户端。
