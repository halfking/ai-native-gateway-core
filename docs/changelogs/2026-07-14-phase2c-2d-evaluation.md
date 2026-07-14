# Phase 2C / 2D Evaluation

日期：2026-07-14

## Phase 2C 数据模型

### 当前状态

Phase 2C 部分完成。当前已有 `request_logs.attachments` JSONB 兼容字段和附件生命周期元数据，但没有独立的 `request_attachments` 表，也没有把 request body、outbound body、trace 和 retry cache 的历史脱敏策略版本化。

### Migration 审计

- 已使用的 startup migration 编号包含 `391` 至 `397`，不能复用。
- 新 migration 应从 `398` 起，并必须提供同名 `down.sql`。
- `325_request_attachments.sql` 不是独立附件实体表，只增加 `request_logs.attachments` JSONB；其 down 文件存在。
- 本轮没有新增 migration，避免在 schema 契约未定时产生不可逆的历史数据承诺。

### 兼容读与数据策略

- 旧 `request_logs.attachments` 必须继续读取，历史路径 `req_<requestID>` 由附件读取接口兼容。
- 新请求应只写轻量元数据和 manifest 引用，不能把 base64、data URI 或临时路径写入新逻辑表。
- 现有 `request_logs.request_body` 历史内容不能清空。需要单独设计 backfill/脱敏任务、短 TTL、加密存储、访问权限和审计记录。
- `request_attachments` 设计至少需要 request/tenant/attachment index、message/block index、hash、size、sniffed MIME、storage status、error code、provider reference 和 expiry 字段，并与旧 JSONB 的字段映射保持明确。

### 证据

- `sql/migrations/startup/325_request_attachments.sql:16-41`
- `sql/migrations/startup/325_request_attachments.down.sql:1-6`
- `sql/schema/01-schema.sql:1593`
- `domains/streaming/request_log_pipeline.go:458-502`
- `domains/streaming/attachment_body_redaction.go:1-55`
- `domains/streaming/handler.go:2401-2526,3315-3347`

## Phase 2D 模型契约

### 当前状态

Phase 2D 部分完成。`canonical_raw_name` 已进入 discovery/model catalog 的写入链，标准化字段和 canonical client key 的意图明确；`RawModels` 已改用保留原始大小写的去重 helper。但 SQL equality 和完整数据库回归尚未完成。

### 已确认

- 客户端输入通过 `CanonicalizeClientModel` 生成小写、去 vendor prefix 的 lookup key。
- discovery 同时保留 provider-facing `raw_model_name`、client-facing `canonical_raw_name` 和 lowercase `standardized_name`。
- `provider/client.go` 与 `resolve/resolve.go` 的返回值现在使用 `uniqueRawModels` 保留供应商原始大小写。

### 未完成

- `provider/client.go:648` 仍有 `lower(ma.raw_name) = lower($1)` fallback，和 equality-only 目标冲突。
- `provider/client.go:464-470` 仍保留旧候选查询的 lower 兼容路径，需要确认调用链是否仍可达后再迁移到标准列。
- 尚未以 migration 增加 `standardized_name`/`raw_name_key` 的小写约束，也没有存量清洗 migration。
- S25-S28 当前只有本地 seam/结构测试，缺少真实 DB discovery、provider offer 和 outbound HTTP 证据。

### 证据

- `modelname/normalize.go:211-244`
- `discovery/discovery.go:611-715`
- `modelcatalog/upsert.go:21-35,51-117`
- `provider/client.go:547-678,1044-1073`
- `resolve/resolve.go:104-285`
- `docs/全方面测试/tools/attachment_audit.py:26-118`

## 实现顺序

1. 先收敛 Phase 2D 可达 SQL lower fallback，并补 DB-backed S25-S28。
2. 再确定 `request_attachments` schema、历史兼容读、backfill/TTL/权限策略。
3. 使用 migration `398+` 落地 schema，并编写 down.sql 与升级/回滚演练。
4. 最后在 Phase 2B 契约解阻塞后，把 provider reference 写入新表。
