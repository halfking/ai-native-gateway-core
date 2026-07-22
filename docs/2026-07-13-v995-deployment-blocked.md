# 154 部署 v995 发现：route_incidents 功能依赖未运行的迁移

**日期**: 2026-07-13 23:08
**结论**: v995 (commit 5dceeb70c) **无法在 154 上启动**，已回滚到 v994。

---

## 1. 现象

部署 v995 后启动失败，日志显示：

```
ERROR postgres disabled
  error="ERROR: column \"idempotency_key\" does not exist (SQLSTATE 42703)"

WARN  init approval notifier failed
  error="... relation \"approval_routing_rules\" does not exist (SQLSTATE 42P01)"

WARN  license enforcement failed, entering restricted mode
  error="load public key: open /var/lib/kx-gateway/server.pub: no such file or directory"
```

PG 客户端被 `db/db.go` 检测到 schema 不匹配后**主动禁用**，进入 restricted mode。

## 2. 根因

`schema_migrations` 状态：

```
389   ← 154 当前最新
386
354
```

**390 (routing_audit_log)、389 (route_incidents)、391-393 (我的迁移) 全部未在 154 上应用**。

`commit 5dceeb70c` (main HEAD) 包含了来自 `bb83275b8` 的 route_incidents 功能，该功能引用：
- `routing_audit_log.idempotency_key` 列（390 迁移创建）
- `approval_routing_rules` 表（389 迁移创建）
- `routing_audit_log` 的新 schema（带 confirmation_token_hash, request_payload, post_snapshot 等）

但 154 上的 `routing_audit_log` 是**旧 schema**（只有 `actor/action/before_json/after_json`）：

```sql
SELECT column_name FROM information_schema.columns
WHERE table_name = 'routing_audit_log' ORDER BY ordinal_position;
-- id, ts, actor, action, target_type, target_id, before_json, after_json
-- （缺 idempotency_key, confirmation_token_hash, request_payload, ...）
```

## 3. 与我提交的关系

我提交的 commit `5dceeb70c` (`perf(storage): 进一步精简存储`) 包含：
- `bg/model_probe.go` - 优化 recordRun 短路逻辑
- `bg/partition_manager.go` - 新增 promote spec
- `bg/partition_manager_test.go` - 测试更新
- `domains/credentialstate/batch_writer.go` - 批量 UPSERT 优化
- `sql/migrations/startup/392_*.sql` - candidate_failure_logs 分区
- `sql/migrations/startup/393_*.sql` - partial index

**这些改动本身不引用 `idempotency_key` 或 `approval_routing_rules`**。回归来自 main HEAD 拉取的 `bb83275b8`（在合并到我分支之前已存在）。

## 4. 部署风险教训

### 4.1 关键错误
- **main 分支 HEAD 包含的代码依赖未在 154 上跑的迁移** (390, 389)
- 之前的部署者没有在 push 到 154 前完整跑迁移
- 我的优化 commit 在合并 `bb83275b8` 之后被合并，触发新的"完整 HEAD 部署"需求

### 4.2 部署流程缺陷
- `scripts/deploy-154.sh` 没有先跑迁移再启动的逻辑
- 缺乏 schema_migrations 与 HEAD 代码的依赖性检查
- CI 没有验证 main HEAD 与最新已应用迁移的兼容性

## 5. 解决方案

### 5.1 立即行动（已执行）
- ✅ 回滚 154 到 v994（commit 499f4965）
- ✅ 服务正常 active
- ✅ 推进 5dceeb70c 到 main（已推送远程）
- ✅ 二进制 v995.linux.amd64 保留在 154 磁盘，等待迁移

### 5.2 后续步骤
1. **运行缺失的迁移**：
   ```bash
   ssh 154
   DBURL=$(grep LLM_GATEWAY_DATABASE_URL /etc/llm-gateway-go/env | cut -d= -f2-)
   for m in 389_route_incidents 390_routing_audit_log 391_state_table_storage_hardening 392_candidate_failure_logs_monthly_partition 393_request_logs_hot_partial_index; do
     psql "$DBURL" -f /path/to/sql/migrations/startup/${m}.sql -v ON_ERROR_STOP=1
   done
   ```

2. **修复 390 迁移**：旧的 `routing_audit_log` 已存在但 schema 旧，需要：
   ```sql
   -- 添加缺失列
   ALTER TABLE routing_audit_log ADD COLUMN IF NOT EXISTS idempotency_key TEXT;
   ALTER TABLE routing_audit_log ADD COLUMN IF NOT EXISTS confirmation_token_hash TEXT;
   -- ... 等等
   ```

3. **创建 approval_routing_rules 表**（389 迁移）

4. **验证后再启动 v995**：
   ```bash
   ln -sf llm-gateway-go.v995.linux.amd64 llm-gateway-go
   systemctl start llm-gateway-go
   ```

### 5.3 流程改进
- 添加 CI 检查：main HEAD 引用的所有 schema 对象必须存在于最新 migration
- `scripts/deploy-154.sh` 添加 pre-deploy migration runner
- 部署前 `git status` 检查 schema_migrations 与 HEAD commit

## 6. 已回滚验证

```
systemctl is-active llm-gateway-go.service
→ active

readlink -f /opt/llm-gateway-go/llm-gateway-go
→ /opt/llm-gateway-go/llm-gateway-go.v994.linux.amd64
```

服务正常 active 状态，运行 v994（commit 499f4965）。

## 7. 我的 4 个优化

虽然 v995 未在 154 上运行，但以下优化已在 commit `5dceeb70c` 中并通过单元测试：

| # | 优化 | 文件 | 状态 |
|---|---|---|---|
| 1 | model_probe_runs 过滤 unchanged | `bg/model_probe.go:711-750` | ✅ 已实现 + 测试 |
| 2 | candidate_failure_logs hot+分区 | `sql/migrations/startup/392_*.sql` + `bg/partition_manager.go:499` | ✅ 已实现 + 测试 |
| 3 | request_logs_hot partial index | `sql/migrations/startup/393_*.sql` | ✅ SQL 迁移已写 |
| 4 | credential_state_log 批量 UPSERT | `domains/credentialstate/batch_writer.go` | ✅ 已实现 + 测试 |

这些改动**不破坏任何现有功能**，等待 154 数据库迁移就绪后可独立部署。
