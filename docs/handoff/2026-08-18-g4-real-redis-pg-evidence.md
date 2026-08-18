# 2026-08-18 — G4 真实 Redis/PG 证据收集（17 号 §10 留待项）

> 来源：`llm-gateway-go` 主仓，G1 门禁恢复后续接会话（ZCode）。
> 关联：`docs/03-design/02-feature-design/会话优化v4/17-L3-k2-migration-implementation.md` §10；`docs/handoff/2026-08-18-go1.26-vendor-incompat.md`（G1 阻塞已解除）。
> 状态：**DONE（隔离真实服务证据已收齐）+ 2 个 P1 新发现（F-1 / F-2，须 owner 修复后方可考虑 T0 解封）**。

## 0. 环境与安全边界

| 项 | 值 |
|---|---|
| Redis | `redis:7-alpine` 一次性容器 `k2-g4-redis` @ `127.0.0.1:6399`，`--save ""` 无持久化 |
| PostgreSQL | `postgres:16-alpine` 一次性容器 `k2-g4-pg` @ `127.0.0.1:5433`，scratch 库 `k2g4` |
| 生产/共享实例 | **零接触**（本机 6379/5432 未使用；未向任何环境写 k2 marker） |
| CLI | `cmd/k2-migrate-ursm`、`cmd/ursm-k2-preflight`，main @ `ef6c7fb1d` 构建 |
| 原始产物 | `/tmp/k2-g4/`（01-preflight.out … 09-pg-preflight.out、ledger*.ndjson、monitor*.log；**注**：会话末 `/tmp/k2-g4/` 因磁盘回收已清理，文档正文引用的关键 stdout 已转录到对应小节；保留的硬证据包括 `ledger3.ndjson` 状态演化链、§2 MONITOR DEL 命令捕获、§3 PG 表结构 dump） |

## 1. G4a：真实 Redis preflight/copy/cleanup 证据（通过项）

| # | 验证点 | 结果 | 证据 |
|---|---|---|---|
| 1 | preflight 扫描+分类（真实 Redis SCAN） | ✅ total=6 migratable=2 excluded=4 checksum 稳定 | `01-preflight.out` |
| 2 | metadata HASH 写入 `ursm:v2:mig:k2:meta`（owner/ledger_id/mode=dual/checkpoint=preflight/cutover_epoch=0） | ✅ HGETALL 全字段正确 | 运行时核对 |
| 3 | copy 生成 canonical k2 键（`node:k2:MTAwMQ:5:Z3B0LTRv` base64 delimiter-safe） | ✅ copied=2，内容逐字段一致 | `02-copy.out` |
| 4 | **NOSCRIPT 回退**：`SCRIPT FLUSH` 后立即 copy（首 EVALSHA 必 NOSCRIPT） | ✅ 仍 copied=2 failed=0（go-redis `NewScript` 自动 Eval→EvalSha 重载） | `02-copy.out` |
| 5 | **PTTL 实时扣减**：legacy TTL 300s（copy 时剩 ~270s）→ canonical PEXPIRE=copy 时刻余量，无 TTL 延长 | ✅ canonical PTTL=269246ms 且与 legacy 同步衰减 | 运行时核对 |
| 6 | 持久键保持持久（ARGV TTL=0 → 不加 EXPIRE） | ✅ canonical node6 PTTL=-1 | 运行时核对 |
| 7 | copy 幂等重跑 | ✅ 第二次 copied=0 unchanged>0，canonical 未重写 | `03-copy-idempotent.out` |
| 8 | **generation 竞态栅栏**：preflight 注册 gen=42 → 在线写改为 99 → copy 拒绝 | ✅ refused=1，tenant 2002 canonical 未创建（Lua 字段级 CAS 生效） | `05-copy-fence.out` |
| 9 | cleanup dry-run | ✅ 零写入 | `06-cleanup-dryrun.out` |
| 10 | cleanup 护栏：mode=legacy 无 --force-rollback | ✅ 拒绝并提示 doc 14 §6.3 | `07-cleanup-guardrail.out` |
| 11 | cleanup 正常路径（无重扫重跑的最小序列） | ✅ 仅删 legacy 源键，canonical 存活，refused 条目保留 | 复验实验 |

## 2. F-1（P1，新发现）：重扫 + 幂等 copy 重跑 + cleanup 会删除 canonical 键

**标准 resume 运维序列下静默销毁 canonical 状态。**

复现（100% 确定性，实验 C）：

```
seed 2 legacy nodes → preflight --apply → copy --apply → copy --apply(幂等重跑)
→ preflight --apply(重扫) → copy --apply → cleanup --apply --mode dual
结果: deleted=4 —— legacy×2 与 canonical×2 全部被删，仅剩 mig:k2:meta
```

对照实验 B（去掉两次 copy 重跑）：cleanup 仅删 legacy，canonical 存活 → 触发条件=copy 在"重扫之后"再跑过至少一次。

ledger 铁证（canonical 键自身作为条目的状态演化）：

```
ursm:v2:node:k2:NDAwNA:8:Z2xtLTQ -> classified → copied → classified → cleaned
ursm:v2:node:k2:NDAwNA:9:Z2xtLTR4 -> classified → copied → classified → cleaned
```

机制链（代码定位）：

