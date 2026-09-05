# Session Handoff: isolated-PG 验证 + 附件 cleanup 闭环

> 接续 2026-08-31-2 handoff (`/tmp/handoff-20260831-063300.md`) 的"后续 handoff 优先级 1 / 2"。

## 1. 任务概要

按上一份 handoff 的下一步执行：
1. 在隔离 PostgreSQL/多租户 FORCE RLS 环境验证 migration 627–631、Session aggregate outbox reaper、hot→columnar promote。
2. 统一附件 preview/execute/history 的 tenant scope、cleanup_run、tombstone、retryable failure 审计闭环。

不回滚任何已部署 migration；不绕过 pre-commit；所有修正补定向测试和真实环境验证记录。

## 2. 当前状态

- 工作目录：`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go`
- 分支：`main`，最后 commit `3b3497935`，working tree 含 4 个未提交版本文件（已纳入本会话 commit）。
- 全部 verify 脚本 exit 0（见 §3）。
- 13 个 reaper 测试（6 integration + 7 unit）全过；5 个新 attachment unit test 全过。
- 38 条已登记 migration checksum 一致（fix 后）。

## 3. 完成的工作

### 3.1 isolated PG 与 verify 脚手架

| 路径 | 用途 |
| --- | --- |
| `scripts/audit/start-isolated-pg.sh` | 启动 `kx-citus` 容器（端口 15433），DSN 输出到 `/tmp/audit-pg.env` |
| `scripts/audit/stop-isolated-pg.sh` | 停止/销毁 |
| `scripts/audit/psql-isolated.sh` | 通用 psql wrapper（支持 `-c` / `-f`） |
| `scripts/audit/sql/min-prereqs.sql` | audit 最小 fixture（get_current_tenant、candidate_failure_logs family、sessions、session_turns_hot、ensure_candidate_failure_logs_partition） |

### 3.2 验证脚本（独立 PG）

| 路径 | 验证内容 | exit |
| --- | --- | --- |
| `scripts/audit/verify-migrations-627-631.sh` | 627-631 up / down / up + checksum + 630 FORCE RLS 三 case | 0 |
| `scripts/audit/verify-promote-candidate-failure-logs.sh` | promote 628：50 行 hot → columnar，aggregation_id 完整保留 | 0 |
| `scripts/audit/verify-outbox-reaper.sh` | `go test -tags=integration -run TestReaper_ ./domains/session/v2/...` | 0 |

### 3.3 代码改动

| 路径 | 改动 |
| --- | --- |
| `sql/migrations/startup/632_audit_attachments_filesystem_cleanup.sql` (+ `.down.sql`) | 新增 FS 审计表，与 629 对称 |
| `installer/cmd/llm-gw-installer/embeddata/startup/632_*.sql` | 镜像到 installer embed（保持 byte-for-byte） |
| `installer/cmd/audit-dbinit/` (新目录) | audit 专用 Go binary，stage embed SQL 并调用 `dbinit.Runner`（本会话未走通，详见 §4 P1） |
| `domains/session/v2/session_aggregate_outbox_reaper.go` | 修真实 bug：lease 秒数改 `text` 而非 `int`（`fmt.Sprintf("%d", ...)`） |
| `domains/session/v2/session_aggregate_outbox_reaper_integration_test.go` (新文件) | 6 个 integration 测试 + pgxmock seam |
| `domains/session/v2/session_aggregate_outbox_reaper_test.go` | 同步修复：mock test 期望 `text` 租约 + 加 `fmt` import |
| `admin/data_lifecycle_attachments.go` | 重写：tenant scope helper / runWithRetry / malformed hash + size 防御 / FS 平行审计行 / 结构化 slog |
| `admin/data_lifecycle_attachments_test.go` | 新增 5 个 unit test（tenant scope / retryable / runWithRetry / malformed-hash SQL 守卫 / FS audit row SQL 守卫）+ 既有 2 个 UUID test 保留 |
| `admin/data_lifecycle_attachments_filesystem.go` | 加入 cleanup_run_id / triggered_by_user / FS audit row 写入（请求体可传 `cleanup_run_id` 关联 DB 端） |
| `scripts/verify-migration-checksums.sh` | fix first-write-wins bug：允许多 SHA 匹配（pending + applied+verified 共存） |
| `docs/db-changelog.md` | 增 632 ledger 条目，sha256 由脚本计算后回填 |

