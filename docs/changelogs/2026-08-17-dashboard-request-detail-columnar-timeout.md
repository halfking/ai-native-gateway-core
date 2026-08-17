# 2026-08-17 — dashboard 实时请求流 → 点击请求 报 `query failed`

## 现象

`https://llmgo.kxpms.cn/dashboard` 实时请求流里点击任意请求，详情抽屉报 `query failed`。
多次重试都会失败，影响所有 dashboard 用户。

## 倒查路径

1. **前端 → API**：浏览器 `RequestLogDrawer.vue:68` 调 `getRequestLogDetail(id)` →
   `GET /api/logs/{request_id}`。
2. **API → 服务端**：`admin/handler.go:980` `mux.HandleFunc("/api/logs/", admin(h.handleLogs))`，
   `handleLogs` 末尾 `getLog`。
3. **服务端 → DB**：`admin/logs.go:787-799` `getLog` 跑主查询
   `SELECT ... FROM request_logs_with_current_month rl
    LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id
    WHERE rl.request_id = $1`
4. **DB → planner**：
   `pg_stat_statements` + `EXPLAIN ANALYZE` 在 154 实测，body 视图扫描 30s+ timeout。

## 根因（154 上 EXPLAIN ANALYZE 证据）

`request_logs_bodies_with_current_month` 是 UNION ALL 视图：

```sql
SELECT … FROM request_logs_bodies_hot
UNION ALL
SELECT … FROM request_logs_bodies   -- partition (columnar)
```

- `request_logs_bodies_hot`：heap 表，`(request_id)` UNIQUE idx，单 ID 查询 **0.4ms**。
- `request_logs_bodies_2026_08`：Citus **columnar** 月分区，**2020 MB / 140,999 行**。
  Columnar 表**不支持 btree 索引访问**（chunk group min/max 元数据只过滤已加载 chunk）。
  即使请求在 hot 里命中，planner Append 仍包含 columnar 分区，
  必须 ColumnarScan + JSONB 反压。单 ID 查询：29s statement_timeout 都打爆。

主查询 5s context deadline exceeded → 154 日志记：
```
WARN admin getLog scan failed request_id=245f6c6ebfc85fc735490b2307b7625e
     error="timeout: context deadline exceeded"
ERROR http_request path=/api/logs/245f6c6ebfc85fc735490b2307b7625e
      status=500 duration_ms=5000 error.message="Internal Server Error"
```

前端拿到 500 body → 渲染 `query failed`。

## 修复

`admin/logs.go::getLog` 拆 body 查询为两步：

1. 主查询只读 `request_logs_with_current_month`（metadata + 主表内嵌 body 列），
   不再 JOIN body 视图 — 1.8ms（hot 命中 + columnar chunk group filter 元数据命中）。
2. 新 helper `fetchRequestBodies(requestID)`：
   - 阶段 1：`SELECT … FROM request_logs_bodies_hot WHERE request_id = $1 LIMIT 1`
     （3s ctx，idx 命中 <1ms）。覆盖 dashboard 高频路径（24h 内请求全在 hot）。
   - 阶段 2：阶段 1 miss 时回退
     `SELECT … FROM request_logs_bodies_with_current_month WHERE request_id = $1 LIMIT 1`
     （独立 20s ctx）。columnar 单 ID 仍慢，但不会让主接口 5s 超时。
3. helper 返回 sql.ErrNoRows 时，caller 把 body 置 nil，**metadata 仍返回 200**。
   抽屉显示「请求/响应」Tab 之外的所有字段（latency/tokens/cost/model/provider/error_kind…），
   用户至少能继续看关键元数据排查。

### 为什么不是改 DB / 迁移？

- 加 btree 到 columnar 分区：Citus columnar 不支持 btree（社区 issue），物理限制。
- 加 BRIN 到 columnar 分区：可加速 chunk group skip（chunk group 已是 BRIN-like min/max），
  但只能减小扫描范围、不能避免 JSONB 反压。本次未做；列入下期优化（rule 03 §6 验证后另开 issue）。
- 把 columnar 改回 heap：违反规则 33 §2 设计意图（历史数据只读 + zstd 列存压缩 5–10×），
  会爆 35 GB 的 hot 表 → 几 TB 普通 heap。
- 改 dashboard 走 hot 表单独查询：等于绕过 `/api/logs/{id}` 合约，多处前端都要改。

### 为什么 hot 是 heap、cold 是 columnar？

| 表 | 数据 | 用途 | 访问模式 |
|---|---|---|---|
| `request_logs_bodies_hot` | 24h 内写入 | dashboard 实时流 + 详情抽屉 | 频繁单 ID + 小范围 |
| `request_logs_bodies_2026_08` (columnar) | 已 promote 的历史 | 月度报表 / 长期归档 | 偶发全表扫描 + 压缩存储 |
| `request_logs_bodies_with_current_month` (view) | 两者合并 | 单一入口给任意 caller | 不区分 hot/cold 时被迫扫全部分区 |

视图的"统一入口"假设对 list/agg 查询成立（planner 一次扫到底），
但**对单 ID 查 body 不成立**（plan Append 必扫最慢的列存分区）。

## 验证（154 上 L1-L4，rule 03 §6.0）

- **L1**：服务存活，端口监听，`/healthz` 200。
- **L2**：DB `SELECT 1` OK；`request_logs_bodies_hot` idx 命中 0.4ms（直接跑 SQL）。
- **L3**：拿真实实时流命中 ID `245f6c6ebfc85fc735490b2307b7625e` 跑
  `curl /api/logs/245f6c6ebfc85fc735490b2307b7625e -H Bearer $JWT` →
  HTTP **200**，完整 JSON 包含 `request_id / ts / client_model / status / request_body / response_body`，
  `duration_ms=85`（原 5000 timeout）。
