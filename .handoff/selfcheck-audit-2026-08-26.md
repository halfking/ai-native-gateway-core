# 自检流程审计 — 上下文交接文档

> 用于把当前审计会话的工作上下文固化成可被新会话（主代理 + 子代理）消费的入口。
> 任何在本会话之外重启的工作请从此处读取。

---

## 1. 项目坐标

| 项 | 值 |
|---|---|
| 工作区 | `/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4` |
| 平台 | darwin / arm64 |
| 主分支 | `main` |
| 最近提交 | `5be62a436 fix(db): guard tool calls index against JSON scalars` |
| Go 版本 | 项目 `go.mod` 顶部声明（见 `go.mod:3`） |
| 数据库 | PostgreSQL（加密 `.env.{184,252,71,kaixuan-1}.enc`，无本地明文） |

---

## 2. 触发场景与已落地结论

### 2.1 触发场景（用户原文摘要）

- apigpt 欠费下线（针对 `gpt-5.6-terra` 模型）
- 用户已充值，期望：自检流程能自动恢复访问
- 用户原则：
  1. 按 token 计费（非周期性）节点 **≥5 分钟** 探测一次
  2. 欠费期间探测不花钱
  3. 一个节点只用一个模型探
  4. 探测成功 → 拉凭据下全量模型清单 → 清空替换原清单
  5. 拉取失败 → 不更新原模型清单
  6. 一个模型可用 → 默认全部可用
  7. **欠费下线对象是凭据（credential），不是模型节点（model binding）**

### 2.2 关键修复（已落地）

**修复时间锚点：2026-08-26 self-check audit**

| 修复 | 文件:行 | 说明 |
|---|---|---|
| `restoreAllBindingsOnCredentialSuccess` | `bg/credential_probe_v2.go:1199` | 探测成功后扇出清空整张凭据下 auto-* 标记的 cmb |
| `loadBoundRawModelsAll` | `bg/credential_probe_v2.go:1279` | healthy 分支无 `cmb.available` 过滤，全量拉取 |
| writeHealth 分支切换 | `bg/credential_probe_v2.go:1011-1035, 1043-1051` | healthy-ready 走 fan-out，failed 走原路径 |
| 硬配额硬解封 bypass | `bg/credential_probe_v2.go:998-1001` | 即使旧 quota_state=hard 探测成功也允许翻 ready |
| Pin 测试套件（10 个测试） | `bg/credential_probe_v2_recharge_recovery_test.go` | 全部 PASS |
| 测试基线 | `go test ./bg/ ./credentialhealth/ ./provider/...` | 全绿 |

### 2.3 架构数据

**探测调度器（频率对照）**

| Worker | 默认 | 文件:行 |
|---|---|---|
| `BalanceQuotaProbe` | **2 min** | `bg/balance_quota_probe.go:67` |
| `PeriodicQuotaProbe` | 5 min | `bg/periodic_quota_probe.go:67` |
| `CredentialProbeV2.cycleAll` | 1 h | `bg/credential_probe_v2.go:70` |
| `CredentialProbeV2.fastReprobe` | 5 min | `bg/credential_probe_v2.go:71` |
| `HealthAutoRecover` | 1 min | `bg/health_auto_recover.go:50` |
| `CredentialSelfcheckWorker` | 5 min/cycle | `bg/credential_selfcheck.go:79` |

**欠费下线路径（凭据级，非模型级）**

```
HTTP 4xx/402/403/429 + "insufficient balance" / "quota exhausted" / 中文欠费
   ↓ errorsx.ClassifyError
KindQuota / KindQuotaBalance / KindQuotaPermanent
   ↓ Writer.WriteOnError (domains/credential/writer.go:246-260)
credentials.availability_state = 'suspended'
credentials.quota_state = 'balance_exhausted' | 'permanently_exhausted'
credentials.availability_recover_at = NULL  ← 关键：永不到期，必须主动探活
   ↓ BalanceQuotaProbe (2 min)
SubmitFastProbe / ProbeNowAsync → CredentialProbeV2.ProbeNow
   ↓ 探测成功
writeHealth:
  - availability_state='ready', quota_state='ok' (绕过硬配额守卫)
  - restoreAllBindingsOnCredentialSuccess (清整张凭据 cmb)
  - restoreBindingOnProbeSuccess (清 auto_probe_model_binding)
  - loadBoundRawModelsAll → cache healthy_confirmed
```

