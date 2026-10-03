# 设计评审稿：把 `ursm_node_snapshot_min` 改为按日时间分区表

> **文档性质**：这是一份**独立的设计评审稿**，供未参与前期讨论的工程师阅读与拍板。
> 提案原文见 `2026-10-03-ursm-storage-cutover-decisions.md` §10（文件第 254~395 行）。
> **本稿不修改提案文件**，只在 §9 逐条记录「核实结论」，与提案冲突处以本文为准。
>
> **状态**：仅设计，**未实施**。未改动任何生产环境、未改动 Go 源码、未触碰 252/154/245 三台服务器。
>
> **核实基准**：本文所有 DDL、代码引用、行号均由本人于 2026-10-03 逐个 read/grep 本仓库源码得到。

---

## 0. 证据分级约定（★ 请先读这一节）

全文对每条结论标注来源强度，三类不得混用：

| 标记 | 含义 | 可信度 |
|---|---|---|
| **【实测】** | 2026-10-03 在 252 生产库上用只读手段（`pg_class` / `pg_stat_*` / `pg_relation_size`）采到的数字 | 最高，但**本人本次评审未复测**，系转引提案 §1.1/§10 的采集结果 |
| **【源码】** | 本人实际 read/grep 本仓库文件得到的原文，含文件路径与行号 | 高，可自行复核 |
| **【推断】** | 由前两类推导或由行业惯例得出，**尚未验证** | 中低，评审时需确认 |

★ 标记用于**评审时必须停下来看的结论**。

---

## 1. 背景：这张表为什么会一直涨

### 1.1 表规模与写入形态 【实测】

| 项 | 数值 | 备注 |
|---|---|---|
| 行数 | **1,871.9 万** | 2026-10-03 |
| 堆（heap） | **9,129 MB** | 轨迹：8,768 → 9,054 → 9,129 MB |
| 索引 | **1,362 MB** | `_pkey` 1,215 MB + `_ts_idx` 144 MB（提案 §10.5②） |
| 合计 | **约 10 GB** | |
| 写入方 | 两个 Go 网关实例 | 154 authoritative + 245 shadow 双写 |
| 节点规模 | **1,292 个** | |
| 写入周期 | **60 秒**一批，每批 **1,253~1,259 行** | `writer.go` 的 `PersistIntervalSec`，见 `cmd/gateway/main.go:1265-1267` 默认值 60 |
| 写放大 | **1.83×**（双写） | ★ 定义存疑，见 §9 Finding D |

### 1.2 留存行为 【源码】

留存由 `domains/ursm/v2/persist/retention.go` 的 `SnapshotRetentionWorker` 负责，
在 `cmd/gateway/main.go:1312-1317` 启动，**独立于 URSM v2 mode 与 persist writer 的开关**：

```go
// cmd/gateway/main.go:1309-1317
// 快照保留清理独立于 URSM v2 mode / persist writer 的启用状态接线：
// shadow 未开 double-write 时 persist 不落盘，但历史残留行仍需回收，
// 且清理本身不依赖 Redis（容量基线 §5 门禁项 1 的解除条件）。
if dbConn != nil && dbConn.Enabled() {
    snapshotRetentionWorker = persist.NewSnapshotRetentionWorker(dbConn.Pool(), persist.SnapshotRetentionConfigFromEnv())
    if !snapshotRetentionWorker.Disabled() {
        snapshotRetentionWorker.Start()
    } else { ... }
}
```

worker 行为 【源码】：
- `retention.go:143` —— `ticker := time.NewTicker(time.Hour)`，**每小时一轮**；
- `retention.go:142` —— `Start()` 后立即先跑一轮；
- `retention.go:148` —— `Stop()` 时再跑最后一轮；
- `retention.go:41-43` —— 默认配置 `Retention: 30 * 24 * time.Hour`、`BatchSize: 5000`、`MaxCleanupWindow: 10 * time.Minute`；
- `retention.go:205-207` —— 单轮超过 `MaxCleanupWindow`（10 分钟）即返回，剩余量留给下一个 tick。

生产上保留期为 **7 天**，来自环境变量 `URSM_SNAPSHOT_RETENTION_DAYS`
（读自 `retention.go:49-60`；7 天这一取值见 `installer/cmd/llm-gw-installer/embeddata/startup/818_ursm_snapshot_typed_columns.sql` 头注释）。

> ★ **代码默认值是 30 天，不是 7 天。** 这一点提案全文未提，是本稿最重要的提醒之一，
> 详见 §9 Finding B 与 §10.1。

### 1.3 ★ 为什么 DELETE 型留存不归还磁盘 【实测 + 推断】

**实测轨迹**：全堆体积 8,768 → 9,054 → 9,129 MB **单调增长，从未缩小**；
同期行数**下降了 115.8 万**，堆反而 **+75 MB**。

**推断（机理）**：PostgreSQL 的普通表删除行时只把元组标记为死元组，物理页留在原位。
VACUUM 的作用是把这些空页**交给后续插入复用**（`FreeSpaceMap`），**不会**把文件尾部的空页
截断归还操作系统（截断只有 `VACUUM FULL` / `TRUNCATE` / `DROP TABLE` 会做）。
本表是从**最老的一端**开始删的（`retention.go:232` 的 `ORDER BY snapshot_ts, ...` 升序，
且 `retention.go:191` 的 `floor` 初值为零值时间 = 无下界），
**删出来的空页位于堆的物理头部而非尾部**，VACUUM 只能复用、永远无法截断 ⇒ 表体积只增不减。

对照【源码】：本仓库已有的清理脚本 `scripts/252-monitor/pg17-drop-old-columnar-partitions.sh:66`
走的是 `ALTER TABLE ... DETACH PARTITION` + `DROP TABLE IF EXISTS`，
`DROP TABLE` 会**立即把文件 unlink，空间即时归还文件系统**——这正是当前表做不到的事。

**结论**：这不是调参问题（VACUUM 参数、fillfactor 都救不了），是**表形态问题**。
正解是改成时间分区表，把「按行删」换成「按分区 DROP」。

---

## 2. 现状 DDL 【源码，逐字核实】

### 2.1 建表语句

`deploy/sql/schemas/baseline/01-schema.sql:18198`（`installer/cmd/llm-gw-installer/embeddata/01-schema.sql:18198` 同）：

```sql
-- 01-schema.sql:18195-18231
--
-- Name: ursm_node_snapshot_min; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ursm_node_snapshot_min (
    snapshot_ts timestamp with time zone NOT NULL,
    recovery_epoch bigint NOT NULL,
    provider_id integer NOT NULL,
    credential_id integer NOT NULL,
    raw_model_name text NOT NULL,
    canonical_name text,
    tenant_id text DEFAULT ''::text NOT NULL,
    available boolean NOT NULL,
    health_status text,
    fail_streak integer,
    cool_until timestamp with time zone,
    sr_1m real,
    sr_5m real,
    sr_30m real,
    samples_1m integer,
    samples_5m integer,
    samples_30m integer,
    lat_p50_ms integer,
    lat_p95_ms integer,
    score real,
    price_in_per_1m numeric,
    price_out_per_1m numeric,
    billing_mode text,
    trust_level real,
    baseurl_latency_ms integer,
    conc_used integer,
    conc_limit integer,
    fp_used integer,
    fp_limit integer,
    source_priority integer,
    generation bigint,
    payload jsonb
);
```

**共 32 列**。存储引擎为 **heap**：该文件在 18198 之前最后一次
`SET default_table_access_method` 是 17562 行的 `= heap`。

### 2.2 主键与索引

```sql
-- 01-schema.sql:22631-22635
ALTER TABLE ONLY public.ursm_node_snapshot_min
    ADD CONSTRAINT ursm_node_snapshot_min_pkey PRIMARY KEY (snapshot_ts, tenant_id, credential_id, raw_model_name);

-- 01-schema.sql:27486
CREATE INDEX ursm_node_snapshot_min_ts_idx ON public.ursm_node_snapshot_min USING btree (snapshot_ts);
```

**全部 4 个对象**：1 张 heap 普通表 + 1 个主键 + 1 个 btree 索引。无分区、无 RLS
（`retention.go:10` 注释确认 `relrowsecurity = f`）。

### 2.3 ★ 但基线里的列清单已经落后于生产 【源码】

生产实际有 **56 列**：基线的 32 列，加上 818 迁移新增的 **24 个 typed 列**
（`installer/cmd/llm-gw-installer/embeddata/startup/818_ursm_snapshot_typed_columns.sql`）：

