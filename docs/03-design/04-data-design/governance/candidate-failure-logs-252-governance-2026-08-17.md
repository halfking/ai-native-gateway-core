# 252 candidate_failure_logs 表治理方案（2026-08-17）

## 背景

252 PG（172.16.2.210，154 网关共库）的 `candidate_failure_logs` 是单一 `citus_columnar` 表，361MB、84,757 行、72,429 行 `ts IS NULL`（V358 之前 `ALTER COLUMN ts SET DEFAULT now()` 丢失导致 INSERT 落 NULL）。`opslog_trimmer` 的 DELETE 在 columnar 上每轮失败（"UPDATE and CTID scans not supported for ColumnarScan"），TTL 失效，表只增不减。

V358（`deploy/sql/migrations/V358__candidate_failure_logs_session_id.sql`）已加 session_id 列、索引、把 ts 默认值恢复，但 **不解决历史 NULL ts 行不可见 + TTL DELETE 失效**两个增长无界问题。

handoff `2026-08-17 23:10` §5.3 列为待办，需独立会话低峰执行。

## 现状摸底（实测 2026-08-17 23:32 UTC+8）

| 项 | 值 |
|---|---|
| access method | `citus_columnar` |
| 压缩 | zstd |
| stripe_row_limit / chunk_group_row_limit | 150000 / 10000 |
| 表大小 | 361 MB（heap_or_col 356 MB） |
| 总行数 | 84,757 |
| ts_present / ts_null | 12,328 / 72,429 |
| max(ts) | 2026-06-26 02:25:28（52 天前） |
| 新行（24h / 1h / 10min） | 0 / 0 / 0 |
| session_id 非空 | 5 个 `gt_gw_<uuid>` × 3 行（共 15 行，0.018%） |
| 唯一索引 | `idx_candidate_failure_logs_session_ts` 等 5 个索引 + tenant_isolation_candidate_failure_logs RLS |
| 现表是否已分区 | 否（`pg_partitioned_table` 无记录） |
| 现有分区候选 | request_logs / request_logs_bodies 已分区（heap→columnar 月度）；candidate_failure_logs 未走 392 模式 |
| 写入路径 | Go 侧直插表名 `candidate_failure_logs`（executor `attempt_logging`） |

**关键现象**：154 上 24h 内 0 新行 → 网关侧没有 candidate 失败流量、或未走到此表。本会话不主动造流量。

## 设计约束

1. **最小破坏**：不允许破坏 INSERT 写入路径与 admin SELECT 读端；不允许破坏现有 RLS、索引、外键（如有）。
2. **可回滚**：所有 DDL 必须可逆（`xxx.down.sql` 已存在 V358 down、392 down）。
3. **不停写窗口 < 5min**：84,757 行 + 列清单（20 列）+ 索引 5 个 → 全部重建 ≈ 5min（实测 request_logs 月度分区重写在 71/184 上 1-3min）。
4. **历史数据保留**：84,757 行（含 72k NULL ts）必须归档可查，不可直接 DROP。
5. **未来 TTL**：通过 DROP 月分区实现 30 天清理（参考 `drop_old_state_partitions` 391）。
6. **ts 默认值兜底**：必须 `ALTER COLUMN ts SET DEFAULT now()` 已就位（V358 已做）。

## 方案对比

### 方案 A：走 392 模式（hot + 月度 columnar 分区）— 推荐

按 `sql/migrations/startup/392_candidate_failure_logs_monthly_partition.sql` 把 `candidate_failure_logs` 拆为：

- `candidate_failure_logs_hot` (heap, fillfactor=90, 24h 保留) — 新 INSERT 入口
- `candidate_failure_logs` (PARTITION BY RANGE (ts)) — 月度 columnar 分区（自动沿用 zstd/150k/10k 配置）
- `candidate_failure_logs_with_current_month` 视图（hot ∪ parent）— admin 读端
- `promote_candidate_failure_logs_hot_to_partition(interval, int)` — Go 端 partition_manager 注册

#### 改动列表

