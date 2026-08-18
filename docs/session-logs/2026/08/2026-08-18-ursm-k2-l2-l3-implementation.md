# 2026-08-18 — URSM k2 迁移 L2/L3 完成与审计

## 范围

L1 之后按批准计划继续 L2（schema-aware store/recovery/bootstrap/config）与 L3（preflight/copy/cleanup/rollback/PG ledger/CLI），全程 vertical TDD + 仅 miniredis/sqlmock 替身证据。`M5-0/T0` 仍 `BLOCKED / NO-GO`。

## 提交（worktree `feat/ursm-k2-migration` 基点 `fafdc60a5`）

- `28f6afb65` feat(ursm): dual-schema record and canonical-first reads (L2 core)
- `69a526a76` feat(ursm): schema-aware persist, recovery gates and bootstrap (L2)
- `d9dc9b203` feat(ursm): preflight/copy/cleanup/rollback machinery and PG ledger (L3)
- `2b81a02ce` fix(ursm): add KeySchema.String for the durable ledger schema column

## L2 产物

- `store.NodeKeySet` 增加 `Prefix` 和 tuple 字段；`K2KeySetForTenant` 派生 canonical 集；`record_request_dual.lua` 以 10 KEYS 维护两套集，跨集 manual_hold/dedup gate；`Store.SetKeySchemaMode` 启动期固定模式，`RecordRequestKeySet` 在 dual 走双脚本，legacy 字节等价。
- `PipelineNodeViews` 按模式分支：legacy 只读 legacy、dual canonical-first + exact-tuple fallback、canonical 只读 canonical 无 fallback；空 tenant 在 dual 下保留 legacy 写入。
- `persist.Collect` 走 `ParseNodeKeyAny`，记录 schema origin，canonical twin 替换 legacy twin 去重；不可解析键记为 ambiguous 排除。
- `recovery.Manager.SetKeySchemaMode`；`countWarmupNodes` 按模式去重（dual→distinct tuple、canonical→仅 k2、legacy→原行为）；`ValidateCoverage` 在 canonical 模式下拒绝 legacy manifest。
- `bootstrap.Apply` 增加 `SchemaMode`：legacy 字节、双模式双写、canonical 仅写 canonical 且空 tenant 报错；manifest 在 dual/canonical 下收录 canonical key。
- `Config.KeySchemaMode` + `URSM_V2_KEY_SCHEMA_MODE` env（非法值 Validate 拒绝）；`Manager.New` 同步设 store/recovery mode；`cmd/gateway/main.go` bootstrap 透传 mode。

## L3 产物