**探测模型选择（一个节点一个）**

`bg/shared_pick.go:48` `PickProbeModelForCredential` 4 级选择：
1. manual（admin 锁定）
2. request_logs（7 天 200 OK 最多）
3. featured（`routing_policy.featured_models`）
4. random over available bindings（兜底）

---

## 3. 任务坐标

### 3.1 已完成（✅ 不要重做）

- 定位 self-check 核心实现文件
- 审计 apigpt 节点探测计划及频率
- 审计充电后凭据恢复逻辑
- 审计欠费下线逻辑（确认凭据级）
- 跑完所有 pin 测试（10/10 PASS）+ bg/credentialhealth/provider 回归
- 2026-08-26 修复（`restoreAllBindingsOnCredentialSuccess`、`loadBoundRawModelsAll`、分支切换）

### 3.2 待执行（下一阶段 ⏭️）

| 编号 | 任务 | 优先级 | 状态 |
|---|---|---|---|
| T1 | 跑 `go test ./... -count=1` 完整回归（确认跨包无破坏） | 高 | 待执行 |
| T2 | 端到端验证：构造 httptest mock upstream，模拟"apigpt 欠费→恢复→探测成功→全凭据 binding 翻 ready" | 中 | 待执行 |
| T3 | 检查 admin 端点 `POST /api/admin/probe/force/{id}` 是否在 cmd/gateway/main.go 中注册（快速核对） | 低 | 待执行 |
| T4 | 在 `docs/` 增补"充值类节点自检设计"文档，引用 `restoreAllBindingsOnCredentialSuccess` 修复 | 中 | 待执行 |
| T5 | `errorsx/regression_apigpt_credit_test.go` 跑一次确认仍 PASS（已通过） | 低 | ✅ 已确认 |
| T6 | 验证 `cmd/gateway/main.go:3177` balanceQuotaProbe wiring 仍正确（已确认） | 低 | ✅ 已确认 |

### 3.3 已知约束

- **数据库**：`.env.*.enc` 加密，无明文本地数据库可连；T2 必须用 `httptest.Server` mock upstream
- **测试覆盖**：当前以源码扫描 pin 测试为主，缺端到端集成测试
- **代码改动**：本会话在 `bg/credential_probe_v2.go` 与 `bg/credential_probe_v2_recharge_recovery_test.go` 有改动；其他文件未改

---

## 4. 关键文件路径速查

```
bg/credential_probe_v2.go                                  ← 核心修复
bg/credential_probe_v2_recharge_recovery_test.go           ← Pin 测试（10 个）
bg/balance_quota_probe.go                                   ← 余额专用调度器（2min）
bg/periodic_quota_probe.go                                  ← 周期配额调度器（5min）
bg/shared_pick.go                                           ← 探测模型 4 级选择
bg/default_probe_picker.go                                  ← 每日 0:00 重新选探测模型
bg/credential_recovery.go                                   ← 自动恢复
credentialhealth/checker.go                                 ← 失败率检测
domains/credential/writer.go                                ← 欠费下线入口（写凭据级）
errorsx/classify.go                                         ← 错误分类（KindQuota*）
errorsx/regression_apigpt_credit_test.go                    ← apigpt 错误分类回归
cmd/gateway/main.go                                         ← worker wiring（3177: balanceQuotaProbe）
admin/credential_state_handlers.go                          ← admin 强制探测端点
sql/objects/views/v_routable_credential_models.sql          ← 路由视图（is_routable 过滤）
```

