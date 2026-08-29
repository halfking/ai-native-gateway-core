# JournalSnapshot 与供应商错误闭环审计（2026-08-29）

## 特性需求矩阵

| 需求 | 当前结论 | 证据 |
| --- | --- | --- |
| JournalSnapshot bounded、显式截断 | 已实现事件数上限与截断计数；序列化大小/Metadata 尚未实现 | `domains/dispatch/pipeline.go`, `domains/dispatch/observation.go` |
| JournalSnapshot caller auth fail-closed | terminal push 使用 trusted dispatch tenant；外部 pull consumer 使用 tenant-scoped contract | `domains/dispatch/journal_consumer.go`, `cmd/gateway/main_dispatch_observation.go` |
| JournalSnapshot durable idempotency | receipt 唯一键 `(tenant_id, request_id, snapshot_version)`、payload hash、lease、completed 状态 | migration 618, `domains/requestjourney/journal_snapshot_receipt.go` |
| 供应商失败可观测 | upstream/circuit/limiter/key-rotation 进入 candidate failure writer；3 秒 best-effort | `domains/streaming/executors/executor_dispatch.go` |
| provider error 聚合 | 按 tenant + 十分钟 bucket + fingerprint 聚合，重复扫描覆盖 bucket count；RLS/FORCE RLS | migration 620, `bg/provider_error_aggregator.go` |
| credential error detail tenant isolation | tenant_admin 仅可查询自身 tenant credential；super_admin/admin_key 使用显式 all-tenant 语义 | `admin/vendor_credential_error_handlers.go` |

## 本次审计发现与修正

### P0 — credential error detail 跨租户 IDOR（已修正）

原 API 仅按 credential ID 查询，未校验 credential tenant。现在所有查询均接收 `EffectiveTenantIDAll(r)`：credential 元数据、candidate failure summary/recent、quality scores 均按 tenant 过滤；quality scores 通过 credentials join 校验归属。tenant mismatch 与不存在均返回 404 形状，避免存在性泄露。

### P0 — provider error 聚合跨租户及滑窗重复计数（已修正）

原聚合器使用 15 分钟滑窗并将 COUNT 递增写入全局 fingerprint，导致重叠窗口重复计数且覆盖 tenant/request/context。现按 tenant 和固定十分钟 aggregation bucket 分组，并以 bucket 唯一键 upsert 替换 occurrences；聚合 worker 通过事务局部 super-admin/bypass GUC 读取所有租户，provider_error_details 启用 FORCE RLS。

### P1 — JournalSnapshot sequence 域混用（已修正）

receipt 可用时不再使用 recorder 的 global MaxSeq 判断快照完成；receipt status 是唯一完成边界。无 durable receipt 的兼容路径仍保留旧行为，生产 DB 启用 receipt migration 618 后应使用 durable path。

### P1/P2 — schema 与生命周期（已修正）

candidate failure hot 表补齐 writer 使用的 `session_id` 与 `per_attempt_latency_ms`（startup 617 / deploy V363）；provider error tenant/bucket schema 的权威来源为 startup 620。ProviderErrorAggregator 的 Stop 在未启动、重复和并发调用时安全返回。

## 数据流与状态闭环

- dispatch candidate failure → `candidate_failure_logs_hot`：独立超时、失败不阻塞 failover。
- hot failure → provider error bucket aggregate：advisory lock 防并发实例，固定 bucket 防重放。
- terminal JournalSnapshot → receipt claim → journey projection → receipt completed；claim 失败或 projection 失败不影响 settlement，lease 到期可重试。
- admin credential detail → authenticated tenant context → credential ownership predicate → tenant-filtered detail queries；未授权不返回 partial payload。

## 验证

- `go test ./admin ./bg ./domains/requestjourney ./domains/dispatch ./domains/streaming/executors ./sql/migrations/startup -count=1`：通过
- `go test -race ./domains/requestjourney ./domains/dispatch ./domains/streaming/executors -count=1`：通过
- `go build ./...`：通过
- `go vet ./...`：通过
- `bash ~/.agents/skills/llm-gateway-deploy-test/test.sh --env local --dry-run`：通过

## 仍未闭合风险

1. provider aggregator 尚无真实 PostgreSQL integration（RLS、bucket replay、migration 620 在目标库的执行需授权 DB）。
2. candidate failure writer/RLS 与 hot-to-partition promote 仍需真实 PG integration。
3. ErrorDetailTab 尚无 browser/UI 双主题和请求取消竞态验收；当前后端 API 已固定实际路径 `/api/vendors/credentials/{id}/error-detail?hours=1|24|168`，但设计文档部分旧路径需同步。
4. response-body-missing 历史扫描、告警阈值和 dashboard SSOT 尚未落地；当前实时指标仍需统一命名/避免双计数。
5. 245/154 canary 未在本轮执行；local dry-run 不能替代预发/生产验收。

## 审计结论

后端 P0 数据隔离与聚合重复计数问题已修正并通过单元/竞态验证。可合并代码修正；在真实 PostgreSQL、浏览器双主题和 245 canary 证据补齐前，不得宣称生产级 UI/数据库/零中断发布已验收。
