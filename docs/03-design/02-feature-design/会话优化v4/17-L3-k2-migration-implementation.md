# 17 · URSM k2 迁移 L3 实施记录（slice 11-13）

> **状态**：L3 落地（slice 11-13 + CLI）。L1 已落盘（`981c804e8`）→ 16 号文档对齐（`36d959bd3`）→ L3 实施（本会话）。
> **事实基线**：`36d959bd3`（`main` 与 `origin/main` 一致）。
> **边界重申**：`M5-0/T0 = BLOCKED / NO-GO` 仍生效；本会话仅交付纯逻辑 + miniredis 单测 + dry-run CLI，**未触碰生产 Redis**。

## 2 目录结构

新增包 `domains/ursm/v2/migration/`（独立于既有 URSM 实现，仅依赖 14 §0 移交的 `store/keys_k2.go` 导出符号）：

| 文件 | 角色 | 14/15/16 章节 |
|---|---|---|
| `metadata.go` | `Metadata` 与 `Mode`/`Checkpoint` 枚举、JSON 编解码、`Validate()` 校验 | 14 §3 |
| `ledger.go` | NDJSON ledger、确定性 checksum（sorted by source_key）、resume 接口 | 14 §4、15 §2 |
| `classify.go` | `Preflight` SCAN + 四类分类 + 15 §2 round-trip 硬闸 | 14 §4、15 §2 |
| `copy.go` | `Copy` 实施、PTTL 语义（`-2`/`-1`/正值扣减不延长）、resume guard | 14 §6.1 |
| `cleanup.go` | `Cleanup` 限速、可暂停、rollback 保留证据、checksum mismatch 拒绝 | 14 §6.2/§6.3 |
| `metadata_redis.go` | Redis metadata HASH（key=`ursm:v2:mig:k2:meta`）、`CASCheckpoint` Lua | 14 §3 |
| `migration.go` | `Migrator` 顶层结构 + `PreflightDriver` | 15 §4 |

新增 `cmd/k2-migrate-ursm/main.go`：子命令 `preflight|copy|cleanup|status`；**默认 dry-run**；`--apply` 才写。CLI 默认值与 `cmd/migrate-ursm-v2/main.go` 同形态（dry-run 默认、`--apply` 才写、`--force-rollback` 单独开启 cleanup）。

## 3 与 14 §3 metadata 字段对齐

| 字段 | Redis HASH field | 类型 | 实现位置 |
|---|---|---|---|
| `owner` | `owner` | string | `MetadataStore.Write` |
| `ledger_id` | `ledger_id` | string | 同上 |
| `mode` | `mode` | `legacy`/`dual`/`canonical` | 同上 |
| `cutover_epoch` | `cutover_epoch` | 十进制 int64 | 同上 |
| `started_at` / `updated_at` | `started_at` / `updated_at` | RFC3339Nano | 同上 |
| `preflight_checksum` | `preflight_checksum` | sha256 hex | `Preflight.Preflight` 返回值 |
| `rollback_deadline` | `rollback_deadline` | RFC3339Nano | 可选字段，未设则不写 |
| `checkpoint` | `checkpoint` | 8 状态枚举 | `MetadataStore.Write` / `CASCheckpoint` |

metadata HASH key：`ursm:v2:mig:k2:meta`（与既有 `ursm:v2:meta:*` 平级、不冲突；命名登记在 14 §3 ledger 列名下，由本次实施一并冻结）。

## 4 与 14 §4 preflight 对齐

- 四类分类：`migratable` / `canonical_present` / `ambiguous` / `excluded_non_authoritative`。
- 15 §2 round-trip 硬闸：legacy node 必须能用 `legacyNodeKeyForTenant` 重编码为相同字节（package 内实现以避免 import 未移交的 `store.NodeKeyForTenant`），否则 `ambiguous` 且 `reason="round-trip mismatch"`。
- excluded 命名空间：`binding:` / `credential:` / `provider:` / `meta:` / 包含 `:request_dedup:` 的 key 一律 `excluded_non_authoritative`。
- 确定性 checksum：sorted by source_key 后 sha256；测试 `TestPreflightPreflightChecksumStableAcrossRunOrder` 验证 SCAN 顺序差异不影响 checksum。

## 5 与 14 §6.1 TTL/copy 对齐

- `pttl == -2`：source missing，`CopyStatusSkipped`，不写 target。
- `pttl == -1`：target 不设 PEXPIRE（保留无 TTL）。
- `pttl > 0`：target `PEXPIRE = min(scan_snapshot, current)`，绝不延长（测试 `TestCopyDecrementsPositiveTTL` 校验 7000-8000ms 范围且不超过 8000ms）。
- `generation` 不匹配：`CopyStatusRefused`，不写 target。
- `field_checksum` 不匹配：`CopyStatusRefused`，不写 target。
- 已 copied 行的二次 copy：target 字段 checksum 一致 → `CopyStatusUnchanged`，不重写（resume idempotent）。