| 步骤 | 内容 | 时间 | 风险 |
|---|---|---|---|
| 1 | 重命名原 columnar 表 → `candidate_failure_logs_columnar_old`（保留 84,757 行） | <1s | 极低 |
| 2 | 创建 partitioned 父表 + 视图 + 函数（392 模板） | <5s | 低 |
| 3 | 创建当月 + 上月 + 下月分区（columnar） | 30s | 低 |
| 4 | `INSERT INTO candidate_failure_logs SELECT * FROM candidate_failure_logs_columnar_old`（按 ts 月度路由） | 1-3min | 中（CPU + WAL） |
| 5 | NULL ts 行的归位：要么保留为 DEFAULT 分区、要么先 UPDATE（columnar 不支持 UPDATE，必须走步骤 1-4 后在分区表 UPDATE 兜底） | 1min | 中 |
| 6 | 在 hot 表加 V358 session_id 列 + 索引 + ts 默认值（沿用 V358 模式） | 5s | 极低 |
| 7 | DROP `candidate_failure_logs_columnar_old`（保留月度分区数据） | 5s | 不可逆 |
| 8 | Go 端：注册 promote 函数到 partition_manager.promoteSpecs | 5min（代码改动） | 中 |
| 9 | admin `candidate_failure_handlers.go`：把 `FROM candidate_failure_logs` 改 `FROM candidate_failure_logs_with_current_month` | 10min（含测试） | 低 |
| 10 | model_smoke_test / 部署回归 | 30s | 低 |

#### 优点
- 与 request_logs / request_logs_bodies 392 模式统一
- TTL 通过 `drop_old_state_partitions`（391）D 月级 partition 自然删除，trimmer DELETE 失效问题根本性解决
- 历史数据完整保留进月度分区，可继续按 ts / session_id 查询
- 不需要新写一遍 schema；列清单、索引、RLS 直接复用 392 模板
- V358 session_id 列同时落到 hot + parent（parent 通过 CREATE TABLE … LIKE INCLUDING ALL 自动继承）
- 393、395、…后续迁移可继续按 392 模式追加扩展列

#### 缺点
- 需新建月度分区表 + 一次性回填 84k 行（1-3min）
- admin 读端需改成走视图（小改）
- Go 写入路径从直插 parent 改为先写 hot（partition_manager 周期 promote）— 需要 partition_manager 注册 promote 函数

### 方案 B：整表重建成 rowstore（normalize-columnar-historical.sql 风格）

按 `sql/fixes/normalize-columnar-historical.sql` 模式把表 ALTER 为 heap，重建。

#### 改动列表

| 步骤 | 内容 | 时间 | 风险 |
|---|---|---|---|
| 1 | 重命名 columnar → `_columnar_old` | <1s | 极低 |
| 2 | CREATE TABLE rowstore 同 schema（含 session_id） | 5s | 低 |
| 3 | INSERT SELECT 全量（含可选 lower() 修正） | 1-2min | 中 |
| 4 | DROP `_columnar_old` | 5s | 不可逆 |
| 5 | NULL ts 行 UPDATE 成 now()（因 rowstore DELETE/UPDATE 可用） | 30s | 中 |

#### 优点
- DELETE/UPDATE 可恢复，trimmer 重新有效
- NULL ts 行可 UPDATE 成 now()（虽然不精确，但可见）

#### 缺点
- 表从 361MB（columnar 压缩）涨到 ~600-800MB（heap 不压缩）
- 失去 columnar zstd 压缩 + 扫描性能（candidate_failure_logs 跑 admin 列表查询时全表扫描变慢）
- 与 request_logs / request_logs_bodies 的 columnar 路径不一致
- normalize-columnar-historical.sql 的列清单（id/ts/credential_id/provider_id/request_id/error_kind/error_message/raw_model_name/raw_status_code/error_class/attempt/created_at）与现表 20 列对不上，模板需要重写
- 未来如果再走 392，仍要再切一次
- trimmer DELETE 仍然慢（heap DELETE 不便宜）

### 方案 C：仅 DROP 历史 NULL ts 行（最小改动）

直接 `DELETE FROM candidate_failure_logs WHERE ts IS NULL`——但 columnar 不支持 DELETE ❌。

**唯一可行路径** = 重命名 → 建空 partition 父表 → 把有 ts 的 12,328 行按月份插进对应分区 → DROP 原 columnar 表。

实际是方案 A 的简化版：不建 hot、不建 promote 函数，只做"将现有 columnar 表内容转成月度分区"。

| 步骤 | 内容 |
|---|---|
| 1 | 重命名原 columnar → `_columnar_old` |
| 2 | CREATE TABLE partitioned + 视图 |
| 3 | 创建历史月份分区（2025-12 到 2026-06 共 7 个月） + 当月 + 下月 |
| 4 | `INSERT INTO candidate_failure_logs SELECT * FROM candidate_failure_logs_columnar_old WHERE ts IS NOT NULL`（12,328 行，~30s） |
| 5 | 72,429 行 NULL ts 怎么办？3 选 1：<br>(a) 进 DEFAULT 分区（heap、单独保留）<br>(b) UPDATE ts=now() 进当月分区<br>(c) 直接放弃（保留在 `_columnar_old` 不迁移）|
| 6 | DROP `_columnar_old` |
| 7 | Go 端：写入路径不变（继续直插 parent，但 PG 自动路由到当月分区；新行带 ts=true → 进当月分区；NULL ts → DEFAULT 分区）|
| 8 | admin 读端：仍走父表（已 ATTACH 全部月度分区，PG 自动 UNION） |