### 3.4 docs 更新

- `docs/audit/2026-08-31-4h-correction-audit.md` 追加 §2/§3/§4（不替换既有内容）。
- `.handoff/2026-08-31-isolated-pg-verification-and-attachment-cleanup.md`（本文件）。

## 4. 阻塞 / 风险 / 下一步

### 4.1 P0（staging 部署前必修）

1. **promote_session_bodies_hot_to_partition 与 promote_session_turns_hot_to_partition 的真实环境验证**。fixture 量太大（需要 614/615/626 全部 startup + session_bodies/_hot + session_turns/_hot + ensure_partition 函数），本会话未做。建议在 staging 用 installer 完整 init 后跑 `SELECT promote_session_bodies_hot_to_partition('0s', 1000);` 与 `SELECT promote_session_turns_hot_to_partition('0s', 1000);` 端到端。
2. **`sql/schema/01-schema.sql` dump 自洽性修复**。该 dump 是 production-aligned pg_dump，不自洽（plpgsql function 引用后面才 CREATE 的表 + dump 顶端 syntax error），导致 `installer/cmd/audit-dbinit`（本会话的 audit-only 路径）走 `dbinit.Runner.InitSchema` 也会在 01-schema 这一步失败。建议：在 installer pipeline 改成"先 apply 511..626 startup migrations 再 pg_dump 生成新 01-schema.sql"，或者把 01-schema.sql 拆分为多个 schema-domain 文件。

### 4.2 P1（后续 audit pass）

1. `request_attachments` (mig 401) status enum 接入 data_lifecycle 路径，替换 `request_logs.attachments` JSONB 元数据为关系表。
2. 附件 cleanup execute 迁到 `data_lifecycle_jobs.go` 的 `StartJob` 异步 JobType 路径（`JobTypeCleanupAttachments`），引入 progress/heartbeat/cancel 与 per-job audit linkage。
3. 附件 DB 与 FS 真正 Saga（2PC 或 outbox + reconciler）；本会话通过 `cleanup_run_id` + 两张审计表做了最小关联，但跨表一致性仍依赖人工。
4. `audit_attachments_cleanup` 的 super_admin 读端点：`GET /api/admin/attachments/cleanup/runs/{id}` 返回 run + 命中行 + 关联 FS 审计。
5. ConnectionRegistry 写超时 goroutine 资源上限；VACUUM FULL 多副本互斥；provider detail tabs/menu parity 守门测试。

### 4.3 已闭合

- 627-631 在 isolated PG 真实环境 up/down/up + checksum 验证 ✓
- 630 FORCE RLS 多租户三 case 验证（tenant_a=1, tenant_b=0, super_admin=1）✓
- reaper 真实 bug（lease int→text）修复 + 6 个 integration 测试覆盖 ✓
- 628 promote aggregation_id 不丢失验证 ✓
- 附件 cleanup tenant scope / retryable / malformed 防御 / FS 平行审计 / 结构化 slog ✓
- 5 个新 unit test + 既有 UUID test 保留 ✓
- `verify-migration-checksums.sh` first-write-wins bug 修复 ✓

## 5. 引用

- 上一份 handoff：`/tmp/handoff-20260831-063300.md`
- 审计基线：`docs/audit/2026-08-31-4h-correction-audit.md`（含 §2/§3/§4 追加）
- 验证脚本：`scripts/audit/{start-isolated-pg,stop-isolated-pg,psql-isolated,verify-migrations-627-631,verify-promote-candidate-failure-logs,verify-outbox-reaper}.sh`
- 新增测试：`domains/session/v2/session_aggregate_outbox_reaper_integration_test.go`（integration tag），`admin/data_lifecycle_attachments_test.go`（5 个新 case）
- 修改的源文件：`domains/session/v2/session_aggregate_outbox_reaper.go`、`admin/data_lifecycle_attachments.go`、`admin/data_lifecycle_attachments_filesystem.go`、`scripts/verify-migration-checksums.sh`
- 新增 migration：`sql/migrations/startup/632_audit_attachments_filesystem_cleanup.sql` + `.down.sql`
- 新增 audit binary：`installer/cmd/audit-dbinit/{main,embed,exec,stage}.go`（注：当前因 01-schema dump bug 不可用，见 §4.1.2）