| 组 | 列 |
|---|---|
| bigint (10) | `updated_at_ms` `last_probe_at_ms` `last_probe_latency_ms` `last_attempt_ms` `last_ok_ms` `last_request_at_ms` `last_request_error_at_ms` `manual_at_ms` `cool_until_ms` `event_seq` |
| boolean (3) | `disabled` `last_direct_ok` `manual_hold` |
| integer (3) | `success_count` `failure_count` `disable_count` |
| real (3) | `lat_ewma_ms` `empty_response_rate_1m` `empty_response_rate_30m` |
| text (5) | `last_err` `manual_reason` `manual_actor` `disabled_reason` `cool_reason` |

`domains/ursm/v2/persist/writer.go:383-402` 的 INSERT **写了 46 列**，
比基线多出这 24 列中的 24 列、少写 10 列（`lat_p95_ms`、`price_in_per_1m`、
`price_out_per_1m`、`billing_mode`、`trust_level`、`baseurl_latency_ms`、
`conc_used`、`conc_limit`、`fp_used`、`fp_limit`）⇒ 46 + 10 = 56，自洽。

> ★ 结论：**基线 `01-schema.sql` 与生产不同步，缺 818 的 24 列。**
> 这与本次分区改造是两件事，但都落在同一个文件上。提案 §10.2/§10.6 只说
> 「`01-schema.sql:18198` 与 `:22634` 两处要改」，**漏了索引那一处（:27486），
> 也漏了这个既有漂移**。详见 §9 Finding A。

---

## 3. 目标设计

### 3.1 分区键选择：`snapshot_ts`

**选择**：`PARTITION BY RANGE (snapshot_ts)`。

**理由**：
1. `snapshot_ts` 是**唯一的语义时间轴**——`retention.go:230` 的删除条件就是
   `snapshot_ts < NOW() - $1::interval`，留存窗口天然以它为界；
2. 它是**写入时刻**（每 60 秒一批），与行内容无关，不会被 UPDATE 改动
   ⇒ 不会触发行跨分区移动（见 §8 风险 4）；
3. 它是 **PK 的首列** ⇒ 满足 PostgreSQL 对分区表唯一约束的硬性要求
   （见 §3.2），这是本设计能成立的关键。

### 3.2 ★ PK 已含分区键 —— 提案「writer 不用改」的说法成立 【源码核实 + PG 规则】

PostgreSQL 规则：**分区父表上的唯一索引/主键必须包含全部分区键列**，否则建表直接报错。

本表 PK = `(snapshot_ts, tenant_id, credential_id, raw_model_name)`，其中
`snapshot_ts` 既是分区键又是 PK 首列 ⇒ **PK 可以原样建在分区父表上**，
建索引时 PG 自动为每个分区挂上对应分区索引。

再看 writer 的 ON CONFLICT 【源码，`writer.go:402`】：

```go
ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name) DO NOTHING
```

**逐字等于 PK 的四列**，与 `01-schema.sql:22635` 完全一致。
`ON CONFLICT (cols)` 要求存在一个恰好覆盖这些列的唯一索引作为 arbiter，
分区化后该唯一索引由父表 PK 下推到每个分区，**因此 INSERT 语句一个字都不用改**。

> **核实结论：提案 §10.2「writer INSERT 不用改」——成立。**
>
> ★ 但有一个**附加条件**提案没写：这条不变式的代价是
> **`ursm_node_snapshot_min_pkey` 必须保留**。若评审时有人提议「反正没人读，把 PK 一起删了」
> ——**不行**，删了 PK，writer 的 `ON CONFLICT (cols)` 会直接报
> `there is no unique or exclusion constraint matching the ON CONFLICT specification`，
> 全量写入瘫痪。这是硬约束。

### 3.3 分区粒度：按日

**选择**：按自然日（Asia/Shanghai 日历），保留窗口 7 天 ⇒ 稳态约 8~10 个分区。

**为什么按日**：
- 留存窗口只有 7 天，沿用月分区会有 3/4 的分区数据一出生就是死的；
- 按日让「保留 N 天」精确对应「DROP N 天前的分区」，不留残片。

> ★ **对提案 §10.3 的修正（Finding C）**：提案把按日说成是「不沿用月度」的新做法，
> 措辞暗示本仓库只有月度分区。**事实不是**：`usage_facts` 在本仓库**已经是按日分区**的
> （`installer/cmd/llm-gw-installer/embeddata/startup/750_usage_facts_daily_partition.sql`，
> 时区钉扎补丁在 `751_usage_facts_partition_tz_pin.sql`，并已注册进
> `bg/partition_manager.go:1302` 的 `ensureSpecs()`，`partitionUnit: "day"`）。
> **按日分区在本仓库有现成范式、现成踩坑记录，不需要新发明**——这一点提案低估了。
> 本节 §3.4 的设计直接照抄该范式，并把它踩过的坑一并吸收。

### 3.4 本仓库的分区范式 【源码】

**全仓不使用 pg_partman**（`grep -rln "pg_partman" --include=*.sql --include=*.go .` **无任何命中**），
全部是**手写 RANGE 分区 + plpgsql ensure 函数**。

**月度范式**（`deploy/sql/migrations/V359__candidate_failure_logs_hot_and_partition.sql:178-205`）：

```sql
CREATE OR REPLACE FUNCTION ensure_candidate_failure_logs_partition(target_ts timestamp with time zone)
RETURNS text LANGUAGE plpgsql AS $$
DECLARE
    month_start    date := date_trunc('month', target_ts)::date;
    month_end      date := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name text := 'candidate_failure_logs_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF candidate_failure_logs
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            partition_name, month_start, month_end
        );
    ...
```

**按日范式**（`750_usage_facts_daily_partition.sql:63-116`）与月度有三处关键差异，
**这三点是它踩出来的坑，本设计必须照抄**：

| # | 坑 | 750 的做法 |
|---|---|---|
| 1 | **`p_date::timestamptz` 在 DECLARE 初始化器里按会话时区求值** ⇒ UTC 会话得到错位 8 小时的分区窗口 | 751 用**函数级 GUC** `ALTER FUNCTION ... SET timezone = 'Asia/Shanghai'`。`db/db.go:1397-1401` 每次 boot 再幂等收敛一次。`db/db.go:1379-1383` 注释明确：body 内 `SET LOCAL` **不覆盖** DECLARE 初始化器，**函数级 SET 才行** |
| 2 | 父表挂 `DEFAULT` 分区时直接 `CREATE TABLE ... PARTITION OF` 会先校验 DEFAULT 内是否有落入新区间的行 ⇒ 报 `updated partition constraint for default partition ... would be violated by some row`，且**该日永远补不上分区** | 改为 **move-then-attach**：advisory xact 锁串行化 → `pg_inherits` 幂等短路 → `CREATE TABLE ... (LIKE ... INCLUDING DEFAULTS INCLUDING INDEXES)` → AE 锁 DEFAULT → `DELETE ... RETURNING` 搬走行 → `ATTACH PARTITION` |
| 3 | 多入口并发 ensure（boot / installer / 升级通道 / 24h tick 在 252 共享库上并发） | `pg_advisory_xact_lock(hashtext('ensure_...:' || pname))` |

**分区命名**：750 用 `format('usage_facts_%s', to_char(p_date, 'YYYYMMDD'))`，
即 `usage_facts_20261003`（8 位、无下划线分隔）。本设计沿用该风格。

**时区常量**：`bg/partition_manager.go:33` `var partitionTZ = time.FixedZone("Asia/Shanghai", 8*60*60)`；
`ensureNextMonthPartitions`（`bg/partition_manager.go:351-397`）在 `unit == "day"` 时
用 `offset` 0/1 派生**当日 / 次日**，正是按日分区需要的语义。

### 3.5 目标 DDL