## 6 与 14 §6.2 cleanup 对齐

- 只删 ledger exact key，且 live `field_checksum` 等于 ledger 记录；否则 `Refused`。
- 限速：`RateLimitPerSec=N`，每秒最多 N 个 key；测试 `TestCleanupRateLimited` 校验 ≥900ms（rate=2, 3 个 key）。
- 可暂停：`ctx.Done()` 取消后返回当前进度。
- rollback：`Mode=legacy` 时无 `--force-rollback` 即拒绝（测试 `TestCleanupRollbackPreservesEvidence`）。
- 不引入 `SCAN ... DEL`，只删 ledger 显式登记的 source key。

## 7 L1 移交与本会话新增的 import 边界

L3 仅 import：

```text
github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store
```

且仅使用 14 §0 移交的导出符号（`NodeKeyCanonical` / `WindowKeyCanonical` / `CandidateIndexKeyCanonical` / `ParseNodeKeyCanonical` / `ParseWindowKeyCanonical` / `ParseCandidateIndexKeyCanonical` / `SchemaK2` / `SchemaLegacy` / `ParsedNodeKey` / `ParsedWindowKey` / `ParsedCandidateIndexKey`）。

未 import 的 URSM 包：`store` 的其余符号（`NodeKeyForTenant` 等既有 API）、`recovery.*`、`persist.*`、`index.*`、`manager.*`、`bootstrap.*`。

CLI（`cmd/k2-migrate-ursm/main.go`）未 import 任何 URSM 包，仅 import `migration` 子包 + `redis/go-redis/v9` + 标准库。

## 8 测试矩阵（miniredis only）

| Slice | 文件 | 测试函数 |
|---|---|---|
| 11 | `classify_test.go` | `TestMetadataValidateAndJSON`、`TestPreflightClassifiesLegacyNodeKeyAsMigratable`、`TestPreflightRejectsAmbiguousCollisionPair`、`TestPreflightDetectsCanonicalPresentConflict`、`TestPreflightPreflightChecksumStableAcrossRunOrder`、`TestFieldChecksumStable`、`TestIsExcludedNamespace`、`TestLegacyNodeKeyForTenantExactBytes` |
| 12 | `copy_test.go` | `TestCopySkipsMissingTTLMinus2`、`TestCopyPreservesNoTTLMinus1`、`TestCopyDecrementsPositiveTTL`、`TestCopyFencedByGenerationMismatch`、`TestCopyIdempotentResume` |
| 13 | `cleanup_test.go` | `TestCleanupDeletesOnlyLedgerKeys`、`TestCleanupRollbackPreservesEvidence`、`TestCleanupRateLimited`、`TestCleanupChecksumMismatchRefuses` |

共 17 个测试函数，全部 PASS（`go test -count=1 ./domains/ursm/v2/migration/...` 输出 `ok ... 0.456s`，含 `TestCopyIdempotentResume`/`TestCleanupRateLimited` 的 1-2s 时延测试）。

## 9 G1 本地门禁（本会话范围内）

- `go build ./domains/ursm/v2/migration/...` → PASS
- `go build ./cmd/k2-migrate-ursm/...` → PASS
- `go test -count=1 ./domains/ursm/v2/migration/...` → `ok ... 0.456s`
- `go vet ./domains/ursm/v2/migration/...` → PASS
- `go vet ./cmd/k2-migrate-ursm/...` → PASS

未跑 `go build ./...`（并发会话 WIP 中 `plugin-runtime/supervisor.go` 的 `errors` import 未使用导致本地构建红，与 k2 无关，遵守交接约定不触碰）。

## 10 G4 留待项（不在本会话范围）

- 真实 Redis 上的 preflight/copy/cleanup（含 `NOSCRIPT`/`PExpire` 精度/PTTL 实时扣减）。
- 真实 PG 路径上的 metadata HASH 并发 CAS 验证（`CASCheckpoint` Lua 仅在 miniredis 测过）。
- Lua 双写 atomic（slice 6-10 L2 的 schema-aware store/recovery 移交后再补）。
- 双轴 review / pre-commit / scoped secret scan（按 16 号 §5）。

## 11 变更记录

- 2026-08-18：L3 slice 11-13 落地（`domains/ursm/v2/migration/` + `cmd/k2-migrate-ursm/main.go`），17 个测试函数全部 PASS，CLI 默认 dry-run。