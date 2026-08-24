# 07 · v6 文档与 credentialfpslot 审计报告（2026-08-24）

> **审计对象**：`docs/架构优化v6/01–06 + README` 对照实际代码；`credentialfpslot/`（quota / node_state）实现审计。
> **审计基线**：branch `main`，审计起点 HEAD `9f5acc53e`（修复提交 `4731c9beb` 之后继续）。审计时工作树中无他人未提交文件；本轮全部改动使用 pathspec 定向提交，未触碰任何他人改动。
> **证据等级**：本报告所有"已修复"项均为 `LOCAL_VERIFIED`（miniredis 单测 + race）；未运行生产/staging 验证。

---

## 1. 审计发现（按严重度）

### P1-1 quota：stale metadata 清理条件错误（已修复）

`credentialfpslot/quota.go` `acquireQuotaSlotScript` 预清理循环原实现：

```lua
if val and val ~= clientType then   -- 只清理"其他 clientType"的过期项
```

两个后果：
1. **同类型过期项永不清理**：同 clientType 的 stale mapping 使 `:count` 虚高，enforce 模式会错误地拒绝合法 acquire。
2. **不校验物理 slot 存在性**：release/reclaim/reset 删除物理 slot key 后，metadata mapping 残留直到自身 TTL 结束。

**修复**（提交 `4731c9beb` + 本轮语义对齐）：mapping 仅在**物理 slot key 不存在**（`EXISTS slotKey == 0`）时删除，且对所有 clientType 生效；精确排除 `:exp` / `:count` 辅助字段（后缀匹配）。**物理 slot 仍存活的过期 mapping 保留**——删除它会使 count 低估、让 enforce 绕过 `max_fp_slots`；高估只会保守 block，且在 quota 路径抢占 idle slot（`idle >= gate` → `forget`+`remember` 重归因）时自愈。

配套：`activeSlotCountScript` 的 prune 改为同一 EXISTS 条件（读口径与 enforce 决策口径一致）；`ActiveSlotCount` 入参 clientType 经 `normalizeMetricClientType` 归一（与 acquire 路径同一白名单，`Cursor` 与 `cursor` 寻址同一计数器）。

**影响面说明**：`AcquireWithQuota` 当前主要为测试/停放路径，生产 dispatch 走普通 `Acquire`；因此该 bug 未影响线上请求，属"启用 quota 前必须修复"。

### P1-2 NodeState：损坏 JSON 永久卡死写入 + 批量读取静默 fail-open（已修复）

`credentialfpslot/node_state.go`：
1. `recordNodeOutcomeScript` Lua 中 `cjson.decode(raw)` 无保护——key 值损坏（截断/手改）后**每次 outcome 写入都报错**，损坏状态永久不可自愈。
2. 字段类型漂移（如 `"success_count":"7"`、`"slide_window":5`）会被 Lua 容忍但写回后 Go `json.Unmarshal` 永远失败，形成"Lua 能写、Go 读不了"的死锁。
3. 读取路径无身份校验：A 节点的 payload 若出现在 B 节点的 key 下，会被当作 B 的健康数据参与路由。

**修复**：
- Lua：`pcall(cjson.decode, raw)` 防御解码，失败时以全新 state 继续（本次 SET 覆盖损坏 JSON，key 自愈）；全字段类型收敛（number/string/boolean/table/nil 逐项校验）；滑动窗口逐条重建为合法 `NodeRecord` 形状后才写回。
- Go：`GetNodeState` 对 payload 身份与 key 不一致返回 `identity mismatch` 错误（fail-closed，单条读）；`GetNodeStatesBatch` 对 malformed JSON 与身份不一致同样 fail-open 到零状态（可用），保持"一条毒 key 不能让整批 un-filter 所有节点"的既有语义并写入文档注释。

**范围说明**：保留现有 `llmgw:cred_fp_node:{credentialID}:{model}` key 格式；tenant 维度迁移需版本化 key + 双读写 + 全调用方同步，列为后续任务（见 §4）。

### P2 文档事实问题（已修复，见 §3 清单）

---

## 2. 代码回归测试（新增 10 项，全部通过）

`credentialfpslot/quota_test.go`：
- `TestAcquireWithQuotaPrunesStaleMetadataWhenPhysicalSlotMissing` — 物理 slot 被删后同类型 stale mapping 被清理，enforce acquire 成功、count 收敛为 1。
- `TestAcquireWithQuotaPruneOnlyRemovesMissingPhysicalSlots` — 混合 cursor/codex：只清理物理 slot 已删的 codex 项；cursor 的 live mapping 继续阻断超额。
- `TestAcquireWithQuotaKeepsExpiredMetadataWhilePhysicalSlotLives` — 物理 slot 存活但 metadata exp 过期时**不删除**（gate=9999 排除抢占路径干扰），enforce 仍 block，不得绕过 `max_fp_slots`。
- `TestAcquireWithQuotaRecoversAfterNaturalExpiry` — `FastForward(slotTTL+60s)` 自然过期后下一次 enforce acquire 成功。
- `TestActiveSlotCountNormalizesClientType` — `cursor`/`Cursor` 同计数器；未识别类型归入 `unknown`（计 0）。