---

## 5. 提示词消费指南

**复制定位**：

- 主提示词位于本会话的下一条 AI 输出（标题为"# 主代理 + 子代理 任务执行提示词"），是**单一**提示词，供主代理使用
- 主代理根据提示词派生子代理（`Agent` 工具，`subagent_type=general-purpose` 或 `Explore`）
- 所有子代理共享本上下文文档的工作目录、git 状态、测试基线

**派生子代理的并行度建议**：

- T1（完整回归）独立，可单独跑
- T2（端到端 mock）独立，与 T1 并行安全
- T3/T5/T6 都是只读核对，可并行
- T4（文档）需要基于 T2 完成后写，可串行

**禁止操作**：

- 不要修改 `bg/credential_probe_v2.go` 的核心修复逻辑（已固化）
- 不要删除 `bg/credential_probe_v2_recharge_recovery_test.go` 的 pin 测试
- 不要触碰加密的 `.env.*.enc`

---

## 7. 第二阶段执行上下文（2026-08-26 续作）

> 本节由主代理审计后追加，作为下一阶段（"按 ROI 推进 4 个落点"）的输入。

### 7.1 上轮 fix 已落地的产物（不要重复）

- `metrics/routing_credential_metrics.go`：新增 `RoutingCredentialRecoveryNotifyTotal` CounterVec
- `bg/credential_recovery.go::recover()`：6 个 UPDATE 块改用 `dispatchRecoveryHooks` 闭包（`RETURNING id` → `InvalidateCandidateCache` + `probeSubmitter(id, "")`）
- `bg/credential_recovery_test.go`：新增 `TestRecover_InvalidateAndSubmitOnFlip` / `TestRecover_NoHooksOnError` / `TestRecover_HooksNilSafe`
- `cmd/gateway/main.go:3503-3514` 已装好 `credRecovery.SetProbeSubmitter(nodeProbeWorker.Submit)` + `SetInvalidateCandidateCache(provider.InvalidateCandidateCacheForCredential)`

### 7.2 已确认的事实（grep 实证）

- `SetQuotaUpdatedNotifier` / `EnqueueWithRecovery` / `quotaUpdatedNotifier` / `OnQuotaRecovered` / `MarkCredentialHealthy` / `MarkCredentialExhausted` / `IsExhaustedBy` / `RecoveryService` / `NewRecoveryService` / `CredentialInvalidator` 这些名字**全部不存在**于当前代码库（`grep -rn ... --include="*.go"` 0 命中）
- `/api/webhooks/*` 当前只有飞书回调（`cmd/gateway/feishubot_init.go:106`）和钉钉审批回调（`cmd/gateway/main.go:5717`），**没有充值回调 webhook**
- `cmd/gateway/capabilities.go:61` 字面写着 `webhook_subscription: "planned"` —— 这条能力**没有实现**
- `fastReprobeDelay = 5 * time.Minute`（`bg/credential_probe_v2.go:71`），`fastReprobeQueue` size=64（L91）
- `ProbeQueueWorker.processTask` 成功路径只 `w.complete(...)` (L196-200)，**不通知 CredentialRecovery / dispatcher / cache invalidator**

### 7.3 关键代码坐标

