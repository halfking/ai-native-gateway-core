# 2026-08-20 — stats: tenant-prefix personHash to prevent cross-tenant collision (P2-1)

## 做了什么

修复 `domains/stats/event.go#personHash` 的跨租户串号风险。原先
`personHash(owner, endUser)` 只对 `end_user_id`（无则回退
`api_key_owner_user`）做 SHA-256 取前 8 字节，结果是任意两个租户只要
`end_user_id` 字符串相同，就会产生完全相同的 `person_hash`。

下游 `stats_usage_daily` 和 `stats_usage_monthly` 的主键已经包含
`tenant_id`，所以投影表层面不会撞行；问题在于 `usage_facts.person_hash`
与 `tenant_id` 同行存储，未来如果有任何聚合按 `person_hash` 维度但不
GROUP BY `tenant_id`，就会把跨租户数据悄悄合并。

## 改动清单

| 文件 | 说明 |
|---|---|
| `domains/stats/event.go` | `personHash` 签名扩展为 `personHash(tenant, owner, endUser)`，哈希输入由原 `value` 改为 `tenant + ":" + value`；空 `tenant` 短路返回 `""`（与「无 end_user」行为对齐） |
| `domains/stats/event.go`（call site） | `requestEntryToEvent` 内的单点调用传入已在 scope 中的 `tenant` 局部变量 |
| `domains/stats/event_test.go` | 新增 `TestPersonHash` 与 5 个子测试：跨租户碰撞、确定性、fallback 路径、空 tenant、全空 |

## 为什么这样做

- **`tenant_id` 必须参与哈希**：`person_hash` 的本质语义是「同一个
  人在同一租户下」的稳定标识。如果不掺 `tenant_id`，不同租户的
  「alice」会得到同一个 hash，破坏 dimension_type='person' 的租户隔离。
- **空 tenant 短路**：`EventFromTelemetry` 在 entry 无 `tenant_id` 时
  会回退到 `"default"`（event.go:125-127），所以 call site 永远拿到非空
  tenant。但函数本身仍保留空 tenant 短路防御，作为对调用方的契约：
  「没有 tenant 就不要伪造 hash」。
- **forward-only，不回填**：`usage_facts.person_hash` 历史行不会被重新
  计算。投影表（`stats_usage_*`）的主键包含 `tenant_id`，实际查询不会
  受影响；只有那些裸用 `person_hash` 做 GROUP BY 的 ad-hoc 查询会在
  部署边界出现不连续，本质上是把已经存在的串号风险一次性纠正。

## 验证结果

| 项 | 结果 |
|---|---|
| `go build ./...` | ✅ |
| `go vet ./...` | ✅ |
| `go test -race ./domains/stats/... ./admin/... ./metrics/...` | ✅ |
| `TestPersonHash/cross-tenant same end_user produces different hashes` | ✅ 跨租户 alice hash 不同 |
| `TestPersonHash/same tenant same end_user is deterministic` | ✅ 两次调用相等 |
| `TestPersonHash/fallback path uses owner value but still tenant-prefixed` | ✅ endUser 空时回退 owner 且保留 tenant 前缀 |
| `TestPersonHash/empty tenant returns empty string` | ✅ |
| `TestPersonHash/empty everything returns empty string` | ✅ |

部署后（154）观察：

| 指标 | 预期 |
|---|---|
| `usage_facts.person_hash` 分布（按 `tenant_id` 切片） | 部署后不再出现跨租户相同 hash |
| `stats_usage_daily` / `stats_usage_monthly` 投影 | 与之前一致（主键含 tenant_id） |

## 部署流程

无需 migration，无需 schema 变更。镜像滚动即可。

## 提交链

```
fix(stats): tenant-prefix personHash to prevent cross-tenant collision (P2-1)
  ├── domains/stats/event.go (personHash 签名 + 实现 + call site)
  ├── domains/stats/event_test.go (TestPersonHash + 5 子测试)
  └── docs/changelogs/2026-08-20-stats-personhash-tenant-scope.md
```

## 遗留与风险

- ⚠️ **历史 `usage_facts.person_hash` 不会被回填**：这是 forward-only 变更。
  任何依赖裸 `person_hash` 做跨周期对比的查询会出现部署边界的不连续；
  推荐所有相关查询都 `GROUP BY tenant_id, person_hash`。
- ⚠️ **runbook 未单独维护 `person_hash` 节**：当前 runbook 关注 reconciliation
  / approval / inbox consumer 三大流程，没有 person_hash 专章。本次不
  强造章节，避免与既有结构冲突；后续若加入 ad-hoc `person_hash` 查询，
  再补 runbook 节。
- ✅ **后续清理 task**：
  1. 若 `usage_facts.person_hash` 上线后真有 ad-hoc 跨租户聚合需求，考虑
     单独跑一次性 backfill（`UPDATE usage_facts SET person_hash = encode(
     sha256(tenant_id || ':' || coalesce(end_user_id, owner_user))::bytea,
     'hex')::text` 之类），需要独立 PR 评估窗口。