`credentialfpslot/node_state_test.go`：
- `TestGetNodeStateMalformedJSONReturnsError` — 单条 malformed JSON 返回错误。
- `TestGetNodeStateIdentityMismatchReturnsError` — payload 身份 ≠ key 身份返回 `identity mismatch`。
- `TestGetNodeStatesBatchFailsOpenOnCorruptAndMismatch` — 批量读中 valid 项保留禁用态；corrupt / mismatch / missing 三类均 fail-open 到零状态且带请求方身份。
- `TestRecordNodeOutcomeSelfHealsCorruptState` — 损坏 key 经 `RecordNodeFailure` / `RecordNodeSuccess` 后写回合法 JSON，计数与身份正确。
- `TestRecordNodeOutcomeRewritesWrongTypedFields` — 可解码但字段类型错误的 payload 被归一化重写，Go 读取不再失败。

**测试命令与结果**（2026-08-24，go1.26.4 darwin/arm64）：

```
gofmt -l credentialfpslot/          → 无输出（clean）
go vet ./credentialfpslot           → 通过
go test ./credentialfpslot -count=1 → ok  7.154s
go test -race ./credentialfpslot -count=1 → ok 10.450s
go test ./domains/streaming/executors -run 'Router|Dispatch|NodeState|Cooling' -count=1 → ok 3.839s
```

---

## 3. 文档修正清单（01–06 + README）

| 文档 | 修正 |
|---|---|
| README | 差距口径 "18 处" → "27 处（D1–D27，起草 18 + 复审扩展 9）"；TL;DR 行数刷新为 2026-08-24 快照；"4 项任一即可完成" 改为四级证据等级门禁（IMPLEMENTED / LOCAL_VERIFIED / REAL_DEPENDENCY_VERIFIED / RELEASE_READY），与 05 的 7 步自检对齐 |
| 01 | V2/URSM 改为"默认 authoritative 的 tenant-aware 路径 + legacy/canary/fallback 并存"；能力对照表区分"已有 vs 计划"（MCP/A2A/Fusion、Presidio sidecar、RLS 实证状态）；`session-health-operations.md` 标 [待建] |
| 02 | 头部口径 27 处 + 行数快照说明；D1–D5 行数刷新（6076→6156 等，保留起草快照对照）；D20 由"无自动对账"改为如实描述：`bg/cost_reconciliation_worker.go` 已存在（默认关闭、env/settings 双开关、interval 默认 86400s、admin 端点 `/api/admin/provider-cost-reconciliation` + `/bill`、结果落 `provider_cost_reconciliation` + `provider_events`），标记 `LOCAL_VERIFIED`，生产启用未实证 |
| 03 | 头部口径 27 处；W0-1/2/3 行数快照对照；W2-3 改为"扩量与生产启用"（删除不存在的每日 03:30、`/api/admin/cost/reconcile`、`request_logs.cost_reconcile_status` 表述） |
| 05 | 头部标注 12 个 SLO 与 `v6_*` 指标为 DESIGN/TARGET；新增标签基数约束（禁止 `request_id_hash`/`request_id` 等无界 label，请求级定位走 exemplars）；SLO-5 指标重设计；SLO-9 数据源改为"worker 已存在 + W2 负责扩量与指标暴露" |
| 06 | 头部证据等级声明；`executor_owner` 指标、`EXEC_OWNER_REQUIRED` 门禁、`internal/featureflag` 包、`orchestration.plugin.enabled` 等 flag 全部标 [待建]（代码零证据）；`docs/security/rls-leak.md`、`docs/runbooks/secret-rotation.md`、`rca-template.md`、`incident-comms.md`、`internal/orchestration/api.md` 标 [待建]；R7 链接改指真实路径 `docs/2026-08-19-streaming-error-root-cause.md`；§6 增加现有 runbook 目录盘点 |
| 04 | 相对链接修正 |
| 全部 | **49 处 `../../` 相对链接改为 `../`**（v6 目录仅深一层，原链接全部落到仓库根之外）；修正后链接检查 0 broken |

---

## 4. 未解决项（移交后续，见 handoff）

1. **NodeState tenant-aware 迁移**：`llmgw:cred_fp_node:{cred}:{model}` 无租户维度；legacy/canary 回退共享健康状态。需版本化 key（如 v2 带 tenant）+ 双读写 + Router/Recorder/Admin/LiveStream 全调用方同步，独立立项。
2. **Maintain/ASM 真实 PG RLS 矩阵**（02/D13）：FORCE RLS、NOBYPASSRLS role、`app.current_tenant` GUC、跨租户 negative test 均未实证。
3. **PII 端到端**（02/D12）：Presidio sidecar、SDPStreamSanitizer、zh_pii_rules、2 周 observe FPR/FNR 未落地。
4. **v6 SLO / 指标 / 告警 / 看板落地**（05 全部为 DESIGN/TARGET）。
5. **executor_owner 指标 + EXEC_OWNER_REQUIRED 门禁 + featureflag 基建**（06 多处 [待建]）。
6. **quota 生产启用评估**：`AcquireWithQuota` 仍为测试/停放路径；启用前需补 shadow 运行数据。
7. **[待建] runbook 落盘**：rls-leak、secret-rotation、rca-template、incident-comms、session-health-operations、drain-matrix。

## 5. 工作树与他人改动

审计与修复期间工作树中无他人未提交文件（此前会话中出现的 `installer/`、`executor_dispatch_test.go` 等改动已在各自提交中处理）。本轮提交仅包含：`credentialfpslot/{quota.go,quota_test.go,node_state.go,node_state_test.go}` 与 `docs/架构优化v6/*`。