- **L4**：dashboard 抽屉手动点击，请求/响应 Tab 正常渲染（screenshot 留证）。
  控制台无 ERROR / 4xx / 5xx。

## 回归门禁

新文件 `admin/logs_get_log_columnar_test.go`：

- `TestFetchRequestBodies_HotOnly_PassesInUnderOneSecond`：hot-only 行，
  helper 应在 < 1s 返回 body（断言 elapsed < 1s 且 body 字段解析正确）。
  这条直接锁住本次 regression —— 任何把 hot-only 行再次触发 columnar scan 的改动都会超时。
- `TestFetchRequestBodies_HotMiss_FallsBackToBodiesView`：hot 空 + bodies view 命中时，
  helper 返回 body；都不命中时返回 `sql.ErrNoRows`。
- `TestFetchRequestBodies_TotalMissReturnsErrNoRows`：两边都没行时返回 `sql.ErrNoRows`，
  让 caller 把 body 置 nil 但 metadata 仍 200（不再 500）。
- `TestFetchRequestBodies_CancelledParentCtx_DoesNotHang`：parent ctx 已取消时
  helper 在 < 2s 返回（不是 hang 30s columnar 扫描），锁住 ctx 层级不被
  嵌套取消意外阻断（rule 11 §14）。

跑测试需 `TEST_DATABASE_URL` 指 dev DB（`setupTestDB` 检测到 URL 缺失时自动 skip）。

## 2026-08-17 下午跟进 — 冷路径 ctx 防御

第一次修只处理了 hot path（dashboard 高频命中）。但用户偶尔点"老请求"时
**外层 5s ctx 仍会先把 metadata 卡掉**（metadata view 也包含 columnar 月分区
`request_logs_2026_07/08`）。本次跟进把 ctx 拓扑改成：

| 阶段 | ctx | 派生自 | 原因 |
|------|-----|--------|------|
| `getLog` metadata | **30s** | `r.Context()` | 给冷 metadata 留余量（hot 路径 < 100ms 不受影响） |
| `fetchRequestBodies` hot | 3s | metadata ctx (30s) | 同一接口整体超时一致 |
| `fetchRequestBodies` cold | 20s | metadata ctx (30s) | metadata 慢不会拖累 body（独立 ctx hierarchy） |

并且在 `fetchRequestBodies` 内加 elapsed_ms 结构化日志（hot > 1s INFO，
cold hit/timeout 都记 WARN/INFO），运维能区分：
- `admin getLog metadata slow`（> 1s 但 < 30s）— metadata 慢了
- `admin fetchRequestBodies cold path hit` — 走了 columnar，xx ms 拿到 body
- `admin fetchRequestBodies cold path timeout` — columnar 超 20s，body 没拿到
- `admin fetchRequestBodies hot path slow`（> 1s）— 不应发生，hot idx 应该 < 100ms

154 实测：用一个**真的只在 columnar body 表里存在的** ID
（`abcf2ef8d06bf1c89d6a1d6816c99765`，metadata 不在 hot 也不在 metadata view），
扩展前 → HTTP 500 (timeout 5s)；扩展后 → HTTP 200 (从 columnar bodies 查到 body)。

## 2026-08-17 傍晚优化 — bodyFetchCache (in-memory LRU+TTL)

ctx 防御解决了"不 500"，但用户**重复点击同一 request_id** 仍然每次都走 5s columnar 扫描。
dashboard 实际操作中经常"开 → 关 → 再开"同一条 request 来回比对 payload — 这是缓存最高频的场景。

新增 `admin/logs_body_cache.go`（LRU + TTL + stats），包装在 `fetchRequestBodies` 阶段 0：

| 配置 | 值 | 理由 |
|------|-----|------|
| 容量 | 1024 entries | 每条 body ~20KB JSONB → 20MB 上限（rule 23 §3 footprint 预算） |
| TTL | 5min | body 写入后罕见被修改；超时让 re-fetch（hot 命中便宜） |
| ErrNoRows 缓存 | ✅ | 两端都没找到是确定性结果，5min 内重复点击不再打 PG |
| Transport err 缓存 | ❌ | ctx cancel / conn refused 必须能被 caller 重试（rule 22 §4） |

可观测端点：**GET /api/admin/logs/body-cache-stats**
```json
{"size": 142, "hits": 1023, "misses": 287, "evictions": 5, "hit_rate": 0.781}
```
运维看 `hit_rate` 判断 cold path 是否被 cache 缓解（预期 > 50%）。

回归测试（`admin/logs_get_log_columnar_test.go`）：
- `TestBodyFetchCache_HitUnderOneMillisecond`：第一次走 DB，第二次 < 1ms
- `TestBodyFetchCache_TTLExpires`：TTL 1ms 强制过期 → 第二次走 DB（hits=0）
- `TestBodyFetchCache_NotFoundIsCached`：sql.ErrNoRows 也走 cache
- `TestBodyFetchCache_LRUEvictsOldest`：容量满后最久未用被淘汰

## 遗留与下一步

- 防御性增强：给 `/api/logs/{id}` 加更长 ctx（20s），抵御历史冷请求慢路径。
  留待另开 issue，不混进本 PR（rule 37 精准修改）。
- 抽查 dashboard 其它抽屉（`/api/turns/...`、`/api/sessions/.../turns`）是否有同类 columnar JOIN；
  这次只修了 `getLog` 一处入口，list/agg 类接口未触及。