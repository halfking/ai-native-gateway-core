# 2026-08-27 merge-loss recovery (d2cbaf88b 坏 merge 收口)

## 发生了什么

2026-08-26 20:37 的 `d2cbaf88b` "merge: integrate origin/main (e3f88e024..bc49a4463)" 是一个把多个已合入 audit/feature commit 的实现**静默回滚**、且把未解决的 merge 冲突标记（`>>>>>>> 0e11207f7`）也写进 `bg/credential_recovery.go` 的坏 merge。

受害面（全部已在 main 上等价存在过，但在 d2cbaf88b 后消失）：

- provider: `BindingRawModel`、`ResetKeyRotatorKey`、priority 排序 key、`pricing_plans` fallback SQL
- streaming/executors: priority-bucket 分区、等 cool 兜底（`chooseLeastCooledCandidate`）、`BindingRawModel` 空响应隔离
- streaming: gate-aware `Resumable` 判定（stream timeout / client_write failure）、responses bridge `finishInterrupted`、MiniMax 数据流清洗
- admin: credential keys 事务化删除/重置 (`beginCredKeyTx` + FOR UPDATE)、live-stream credential lanes（store/lifecycle/节点卡片标题 SWAP 修复）、session-export 权限前置、列表 `listTopModels` hot 表查询
- sessionsummary:auto title provisional metadata、SessionSummary.AgentType、`extractDialogueContent`
- dashboardapi: `CostStats.TotalRequests`/近似统计投影
- bg: credential_probe_v2 充值恢复 fan-out、balance_quota_probe 兜底路由 + SetProbeNowAsync、periodic_quota_probe pre-exhausted 分支、probe rollback `MarkTentativeRestore`、node_probe 空响应/TTL 修正、partition_manager recent-promote 覆盖
- credentialfpslot: Lua slot 元数据清理（物理 slot 已删即剪幽灵占用）、ActiveSlotCount clienttype 归一
- errorsx: Apigpt/MiniMax 配额耗尽必须归 credential-fatal 而非 concurrent overload
- requestjourney: ApplyTx（同事务 RLS 投影）+ retry_at 列、出去重
- telemetry: session summary 对象列单层 SELECT、`update_session_summary` 热列、model_offers trigger 同步
- schema mirror: 553 approval resume / 568 priority + context override / 362 provider_code 解析 / seed `claude-sonnet-5` 三镜像一致

## 恢复策略

全部以 `c0dfd4a00`（zcode 侧最后一份含这些实现的一致树）为源；HEAD 上新加（2026-08-26 后半天的）变更（live-stream 凭据泳道、anthropic fail-loud 校验、queue node 卡片标题等）在恢复基础上做叠加保留，测试全部按"实现优先、c0 语义为最终真值"的口径对齐。

## 验证

- `go build ./...` → exit 0
- `go vet ./...` → exit 0
- `go test ./...` → 全包绿（含 admin / bg / streaming / executors / sessionsummary / telemetry / session / credentialfpslot / requestjourney / migrations / errorsx / sessionmeta）

## 衍生动作

- 出 recovery 分支 `fix/zcode-merge-loss-recovery`，rightarrow merge 回 main。
- 后续建议给 `git merge` 产生冲突标记的 commit 加 CI 拦截（搜索 `^<<<<<<<|^=======|^>>>>>>>`在 .go/.ts 中），并把 `d2cbaf88b` 类型的"integrate origin/main"merge 纳入强制 review。