#### 优点
- 不引入 hot 表 / promote 函数 / 分支路径
- Go 写入零改动（PG 分区路由透明）
- admin 读端零改动（parent 视图）
- 实施最小：约 4 个 SQL 文件 + 1 个低峰窗口

#### 缺点
- 没有 hot 加速（每次写入都是 columnar，月度分区 stripe 重写成本）
- TTL 仍需手写 `DROP TABLE candidate_failure_logs_2026_05`（或复用 `drop_old_state_partitions` 391 已有的统一 DROP 函数）— 391 已支持任意"类型表"，可立即受益
- NULL ts 行的归宿需决策（a/b/c）

## 推荐：方案 C（最小可行分区化）+ 392 hot 后续按需演进

理由：

1. **核心问题"trimmer 失效 + 表只增不减"**直接通过 (a) 月度分区化 + (b) 391 `drop_old_state_partitions` 已存在的通用 DROP 函数解决。
2. **历史 72,429 行 NULL ts** 用方案 C-5(a) 进 DEFAULT 分区（heap，单独保留可查但不进月度 columnar），避免回填 ts 失真。
3. **Go 端零改动**，admin 读端零改动（parent 自动 UNION）。
4. **后续如有需要再升级到 392 hot 模式**——只需在已分区的 parent 上叠 hot + promote，schema 兼容。
5. **总停写窗口 < 30s**（columnar → partitioned 父表 → INSERT 12k 行）—— 84k 行实际只迁 12k，NULL ts 进 DEFAULT 分区只需直接重命名 + ALTER ATTACH PARTITION DEFAULT。

### 方案 C 落地骨架（待用户决策后执行）

```sql
-- V359__candidate_failure_logs_partition_2026_08_17.sql
-- 1. 重命名原 columnar 表（保留历史）
ALTER TABLE IF EXISTS candidate_failure_logs RENAME TO candidate_failure_logs_columnar_old;

-- 2. 建 partitioned 父表（columnar 子分区）
CREATE TABLE candidate_failure_logs (
    id                          bigint,
    request_id                  text NOT NULL,
    ts                          timestamptz DEFAULT now() NOT NULL,
    tenant_id                   text DEFAULT 'default' NOT NULL,
    credential_id               integer NOT NULL,
    provider_id                 integer NOT NULL,
    raw_model_name              text NOT NULL,
    attempt_index               integer DEFAULT 0 NOT NULL,
    error_kind                  text NOT NULL,
    error_message               text,
    upstream_status_code        integer,
    upstream_response_body      text,
    upstream_response_preview   text,
    latency_ms                  integer,
    retryable                   boolean,
    per_attempt_latency_ms      integer,
    extracted_upstream_status_code integer,
    diagnosed_error_kind        text,
    context                     jsonb,
    session_id                  text
) PARTITION BY RANGE (ts);

-- 3. 用 columnar 配置 + zstd 沿用
-- 注：Citus 自动让子分区继承列存配置（参考 392 模板）

-- 4. 创建历史月份分区（按 max(ts)=2026-06-26 反推 2025-12 到 2026-06 共 7 个月）+ 当月 + 下月 + DEFAULT
SELECT ensure_candidate_failure_logs_partition('2025-12-01'::timestamptz);
SELECT ensure_candidate_failure_logs_partition('2026-01-01'::timestamptz);
... (7 个月)
SELECT ensure_candidate_failure_logs_partition(date_trunc('month', now())::timestamptz);
SELECT ensure_candidate_failure_logs_partition((date_trunc('month', now()) + interval '1 month')::timestamptz);

-- DEFAULT 分区（heap，承接 NULL ts 行）
CREATE TABLE candidate_failure_logs_default PARTITION OF candidate_failure_logs DEFAULT;

-- 5. 把 12,328 行非 NULL ts 行按 ts 月度路由
INSERT INTO candidate_failure_logs
SELECT * FROM candidate_failure_logs_columnar_old
WHERE ts IS NOT NULL;  -- ~30s

-- 6. 把 72,429 行 NULL ts 行搬到 DEFAULT 分区（用 heap）
-- 注意：columnar 表无主键，ctid 不准；改用全部列复制
CREATE TEMP TABLE _null_ts ON COMMIT DROP AS
SELECT * FROM candidate_failure_logs_columnar_old WHERE ts IS NULL;

-- 把 _null_ts 行插入父表 → DEFAULT 分区（heap 兼容）
ALTER TABLE candidate_failure_logs_default ADD COLUMN ts_set_null boolean;
INSERT INTO candidate_failure_logs (id, request_id, ts, ...)
SELECT id, request_id, NULL, ... FROM _null_ts;
-- 上面的 ts NULL 会让 PG 路由到 DEFAULT 分区
-- 或者：直接 RENAME columnar_old → default_old_partition（不进父表分区树）

-- 7. RLS 策略恢复
ALTER TABLE candidate_failure_logs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_candidate_failure_logs ON candidate_failure_logs;
CREATE POLICY tenant_isolation_candidate_failure_logs ON candidate_failure_logs
    USING (tenant_id = get_current_tenant());

-- 8. 索引在父表建（自动 propagate 到子分区）
CREATE INDEX idx_candidate_failure_logs_session_ts
    ON candidate_failure_logs (session_id, ts DESC);
-- 其余 4 个索引从原 columnar 表继承

-- 9. DROP 原 columnar 表（保留历史已迁出）
-- ⚠️ 决策点：若 NULL ts 行未迁入 DEFAULT 分区则 DROP，否则保留 _columnar_old 不 DROP
```