```sql
-- ===========================================================================
-- 迁移：把 ursm_node_snapshot_min 改为按日 RANGE 分区表
-- 依赖：installer/cmd/llm-gw-installer/embeddata/startup/818_*.sql 已应用
--       （下列列定义 = 01-schema.sql:18198 的 32 列 + 818 的 24 列 = 56 列）
-- ===========================================================================
BEGIN;

-- ---------------------------------------------------------------------------
-- 1) 分区父表
--    ★ PK 保留且四列不变：writer.go:402 的 ON CONFLICT 依赖它作为 arbiter。
--      PG 要求父表唯一索引含全部分区键列，snapshot_ts 已是 PK 首列 ⇒ 合法。
-- ---------------------------------------------------------------------------
CREATE TABLE public.ursm_node_snapshot_min (
    snapshot_ts timestamp with time zone NOT NULL,
    recovery_epoch bigint NOT NULL,
    provider_id integer NOT NULL,
    credential_id integer NOT NULL,
    raw_model_name text NOT NULL,
    canonical_name text,
    tenant_id text DEFAULT ''::text NOT NULL,
    available boolean NOT NULL,
    health_status text,
    fail_streak integer,
    cool_until timestamp with time zone,
    sr_1m real,
    sr_5m real,
    sr_30m real,
    samples_1m integer,
    samples_5m integer,
    samples_30m integer,
    lat_p50_ms integer,
    lat_p95_ms integer,
    score real,
    price_in_per_1m numeric,
    price_out_per_1m numeric,
    billing_mode text,
    trust_level real,
    baseurl_latency_ms integer,
    conc_used integer,
    conc_limit integer,
    fp_used integer,
    fp_limit integer,
    source_priority integer,
    generation bigint,
    payload jsonb,
    -- 以下 24 列来自 818 迁移（基线 01-schema.sql 尚未收录，见 §9 Finding A）
    updated_at_ms bigint,
    last_probe_at_ms bigint,
    last_probe_latency_ms bigint,
    last_attempt_ms bigint,
    last_ok_ms bigint,
    last_request_at_ms bigint,
    last_request_error_at_ms bigint,
    manual_at_ms bigint,
    cool_until_ms bigint,
    event_seq bigint,
    disabled boolean,
    last_direct_ok boolean,
    manual_hold boolean,
    success_count integer,
    failure_count integer,
    disable_count integer,
    lat_ewma_ms real,
    empty_response_rate_1m real,
    empty_response_rate_30m real,
    last_err text,
    manual_reason text,
    manual_actor text,
    disabled_reason text,
    cool_reason text
) PARTITION BY RANGE (snapshot_ts);

-- ---------------------------------------------------------------------------
-- 2) 主键（唯一的显式索引）
--    父表上建一次，PG 为每个分区自动创建匹配的分区索引。
--    ★ 不建 ursm_node_snapshot_min_ts_idx：全仓无 snapshot_ts 单列读路径（§5.1），
--      留存改为 DROP 后它彻底无用。理由与数据见提案 §10.5②。
-- ---------------------------------------------------------------------------
ALTER TABLE ONLY public.ursm_node_snapshot_min
    ADD CONSTRAINT ursm_node_snapshot_min_pkey
    PRIMARY KEY (snapshot_ts, tenant_id, credential_id, raw_model_name);

-- ---------------------------------------------------------------------------
-- 3) 按日 ensure 函数（照抄 750 范式，含 751 时区钉扎）
--    ★ 不建 DEFAULT 分区 —— 理由见 §3.6。
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.ensure_ursm_node_snapshot_min_daily_partition(p_date DATE)
RETURNS void LANGUAGE plpgsql SET timezone = 'Asia/Shanghai' AS $$
DECLARE
    pname    TEXT := format('ursm_node_snapshot_min_%s', to_char(p_date, 'YYYYMMDD'));
    start_ts TIMESTAMPTZ := p_date::timestamptz;
    end_ts   TIMESTAMPTZ := (p_date + INTERVAL '1 day')::timestamptz;
BEGIN
    -- 幂等短路：分区已挂接则无事可做。
    IF EXISTS (
        SELECT 1 FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass
          AND c.relname = pname
    ) THEN
        RETURN;
    END IF;

    -- 并发串行化：boot ensure / installer / 升级通道 / 24h tick 在 252 共享库上会并发
    PERFORM pg_advisory_xact_lock(hashtext('ensure_ursm_node_snapshot_min_daily_partition:' || pname));

    -- 二次短路：拿到锁后可能已被别的入口建好
    IF to_regclass('public.' || pname) IS NOT NULL THEN
        RETURN;
    END IF;

    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.ursm_node_snapshot_min
         FOR VALUES FROM (%L) TO (%L)',
        pname, start_ts, end_ts
    );
END;
$$;

-- 751 同款：函数级 GUC 钉扎，防 UTC 会话产出错位 8 小时的分区边界
ALTER FUNCTION public.ensure_ursm_node_snapshot_min_daily_partition(DATE)
    SET timezone = 'Asia/Shanghai';

COMMENT ON FUNCTION public.ensure_ursm_node_snapshot_min_daily_partition(DATE) IS
'按日分区 ensure。照 750_usage_facts_daily_partition 范式 + 751 时区钉扎。
由 db.go boot ensure 与 bg/partition_manager.go ensureSpecs 24h tick 双通道调用。';

-- ---------------------------------------------------------------------------
-- 4) 预建当日 / 次日 / 后日（显式上海日历派生，与 partition_manager partitionTZ 同源）
--    ★ 建 3 天而非 750 的 2 天：本文不留 DEFAULT 分区兜底（§3.6），
--      多预建一天把「tick 恰好横跨一次失败」的空窗从 24h 压到 48h。
-- ---------------------------------------------------------------------------
SELECT ensure_ursm_node_snapshot_min_daily_partition((now() AT TIME ZONE 'Asia/Shanghai')::date);
SELECT ensure_ursm_node_snapshot_min_daily_partition(((now() AT TIME ZONE 'Asia/Shanghai')::date) + 1);
SELECT ensure_ursm_node_snapshot_min_daily_partition(((now() AT TIME ZONE 'Asia/Shanghai')::date) + 2);

COMMIT;
```

### 3.6 ★ 索引怎么建 & 要不要 DEFAULT 分区

**索引**：父表上**只建 PK 一条**。PG 会为每个分区自动创建对应的分区索引
（`CREATE INDEX`/`ADD PRIMARY KEY` 在分区父表上执行即触发下推），
运维不需要、也不应该手工去每个分区建索引——那是 `PARTITION OF` 方式的自动行为。

**DEFAULT 分区：本设计建议不建**，理由：

1. **本表的诉求是「空间必须归还」**。DEFAULT 分区是一个**永久兜底桶**，
   一旦有越界行落进去（比如 ensure 连续失败），
   它就变成了一个**永不被 DROP 清理、也永不被 VACUUM 截断的黑洞**——
   正是本设计要消灭的那种表。何况现存 DEFAULT 分区已经踩过一次：
   `sql/fixes/2026-10-02-db-storage-reclaim.sql:16-22` 记录
   `usage_facts_default` 534 MB **全是死索引页**，且
   「DROP 掉它，时间越界的写入会直接报 `no partition of relation found`」——
   两头堵死，只能 TRUNCATE。
2. **DEFAULT 会拖慢 ATTACH**（`750` 头注释 §②：PG 无法凭空证明，ATTACH 时仍会全表校验 DEFAULT）。
3. **代价对比**：不留 DEFAULT ⇒ 缺分区时写入**立刻失败并刷日志**（可观测、可告警）；
   留 DEFAULT ⇒ 写入静默落进黑洞，几天后才在磁盘告警里被发现。
   对一张「写丢了就没有第二份」的审计快照表，**响亮失败优于静默丢失**。

> ★ 这个取舍是本设计最需要评审拍板的一点，见 §10 第 4 问。
> 若评审决定要 DEFAULT，就必须把 750 的 move-then-attach 整套搬过来，
> 并且必须给 `ursm_node_snapshot_min_default` 单独加监控（它一有行就要报警）。

---

## 4. 代码改造面

### 4.1 `domains/ursm/v2/persist/writer.go` —— **不用改** 【源码核实】

INSERT 语句原文（`writer.go:382-402`）：

```go
			_, err := tx.Exec(ctx, `
INSERT INTO ursm_node_snapshot_min
  (snapshot_ts, recovery_epoch, provider_id, credential_id, raw_model_name, canonical_name, tenant_id,
   available, health_status, fail_streak, cool_until,
   sr_1m, sr_5m, sr_30m, samples_1m, samples_5m, samples_30m,
   lat_p50_ms, score, source_priority, generation, payload,
   -- 818：24 个由 payload 提升出来的 typed 列
   updated_at_ms, last_probe_at_ms, last_probe_latency_ms, last_attempt_ms,
   last_ok_ms, last_request_at_ms, last_request_error_at_ms, manual_at_ms,
   cool_until_ms, event_seq,
   disabled, last_direct_ok, manual_hold,
   success_count, failure_count, disable_count,
   lat_ewma_ms, empty_response_rate_1m, empty_response_rate_30m,
   last_err, manual_reason, manual_actor, disabled_reason, cool_reason)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22::text::jsonb,
        $23,$24,$25,$26,$27,$28,$29,$30,$31,$32,
        $33,$34,$35,
        $36,$37,$38,
        $39,$40,$41,
        $42,$43,$44,$45,$46)
ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name) DO NOTHING`,
```

**为什么不用改**（三条，逐条核实）：