- `migration.ClassifyKey`：doc 14 §4 四类（migratable / canonical_present / ambiguous / excluded_non_authoritative）+ doc 15 §2 的 strict-segment + re-encode 字节相等硬闸；k2 marker 在 legacy 解析前置拒绝。
- `migration.Preflight` 只读 SCAN `prefix+node:*`，按字段名排序计算 deterministic field checksum，PTTL 精确转 -1/-2/正毫秒；`Report.Go() == ambiguous==0`。
- `migration.MetadataStore`：`<prefix>meta:migration` hash + `metadata_advance.lua` CAS，stale epoch 拒绝，异 owner/ledger_id 拒绝替换。
- `migration.Copier` + `copy_hash.lua`：脚本内重读 HLEN、逐字段 HGET、generation 与 caller 提供的 expected + field checksum 比对，全部通过才 HSET 目标；PTTL 由 Lua 二次读取，`PEXPIRE`` 不得超过源剩余时间；目标已存在且 marker 缺失则 `conflict`，marker 存在则 `already`；不修改 live canonical state。
- `migration.Cleaner.DeleteExact`：仅按 source key + checksum 删除（不变则变更拒绝）；`删除Batch` 接受正数 limit 以支持暂停/续跑；类型定义上无 SCAN/DEL 接口。
- `migration.PGStore`：写入 `ursm_key_migration_runs`（identity）与 `ursm_key_migration_entries`（per-key），事务 upsert，`ON CONFLICT (ledger_id, source_key) DO UPDATE`。
- `sql/migrations/080-ursm-key-migration-ledger.sql` + `.down.sql`：双轨 ensure 的 SQL 源；`db/db.go` `ensureUrsmKeyMigrationLedgerSchema` 镜像同一 DDL，apply 链注入到 `applyMigrationsOnce`。
- `cmd/ursm-k2-preflight/main.go`：dry-run 默认；`--apply` 在 NO-GO 时拒绝；接受 `--owner/--ledger-id/--mode/--rollback-deadline/--limit`；仅写 PG 与 Redis meta，不写 key。

## 门禁

- `go test ./domains/ursm/v2/migration/ -count=1` 17 PASS、0 FAIL（classifier/preflight scan/metadata/copy/cleanup 全路径）。
- `go test ./db/...` ok（与本分支相关 ensure 路径不变）。
- `go test -race ./domains/ursm/... -count=1` 全部 ok，0 DATA RACE。
- `go test ./... -count=1` 全仓 204 包 ok、1 FAIL：`plugin-runtime TestLifecycle_*` 三个测试。**与本分支零关联**（`go list -deps ./plugin-runtime/` 零 ursm/migration 依赖；`git diff fafdc60a5..69a526a76 -- plugin-runtime/` 为空；单跑 `TestLifecycle_DeactivateOrdering` PASS）。归类为并行 worktree（`feat/v5-ui01-menu-sync` 大量 plugin-runtime 改动）引入的 flaky，不阻塞 G1 本地门槛。
- `go vet ./...` ✓；`go build ./...` ✓；`scripts/scan-secrets.sh --mode=strict` 0 findings（cmd/gateway 的 `kxpms.cn` 内网域 WARN 已核验为 fafdc60a5 基线既有，非本分支引入）；`scripts/pre-commit-check.sh` PASS(4) SKIP(2)。
- miniredis/sqlmock/不可达地址/SKIP 一律标注替身。

## 双轴与外部审查受限

- L1 双轴已通过（Standards：无硬违规 + 3 判断题已修；Spec (a)-(h) 全过）。
- L2/L3 双轴审查因平台 5 小时使用上限（2026-08-18 19:00 重置）未在本次会话返回；下一次会话应在 `feat/ursm-k2-migration` 分支重跑 Standards + Spec 双轴，固定点 `fafdc60a5`，与本次合并为最终 L2/L3 双轴记录。
- L3 Lua scripts 的真实方言证据（NOSCRIPT、script cache、TTL 精度）属 G4，不在本次范围内；本次仅证明 miniredis 内行为。

## 并行会话冲突（合并前必须裁决）

- **ledger_id 双记录**：主工作树分支 `feat/v5-ui01-menu-sync` 在 doc 14 §0 与本分支 §10 各记录一个；合并时必须二选一并在 changelog 留痕（推荐 `ursm-v2-k2-20260818-001`，与 CLI 默认 `--ledger-id` 一致）。
- **API 命名差异**：并行会话草稿 doc 16（未提交）使用 `NodeKeyCanonical/ParseNodeKeyCanonical/SchemaK2`；本分支 error-returning `K2NodeKeyForTenant/ParseNodeKeyAny/KeySchemaK2/String`。按用户批准计划的命名实现，重命名需协调。
- **工作区错位**：当前主工作树分支 `feat/v5-ui01-menu-sync` 已含 78 文件跨多包改动（含 plugin-runtime 大重构）；L3 阶段一次误提交被回滚并重做于正确 worktree，最终 L3 提交 `d9dc9b203` 仅含 17 个 L3 相关文件。
- **doc 13 §10 移交清单**:本会话新增的 `domains/ursm/v2/migration/` + `cmd/ursm-k2-preflight/` + `sql/migrations/080-*` + `db/db.go` ensure 函数，需在合并 PR 描述中明确 owner = halfking。

## 裁决不变

`M5-0/T0 = BLOCKED / NO-GO`。G1 判定只能由独立审计在合并 + 重跑双轴后按 doc 14 §7 全门槛作出。G4（真实 Redis restart/NOSCRIPT/PubSub、真实 PG lease/fencing、provider/slow-client/chaos）必须独立验证，T8/245 仅 G5 GO 后。