```
bg/credential_recovery.go
  87-129    CredentialRecovery struct 字段
  131-138   NewCredentialRecovery 构造器
  142-147   SetProbeSubmitter
  152-157   SetInvalidateCandidateCache
  272-317   dispatchRecoveryHooks 闭包
  852-940   recoverExpiredBindings
  981-1067  recoverFreshDegradedBindings
  1128-1215 reconcileStaleNodeProbeStates

bg/probe_queue_worker.go
  142-203   processTask — 成功分支不通知任何东西
  196-200   success 分支只 w.complete()

bg/credential_probe_v2.go
  71        fastDelay = 5 * time.Minute
  91        fastReprobeQueue = make(chan int, 64)
  110-140   SubmitFastProbe
  238-260   ProbeNowAsync
  291-307   fastReprobeQueue → 5min delay → probeOne
  380-462   cycleAll
  547-893   probeCredential
  897-1010  writeHealth
  1139-1163 restoreBindingOnProbeSuccess
  1199-1268 restoreAllBindingsOnCredentialSuccess (apigpt_recharge_2026_08_26)

cmd/gateway/main.go
  2965-2999 CredentialRecovery 装配 + URSMRecoverSink + LookbackHotConfig
  3177-3186 BalanceQuotaProbe 装配 + ProbeNowAsync
  3383     stateManager.SetInvalidateCandidateCache
  3431     nodeProbeWorker.SetInvalidateCandidateCache
  3509-3514 credRecovery.SetProbeSubmitter + SetInvalidateCandidateCache

provider/client.go
  433-466   InvalidateCandidateCacheForCredential(credentialID int)
```

### 7.4 第二阶段 4 个落点（按 ROI 排序）

| 落点 | 涉及文件 | 期望效果 | 优先级 |
|---|---|---|---|
| **A** | `bg/credential_recovery.go`（新增 `OnQuotaRecovered`）+ `bg/credential_probe_v2.go::cycleAll` 成功分支 + `bg/probe_queue_worker.go::processTask` 成功分支 + `cmd/gateway/main.go` 装配 | 探测成功后**主动**通知调度层，消除 "DB 写完了但 cache 还在 stale" 窗口 | **P0** |
| **B** | 新建 `cmd/gateway/webhooks/quota_recharged.go` + 注册 `/api/webhooks/quota/recharged` 路由 + `bg/balance_quota_probe.go` 新增 `OnQuotaRecharged` + 更新 `cmd/gateway/capabilities.go:61` | 充值类供应商 webhook 秒级响应 | **P0** |
| **C** | `bg/credential_probe_v2.go`（fastDelay 5min → 30s）+ `bg/periodic_quota_probe.go`（新增 probeWindowReset 扫 inferQuotaRecoverAt fallback 行） | 周期类供应商探测频率提升 | **P1** |
| **D** | `bg/credential_recovery.go`（新增 `probeSubmitterImmediate` 字段 + `SetProbeSubmitterImmediate`）+ `dispatchRecoveryHooks` 末尾调一次 + `cmd/gateway/main.go` 装配接 `credProbeV2.ProbeNowAsync` | recover() 翻状态后**立即**同步探活 | **P1** |

### 7.5 强约束

- `writeHealth` 是 `cmb.available` 的唯一权威写者；recover() 不能直接 UPDATE cmb.available
- 新 webhook 必须 HMAC 验签（参考 `cmd/gateway/feishubot_init.go`）
- metric 前缀 `llmgw_`
- 测试用 `pgxmock/v4`
- 不要触碰 `nodeProbeWorker.Submit` 现有调用语义
- 不要触碰 `bg/credential_probe_v2.go` 已被 §2.2 列出的核心修复（`restoreAllBindingsOnCredentialSuccess`、`loadBoundRawModelsAll`、分支切换、硬配额硬解封 bypass）
- 不要删除 `bg/credential_probe_v2_recharge_recovery_test.go` 的 pin 测试

### 7.6 第二阶段主提示词位置

主提示词已固化到 `.handoff/next-phase-master-prompt.md`（本目录同层）。主代理从该文件读取后即可按"批次 1（A+B）→ 批次 2（C+D）"的并行模式派生子代理。

---

## 8. 风险与边界

| 风险 | 缓解 |
|---|---|
| 跨包回归可能暴露新 bug | T1 必须跑完整 `go test ./...`；发现失败立即记录 |
| mock upstream 与真实 apigpt 行为差异 | T2 端到端测试覆盖 happy path + 失败保持两种 case |
| 文档与代码漂移 | T4 引用具体行号，写完后用 grep 反查 |
