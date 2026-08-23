# 2026-08-24 — 凭据并发上限保存报 "commit: ... rollback" / 历史 JSON 22P02 修复

> 在 `https://llm.kxpms.cn/providers/12763` 凭据详情中修改「并发上限」并保存时，
> 前端报系统错误：`update credential failed: ERROR: invalid input syntax for type json
> (SQLSTATE 22P02)`。实际排查中发现该 JSON 错误已被更早的提交修复，但暴露出另一个
> 真正阻断保存的 bug：只要改动 `concurrency_limit` / `fp_slot_limit`，保存必然失败。

## 1. 故障摘要

| 项 | 值 |
|---|---|
| 影响操作 | 凭据「编辑抽屉」保存（任何改动 `concurrency_limit` 或 `fp_slot_limit` 的保存）|
| 用户可见错误 | `commit: commit unexpectedly resulted in rollback`（或早期节点的 tags JSON 22P02）|
| 影响范围 | 全后端节点通用（与 build 版本无关）|
| 是否需迁移 | 否（`settings_audit` 表已存在）|

## 2. 根因分析

### 2.1 tags JSON 错误（已修复，记录在此供追溯）

`admin/provider_credential.go` 的 `updateCredential` 在 `PATCH` 时曾把 `tags`
(jsonb 列) 拼成逗号字符串 `strings.Join(req.Tags, ",")` 直接写入，PostgreSQL 拒绝
`""` / `"a,b"` 并报 `22P02`。该问题已由提交 `63d04ab54` 改为 `marshalCredentialTags`
+ `::jsonb` 强转修复，并已随 154 (build 1694) / 245 (build 1696) 部署——复测
`PATCH {tags:[]}` 已返回 `updated`。

### 2.2 fp_slot 保存回滚（本次真正阻断保存的 bug）

`updateCredential` 在事务内、提交前执行了一段审计写入：

```sql
INSERT INTO settings_history (key, old_value, new_value, changed_by, source)
VALUES ($1, $2, $3, $4, 'api')
```

但数据库中**不存在 `settings_history` 表**——审计表早已重命名为 `settings_audit`
（见 `sql/migrations/startup/023_settings_audit.sql`，列名为 `setting_key` /
`old_value`(jsonb) / `new_value`(jsonb) / `operator_user` / `operator_role` 等）。

该 `INSERT` 报错会**中止整个事务**（PostgreSQL 中事务内任一语句失败即进入中止态），
随后 `tx.Commit()` 返回 `commit unexpectedly resulted in rollback`。原代码对审计
`INSERT` 仅 `slog.Warn` 而不 `return`，导致主 `UPDATE credentials` 也一并被回滚，
用户看到保存失败。

复现矩阵（凭据 36，provider 12763）：

| 请求 | 结果 |
|---|---|
| `PATCH {tags:[]}` | ✅ `updated`（tags 修复已生效）|
| `PATCH {concurrency_limit:20, fp_slot_limit:5}` | ❌ `commit: ... rollback` |
| `PATCH {concurrency_limit:20, fp_slot_limit:5, tags:[]}` | ❌ `commit: ... rollback` |

前端 `CredsTab.vue` 在编辑抽屉中：仅改并发上限且未手动改 fp_slot 时，watcher 会用
`GREATEST(1, concurrency/4)` 重新推导 `fp_slot_limit`，因此保存请求携带
`concurrency_limit=20, fp_slot_limit=5`，正好命中本 bug。

## 3. 修复（本 PR）

| 文件 | 改动 |
|---|---|
| `admin/provider_credential.go` | 删除指向不存在的 `settings_history` 表的 `INSERT`；改为在事务 **commit 之后** 调用既有的 `settings.WriteAudit`（`settings/audit.go`），写入正确的 `settings_audit` 表。审计写入与事务解耦，成为真正 best-effort，失败不再连累主更新 |

### 3.1 设计要点

- `settings.WriteAudit(ctx, *pgxpool.Pool, AuditEntry)` 已是仓库内写 `settings_audit`
  的 canonical helper，且文档声明「best-effort: errors are logged but do not fail the
  upstream call」。直接复用，避免再造轮子或新建重复表。
- 改为在 `tx.Commit()` **成功之后** 调用，使用连接池（`h.db`）独立连接，审计失败不会
  中止主更新事务。
- `OldValue` / `NewValue` 为 jsonb，按 `settings_audit` schema 以 JSON 字符串
  （`"25"` / `"5"`）写入；`OperatorUser` 取自 `X-Admin-User` 头（缺省 `admin`），
  `OperatorRole` / `TenantID` 取自 `GetAuthContext`。

## 4. 验证

- `go build ./...` ✅
- `go vet ./admin/...` ✅
- `go test ./admin/... ./settings/...` ✅
- 部署 245 后复测 `PATCH {concurrency_limit:20, fp_slot_limit:5}` → 返回 `updated`，
  且 `settings_audit` 中出现 `credential:36:fp_slot_limit` 审计行。
- 部署 154 后经 `https://llm.kxpms.cn/...` 复测同一保存操作成功。
