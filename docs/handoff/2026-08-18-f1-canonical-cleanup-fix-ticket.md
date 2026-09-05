# F-1 修复 Ticket：禁止重扫后的 copy/cleanup 删除 canonical 键

- **Finding**：F-1
- **优先级**：P1（M5-0/T0 解封前置；数据安全）
- **状态**：RESOLVED — owner 修复 + 隔离 Redis 回归已完成（2026-08-18）
- **Owner**：k2 migration owner（按 14 §0；当前具名 migration owner：`halfking`）
- **发现基线**：`main` @ `ccf3929b9`（G4 真实 Redis/PG 证据已合并）
- **证据**：`docs/handoff/2026-08-18-g4-real-redis-pg-evidence.md` §2、§6

## 摘要

在标准 resume 运维序列中，重扫 `preflight` 会把已经存在的 canonical 键登记为 `classification=canonical_present`，且 `source_key == canonical_key`。随后再次执行 `copy --apply` 时，`copy.go` 的 Resume 守卫只比较目标 checksum，不检查分类；由于目标就是条目自身，checksum 必然相同，条目被盖为 `StatusCopied`。`cleanup --apply --mode dual` 按 `StatusCopied` 删除 `SourceKey`，从而删除 canonical 权威键。

这是静默数据删除：实验 C 中 `deleted=4`，legacy×2 与 canonical×2 全部删除，仅剩 metadata；`refused=0/failed=0`，表面计数正常。实验 B 证明，触发条件是“重扫 preflight 之后至少再执行一次 copy”。M5-0/T0 在修复和回归前保持 **NO-GO**，禁止对任何真实环境执行 copy/cleanup。

## 影响范围

- `domains/ursm/v2/migration/copy.go:101-178`
- `domains/ursm/v2/migration/cleanup.go:111-157`
- `domains/ursm/v2/migration/copy_test.go`
- `domains/ursm/v2/migration/cleanup_test.go`
- 如实现需要，可补充共享分类/ledger 测试；不得擅自扩大到未移交文件。

## 确定性复现（RED）

使用 G4 文档 §6 的隔离真实 Redis 序列，不接触生产或共享实例：

```text
seed 2 legacy nodes
→ preflight --apply
→ copy --apply
→ copy --apply                 # 幂等重跑
→ preflight --apply             # 重扫，把 canonical 登记为 canonical_present
→ copy --apply                  # 触发缺陷：canonical 条目被盖为 copied
→ cleanup --apply --mode dual
```

当前预期（修复前）：

- `deleted=4`；legacy×2 和 canonical×2 都不存在；
- ledger canonical 条目状态：`classified → copied → classified → cleaned`。

对照：去掉“重扫后 copy”时，cleanup 仅删除 legacy，canonical 保留。

## 修复要求

1. **Copy 侧硬限制**：Resume 守卫只有在条目确实为 `ClassificationMigratable` 且 `SourceKey != CanonicalKey` 时，才可把目标 checksum 相同解释为已完成并设置 `StatusCopied`。建议直接在 `copy.go:174-178` 的状态提升前增加该前置条件。
2. **Cleanup 侧双保险**：`cleanupOne` 及其等价 per-entry 删除入口拒绝删除 `canonical_present` 条目，至少拒绝 `SourceKey == CanonicalKey`；同时与 `cleanup.go` 中“never deletes canonical keys”的契约保持一致。拒绝应产生可审计的 skipped/refused 结果，不得伪装成 deleted。
3. **保持既有安全语义**：不得放宽 generation/checksum/PTTL 栅栏、exact-key 删除、dry-run、mode guard 或 rollback 保护；不得用 broad `SCAN+DEL` 替代 ledger exact-key 删除。
4. **状态不可污染**：canonical 条目不得因 copy 被提升到 `StatusCopied`；修复后 cleanup 不得将 canonical 条目标记为 `cleaned`。

## TDD / 回归验收

先将上述实验 C 序列固化为 RED 回归用例，再实施最小修复；同时保留实验 B 作为对照，证明正常 cleanup 仍会删除 legacy source。

必须通过：

- 实验 C 完整序列：cleanup 只删除 legacy×2，canonical×2 仍存在；不得出现 canonical `StatusCopied` / `StatusCleaned`。
- 实验 B 对照序列：无重扫后 copy 时，legacy 仍按既有规则清理，canonical 保留。
- 现有 `copy`、`cleanup`、generation fence、checksum mismatch、TTL、幂等和 dry-run 测试全部通过。
- 隔离真实 Redis（Redis 7）按 G4 §6 重跑，结果与上述断言一致；不得向生产/共享 Redis 写入 marker。
- 回归输出须记录 `deleted`、`refused`、`failed`、canonical key 存在性和 ledger 状态演化。

建议验证命令（owner 环境）：

```bash
go test ./domains/ursm/v2/migration -run 'Test(Copy|Cleanup)' -count=1 -v
# 再按 docs/handoff/2026-08-18-g4-real-redis-pg-evidence.md §6 启动隔离 Redis，执行实验 B/C
```

## 修复与复验（2026-08-18）

- 修复将 ledger-driven `Copy` 收紧为只处理 `ClassificationMigratable`，且在所有 Redis 读写前拒绝空 source/canonical key 与 `SourceKey == CanonicalKey`。因此 canonical-present 自目标既不会被 Resume guard 提升为 `copied`，也不会落入 `DEL/HSET` pipeline。
- `Cleanup.cleanupOne` 对 `canonical_present` 与 self-target 条目返回可审计的 `preserved`，保留历史 ledger 中已污染的 `StatusCopied` 行而不删除其 canonical key；`EntryCleaner.DeleteExact` 同样拒绝 self-target。
- 隔离 `redis:7-alpine` 的完整实验 C 已复验：`preflight → copy → copy → rescan preflight → copy → cleanup` 产生 `copy skipped=1` 和 `cleanup deleted=1 preserved=2`；legacy source 被删除，canonical key 存活，canonical ledger 行保持 `classified`。该实验未接触生产或共享 Redis。
- 自动回归：miniredis 覆盖 canonical self-target copy、历史 `StatusCopied` cleanup、parallel cleaner self-target 与完整 rescan 序列；`go test -count=1 ./...` 通过。


- owner 提交代码、测试和变更说明；不得由本 ticket 执行生产修复。
- fix-review 对 owner commit 做差分审查，确认 copy 与 cleanup 两条删除路径均有 canonical 拒绝保护，且无既有栅栏回退。
- G4 隔离真实 Redis 实验 C 通过后，才可将 F-1 标记 RESOLVED。
- F-1 RESOLVED 之前，M5-0/T0 继续 NO-GO；不得对真实环境执行 k2 copy/cleanup。

## 非本 Ticket 范围

- 两 CLI window 分类差异；
- `entries.tuple_tenant` 空值；
- PG ledger DDL/owner 权限问题（见 F-2 及后续 owner ticket）；
- G1 构建缓存预防措施。
