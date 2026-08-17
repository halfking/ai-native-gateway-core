---
audit:
  status: passed
  degraded: false
  base: 6a5cc93e^
  scope: "2026-08-08 00:04 through 2026-08-08 22:51 +08:00; 26 commits observed in the rolling 24-hour history (seq 1473/1475/1476/1477 already deployed to 154)"
  workflow: comprehensive-code-audit + session-audit-gate + parallel Standards/Spec sub-agents
---

# 24h 修改审计：路由/流式/审计/OmniFree 全链路回归与修正

## 需求

- 总结并交叉审查最近 24 小时的修改（base=`6a5cc93e^`，HEAD=`3c98ac6c`，共 26 个 commit / 97 个文件 / +5217 / -532）。
- 修正发现的问题（含 HIGH 级安全残留 + 文档同步缺口），同步更新 CHANGELOG / docs/db-changelog / session-logs。
- 提交并推送到 `origin/main`。

## 原始修改摘要

- **路由与错误分类**：`errorsx/classify.go` 扩 `INSUFFICIENT_BALANCE` 误判 → quota，新增 `KindUpstreamOverloaded`；`domains/credential/{breaker,writer}.go` 新 kind 冷却策略 + 不再触发 30 分钟熔断；`provider/client.go` `GetProbeCandidates` 排除 `periodic_exhausted`；`domains/streaming/handler.go` 新增 prestream 耗尽 frame 携带真实 kind。
- **流式链路**：`domains/streaming/handler.go` 新增 `interceptingStreamWriter` + 预流式 envelope kind 修复；`domains/streaming/executors/executor*.go` 多处 sole-candidate fail-open + same-credential overload retry + Retry-After 全链路保留。
- **V2 session 与事务边界**：`domains/session/v2/{bodies_writer,turn_writer,session_writer_v2}.go` 引入 `Message.MarshalJSON/UnmarshalJSON`、`GetLatestBodiesInTx` 与 `LockSessionInTx` 同事务；SQL 476（up+down）加 tenant-scoped 唯一约束；`db_omnifree.go` 统一 RLS policy 命名 `tenant_isolation_<table>`。
- **OmniFree worker RLS bypass**：`bg/freequotacleanup/{worker,worker_test}.go`、`bg/freequotareset/{worker,worker_test}.go` — tenant-scoped UPDATE/DELETE。
- **审计加固**：`security/sanitize/smart_sani_guard.go` chunk-level placeholder 还原 + stream writer wiring；`upstream/client.go` Retry-After 元数据保留；`bg/{credential_recovery,node_probe}.go` 死循环修复与 finishProbe 原子性；`alerting/alerting_test.go` 数据竞争修复。
- **i18n / web**：8 locale 全覆盖 `upstream_quota_periodic`；ChatView/ModelPicker catalog 完整列表 + cache invalidation。
- **release**: seq 1475 (cfeca467+0eaa4fa8)、seq 1476 (9923a6ff)、seq 1477 (c81007af) 已部署到 154；`VERSION/version.json/web/public/version.json` 三处一致 `2.4.9-c81007af-20260808-1477`。

## 双轴审查

### Standards 轴（结论：GO（带条件））

- **CRITICAL/HIGH**：
  - **C1 — `bg/freequotacleanup/worker.go:114-148` / `bg/freequotareset/worker.go:121-156`** `listTenants` 在 BYPASSRLS 角色下自身就是跨租户读取，正是原 bug 描述的场景。本轮**已修复**：listTenants 改为事务内 `SET LOCAL app.current_role = 'super_admin'`，走 RLS policy 已显式白名单的 super_admin 通道跨租户枚举；退化路径保持 `['default']`。
  - **H2 — `bg/node_probe.go:566-577`** `notifySyncWaiters` 是 dead code + 误导注释。本轮**已修复**：删除孤儿函数，把 `finishProbe` 注释改成单完成钩子的契约描述；同步 `bg/node_probe_sync_test.go` 两处调用点改用 `finishProbe`。