| # | 理由 | 依据 |
|---|---|---|
| 1 | 表名不变（迁移用「改名 + 自然排空」，新表沿用原名，见 §6） | §6 |
| 2 | 列名、列序、参数个数全不变（56 列里仍只写这 46 列） | `writer.go:383-402` vs §3.5 |
| 3 | `ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name)` 逐字等于保留的 PK 四列，父表唯一索引含分区键 ⇒ PG 接受 | `01-schema.sql:22635` + `writer.go:402` |

> ★ **但有一个新依赖必须写进运维手册**：当前「表不存在 → 写失败」的风险等级不高
> （表一直在），分区化之后变成「**今天的分区不存在 → 全部 1,292 节点这一分钟的快照全丢**」。
> 因此 §3.5 的 ensure 通道（boot + 24h tick）从「优化项」升级为「**可用性硬依赖**」，
> 见 §8 风险 1。

### 4.2 `domains/ursm/v2/persist/retention.go` —— **必须重写** 【源码】

现状逐字（`retention.go:226-244`）：

```go
	const stmt = `
		WITH batch AS (
			SELECT snapshot_ts, tenant_id, credential_id, raw_model_name
			FROM ursm_node_snapshot_min
			WHERE snapshot_ts < NOW() - $1::interval
			  AND snapshot_ts >= $3::timestamptz
			ORDER BY snapshot_ts, tenant_id, credential_id, raw_model_name
			LIMIT $2
		), deleted AS (
			DELETE FROM ursm_node_snapshot_min t
			USING batch b
			WHERE (t.snapshot_ts, t.tenant_id, t.credential_id, t.raw_model_name)
			    = (b.snapshot_ts, b.tenant_id, b.credential_id, b.raw_model_name)
			RETURNING t.snapshot_ts
		)
		SELECT count(*)::bigint AS deleted, max(snapshot_ts) AS next_floor FROM deleted`
	...
	if err := tx.QueryRow(ctx, stmt, w.cfg.Retention.String(), w.cfg.BatchSize, floor).
```

**参数顺序**：`$1 = w.cfg.Retention.String()`（如 `"168h0m0s"`）、`$2 = w.cfg.BatchSize`、`$3 = floor`。

**为什么必须改成 DROP TABLE**：

1. **DELETE 型留存的核心缺陷不会因为分区而消失**——只要还在按行删，空页就还在堆的物理头部，
   VACUUM 仍然无法截断。分区化不改变「按行删」这一事实，磁盘仍会单调增长；
2. 批 DELETE 逐分区执行仍要为 7 天前的每个分区做**逐行索引删除 + 死元组**，
   提案 §10.5② 实测该路径每次扫描读 70,030 个索引项（`BatchSize=5,000` 的 14 倍读放大）；
3. `DROP TABLE` 是唯一能让空间**立即归还文件系统**的形态（§1.3）。

**改造后的设计**：

```go
// 伪代码：查目录 → 逐个 DROP
const listPartitions = `
	SELECT c.oid::regclass::text AS child,
	       pg_total_relation_size(c.oid) AS bytes
	FROM pg_inherits i
	JOIN pg_class c ON c.oid = i.inhrelid
	WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass
	  -- 分区名形如 ursm_node_snapshot_min_20261003，日期为 Asia/Shanghai 日历日
	  AND ((substring(c.relname FROM '_([0-9]{8})$')::date)::timestamp
	       AT TIME ZONE 'Asia/Shanghai') + interval '1 day' <= now() - $1::interval
	ORDER BY 1`

const dropPartition = `SET lock_timeout = '5min'; DROP TABLE IF EXISTS %s`
```

分区名匹配用正则 `_([0-9]{8})$`，与既有脚本
`scripts/252-monitor/pg17-drop-old-columnar-partitions.sh:66` 的
`substring(inhrelid::regclass::text FROM '_([0-9]{4}_[0-9]{2})$')` 同款思路
（那里是月度 `YYYY_MM`，这里是日度 `YYYYMMDD`）。

> 用**分区名里的日期**而不是 `pg_get_expr(relpartbound)` 解析边界表达式：
> 前者稳定、可读、免解析；代价是**分区名格式成了契约**，
> 任何手工建的分区必须遵守 `ursm_node_snapshot_min_YYYYMMDD`，
> 否则会永远清理不到。**这个契约要写进迁移注释和运维文档。**

**逐项处理清单**：

| 现有机制 | 行号 | 改造后 |
|---|---|---|
| `BatchSize`（5000） | `retention.go:42`、`:101-105` 归一化 | **失去意义**。DROP 是一次 DDL，没有「批」的概念。**建议保留字段但标注 deprecated**（`main.go:1313` 通过 `SnapshotRetentionConfigFromEnv()` 整体构造配置，删字段要连带改 `:101-105` 与任何外部配置读取）。★ 是否删除是评审决定项 |
| 小时 ticker | `retention.go:143` | **建议保留 1 小时**。新实现只是一条查 `pg_inherits` 的目录查询，代价 O(分区数) ≈ 10 行，1 分钟一次也毫无压力。保留 1 小时可让「误建的空分区」在 1 小时内被回收 |
| `MaxCleanupWindow`（10 分钟） | `retention.go:43`、`:205-207` | **建议保留**。单次 `DROP TABLE` 很快，但目录查询可能因大量分区变慢，保留墙钟上限作为保护 |
| 游标 `floor` | `retention.go:191`、`:204` | **删除**。DROP 是全有全无，没有「批内推进」概念；且 `floor` 本来就只在内存里、每轮重置（`:191` 初值零值 = 无下界），**不存在需要迁移的持久化进度** |
| 退出条件 `deleted < BatchSize && !nextFloor.After(floor)` | `retention.go:201` | **删除**，改为「本轮无分区可删即退出」 |
| 「删了多少行」统计 | `retention.go:161-168` | **口径必须变**。改为上报 `partitions_dropped` + `bytes_reclaimed`（`DROP` 前用 `pg_total_relation_size` 采集）。★ **注意：现在根本没有 Prometheus 指标**——`CleanupOnce` 只 `slog.Info` 一个 `deleted` 字段（`retention.go:166-167`），全仓 grep 无对应 metric。所以「指标怎么调整」的答案是：**只需改日志字段名与含义，无需动指标注册** |

**新实现的日志建议**：

```go
slog.Info("ursm.v2: snapshot retention dropped expired partitions",
    "partitions_dropped", n, "bytes_reclaimed", totalBytes, "retention", w.cfg.Retention.String())
```

**参照的清理脚本形态**（`scripts/252-monitor/pg17-drop-old-columnar-partitions.sh:62-72`）：

```bash
"SET statement_timeout=0; SET lock_timeout='5min';
 ALTER TABLE $parent DETACH PARTITION $child;"
...
"SET statement_timeout=0; SET lock_timeout='5min';
 DROP TABLE IF EXISTS $child;"
```

> 该脚本走「先 DETACH 再 DROP」两步，本设计建议**直接 `DROP TABLE <child>`**
> ——PostgreSQL 会隐式 detach，一步到位、失败面小一半。
> 保留 `lock_timeout='5min'` 的做法。
>
> ⚠️ 该脚本末尾还有一段对 `columnar_internal.chunk/stripe/chunk_group` 的 `VACUUM FULL`
> （`scripts/252-monitor/pg17-drop-old-columnar-partitions.sh:80-90`），
> **那是列存专有的**（DROP 列存表会在列存元数据表留死元组）。本目标是 **heap** 表，
> **不需要**这段，照抄反而是给 heap 表跑一次 60 分钟的 VACUUM FULL。

### 4.3 `cmd/gateway/main.go` —— **门控不受影响** 【源码】

`main.go:1259-1263` 的 persist writer 门控：

```go
	if ursmV2Mgr != nil && dbConn != nil && dbConn.Enabled() {
		persistEnabled := ursmV2Cfg.Mode != ursmv2api.ModeOff &&
			(ursmV2Cfg.Mode != ursmv2api.ModeShadow || ursmV2Cfg.ShadowDoubleWrite)
		if persistEnabled {
```

**核实结论**：这层门控只看「URSM v2 处于什么 mode」和「DB 连接是否可用」，
**不含任何 schema 知识**，因此**分区化对它零影响，不需要改**。

`main.go:1312-1317` 的 retention worker 接线同样只看 `dbConn`，也**不需要改接线**；
但要注意它**独立于 `persistEnabled`**（`main.go:1309-1311` 注释已说明），
所以 shadow 模式下 retention 照样跑——这与新实现完全兼容（无分区可删即 no-op）。

### 4.4 新增的两个接线点 【源码推断 + 范式】

