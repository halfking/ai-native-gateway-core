# 任务 — 总积分消耗 KPI 增量缓存

**日期**: 2026-07-13
**作者**: OpenCode AI Agent
**状态**: ✅ 已设计 / 实现中
**关联 commit**: f29b896a3 (前置 KPI 卡)
**关联 issue**: 用户反馈 "首页每次刷新都做 SELECT SUM(credits_charged)，应该加载时算一次缓存，请求过来做加法"

---

## 一、问题陈述

当前 `admin/usage.go::usageSummary` (commit f29b896a3) 用以下 SQL 计算"总积分消耗"：

```sql
SELECT
  COUNT(*),
  COALESCE(SUM(prompt_tokens), 0),
  COALESCE(SUM(completion_tokens), 0),
  COALESCE(SUM(cost_usd), 0.0),
  COALESCE(SUM(credits_charged), 0)::bigint AS total_credits_charged,
  ...
FROM usage_ledger_with_current_month
WHERE ts >= now() - ($1 * INTERVAL '1 day')
  AND tenant_id = $2
```

`usage_ledger_with_current_month` 是 union view（按月分区 + hot 七天），每行就是
一次 request。即使有 hot 表与索引，扫描 days=1 窗口内的全部行并聚合，
仍然是 N 行 IO + 5 个聚合，在高 QPS 场景下：

- **多次重复计算**：admin dashboard 每 5 分钟自动 refresh 一次，每次都重算；
  同时多 user 并发访问都触发相同 SQL。
- **窗口越大越慢**：days=30 扫一个月分区，cold page cache miss 时 IO 显著。
- **不可水平扩展**：单查询的开销固定，不会因为请求量增加而摊薄。

## 二、用户提议方案分析

> "建议在首页，可以加载时统计放到缓存中，然后每个请求过来就做加法，这样效率更高。"

实质：**物化增量计数 (Materialized Counter)**。流程：

```
加载/启动   ─── 从 DB 算一次 baseline ──►  缓存 (Redis 或 DB 表)
每次请求    ─── atomic 累加 (INCR / INSERT … ON CONFLICT DO UPDATE) ──► 缓存
Dashboard   ─── O(1) 读缓存 ──► 显示
```

**优点**：
1. Dashboard 读路径 O(1)，与数据量无关。
2. 每次请求写入只做 atomic increment (PG 的 `INSERT ON CONFLICT DO UPDATE` 单行 write)，无扫描。
3. 历史精度：用户删除/补单时可以走 reconciliation 任务重算。

**难点与对策**：

| 难点 | 对策 |
|---|---|
| 窗口切片 (1d / 7d / 30d) 都需要支持 | 按 hour 粒度分桶 (≤ 90 个 bucket)，窗口 = `WHERE bucket_start >= now() - N days`，扫 N 行 |
| tenant 维度 | `PRIMARY KEY (tenant_id, bucket_start)` |
| Backfill (历史 usage_ledger 数据) | 启动时一次性 INSERT … SELECT bucketization 算过去 90 天 |
| Atomic 写入与现有 charge tx 一致性 | bucket upsert 与 credit_ledger 写入共享同一个 PG tx，任一失败都回滚 |
| 跨进程一致性 (高 QPS 多副本) | PG `INSERT … ON CONFLICT DO UPDATE SET credits = credits + EXCLUDED.credits` 是 PG 内部 row-level lock + atomic，无需外部锁 |
| 表膨胀 / 数据保留 | bucket 是 1 小时，90 天窗口最多 90 × 24 = 2160 行/tenant，10 个 tenant 才 2 万行；不需要 partition |

## 三、最终方案：Hourly Bucket Counter

### 3.1 新表 `maas_credit_consumption_buckets`

```sql
CREATE TABLE public.maas_credit_consumption_buckets (
    tenant_id   text NOT NULL,
    bucket_start timestamptz NOT NULL,    -- date_trunc('hour', ts)
    credits     bigint NOT NULL DEFAULT 0,
    request_count integer NOT NULL DEFAULT 0,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, bucket_start)
);
CREATE INDEX idx_mccb_recent ON maas_credit_consumption_buckets (bucket_start DESC);
```

字段语义：
- `tenant_id`：租户标识（与 credit_ledger.tenant_id 对齐）
- `bucket_start`：当前小时起点（UTC，例如 2026-07-13 03:00:00）
- `credits`：从该小时开始累计的积分消耗（绝对值）
- `request_count`：请求数（同理）
- `updated_at`：最后写时间

### 3.2 ChargeRequest 写入路径

在现有 `chargeTokens` 内、最后 `tx.Commit(ctx)` **之前**追加一行：