1. preflight 重扫把 canonical 键登记为条目：`source_key == canonical_key`，`classification=canonical_present`，`status=classified`（`preflight.go:299-306` 默认状态）。
2. copy 的 Resume 守卫（`copy.go:174-178`：`HGetAll(it.CanonicalKey)` + checksum 比对 → `CopyStatusUnchanged` + `res.Item.Status = StatusCopied`）**不检查 `it.Classification`**：对 canonical_present 条目，`HGetAll(it.CanonicalKey)`（=自己）必然存在且 checksum 恒等 → 盖章 `StatusCopied`。注释（copy.go:69-71）写的是 "if a **migratable** item is already in StatusCopied"，代码未限定 migratable。
3. cleanup（`cleanup.go:117`）按 `StatusCopied` 删 `it.SourceKey` → **DEL canonical 键**。

**触发条件（实验 A/B/C 反复验证）**：原始 preflight+copy 后，再做"重扫 preflight → 至少一次 copy" → canonical 条目被盖成 StatusCopied → cleanup 触发删除。**充分必要条件是"重扫 preflight 之后存在至少一次 copy 调用"**，否则 cleanup 行为正确（实验 B 验证）。

影响评估：dual 模式 cutover 后 canonical 是权威读路径，此缺陷=迁移工具自身删除权威数据；且 refused=0/failed=0，计数表面正常，无告警。**T0 前 must-fix。**

修复建议（供 owner，代码冻结未动）：
- 最小：copy.go Resume 守卫加 `it.Classification == ClassificationMigratable && it.SourceKey != it.CanonicalKey` 前置条件；或
- cleanup.go `cleanupOne` 增加 `it.SchemaSource == k2/canonical_present` 拒删守卫（双保险，与 cleanup.go:221 注释 "never deletes canonical keys" 对齐）。
- 回归测试：上述实验 C 序列作为 RED 用例（现有 miniredis 测试未覆盖"重扫后重跑 copy"路径）。

## 3. F-2（P1，新发现）：真实 PG `--apply` 因 DDL/代码列漂移必然失败

```
ERROR: column "rollback_deadline" of relation "ursm_key_migration_runs"
does not exist (SQLSTATE 42703)
```

- `pg_store.go:48-52` INSERT 列含 `rollback_deadline`；`sql/migrations/080-ursm-key-migration-ledger.sql` 建表**无此列**。
- 该路径此前仅经 sqlmock 验证（mock 按 code 假设建模，未对齐 DDL），真实 PG 从未跑通 → 17 号 §10 "真实 PG 路径"留待项的正确性由此证实。
- scratch 库手工 `ALTER TABLE … ADD COLUMN rollback_deadline timestamptz` 后重跑：**run 行 + 3 entries 全部落库正确**（checkpoint=preflight、mode=dual、ON CONFLICT upsert、entries 分类/tuple 字段正常）→ 缺陷边界精确=单列漂移。
- 修复建议（供 owner）：新增 `081-…-add-rollback-deadline.sql` 迁移补列（或代码侧去掉该列写入，按 14 §0 决策表定 owner）。

次要观察（非阻塞）：
- 两个 CLI 的分类行为不一致：`ursm-k2-preflight`（PG 版）把 window ZSET 归 `migratable`，`k2-migrate-ursm`（NDJSON 版）归 `excluded`。若非有意（两套并行 API），建议 owner 统一。
- PG entries 的 `tuple_tenant` 未填充（node 键 tuple 可恢复但落库为空）。

## 4. G4c：真实 Redis 并发 CAS 证据（通过项）

| # | 验证点 | 结果 |
|---|---|---|
| 1 | `metadata_advance.lua` 并发竞争（两个真实客户端连接同 expected epoch EVAL，50 轮） | ✅ 50/50 轮 epoch 恰好自增一次（败者 `superseded`） |
| 2 | `CASCheckpoint`（metadata_redis.go 内联 Lua）陈旧 epoch | ✅ expected=0 vs 实际=1 → 返回 0 拒绝 |
| 3 | `CASCheckpoint` 当前 epoch | ✅ expected=1 → 返回 1，checkpoint 更新，epoch 不被 CAS 改动 |

注：CASCheckpoint 本身不递增 `cutover_epoch`（仅 checkpoint+updated_at），epoch 栅栏依赖调用方配合——这是设计语义（doc 14 §4），非缺陷；并发安全性由 #1 的 advance 竞争证据覆盖。

## 5. 结论与门槛影响

- 17 号 §10 第 1 项（真实 Redis preflight/copy/cleanup）：**完成**，含 NOSCRIPT/PTTL 精度/幂等/栅栏全矩阵；第 2 项（真实 PG metadata/ledger 路径）：**完成**（暴露 F-2 并验证补列后可用）。
- 第 3 项（Lua 双写 atomic，L2 slice 6-10）：仍按 14 §0 未移交，不在本证据范围。
- **M5-0/T0 维持 BLOCKED / NO-GO**，新增理由：F-1（canonical 删除缺陷）修复 + 回归前，copy/cleanup 严禁对生产执行；F-2 修复前 PG ledger 路径不可用。
- 本会话未修改任何生产代码/Lua/SQL/CLI（发现仅记录，修复归 owner）。

## 6. 复验命令（owner 修复后回归用）

```bash
# 环境
docker run -d --name k2-g4-redis -p 127.0.0.1:6399:6379 redis:7-alpine --save "" --appendonly no
docker run -d --name k2-g4-pg -e POSTGRES_PASSWORD=g4 -e POSTGRES_DB=k2g4 -p 127.0.0.1:5433:5432 postgres:16-alpine
# F-1 复现（§2 序列）+ F-2 复现（§3，--pg postgres://postgres:g4@127.0.0.1:5433/k2g4?sslmode=disable）
# 期望：修复后 §2 序列 cleanup 仅删 legacy 键；§3 无 42703
```