分区化后**多了两处本表相关的接线**，都不在上面三个文件里：

1. **`bg/partition_manager.go:1227` `ensureSpecs()`** —— 加一行：

   ```go
   {fnName: "ensure_ursm_node_snapshot_min_daily_partition",
    label: "ursm_node_snapshot_min (daily)",
    argExpr: "$1::date", partitionUnit: "day"},
   ```
   照抄 `bg/partition_manager.go:1302` 的 `usage_facts` 条目。
   `ensureNextMonthPartitions`（`bg/partition_manager.go:351-397`）已支持 `unit == "day"`
   的 offset 0/1 语义（当日 / 次日），**无需改动该函数本身**。
   ★ `bg/partition_manager.go:1252-1257` 的注释记录了「473 同族」教训：
   迁移建了分区但**忘了接进 `ensureSpecs()`**，到下月 1 日全线
   `no partition of relation found`。本表不能重蹈。

2. **`db/db.go`** —— boot 期兜底 ensure，照 `db/db.go:315` /
   `ensureUsageFactsDailyPartition`（`db/db.go:1387-1420`）的形态：
   ① 幂等 `ALTER FUNCTION ... SET timezone`（对应 751）；② 调 ensure 建当日分区。
   ★ `db/db.go:1375-1377` 的注释警告：`psql --single-transaction` **不能**承载
   `CREATE TABLE PARTITION OF`，因为 `schema_migrations` 在事务内看不到 autocommit DDL。
   本设计用的是 plpgsql 函数 + `EXECUTE`（在函数内是 autocommit 语义），
   与 750 走的是同一条路，**不受该限制**；但**盖章（stamp）那一步必须在事务外单独执行**。

---

## 5. 迁移路径：改名 + 自然排空

### 5.1 ★ 前置：`全仓无读路径` —— 我重新 grep 验证，结论需限定 【源码】

提案 §10.5① 的结论我重新验证了一遍，**方向成立但表述需要收紧**。

`grep -rn "ursm_node_snapshot_min" --include=*.go . | grep -v _test.go` 的全部命中：

| 位置 | 性质 |
|---|---|
| `domains/ursm/v2/persist/writer.go:383` | INSERT（`ON CONFLICT DO NOTHING`） |
| `domains/ursm/v2/persist/retention.go:229`、`:235` | 批 SELECT + DELETE |
| `domains/ursm/v2/persist/writer.go:99` | 注释 |
| `domains/ursm/v2/persist/retention.go:181` | 注释 |
| `internal/trace/stage_events_retention.go:6` | 注释（提到本表） |
| `installer/cmd/llm-gw-installer/main.go:722` | 注释 + `//go:embed` 登记 |
| `installer/internal/dbinit/runner.go:686` | 注释 + 启动文件清单登记 |

**Go 侧确实零 SELECT 读路径** ✔ —— 这一点成立，路由状态读 Redis 不读这张表。

**但「全仓无任何 SELECT」不成立**，我另外 grep 到三处非 Go 引用：

| 位置 | 性质 | 是否影响迁移 |
|---|---|---|
| `sql/fixes/2026-09-20-canonical-dedup-cleanup.sql:17` | **UPDATE** `ursm_node_snapshot_min u SET canonical_name = ...` | 818 头注释已称其为「一个**已执行的** 09-20 一次性脚本」。**已执行完毕，不构成读依赖**；但它证明历史上存在过全表 UPDATE，见 §8 风险 4 |
| `sql/fixes/2026-10-02-db-storage-reclaim.sql:269` | `ANALYZE public.ursm_node_snapshot_min` | 一次性运维脚本 |
| `.db-audit/sql/07_dist2.sql:33`、`:45`、`.db-audit/sql/04_slow2.sql:33`、`.db-audit/sql/09_final.sql:9`、`.db-audit/sql/10_cols.sql:6`、`.db-audit/sql/23_crashlog.sql:17` | 审计脚本的 `count(*)` / `min`/`max` / `pg_stats` 查询 | 人工触发的审计，不在运行时链路 |

**对迁移的结论**：没有**运行时**读路径 ⇒
**不需要拷贝 1,871.9 万行**，改名切换对应用侧完全无感。✔ 提案 §10.7 成立。

> ⚠️ 附带影响：审计脚本里那些 `FROM ursm_node_snapshot_min`（无表名限定子查询）
> 在分区化后语义会变（`count(*)` 仍对，但结果只覆盖**现存分区**；
> `_legacy` 还在时两表都存在，审计脚本会漏读 `_legacy`）。审计脚本需同步更新，见 §8 风险 6。

### 5.2 迁移步骤

```sql
-- ===========================================================================
-- 步骤 1：单事务原子切换（★ 关键：把 RENAME + 建父表 + 建首个分区放进同一事务）
--   RENAME 只需 ACCESS EXCLUSIVE 锁且不改数据（纯目录操作，无表重写），
--   所以整个事务是毫秒级的；期间并发的 INSERT 只是在锁上排队，
--   提交后立即成功并路由进新表 —— 不丢数据、不需要停机。
--   ★ 为什么不拆成三步：拆开后「父表已建、分区未建」的窗口里，
--     writer 会 100% 失败（无 DEFAULT 分区兜底，§3.6）。单事务消除了这个窗口。
--   ★ 务必带 lock_timeout：拿不到锁就快速失败，不要把写入堵在锁队列里。
-- ===========================================================================
BEGIN;
SET LOCAL lock_timeout = '10s';

-- 1a) 旧表改名保留。历史不丢，运维仍可查。
ALTER TABLE public.ursm_node_snapshot_min RENAME TO ursm_node_snapshot_min_legacy;

-- 1b) 新分区父表（DDL 见 §3.5，此处省略列定义）
CREATE TABLE public.ursm_node_snapshot_min ( ... 56 列 ... ) PARTITION BY RANGE (snapshot_ts);
ALTER TABLE ONLY public.ursm_node_snapshot_min
    ADD CONSTRAINT ursm_node_snapshot_min_pkey
    PRIMARY KEY (snapshot_ts, tenant_id, credential_id, raw_model_name);

-- 1c) ensure 函数 + 预建当日分区（函数定义见 §3.5）
CREATE OR REPLACE FUNCTION public.ensure_ursm_node_snapshot_min_daily_partition(DATE) ... ;
ALTER FUNCTION public.ensure_ursm_node_snapshot_min_daily_partition(DATE) SET timezone = 'Asia/Shanghai';
SELECT ensure_ursm_node_snapshot_min_daily_partition((now() AT TIME ZONE 'Asia/Shanghai')::date);

COMMIT;
```

**每一步的验证点**：

| # | 验证 | 方法 |
|---|---|---|
| V1 | 改名后旧表完整保留 | `SELECT count(*), pg_size_pretty(pg_total_relation_size('ursm_node_snapshot_min_legacy')) FROM ursm_node_snapshot_min_legacy;` 期望 ≈ 1,871.9 万行 / ≈ 10 GB |
| V2 | 新表确为分区父表 | `SELECT relkind, relispartition FROM pg_class WHERE relname='ursm_node_snapshot_min';` 期望 `relkind='p'`、`relispartition=false` |
| V3 | 首个分区已挂 | `SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid=i.inhrelid WHERE i.inhparent='ursm_node_snapshot_min'::regclass;` 期望 1 行，名为 `ursm_node_snapshot_min_YYYYMMDD` |
| V4 | 写入真的进来了 | 新表 `count(*)` 在 1~2 个周期（60~120 秒）内 > 0 |
| V5 | 写入速率正常 | 新表行数增速 ≈ 1,253~1,259 行/分钟 |
| V6 | 分区边界是上海日历 | `SELECT pg_get_expr(relpartbound, oid) FROM pg_class WHERE relname LIKE 'ursm_node_snapshot_min_2%';` 期望边界形如 `FOR VALUES FROM ('2026-10-03 00:00:00+08')`（**必须是 +08，不能是 Z**） |
| V7 | `_legacy` 不再增长 | 连续 2 个周期 `_legacy` 的 `n_tup_ins` 不再变化 |

**观察期（7 天）**：
- 新表自然填满 7 天；`_legacy` 保持冻结；
- ★ **注意：切换后 retention worker 打的是新表名，扫不到任何过期分区 ⇒ no-op，
  `_legacy` 不会被自动清理**。它会一直占着 10 GB，直到第 3 步显式 DROP。
  这 10 GB 的冻结占用是**有意为之**（保底可回滚），但必须有人负责在第 7 天执行。

```sql
-- 步骤 2（观察 ≥ 7 天后）：DROP 旧表，空间立即归还操作系统
DROP TABLE public.ursm_node_snapshot_min_legacy;
```