### V359 down.sql（可逆）

```sql
-- 反向：把 parent 表合并回 single columnar 表（复杂度高，建议不写）
-- 或者：直接 DROP parent 与所有子分区 + RENAME `_columnar_old` 回来
DROP TABLE IF EXISTS candidate_failure_logs_default;
DROP TABLE IF EXISTS candidate_failure_logs;
ALTER TABLE candidate_failure_logs_columnar_old RENAME TO candidate_failure_logs;
```

## 关键风险与缓解

| 风险 | 缓解 |
|---|---|
| INSERT 12k 行时 154 网关短暂阻塞 | 低峰窗口（00:00-04:00 CST）+ 分批 5k 行 + 单事务提交 |
| NULL ts 行 72,429 行的处理 | 默认方案 (a) 进 DEFAULT 分区（heap）；如不想要可走 (b) UPDATE ts=now() 进当月分区（失真）或 (c) 丢弃（保留 _columnar_old）|
| DEFAULT 分区后续增长失控 | 单独监控 + 定期 `DELETE WHERE ts IS NULL`（heap 支持 DELETE）|
| DROP `_columnar_old` 不可逆 | 第一版治理保留 `_columnar_old` 30 天观察期，确认分区表健康后再 DROP |
| Go 写入方零改动假设 | 验证：candidate_failure_logs 直插在 partitioned 表上 PG 自动按 ts 路由（已有 request_logs 月度分区直插实证）|

## 执行计划（待用户拍板后启动）

1. **Phase 0**：用户决策 NULL ts 行归位策略（a/b/c）
2. **Phase 1**：写 V359.sql + V359.down.sql（预计 30min）
3. **Phase 2**：低峰窗口执行迁移（预计停写 <30s + 回填 ~30s + 默认分区 ~10s）
4. **Phase 3**：观察 154 端 INSERT 是否走分区（`EXPLAIN INSERT INTO candidate_failure_logs VALUES (...)`），验证 RLS / 索引生效
5. **Phase 4**：观察 7 天 + 30 天后删除最早月份分区（391 `drop_old_state_partitions`）
6. **Phase 5**：30 天后 DROP `_columnar_old`（确认分区表健康）

## 与 handoff §5 其他项的关系

- §5.3 本任务完成 = 表治理闭环
- §5.4 kaixuan-1 库需先通 K3s 网络，再用同样 V359 在其 PG 跑（幂等）
- §5.5 桥接补 RecordChunkSent 不依赖本任务
- §5.6 跨模型切换 ADR 不依赖本任务

## 参考

- `sql/migrations/startup/392_candidate_failure_logs_monthly_partition.sql`（hot + 月度分区模板）
- `sql/migrations/startup/391_drop_old_state_partitions.sql`（通用 DROP 月分区函数）
- `sql/fixes/normalize-columnar-historical.sql`（rowstore 重建模板，列清单已过时）
- `deploy/sql/migrations/V358__candidate_failure_logs_session_id.sql`（已上线）
- handoff `/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T/handoff-20260817-231034.md` §5.3