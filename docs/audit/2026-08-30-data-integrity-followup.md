# 数据完整性审计跟进（2026-08-30）

## 范围

本轮复核 `JournalSnapshot` durable receipt、candidate failure hot promotion 与 provider error aggregation 的运行时/迁移闭环，基线为上一阶段 `main`。

## 已修正

### P0：candidate failure promote 的 delete-before-insert 丢数窗口

历史 V359 重新定义的 promote function 使用 `SELECT *`，在 hot 表新增 `aggregation_id` 而 parent 未同步时会产生列不匹配。该实现先删除 hot 行，再捕获 INSERT 错误并返回 `0`，导致已删除行无法恢复。

Migration 624 / deploy V367 采用显式共同列和同一 data-modifying CTE：INSERT 失败会使关联 DELETE 回滚。migration 不再恢复不安全的旧函数；down path 仅验证安全函数仍存在。hot RLS policy 增加事务局部 super-admin/bypass 分支，以支持受控后台聚合读取。

### P1：JournalSnapshot receipt runtime schema 与 lease fencing

- `db.ensureJournalSnapshotReceiptSchema` 现在补齐 `projection_base_seq` 和约束，防止未执行 startup migration 时 durable claim 以“列不存在”失败。
- receipt claim 的 nil receiver 不再访问 `s.owner` 并 panic。
- completed receipt 的 NULL `claim_owner` 使用 nullable pgtype 扫描，重复投递正确返回 `AlreadyCompleted`。
- 任何未到期 processing lease 均拒绝 reclaim；不会因同 hostname/configured owner 相同而绕过 fencing。
- gateway 生成进程级随机 receipt owner nonce，同时保留稳定 instance ID 作为观测/实例身份。

## 本地验证

```bash
go test ./db ./domains/requestjourney ./cmd/gateway ./sql/migrations/startup -count=1
```

覆盖 runtime receipt schema、completed NULL owner、same-owner live lease、nil receipt、stable projection base 与 migration 624 SQL contract。

## 仍需授权 PostgreSQL 证据

1. migration 624 在目标 PG 上针对 column mismatch 注入失败时，hot DELETE 确实回滚。
2. receipt migration 623/runtime ensure 与真实 pgx pool、RLS、lease reclaim/partial projection 的集成。
3. aggregation source 跨 hot retention/promotion/迟到行后仍能正确重算同一 bucket。
4. non-owner role 的 candidate failure hot/parent RLS policy 在 worker transaction bypass 下的跨 tenant 读取与 promotion。
5. 任何已由历史 V359 promote 丢失的记录只能通过备份/审计源核查；本次 migration 无法重建已删除数据。

## 结论

已消除可由代码和迁移定义直接证实的 delete-before-insert 静默丢数和 receipt runtime/fencing 漏洞。真实数据库迁移、RLS 与历史恢复不能在未授权环境中声明通过，必须按 `docs/06-deployment/04-runbooks/journal-provider-next-phase.md` 的隔离 PG 门禁执行。