### 5.3 `_legacy` 留多久

| 方案 | 保留期 | 磁盘代价 |
|---|---|---|
| **推荐** | 7 天（对齐留存窗口，正好覆盖一次完整的留存周期审计） | 峰值 ≈ 新表 4~7 GB + `_legacy` 10 GB ≈ **14~17 GB** |
| 激进 | 1~3 天 | 峰值低，但**放弃回滚能力**（见 §7） |
| 提案 §10.7 的说法 | 「`_legacy` 保留 7 天足够；若无人看可提前到切换后立即」 | 同上 |

★ `_legacy` 里是**切换前的 7 天历史**，与新表的内容不重叠（不重不漏）。
有无人工审计需求是**产品决策**，见 §10 第 5 问。

---

## 6. 回滚方案

回滚能力**分三个阶段递减**，请评审时明确知道每个阶段能退到哪。

### 阶段 A：步骤 1 提交后、观察期内（`_legacy` 还在）

**可完整回滚**，代价 = 丢失切换后新表里积累的快照。

```sql
BEGIN;
SET LOCAL lock_timeout = '10s';
-- 新表改名让位
ALTER TABLE public.ursm_node_snapshot_min RENAME TO ursm_node_snapshot_min_failed;
-- 旧表改回原名，writer 无需改代码即恢复写入
ALTER TABLE public.ursm_node_snapshot_min_legacy RENAME TO ursm_node_snapshot_min;
COMMIT;
-- 确认新写入恢复后再清理
DROP TABLE public.ursm_node_snapshot_min_failed;
```

**代码侧**：必须同时回滚二进制（retention 变更是随二进制发布的）。
若只回滚数据不回滚代码，新的 DROP 型 retention 打到普通表上会
**匹配不到任何分区、静默 no-op ⇒ 表重新变成只增不减**。
★ 这是最危险的一种半回滚状态，**数据与代码必须同进同退**。

**保留什么**：`_legacy` 的 1,871.9 万行完整保留。
**代价**：切换后 N 天的快照（1,292 节点 × N × 1,440 份）永久丢失；
若 N=7，约 1,300 万行 / 5~7 GB 审计数据消失。

### 阶段 B：`_legacy` 已 DROP 之后

**不可回滚。** 数据层面没有退路，只能向前修。

仍可做的事：
- 回滚 retention 代码（但**没有分区就会 no-op**，等价于关掉留存 ⇒ 磁盘重新单调增长，
  需立刻手工介入）；
- 重新走一次 §5.2 的改名 + 排空（新表改名让位 → 建新父表），代价是丢掉上次切换后的数据。

**结论**：`_legacy` 的 DROP 是**单点不可逆操作**，执行前必须有第二次
`pg_dump` 留档，并在工单里写明「此步之后无退路」。

### 阶段 C：二进制层面

- 新代码若在观察期内出问题，**先看是否与分区有关**：
  retention 改写是唯一的行为变更面；writer 逐字未改（§4.1），
  因此「快照写入中断」类故障**不太可能由本次改造引起**，
  应先查 ensure 分区是否建成功（§8 风险 1）。

---

## 7. 风险清单

| # | 风险 | 触发条件 | 影响面 | 探测手段 | 缓解办法 |
|---|---|---|---|---|---|
| **1** | ★ **ensure 失败 ⇒ 写入全线中断** | 当日分区缺失（boot ensure 与 24h tick 双双失败、跨午夜时区钉扎失效、迁移遗留缺陷） | **全量快照写入**：1,292 节点每 60 秒一批全部落库失败。因无 DEFAULT 分区（§3.6），**响亮失败** | writer 的 `tx.Exec` 报错 → `ursm` 相关日志；`SELECT count(*) FROM pg_inherits WHERE inhparent='ursm_node_snapshot_min'::regclass` 查不到今天；**建议加告警：分区数 < 2 即报警** | ① boot + 24h tick **双通道**（§4.4）；② 预建 3 天而非 2 天；③ 照抄 750+751 的时区钉扎；④ `bg/partition_manager.go:1252-1257` 记载的「473 同族」教训必须写进检查单 |
| **2** | **保留期回落成 30 天** | `URSM_SNAPSHOT_RETENTION_DAYS` 未设置/被清空（`retention.go:49-52` 走默认值 30 天） | 稳态分区数 8 → **31**，体积 4~7 GB → **13~19 GB**（§9 Finding E 口径），几乎回到今天的水平 | 定期 `SELECT count(*) FROM pg_inherits WHERE inhparent='ursm_node_snapshot_min'::regclass` | 把 env 写进部署清单并加启动日志断言；`retention.go:1315` 现有日志只打「disabled」，建议补打实际生效的 Retention 值 |
| **3** | **分区数量随时间增长** | 保留期配置意外变大；或历史分区因各种原因没被 DROP | 目录查询变慢、规划期开销上升、catalog 膨胀 | 同上分区计数 | 保留期封顶（如 ≤ 14 天）；对 > 30 天的分区配置告警 |
| **4** | ★ **DROP 的锁** | 清理 worker 执行 `DROP TABLE` | 需父表 + 子表的 ACCESS EXCLUSIVE。与之冲突的只有**写向该子分区的事务**——而被 DROP 的分区上界已早于保留期，**无活跃写入** ⇒ 正常情况下不阻塞任何写入 | `pg_locks` 查 `not granted`；`lock_timeout` 超时会在日志里留痕 | 沿用既有脚本的 `SET lock_timeout='5min'`（`pg17-drop-old-columnar-partitions.sh:66`）；保持 1 小时一次的低频 |
| **5** | **autovacuum 行为变化** | 分区化后 | 每分区独立触发，**触发次数变多、每次工作量变小**（提案 §10.4 称 autovacuum 调参可撤，属【推断】，需观察）。★ **另有一处易被忽略的破坏**：分区父表自身的 `pg_stat_user_tables` 行几乎为 0（父表不存行），**任何按 `relname='ursm_node_snapshot_min'` 过滤的监控/看板会静默归零** | `pg_stat_user_tables WHERE relname LIKE 'ursm_node_snapshot_min%'` | 监控口径改为「按 `pg_inherits` 汇总所有子分区」；见 §7 验证方案给出的 SQL |
| **6** | **分区表上的 UPDATE** | 未来若出现改 `snapshot_ts` 的 UPDATE ⇒ PG 走 DELETE+INSERT **跨分区移动**（本设计最大的性能地雷）；若 UPDATE 不改 `snapshot_ts`（如 `sql/fixes/2026-09-20-canonical-dedup-cleanup.sql:17` 那种只改 `canonical_name`）⇒ 行留在原分区，但仍会**重写整行** | 未来的一次性数据修复可能变成 9 GB 级别的重写 | 审计脚本执行前后比对 `n_tup_upd` | 在表上加 `COMMENT ON TABLE` 注明「改 `snapshot_ts` 会触发跨分区移动」；若将来确需批量 UPDATE，按分区循环（`UPDATE ... WHERE snapshot_ts ∈ [d, d+1)`）以限制 bloat |
| **7** | **迁移期临时空间** | 观察期内 `_legacy`（10 GB）与新表（4~7 GB）并存 | 磁盘峰值 **14~17 GB**。★ 磁盘可用空间本次**未核实**（提案 §10.6 写「可用 102 GB」，我无法复核） | `SELECT pg_size_pretty(pg_database_size('llm_gateway'));` + `df -h` | 执行前实测可用空间并写入工单；`_legacy` 满 7 天即 DROP |
| **8** | **审计脚本语义漂移** | `.db-audit/sql/*.sql` 与 `sql/fixes/*` 里的表名引用 | 审计漏读 `_legacy`；分区化后 `count(*)` 只覆盖现存分区 | 人工复核 | 把 §5.1 列出的 6 个审计脚本位置纳入改动清单 |
| **9** | **基线 schema 漂移** | 本次只改分区形态，818 的 24 列仍不进基线 | 下次基线重建会得到一张**缺 24 列**的分区表 | `diff` 两份 01-schema.sql（当前 md5 相同，说明两份同步，但都缺 24 列） | 见 §9 Finding A 与 §10 第 6 问 |

---

## 8. 验证方案

改造后如何**证明它真的 work**，而不是「看起来正常」。

### 8.1 ★ 核心验收：稳态体积与每日归还

**目标值**：稳态总占用 **4~7 GB**（口径见 §9 Finding E），且**每天净减少约一天的量**。

