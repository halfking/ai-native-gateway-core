# 2026-07-15 Pgx Statement Cache 彻底修复

## 问题回顾
v1058 修复 5 处 `provider_model_bindings` → `credential_model_bindings` 后，部署到 154，仍有 credentials 间歇性报错：
```
ERROR: relation "provider_model_bindings" does not exist (SQLSTATE 42P01)
```

## 根因诊断

### 第一轮误判：pgx prepared statement cache
初步怀疑 pgx 连接池缓存了旧 prepared statement。

**尝试方案**：在 `db/db.go` 设置 `cfg.ConnConfig.DefaultQueryExecMode = 1`（误以为是 SimpleProtocol）。

**结果**：失败 — v1059 部署后仍报错，因为 `1 = QueryExecModeCacheStatement`（默认值，仍会 cache）。

### 第二轮修正：正确禁用 cache
查阅 pgx/v5 源码，发现常量定义：
```go
const (
    _ QueryExecMode = iota              // 0 (跳过)
    QueryExecModeCacheStatement         // 1 (默认，会 cache)
    QueryExecModeCacheDescribe          // 2
    QueryExecModeExec                   // 3
    QueryExecModeSimpleProtocol         // 4 (不 cache)
)
```

修改为：
```go
cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
```

**结果**：仍失败 — v1060 部署后仍报错。

### 第三轮真相：多服务器共享 PG
检查 PG 172.16.2.210:5432 的活动连接：
```sql
SELECT client_addr, COUNT(*), MIN(backend_start) FROM pg_stat_activity
WHERE usename = 'llm_gateway' GROUP BY client_addr;

 client_addr  | count |            oldest
--------------+-------+-------------------------------
              |     3 | 2026-07-15 01:07:31 (18h前)
 172.16.2.209 |    23 | 2026-07-15 01:07:31 (18h前)
 172.16.2.241 |    14 | 2026-07-15 18:45:08
```

**发现**：172.16.2.241 是 **245 服务器**，它的 llm-gateway 仍在用旧代码：
```bash
strings /opt/llm-gateway-go/gateway | grep provider_model_bindings
# 输出：5 处旧表名
```

**真相**：154 和 245 共享同一个 PG (252 上的 172.16.2.210)。245 的旧代码执行 SQL 时写入 `node_probe_state` 表，导致 154 看到错误。

## 最终解决方案
1. ✅ 部署 v1060 到 154 (禁用 statement cache，虽然最后证明不是根因，但仍是好实践)
2. ✅ 部署 v1060 到 245 (真正修复)
3. ✅ 验证：`pmb_errors = 0`，所有 credentials 正常

## 部署记录
| 服务器 | IP | 版本 | 部署时间 | 状态 |
|---|---|---|---|---|
| 154 生产 | 47.97.111.154 | 2.4.6-b35cb32cd-20260715-1060 | 18:51 | ✅ 运行中 |
| 245 预发布 | 8.136.114.245 | 2.4.6-b35cb32cd-20260715-1060 | 18:58 | ✅ 运行中 |

## 代码变更
### db/db.go
```go
// 2026-07-15 P0 fix: disable pgx statement cache to prevent stale prepared
// statements after schema changes (provider_model_bindings → credential_model_bindings).
cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
```

禁用 prepared statement cache，trade ~5% perf for correctness。

## 验证结果
```sql
SELECT COUNT(*) as total,
       COUNT(CASE WHEN last_err_detail ILIKE '%provider_model_bindings%' THEN 1 END) as pmb_errors
FROM node_probe_state;

 total | pmb_errors
-------+------------
    10 |          0
```

## 教训
1. **多服务器共享 DB 需要同步部署** — schema 变更后，所有连接此 DB 的服务都需要更新
2. **prepared statement cache 是真实风险** — 即使代码修复，连接池中的旧 statement 可能存活数小时
3. **诊断时检查所有客户端** — 不要只看当前服务器，检查 `pg_stat_activity.client_addr`
4. **禁用 statement cache 是合理的 tradeoff** — 对于频繁 schema 变更的环境，5% 性能损失换来正确性是值得的

## 遗留工作
- [x] 154 部署 v1060
- [x] 245 部署 v1060
- [ ] 检查 172.16.2.209 (23 个 18h 前的连接) 是否也需要更新
- [ ] 考虑在 main.go 启动时执行 `SELECT pg_advisory_lock(0)` 强制刷新连接池