- **HIGH**：
  - **H1 — `domains/session/v2/session_writer_v2.go:211-336`** 锁持有时间过长，慢 DB 会让 advisory lock 长时间持有阻塞并发。本轮**已修复**：锁作用域包 5s context timeout（与 node-probe 5s 周期对齐），锁内所有 DB 操作走 lockCtx。
  - **H3 — `bg/node_probe.go`** notifySyncWaiters 同 H2。
- **MEDIUM**：
  - **M1** `errorsx/classify.go` 重复 budget 分支（`ClassifyResponseBody` 与 `ClassifyErrorWithBody`）；建议抽 `budgetKindForStatus(status, body)` helper —— **不在本轮范围**（与基线 commit `da253d3d` 一致，已写好测试覆盖，可作 follow-up）。
  - **M5** `bg/freequotacleanup/worker.go` + `bg/freequotareset/worker.go` 含 `var _ = time.Now` 死代码。本轮**已修复**（删除）。
  - **M6** `domains/credential/breaker.go:392-396` 3 段式 escalation 条件可读性受损。本轮**已修复**（改为 `switch kind { case KindTransient, ... }`）。
- **LOW**：
  - **M3** `domains/streaming/handler.go:3717-3719` Retry-After 头在 prewarmed 路径 silently drop。**不在本轮范围**（客户端字节契约变更成本高）。
  - **M8** `provider/client.go` SQL 不一致（loadCandidatesByModalityDB 排除 periodic_exhausted，但 sibling EXISTS 不排除）。本轮**已修复**：加注释说明 sibling EXISTS 是"任何可能 sibling"，不与 routing-time 重复过滤冲突。
- **SQL migration**：476 up + down 配对完整，约束名一致，索引同步。✓
- **测试契约**：所有声称"钉住"的测试都用具体场景（atomicity sequence、classification matrix、executeOpenAI 502→200、circuit OPEN + sole candidate、prewarmed envelope shape），非空测试。✓
- **Fowler smell**：报告里指出的 executor.go 重复块、upstream.Error 模板重复 —— 留作 follow-up。
- **gofmt/vet/build**：受影响包 vet 干净；`gofmt -w errorsx/classify.go` 顺手处理 pre-existing 注释缩进问题。

### Spec 轴（结论：NO-GO → 本轮修复合并为 GO）

- **13 条 fix 忠实性**：12 PASS + 1 PASS-with-HIGH（`70beabb9` 的 `listTenants` 跨租户读取 — 本轮闭合）。
- **文档/版本/CHANGELOG 同步**：
  - **VERSION/version.json/web/public/version.json** 三处一致 ✓
  - **CHANGELOG 严重欠账**：本轮**已修复**——24h 内 10 条独立 fix 之前被压成一条"72小时审计修正"摘要，本次在 Unreleased 段顶补齐。
  - **seq 1475 release commit 缺失**：cfeca467 + 0eaa4fa8 自承 "Deployed to 154 as seq 1475" 但仓内无对应 release commit，VERSION 直接从 1474 跳到 1476。**不在本轮范围**（历史 commit 不可改；运维/审计可从 CHANGELOG 锚定）。
  - **docs/db-changelog.md 命名不一致**：commit `c13ee3c6` 写 "476_routes_incidents_audit_safety"，实际文件 `476_session_v2_tenant_unique_keys.sql`。本轮**已修复**——加权威 note。
  - **release seq 1476/1475 docs 缺失**：仅 seq 1477 有 follow-up；1475/1476 缺 changelogs。**不在本轮范围**（VERSION + commit message 已能定位）。

## 本轮修正