```sql
-- Q1: 父表 + 全部子分区的总占用（★ 注意：不能只查父表，PG 的
--     pg_total_relation_size('ursm_node_snapshot_min') 对分区父表返回近乎 0）
SELECT pg_size_pretty(SUM(pg_total_relation_size(c.oid))) AS total,
       COUNT(*)                                   AS partitions
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass;
```

**连续 3 天各跑一次 Q1**，`total` 应当稳定在同一个量级（±一个分区的抖动），
而不是像今天这样单调爬升。

### 8.2 ★ 证明空间真的还给了操作系统

**这是本改造唯一真正要证明的事**，分三层，缺一不可：

```sql
-- L1: PG 侧记账 —— DROP 后表/分区目录项消失
SELECT c.relname, pg_size_pretty(pg_total_relation_size(c.oid))
FROM pg_class c WHERE c.relname LIKE 'ursm_node_snapshot_min_2%' ORDER BY 1;

-- L2: 库级记账 —— pg_database_size 下降
SELECT pg_size_pretty(pg_database_size('llm_gateway'));
```

```bash
# L3: ★ 最关键的一层 —— 操作系统层面的实际可用空间
# 在 252 上执行（容器内 + 宿主机各一次）
docker exec llm-gateway-pg df -h /var/lib/postgresql/data
df -h <数据目录挂载点>
```

> ★ **只看 `pg_class` / `pg_database_size` 是不够的**——
> 那只证明 PG 的记账变了。必须用 `df` 证明**文件系统真的腾出了空间**。
> 反过来说，如果 DROP 后 `pg_database_size` 降了但 `df` 没动，
> 那就是 PG 数据目录和实际挂载点不是同一个卷，需要先查清再上线。
> （**前提：`df` 的可用空间受同盘其他写入影响，短窗口内可能被噪声淹没。
> 建议在 DROP 前后 60 秒内连测，或选一次其他写入最少的窗口。**）

### 8.3 每日归还量

```sql
-- 每个分区各占多少（每天应新增一个、消失一个）
SELECT c.relname,
       pg_size_pretty(pg_total_relation_size(c.oid))  AS size,
       to_timestamp(regexp_replace(c.relname, '.*_([0-9]{8})$', '\1'), 'YYYYMMDD') AS part_date
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass
ORDER BY c.relname;
```

配合 §8.1 的连续 3 天曲线，应看到**锯齿状**：每天涨一个分区、每天退一个分区，
净值持平——而不是今天的单调爬升。

### 8.4 逐行成本（★ 用来结算 §9 Finding E 的口径分歧）

```sql
-- 限单个分区（有界，不扫全表）
SELECT (SELECT count(*) FROM ursm_node_snapshot_min WHERE snapshot_ts >= '2026-10-03 00:00+08'
                                                 AND snapshot_ts <  '2026-10-04 00:00+08') AS rows;
-- 配合该分区的 pg_relation_size 相除，得到**当日实测 B/行**
```

判定标准：
- 若 **≈ 220 B/行** ⇒ 818 的 payload 瘦身确实生效了，§9 Finding E 的「提案口径」成立，稳态 4 GB 级别；
- 若 **≈ 450~512 B/行** ⇒ 瘦身没生效或死元组堆积，稳态 6~7 GB，**提案的 3.6 GB 是乐观值**。
- ★ **务必在 DROP 之后、autovacuum 跑完之前/之后各测一次**，以区分「死元组」与「活行本身大」。
  对比方法：`SELECT n_dead_tup, n_live_tup FROM pg_stat_user_tables WHERE relname LIKE 'ursm_node_snapshot_min_2%';`

### 8.5 行为等价性验证

```sql
-- ① 写入速率未变（应 ≈ 1,253~1,259 行/分钟）
--    对比：切换前 1 小时的 n_tup_ins 与切换后 1 小时的 n_tup_ins
SELECT relname, n_tup_ins, n_tup_del, n_tup_upd, n_live_tup, n_dead_tup
FROM pg_stat_user_tables WHERE relname LIKE 'ursm_node_snapshot_min%' ORDER BY relname;

-- ② 冲突去重仍然生效（分区化不应改变 ON CONFLICT 行为）
--    同一 snapshot_ts 上不应出现重复 PK
SELECT snapshot_ts, tenant_id, credential_id, raw_model_name, count(*)
FROM ursm_node_snapshot_min
WHERE snapshot_ts >= '2026-10-03 00:00+08' AND snapshot_ts < '2026-10-04 00:00+08'
GROUP BY 1,2,3,4 HAVING count(*) > 1;
-- 期望：0 行

-- ③ 留存仍在工作（观察期结束后验证）
SELECT count(*) FROM pg_inherits i
JOIN pg_class c ON c.oid=i.inhrelid
WHERE i.inhparent='ursm_node_snapshot_min'::regclass
  AND ((substring(c.relname FROM '_([0-9]{8})$')::date)::timestamp AT TIME ZONE 'Asia/Shanghai')
      + interval '1 day' <= now() - interval '7 days';
-- 期望：0 行（无过期分区堆积）
```

---

## 9. ★ 核实结论：与提案 §10 不符 / 提案未覆盖之处

以下 6 条是本人逐条 read 源码后与提案 §10 的差异。**A/D/E 三条会影响评审结论，建议优先讨论。**

### Finding A — 提案说「改两处」，实际是三处，且基线早已漂移

- **提案 §10.2/§10.6 说**：`deploy/sql/schemas/baseline/01-schema.sql:18198`（CREATE TABLE）
  与 `:22634`（PK 约束）**两处**要同步。
- **源码事实**：
  1. 该文件里本表还有**第三处**——`:27486` 的
     `CREATE INDEX ursm_node_snapshot_min_ts_idx`。提案没提。是否保留要一起决定（§3.6 建议不建）。
  2. `installer/cmd/llm-gw-installer/embeddata/01-schema.sql` 与基线文件
     **md5 完全相同**（`904f4b531e79555591c45880d0ffae80`），
     **两份都必须改**，否则安装器自建库会与基线不一致。提案没提。
  3. ★ 基线的 32 列**本身就落后于生产**：818 迁移新增的 24 列从未回写进基线
     （`writer.go:383-402` 写 46 列，基线只有 32 列）。
     ⇒ 基线重建出来的表**装不下 writer 的 INSERT**。
     这是本次改造之前就存在的问题，但既然都要动这个文件，应一并修。
- **结论**：**「两处」的说法不准确**，应为「两份文件的至少三处」。

### Finding B — 提案按 7 天留存测算，但代码默认值是 30 天

- **提案 §10.3** 的全部容量测算（7 天稳态 3.6/7.2 GB）都建立在「保留期 = 7 天」上。
- **源码事实**：`retention.go:41` `Retention: 30 * 24 * time.Hour`。
  7 天**只来自环境变量** `URSM_SNAPSHOT_RETENTION_DAYS`（`retention.go:49-60`）。
- **影响**：任何一次 env 丢失/清空（重启、部署模板漏配、容器重建），
  保留期静默变 30 天 ⇒ 按日分区的稳态从 8 个分区变成 31 个 ⇒
  **体积回到接近今天的 10 GB 量级，分区化等于白做**。
  这是分区化方案下**最可能发生的失败模式**，且是静默的。
- **结论**：提案**未提及**这一风险。必须把 env 固化为部署契约并加启动断言（§7 风险 2）。

### Finding C — 提案把按日说成偏离本仓库惯例，实际本仓库已有按日分区先例

- **提案 §10.3**：「分区粒度：建议**按日**，不是沿用月度」，
  并在 §10.1 列出 `auto_route_selections` / `cache_metrics` / `candidate_failure_logs` /
  `credential_model_index` **全为月度**作为「既定模式」。
- **源码事实**：`usage_facts` **已经是按日分区**（迁移 750 + 751 时区钉扎），
  并已接入 `bg/partition_manager.go:1302`（`partitionUnit: "day"`）。
- **影响**：方向上**提案的结论仍然正确**（按日对 7 天留存是对的），
  但**论据错了**，且**错过了现成范式**。
  更关键的是，750 的头注释记录了按日分区的**三个真实踩坑**
  （时区错位 8h、DEFAULT 约束拒绝导致 boot 打进 no-DB 并触发自动回滚、并发 ensure），
  提案一个字都没提。按日分区在本仓库不是「零风险新做法」，而是「**已踩过一轮的成熟做法**」。
- **结论**：**「不是沿用月度」的前提不成立**；正确表述是「沿用 750/751 的按日范式」。

### Finding D — 「1.83× 写放大」与「双写使行数翻倍」不能同时成立

- **提案 §10.3** 的表按「双写 = 3,614,400 行/天」「单写 = 1,807,200 行/天」测算，
  即认定**双写会让行数翻倍**。
