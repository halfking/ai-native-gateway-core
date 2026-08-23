# 2026-08-23 — sessionmeta provisional 154 + migration 561 schema repair

## sessionmeta provisional（build 1689 @ 154）

- 部署：`build_seq 1689` / `git_sha ae9277c0`，仅 154，未触 245
- arrival 路径：`MaybeGenerateProvisionalMetadata` → `session_title_states.title`（source=`auto-title`）
- 154 验证：
  - 首轮：`gw_f29ef172-…` → `修复 sessionmeta provisional title 集成测试`
  - 第二轮：title 不变（不覆盖已有 title）

## 集成测试（本地）

- `domains/streaming/handler_provisional_metadata_test.go` — wiring + branch session 跳过
- `admin/auto_title_provisional_test.go` — enabled gate、已有 title 跳过（pgxmock）
- `internal/titlestore/store.go` — `dbPool` 接口化以支持 mock

## migration 561 schema drift（154 生产修复）

**症状**：chat 500，`query candidates failed: column mo.priority does not exist`

**根因**：`schema_migrations` 已记录 `561`，但 `credential_model_bindings.priority` 与 `model_offers` view 未落地（迁移标记与 DDL 不一致）。

**修复**：在 154 PG 重跑 idempotent `561_credential_priority_flag.sql`（2026-08-23 18:44 UTC+8）。

**修复后**：`candidates_resolved candidates_count=3 err=null`（SQL 恢复）；chat 仍可能 503（凭据/upstream 耗尽，与 schema 无关）。

## SSOT

- `sql/objects/views/model_offers.sql` 补 `cmb.priority` 列，与 561 对齐