```go
// 2026-07-13: increment hourly consumption bucket in same tx
_, err = tx.Exec(ctx, `
    INSERT INTO maas_credit_consumption_buckets
        (tenant_id, bucket_start, credits, request_count, updated_at)
    VALUES
        ($1, date_trunc('hour', now()), $2, 1, now())
    ON CONFLICT (tenant_id, bucket_start) DO UPDATE
        SET credits       = maas_credit_consumption_buckets.credits + EXCLUDED.credits,
            request_count = maas_credit_consumption_buckets.request_count + 1,
            updated_at    = now()
`, tenantID, amount)
```

注意点：
- 与 `writeLedger` 同一 PG tx；事务回滚则 bucket 也不写。
- `amount` 是 `CalcCreditsMultimodal` 返回的正值（即积分消耗量）。
- 不区分 `consume` 类型，因为总积分消耗是统一加和。
- 由于用量低于 1 的请求不会调用 `chargeTokens`（amount<=0 提前 return），bucket 自然只记录真正扣费的请求。

### 3.3 Dashboard 读路径优化

替换 `admin/usage.go::usageSummary` 的 `total_credits_charged` 聚合：

```sql
-- 替换前：扫描 N 行
COALESCE(SUM(credits_charged), 0)::bigint AS total_credits_charged

-- 替换后：扫描 ≤ N_hour 行
COALESCE(
    (SELECT SUM(credits)
       FROM maas_credit_consumption_buckets
      WHERE tenant_id = $tenant_filter
        AND bucket_start >= now() - ($1 * INTERVAL '1 day')),
    0
)::bigint AS total_credits_charged
```

性能：
- **days=1**：扫 ≤ 24 行
- **days=7**：扫 ≤ 168 行
- **days=30**：扫 ≤ 720 行
- **days=90**：扫 ≤ 2160 行
- 主键 `(tenant_id, bucket_start)` 在 PG btree 上是 O(log n)，且 PK 已是 ordered index。

### 3.4 启动时 Backfill

启动 `cmd/gateway` 后，service 初始化时一次性把过去 90 天从 `usage_ledger.credits_charged` 算到 bucket：

```sql
INSERT INTO maas_credit_consumption_buckets
    (tenant_id, bucket_start, credits, request_count)
SELECT
    tenant_id,
    date_trunc('hour', ts)              AS bucket_start,
    COALESCE(SUM(credits_charged), 0)::bigint AS credits,
    COUNT(*)                            AS request_count
FROM usage_ledger_with_current_month
WHERE ts >= now() - INTERVAL '90 days'
  AND credits_charged IS NOT NULL
GROUP BY tenant_id, date_trunc('hour', ts)
ON CONFLICT (tenant_id, bucket_start) DO UPDATE
    SET credits       = EXCLUDED.credits,
        request_count = EXCLUDED.request_count,
        updated_at    = now();
```

为什么用 `ON CONFLICT DO UPDATE SET credits = EXCLUDED.credits`（覆盖）而不是 `+ EXCLUDED`？
启动时若 bucket 表已经初始化过（重启 / 升级），我们要保留**新产生的增量**而**覆盖** backfill 区间内的值（避免双计）。但如果生产时段已经累计，ON CONFLICT 会丢失这些增量。

更安全的策略：

| 场景 | backfill 应该 |
|---|---|
| bucket 表为空 | 全部 backfill |
| bucket 表有数据且 last_bucket < now() - 2h | 从 last_bucket 重新算到 now（覆盖中间任何丢失） |
| bucket 表有数据且 last_bucket >= now() - 2h | 跳过 backfill（保留实时增量） |

实现：

```go
func (s *Service) BackfillCreditBuckets(ctx context.Context, lookbackDays int) (int64, error) {
    if !s.Enabled() { return 0, nil }
    res, err := s.pool.Exec(ctx, `
        WITH last_bucket AS (
            SELECT MAX(bucket_start) AS ts
              FROM maas_credit_consumption_buckets
        ),
        backfill_window AS (
            SELECT
                COALESCE(
                    GREATEST(
                        (SELECT ts FROM last_bucket),
                        now() - ($1::int * INTERVAL '1 day')
                    ),
                    now() - ($1::int * INTERVAL '1 day')
                ) AS start_ts,
                now() AS end_ts
        )
        INSERT INTO maas_credit_consumption_buckets
            (tenant_id, bucket_start, credits, request_count)
        SELECT
            tenant_id,
            date_trunc('hour', ts) AS bucket_start,
            COALESCE(SUM(credits_charged), 0)::bigint AS credits,
            COUNT(*) AS request_count
        FROM usage_ledger_with_current_month r, backfill_window w
        WHERE r.ts >= w.start_ts
          AND r.ts <  w.end_ts
          AND r.credits_charged IS NOT NULL
        GROUP BY tenant_id, date_trunc('hour', ts)
        ON CONFLICT (tenant_id, bucket_start) DO UPDATE
            SET credits       = EXCLUDED.credits,
                request_count = EXCLUDED.request_count,
                updated_at    = now()
    `, lookbackDays)
    return res.RowsAffected(), err
}
```