- **源码事实**：`writer.go:402` 是 `ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name) DO NOTHING`。
  154 与 245 写的是**同一批 1,292 个节点、同一组 PK 四列**（`retention.go:182-183` 注释：
  「同一 snapshot_ts 上约有 1,008 行（一次 flush 全量落盘全部组合）」）。
  ⇒ **PK 相同的行，输的那一方插入 0 行**。
- **推断**：**堆里的行数增速应当是 1×，不是 2×**。
  「1.83×」更可能体现在 **INSERT 尝试次数 / 索引探测 / WAL** 上，
  而不体现在堆页数上。
- **佐证（也是反证）**：实测每批 1,253~1,259 行 ≈ 1,292 个节点的一次 flush，
  这本身就说明**行增速是单写的量级**。
- ★ **但有一个数字对不上，必须查清**：1,871.9 万行 ÷ (1,259 行/分 × 1,440 分/天)
  ≈ **10.3 天**，而保留期是 7 天（稳态应为 ~1,270 万行）。**多出的 600 万行从何而来？**
  两种可能：(a) 实测行数含大量死元组未回收；(b) 保留期实际未稳定在 7 天。
  这直接决定 §8.4 的 B/行 口径。
- **结论**：提案的容量表**除以 2 的那一步依据不足**。**必须在评审会上用一条
  O(1) 只读 SQL 判定**（见 §10 第 2 问），否则容量收益会被高估一倍。

### Finding E — 提案的 3.6 GB 与同日实测的堆/行 差 2.3 倍

- **提案 §10.3**：双写 堆/天 ~771 MB ÷ 3,614,400 行 = **223 B/行**；据此得单写稳态 ~3.6 GB。
  223 B/行 这个数来自 `writer.go:99-101` 的实测声称：
  「payload 215 → 34 B/行，整行 402 → 220 B（-45.1%）」。
- **源码事实（同日）**：`818_ursm_snapshot_typed_columns.sql` 头注释记录
  「表 heap 8,768 MB / 19.9M 行 = **441 B/行**」；
  而 §1.1 的实测是堆 9,129 MB / 1,871.9 万行 = **512 B/行**。
  ⇒ **实测的 512 B/行 是提案采用的 223 B/行 的 2.3 倍。**
- **推断（合理解释）**：9,129 MB 里含**死元组**（§1.3 已证明 DELETE 留下大量空页），
  而 220 B/行 描述的是**活行**。两者不是同一口径。
  分区化后死元组趋近 0，所以**用 220 B/行 预测稳态在方向上是对的**——
  但这是一个**未经实测的关键假设**，而且它同时依赖 Finding D 的行数口径。
- **结论**：**「稳态 3.6 GB」是【推断】，不是实测**，
  合理区间应写成 **4~7 GB**。评审时应把它当**待验证假设**而非既定结论，
  并按 §8.4 在改造后实测结算。

### Finding F — 提案 §10.5①「全仓无任何 SELECT」需限定

- **源码事实**：Go 侧零读路径 ✔（§5.1 完整 grep 结果）。
  但 `sql/fixes/2026-09-20-canonical-dedup-cleanup.sql:17` 存在对本表的 **UPDATE**，
  `.db-audit/sql/` 下有 6 处审计查询。
- **影响**：对「不需要拷 1,871.9 万行」这个结论**没有影响**（都是一次性/人工脚本）。
  但这些位置需要在改造时同步更新，否则审计会漏读（§7 风险 8）。

---

## 10. ★ 评审时需要拍板的问题

以下 8 点**无法从源码确定**，必须由人决定。

1. **保留期以哪个为准？** 代码默认 30 天（`retention.go:41`），252 环境变量是 7 天。
   是否把 7 天固化为部署契约 + 启动断言？若是，稳态分区数锁死为 8~10；
   若允许运维临时调大到 30 天，容量上限要按 13~19 GB 预留。（Finding B）

2. **行数到底是不是双写翻倍？** 这是 §9 Finding D。请在 252 上跑一条 O(1) 只读 SQL 判定：
   ```sql
   -- 判定：若 ins 尝试数 ≈ 落库行数的 1.83 倍，则双写在"尝试"层面翻倍、在"堆"层面不翻倍
   SELECT relname, n_tup_ins, n_tup_upd, n_tup_del,
          (SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
           WHERE c.relname LIKE 'ursm_node_snapshot_min%') AS idx
   FROM pg_stat_user_tables WHERE relname LIKE 'ursm_node_snapshot_min%';
   ```
   以及 10.3 天 vs 7 天的行数差额去哪了（死元组？还是保留期没稳在 7 天？）。
   **这个答案直接决定 §8 的容量收益是 3.6 GB 还是 7.2 GB。**

3. **稳态容量目标按哪个口径对外承诺？** 3.6 GB（提案，实测活行 220 B/行）
   还是 4~7 GB（含 bloat 保守区间）？（Finding E）

4. **要不要建 DEFAULT 分区？** 本稿建议**不建**（§3.6，理由：黑洞 + ATTACH 变慢 + 静默丢数优于响亮失败），
   代价是「缺分区 = 写入全线中断」。若评审决定要，则必须同时接受
   ① 移植 750 的 move-then-attach 整套、② 给 `_default` 单独加「有行即报警」的监控、
   ③ 承担 ATTACH 的全表校验开销。**这是本设计最需要拍板的一点。**

5. **`_legacy` 保留 7 天还是更短？** 切换前 7 天的历史有没有**人工审计需求**？
   若没有，可以 1~3 天后即 DROP（省下 10 GB 冻结占用），但**代价是提前失去回滚能力**（§6 阶段 B）。
   同时请确认：`_legacy` DROP 之前是否要求再做一次 `pg_dump` 留档？

6. **基线 schema 缺 818 的 24 列，本次修不修？**（Finding A）
   这是**改造前就存在的漂移**，与分区化无关。同一文件、同一批次改完最省事，
   但会扩大本次变更的评审面。**倾向：本次一并修**，请确认。

7. **`ursm_node_snapshot_min_ts_idx` 删还是留？** 本稿建议**不建**（§3.6：留存改 DROP 后无任何查询用它）。
   但要注意它是**已存在的生产索引**，删除本身是一个 DDL 动作。
   另外：基线 `:27486` 那一行是保留还是删除，决定了以后基线重建会不会又长出来。

8. **监控口径改造归谁？**（§7 风险 5）分区父表的 `pg_stat_user_tables` 行几乎为 0，
   所有按表名过滤的看板/告警会**静默归零**——这是分区化的经典副作用，
   且不报错。需要确认由谁负责把这些查询改成按 `pg_inherits` 汇总。

---

## 11. 附：改动清单速查

| # | 文件 | 动作 | 依据 |
|---|---|---|---|
| 1 | 新增迁移 `82x__ursm_snapshot_min_partition.sql` | 建 ensure 函数 + 预建当日/次日/后日分区 | §3.5 |
| 2 | 新增迁移（下一步） | `RENAME` + 建父表 + 首个分区（单事务） | §5.2 |
| 3 | `domains/ursm/v2/persist/retention.go` | `deleteBatch` → `dropExpiredPartitions`；删游标/退出条件；`BatchSize` 标 deprecated；日志改 `partitions_dropped` + `bytes_reclaimed` | §4.2 |
| 4 | `bg/partition_manager.go`（`ensureSpecs()`，`:1227`） | 加 `ursm_node_snapshot_min (daily)` 条目 | §4.4 |
| 5 | `db/db.go` | boot 期 ensure 兜底 + `ALTER FUNCTION SET timezone` | §4.4 |
| 6 | `deploy/sql/schemas/baseline/01-schema.sql` | `:18198` 建表改 `PARTITION BY RANGE (snapshot_ts)` 并补 818 的 24 列；`:22635` PK 保留；`:27486` 索引删除 | Finding A / §3.6 |
| 7 | `installer/cmd/llm-gw-installer/embeddata/01-schema.sql` | 同上（两份 md5 相同，必须同步） | Finding A |
| 8 | `domains/ursm/v2/persist/writer.go` | **不改** | §4.1 |
| 9 | `cmd/gateway/main.go` | **不改**（门控与 schema 无关） | §4.3 |
| 10 | `.db-audit/sql/{04_slow2,07_dist2,09_final,10_cols,23_crashlog}.sql`、`sql/fixes/2026-10-02-db-storage-reclaim.sql:269` | 适配分区表查询口径 | §5.1 / 风险 8 |
| 11 | 部署清单 | 固化 `URSM_SNAPSHOT_RETENTION_DAYS` | 风险 2 |