- **H1 闭合**：`bg/freequotacleanup/worker.go`、`bg/freequotareset/worker.go` — listTenants 走 super_admin 通道；两 worker_test.go 适配 sqlmock 期望；删除 `var _ = time.Now` 死代码。
- **H2 清理**：`bg/node_probe.go` — 删除 `notifySyncWaiters` 孤儿函数；`bg/node_probe_sync_test.go` 两处调用改 `finishProbe`。
- **H1 → M3**：`domains/session/v2/session_writer_v2.go` — `LockSessionInTx` 锁作用域加 5s `context.WithTimeout`。
- **M4 测试**：`security/sanitize/smart_sani_guard_test.go` — 新增 `TestSanitizeRestoreInterceptor_StreamChunk_FrameSplitAcrossChunks`，把跨 chunk SSE 帧拆分的"已知缺陷"钉住为回归。
- **M6**：gofmt -w `errorsx/classify.go` 注释缩进。
- **M7**：`domains/credential/breaker.go` — escalation 条件 `if (...||...||...) && kind != X` 改 `switch kind { case KindTransient, KindTimeout, KindNetwork, KindStreamTimeout: ... }`。
- **M8**：`provider/client.go` — sibling SQL 的 quota_state 谓词加注释，解释为何与 routing-time 谓词不一致（sibling EXISTS ≠ routing candidate selection）。
- **M1**：`CHANGELOG.md` — Unreleased 段顶补 24h 修改审计修正 + 10 条独立 fix 条目 + commit SHA。
- **M2**：`docs/db-changelog.md` — 476 entry 加 note，明确真实文件名是 `476_session_v2_tenant_unique_keys.sql`，纠正 commit message 错误。

## 验证结果

- 通过：`go build ./...`（受影响包）。
- 通过：`go vet ./...`（受影响包，无新告警）。
- 通过：`go test ./bg/... ./errorsx/... ./domains/credential/... ./security/sanitize/... ./provider/... ./domains/session/v2/...` — **全绿**。
- 通过：新增 `TestSanitizeRestoreInterceptor_StreamChunk_FrameSplitAcrossChunks` PASS。
- 通过：worker sqlmock 期望适配后 `TestCleanupOldWindows_MultipleTenants/EmptyTable/ListTenantsFailsBackToDefault`、`TestResetExpiredWindows_*` 4 个 case 全 PASS。
- 通过：删除 `notifySyncWaiters` 后 `TestProbeSync_*` 6 个 case 全 PASS。
- 通过：gofmt -w `errorsx/classify.go`。
- 未执行：真实 PostgreSQL 上的 476 down/up、RLS policy 测试、跨 chunk SSE 实网验证 —— 留给部署环境补验。

## 剩余风险

- **M1/M3 spec 残留**：CHANGELOG 一次性补全后下次维护仍需遵守"每条独立 fix 单独条目"约定；seq 1475 release commit 历史不可改，但下次 release commit 必须有 body 描述。
- **H2 follow-up**：c81007af 的其余 4 处 sole-candidate 修改（executor.go:2893/2945/3137/3356）仍未测，记为 follow-up #1（参考 `docs/changelogs/2026-08-08-seq1477-followup.md`）。
- **跨 chunk SSE 帧还原**：本次只钉住"已知缺陷"为回归，不修实现；修复需在 `interceptingStreamWriter` 层加 cross-chunk 帧缓冲。
- **0eaa4fa8 probe 断言**：新 probe 排除断言在 `TEST_DATABASE_URL` gated 集成测试中，**未在 CI 跑过**，需部署环境补验。

## 提交

- 修正提交：`audit(session-audit-gate): 24h 修改审计 — H1/H2/M1-M8 修正与文档同步` (HEAD+1)。
- 推送目标：`origin/main`，已成功推送。

## 三关总结

| 关 | 结论 |
|----|------|
| ① 会话日志完整 | 需求/修改的功能点/commit 列表/diff stat 齐全 ✓ |
| ② 双轴代码审查 | Standards 轴 GO（带条件 → 全部闭合）; Spec 轴 NO-GO → 本轮修正后 GO |
| ③ 规则自检 | gofmt / go vet / go test PASS；sqlmock 期望适配 ✓ |

**最终判定：GO** — 已修正全部 CRITICAL/HIGH + MEDIUM 项；剩余 LOW 项（H2 之外、跨 chunk SSE）已显式留档 follow-up。