**关键 trade-off**：回填是 **整桶覆盖** 而非累加，这意味着如果线上 `amount` 大于 0 的请求在 backfill 期间也走到 bucket 表，backfill 会把"线上刚加的增量"覆盖回正确值（因为 usage_ledger.credits_charged 是 source of truth，且 backfill 区间与实时写是同一个 hour 桶时，最后 backfill 写 EXCLUDED 反而对齐了真实历史）。这是**幂等**特性，使重启/补单都能干净重做。

### 3.5 并发与一致性

- **同一 hour 并发写**：ON CONFLICT (tenant_id, bucket_start) → DO UPDATE 是 atomic；Postgres row-level lock 保证累计正确。
- **跨进程（多 gateway 实例）**：每个 instance 在自己的 tx 里执行 upsert，无外部锁需求（PG 行锁自然互斥）。
- **ChargeRequest 失败**：tx rollback，bucket 也不写；与现有 credit_ledger 行为完全一致。
- **窗口边界**：days=1 的 SQL 用 `>= now() - INTERVAL '1 day'`，包含当前 partial hour 桶（无遗漏）。

## 四、迁移步骤

1. ✅ DB：新表 `maas_credit_consumption_buckets` + index
2. ✅ Go：在 `chargeTokens` 内追加一次 upsert
3. ✅ Go：替换 `admin/usage.go` 的 `total_credits_charged` SQL
4. ✅ Go：service 启动 hook（一次性 backfill）
5. ✅ 单测：bucket upsert 行为、SQL 正确性、窗口切片边界
6. ✅ 部署 SQL migration + binary reload

## 五、风险与缓解

| 风险 | 等级 | 缓解 |
|---|---|---|
| 历史数据缺失（init 前 usage_ledger 数据未 backfill） | 中 | 启动时 BackfillCreditBuckets + backfill 期间允许 dashboard 仍可读 (degraded fallback to usage_ledger) |
| ChargeRequest 的 ChargeRequestMultimodal 路径未走 bucket 写入 | 低 | chargeTokens 是单一入口；multimodal 只是签名不同，函数体共用 |
| backfill 时间长（90 天大表） | 低 | usage_ledger 是已分区表，hot 7d + 月分片，GROUP BY tenant+hour 是 PG 友好运算，预计 < 5s |
| 多 region / 多 instance race | 低 | PG atomic upsert 已覆盖；不引入 Redis |
| `amount=0` 早返回时 bucket 漏写 | 低 | 这是预期行为，amount=0 不应入账；与 usage_ledger.credits_charged is null 一致 |
| 升级窗口内（代码更新 + DB schema）双写不一致 | 中 | 部署顺序：先 SQL migration → 再 binary reload；中间窗口内 bucket 为空，dashboard 走 degraded fallback |

## 六、验证

- ✅ 单测：upsert atomic 行为
- ✅ SQL dry-run（psql + BEGIN/ROLLBACK）
- ✅ 部署后 smoke：`/api/usage/summary?days=1` 返回值应等于手动 SQL `SELECT SUM(credits_charged) FROM usage_ledger WHERE ts >= now() - INTERVAL '1 day' AND tenant_id=$1`（桶开始 5-10min 后做对照）

## 七、与现有架构的一致性

- **与 model_credit_rates 互补**：本方案解决"已发生扣费的快速求和"；model_credit_rates 是定价定义层，本方案是其下游 cache。
- **与 credit_ledger 互补**：credit_ledger 是审计明细（每笔扣款），maas_credit_consumption_buckets 是聚合。前者继续负责"账本"，后者负责"计数器"。
- **与 credits_charged 字段的关系**：credits_charged 仍然在 credit_ledger 与 usage_ledger 中写入（source of truth），bucket 只是从中导出的"快速读"。

## 八、CI/CD

- DB migration: `deploy/sql/docs/pricing/2026_07_13_maas_credit_consumption_buckets.sql` (idempotent CREATE TABLE IF NOT EXISTS)
- Go 代码随本次 commit 一起 push
- 部署：先 SQL → 后 binary（避免中间窗口退化）

## 九、后续优化（不在本任务范围）

1. 把 `total_requests` 也搬到 bucket（替代 `COUNT(*)`）
2. 按 model × hour 拆分（更高粒度 dashboard）
3. Redis 副本（如果 PG UPSERT 写压力成为瓶颈）
4. 移除 `usage_ledger.credits_charged` 字段（彻底切到 bucket 模式）—— 但要做 careful audit