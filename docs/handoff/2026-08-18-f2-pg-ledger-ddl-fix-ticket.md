# F-2 修复 Ticket：对齐 URSM K2 ledger 的 `rollback_deadline` DDL 契约

- **Finding**：F-2
- **优先级**：P1（M5-0/T0 解封前置；ledger 不可用）
- **状态**：RESOLVED — forward migration、runtime ensure 与隔离 PostgreSQL 回归已完成（2026-08-18）
- **Owner**：k2 migration owner（按 14 §0；当前具名 migration owner：`halfking`）
- **发现基线**：`main` @ `ccf3929b9`（G4 真实 Redis/PG 证据已合并）
- **证据**：`docs/handoff/2026-08-18-g4-real-redis-pg-evidence.md` §3、§6

## 摘要

`domains/ursm/v2/migration/pg_store.go:48-65` 的 `OpenRun` INSERT 明确写入 `ursm_key_migration_runs.rollback_deadline`，但正式迁移 `sql/migrations/080-ursm-key-migration-ledger.sql:17-38` 和 `db/db.go` 的 runtime ensure DDL 都没有该列。真实 PostgreSQL 执行 `--apply` 时稳定返回：

```text
ERROR: column "rollback_deadline" of relation "ursm_key_migration_runs" does not exist (SQLSTATE 42703)
```

手工执行 `ALTER TABLE ... ADD COLUMN rollback_deadline timestamptz` 后重跑，run 行和 3 条 entries 均可正确落库，故障边界已精确定位为单列 DDL/代码漂移。此前 sqlmock 只按代码查询建模，未能发现真实 schema 不一致。

在修复、真实 PG 回归和 ledger 证据确认前，M5-0/T0 保持 **NO-GO**，不得对任何真实环境执行 k2 copy/cleanup 或依赖 PG ledger 的运维序列。

## 影响范围

- `domains/ursm/v2/migration/pg_store.go:40-68`
- `sql/migrations/080-ursm-key-migration-ledger.sql:17-38`（历史 DDL，仅作契约基线，不应仅修改历史文件掩盖已部署数据库）
- `sql/migrations/080-ursm-key-migration-ledger.down.sql`（按 owner 决策同步检查）
- `db/db.go:3901-3977` runtime ensure 镜像
- `domains/ursm/v2/migration/pg_store_test.go`（当前缺失，需补真实 PG 集成覆盖）
- 新增后续 forward migration（建议 `081-…-add-rollback-deadline.sql` 及对应 down 文件）

## 确定性复现（RED）

在隔离 scratch PostgreSQL（G4 §6，`postgres:16-alpine`，`127.0.0.1:5433/k2g4`）中：

1. 初始化 080 ledger schema；
2. 运行真实 PG `--apply` / `PGStore.OpenRun`，传入非空 `RollbackDeadline`；
3. 观察 SQLSTATE `42703`。

修复前结果：run INSERT 失败，ledger 无法作为真实 apply 的 durable audit record。手工补列后成功，仅用于确认边界，不是正式修复方案。

## 修复要求

1. **新增可前滚 migration**：新增 081（或按仓库编号策略确定的后续版本）为既有 `ursm_key_migration_runs` 补充：
   - `rollback_deadline TIMESTAMPTZ`；
   - nullability/default 必须与 `RunRecord.RollbackDeadline` 语义和 `pg_store.go` 参数一致；
   - 对已执行 080 的数据库安全、幂等、可审计。
2. **同步 runtime ensure**：`db/db.go` 中的 ensure schema 必须包含同一列契约，避免“迁移包正确但启动自修复仍漂移”。不要只改 080 历史文件而遗漏已部署数据库升级路径。
3. **检查 down/回滚策略**：按仓库 migration 规范提供对应 down 或明确不可逆说明；不得在存在 in-flight ledger/run 时静默丢弃审计列。若 rollback 需要保护条件，应在迁移说明与 SQL 中体现。
4. **保持代码契约**：除非 owner 明确决定修改领域契约，否则保留 `PGStore` 写入 `rollback_deadline` 的行为；不得用删除 INSERT 列的方式让测试“通过”而丢失 rollback gate 数据。
5. **真实数据库测试**：补 `pg_store_test.go` 或等价集成测试，使用真实 PostgreSQL 初始化 migration 后验证 schema 与 query；sqlmock 可保留作快速测试，但不能作为唯一证据。

## TDD / 回归验收

先固化真实 PG RED 用例，再补 migration/ensure，实现 RED→GREEN：

- 仅执行 080 + 现有代码时，测试应复现 SQLSTATE `42703`（或等价明确缺列错误）。
- 执行 080 + 081（以及 runtime ensure 路径）后，`OpenRun` 成功；非空 `rollback_deadline` 可读回且类型为 `timestamptz`，null 行为符合领域契约。
- 随后 `UpsertEntries` 成功，run 行与 entries 均提交；覆盖 `checkpoint=preflight`、`mode=dual`、分类/tuple 字段和 `ON CONFLICT` 的既有行为。
- 对已执行 080 的旧库执行 081 后重跑成功；对新库完整 migration 顺序执行成功；重复执行迁移保持幂等。
- runtime ensure 与正式 SQL migration 的列集合一致，避免新库/旧库分叉。
- G4 §3 的真实 PG apply 回归无 `42703`，并保留可审计的 run + entries 证据；不得接触生产/共享 PG。

建议验证命令（owner 环境）：

```bash
go test ./domains/ursm/v2/migration -run 'TestPGStore|Test.*Migration.*Ledger' -count=1 -v
# 再按 docs/handoff/2026-08-18-g4-real-redis-pg-evidence.md §6 启动隔离 PostgreSQL，执行 §3 apply/回归
```

## 修复与复验（2026-08-18）

- 新增 forward-only root migration `081-ursm-key-migration-ledger-add-rollback-deadline.sql`，以幂等的 nullable `TIMESTAMPTZ` 补列；历史 080 未被重写。081 down 明确为非破坏性，保留 rollback-gate 审计时间戳。
- `ensureUrsmKeyMigrationLedgerSchema` 的 fresh schema 已包含该列，并在 `CREATE TABLE IF NOT EXISTS` 后无条件执行 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`，覆盖既有 080 表。
- 隔离 `postgres:16-alpine` Testcontainers 回归真实执行 080 → 081 → 081，验证列在 080 后缺失、081 后为 nullable `timestamp with time zone`、`PGStore.OpenRun` 写入/读回 deadline、`UpsertEntries` 成功写入 3 条 entries；未出现 SQLSTATE 42703。
- 独立 runtime ensure 回归从 080-only schema 调用 ensure 两次，确认升级和重跑均成功。`go test -count=1 ./...` 通过。


- owner 提交 081 migration、runtime ensure、真实 PG 集成测试和回滚说明；不得由本 ticket 执行生产 schema 变更。
- fix-review 对 owner commit 做差分审查，确认正式 migration、runtime ensure、代码 query 与测试 schema 四者一致，并检查 down/既有库升级风险。
- G4 隔离真实 PG apply 回归通过后，才可将 F-2 标记 RESOLVED。
- F-2 RESOLVED 之前，M5-0/T0 继续 NO-GO；不得对真实环境执行依赖 PG ledger 的 k2 运维序列。

## 非本 Ticket 范围

- F-1 canonical 删除缺陷（见 `docs/handoff/2026-08-18-f1-canonical-cleanup-fix-ticket.md`）；
- PG owner takeover、ledger identity 不可复用和 fencing token（建议另立 owner/权限 ticket）；
- 两 CLI window 分类差异；
- `entries.tuple_tenant` 空值；
- G1 构建缓存预防措施。
