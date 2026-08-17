---
archived_at: 2026-08-17
archive_reason: session log (process doc, completed)
source_path: docs/session-logs/2026-08-17-b3-pr1-journey-unification.md
category: process/session-log
---

# 会话日志:B3 PR1 — 追踪合一事件点迁移(2026-08-17 深夜)

## 需求

承接 `AUDIT_24H_20260817.md` §E.3 的 B3 两 PR 方案,执行 PR1:把仅存的两个
`state_transition_logger` 写入点迁入 requestjourney(写同一张
`request_state_transitions` 表的 journey 行),删除 V3.2 legacy logger 全链,
并按报告 §E.5 滚动 B2b 上线后监控快查。

## 修改的功能点

1. **wrapper.go:222(retry)→ journey**:新增 `requestjourney.Lifecycle.RetryScheduled`
   (发 `retry_scheduled`/`retrying`);streamretry request carrier 承载 lifecycle
   绑定(`BindJourneyObserver`/`JourneyObserverFromCtx`,本地接口避免 domain import)。
2. **修复既有缺陷:streamretry 重试后 journey 静默冻结**。wrapper 重试以自身 ctx
   重调 handler → 每次新建 Lifecycle → seq 从 1 重启 → 第 2+ 次尝试的事件被
   Projection 以 `ErrSequenceConflict` 全拒。修复:`ensureRequestJourney` 从 carrier
   读上一 attempt 的 `SequenceHighWater` 种子新 lifecycle 并回绑;`withRequestCarrier`
   幂等。与 dispatch `JourneySharedSeq` 单原子协议兼容。
3. **handler.go:3004(route_decision)→ 删除而非迁移**:journey 已覆盖同一边界
   (`route_resolved` 四协议入口 + dispatch `credential_selected` 记录实际选中凭据);
   旧行独有载荷(candidates_count/profile)在 content-free 契约(530)下不可表达。
4. **表保留清理承接**:旧 logger 的 7 天清理是该表(journey+legacy 行)唯一保留机制;
   移植为 `requestjourney.RetentionWorker`(1h/7d/事务级 bypass_rls),main.go 原位替换。
5. **删除**:dispatch `state_transition_logger.go` + globals + 单测 + integration 测试 +
   `wireStateTransitionLogger`;连带退役写-only 的 tenant carrier/requestID ctx 机制
   (`SetAuthenticatedTenant` 等)。
6. **保留并显式说明**:admin auditLogger(V3.2-LP5 节点操作审计,独立 admin 域写入方,
   人工操作审计语义,不属请求生命周期契约)。

## 影响分析

- 表写入方从 3 → 2(requestjourney + admin audit);legacy 行新增来源归零,
  `/api/admin/requests/{id}/transitions` 只剩审计行与历史行(PR2 收敛前提成立)。
- journey 行为增量:重试请求现完整记录(每 attempt 一组事件 + 重试边界),
  此前第 2+ 次尝试事件全丢。
- 写入可靠性:内存队列+3 次退避 → recorder fan-out + outbox replay。
- 保留清理语义不变(同 tick/retention/RLS bypass 方式)。

## 验证

- `go build ./...` / `go vet ./...` 全绿;全仓 `go test -short` 零失败;
- golangci-lint `--new-from-rev=bb078d0af` **0 issue**(CI 棘轮预演,本次 push 即首跑);
- 新增测试 4 组:Lifecycle retry 事件+seq 种子、carrier 绑定+retry 发射、
  双 pass 重入 seq 连续性(真实 ServeHTTP 驱动)、retention 契约。

## 监控快查(§E.5)

- park 复活:无(命中均为 park 注释行本身);
- B2b 判据告警勘误:`URSMv2LegacyFallback` 已由远程演进改名为
  `RoutingStateFallbackRatioHigh`(`routing_state_source_total{source="fallback"}`,
  生产者在 `domains/ursm/v2/statesource`,指标面健在),已记入报告 §E.5;
- MNF 收敛/MM 附件流量/7 天静默:需生产观察,待办不变。

## 待办移交

- B3 PR2(观察 journey 查询正确性后撤 transitions endpoint);
- CI 棘轮首跑观察(本次 push 触发);
- pre-existing U1000 清理(executors 9 + streaming 14)。

audit: 双轴自审通过(Standards:与 requestjourney 既有模式一致/无新死代码;
Spec:严格落在 §E.3 PR1 范围,三处超出均已在报告 §E.3.1 说明理由)。
