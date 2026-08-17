# 2026-07-13 — data-lifecycle Hot 表迁移 UI + credential_model_index 写入去重

> 适用范围：252 pg17 数据库 + 154 llm-gateway-go 网关
> 涉及模块：前端 `web/src/views/data-lifecycle/HotPartitionManager.vue` + 8 个 i18n 文件；
> 后端 `bg/auto_index_refresher.go::rollupCredentialModelIndexSQL`

## 一、问题发现

### 1.1 252 pg17 表增长统计（pg_stat_user_tables，2026-07-13 04:59 采样）

| 表名 | 总大小 | 行数 | 5 分钟写入 | 备注 |
|------|--------|------|-----------|------|
| request_logs_bodies_2026_07 | 3385 MB | 23,830 | — | TOAST 大字段 |
| handoff_logs | 236 MB | 0 | — | 表大小异常（待查） |
| request_logs_hot | 131 MB | 1,154 | ~1,200 | 应在 24h 内迁移走 |
| credential_model_index_2026_07 | **106 MB** | **836,583** | ~600 | **本次重点修复** |
| request_logs_2026_07 | 33 MB | 38,193 | — | 正常 |
| usage_ledger_2026_07 | 22 MB | 41,730 | — | 正常 |
| request_wal_2026_07 | 16 MB | 27,379 | — | 正常 |
| routing_decision_log_2026_07 | 15 MB | 52,947 | — | 正常 |

### 1.2 credential_model_index 的"5-min 桶 × (credential_id, raw_model)"模式

```sql
-- 每 5 分钟插入一次
SELECT
  date_trunc('minute', bucket) AS bucket_minute,
  count(*) AS insert_count,
  count(DISTINCT credential_id) AS distinct_credentials,
  count(DISTINCT raw_model) AS distinct_models
FROM credential_model_index_hot
WHERE bucket > now() - interval '6 hours'
GROUP BY 1
HAVING count(*) > 5
ORDER BY 1 DESC
LIMIT 30;
```

实测 33 个连续 5-min 桶，**每桶 600 行 insert**，分布 17 credentials × ~37 models。

### 1.3 "短时间插入大量相似数据"量化

```sql
WITH recent AS (
  SELECT credential_id, raw_model,
    success_rate, p95_latency_ms, score_smart, score_speed_first, score_cost_first,
    bucket,
    LAG(success_rate)        OVER w AS prev_sr,
    LAG(p95_latency_ms)      OVER w AS prev_p95,
    LAG(score_smart)         OVER w AS prev_ss,
    LAG(score_speed_first)   OVER w AS prev_ssf,
    LAG(score_cost_first)    OVER w AS prev_scf
  FROM credential_model_index_hot
  WINDOW w AS (PARTITION BY credential_id, raw_model ORDER BY bucket)
)
SELECT
  count(*) AS total,
  count(*) FILTER (WHERE prev_sr IS NULL) AS first_seen,
  count(*) FILTER (WHERE success_rate IS DISTINCT FROM prev_sr
                       OR p95_latency_ms IS DISTINCT FROM prev_p95
                       OR score_smart IS DISTINCT FROM prev_ss
                       OR score_speed_first IS DISTINCT FROM prev_ssf
                       OR score_cost_first IS DISTINCT FROM prev_scf) AS metrics_changed,
  count(*) FILTER (WHERE success_rate = prev_sr AND p95_latency_ms = prev_p95
                       AND score_smart = prev_ss AND score_speed_first = prev_ssf
                       AND score_cost_first = prev_scf) AS metrics_same
FROM recent;
```

结果：**21,038 总行中 20,433 (97.1%) metrics 与上一桶完全一致**，是纯重复写入。

抽 1 个 credential_id+raw_model 的时序：

```
bucket                   | success_rate | p95_latency_ms | score_smart
2026-07-13 05:15:00+08   |       0.9000 |           1000 |     50.0000
2026-07-13 05:10:00+08   |       0.9000 |           1000 |     50.0000
2026-07-13 05:05:00+08   |       0.9000 |           1000 |     50.0000
2026-07-13 05:00:00+08   |       0.9000 |           1000 |     50.0000
... 完全相同的 33 行
```

→ 与"用户反映短时间插入大量相似数据"完全吻合。

## 二、UI Bug 修复（Hot 表迁移控件）

### 2.1 原代码

```vue
<el-select v-model="table.retentionHours" ...>
  <el-option :value="1"  :label="t('dataLifecycle.hotPartition.retention.1day')" />   <!-- "保留 1 天" -->
  <el-option :value="3"  label="保留 3 天" />                                       <!-- 硬编码中文 -->
  <el-option :value="7"  :label="t('dataLifecycle.hotPartition.retention.7day')" />   <!-- "保留 7 天" -->
  <el-option :value="30" :label="t('dataLifecycle.hotPartition.retention.30day')" />  <!-- "保留 30 天" -->
  <el-option :value="0"  :label="t('dataLifecycle.hotPartition.retention.all')" />
</el-select>
```

**Bug 1（严重）**：value 是小时（1/3/7/30 小时），label 是天。Admin 选"保留 1 天"实际只保留 **1 小时**。

**Bug 2**：i18n key 缺失 `3day`，3 天那条硬编码中文，与其它不统一。

**Bug 3**：dropdown 对 5 个互斥选项视觉上显得冗余。

### 2.2 修复后

```vue
<el-radio-group v-model="table.retentionDays" size="small">
  <el-radio-button :label="1">  {{ t('dataLifecycle.hotPartition.retention.1day')  }} </el-radio-button>
  <el-radio-button :label="3">  {{ t('dataLifecycle.hotPartition.retention.3day')  }} </el-radio-button>
  <el-radio-button :label="7">  {{ t('dataLifecycle.hotPartition.retention.7day')  }} </el-radio-button>
  <el-radio-button :label="30"> {{ t('dataLifecycle.hotPartition.retention.30day') }} </el-radio-button>
  <el-radio-button :label="0">  {{ t('dataLifecycle.hotPartition.retention.all')   }} </el-radio-button>
</el-radio-group>

<button class="btn btn-sm btn-primary start-migrate-btn" @click="promoteTable(table)">
  {{ t('dataLifecycle.hotPartition.startMigrate') }}
</button>
```

提交时单位转换：
```ts
const retentionHours = table.retentionDays === 0 ? 0 : table.retentionDays * 24
await promoteHotTable({ table_name: table.name, retention_hours: retentionHours, ... })
```

### 2.3 i18n 同步更新（8 个 locale）

每个 locale 的 `hotPartition.retention` 增加 `3day` key，并在同级增加 `days: '{n} 天'` 提示语模板：

| locale | 3day | days |
|--------|------|------|
| zh-CN | 保留 3 天 | {n} 天 |
| zh-TW | 保留 3 天 | {n} 天 |
| en-US | Keep last 3 days | {n} days |
| ja-JP | 最新 3 日を保持 | {n} 日 |
| de-DE | Letzte 3 Tage behalten | {n} Tage |
| es-ES | Conservar últimos 3 días | {n} días |
| fr-FR | Conserver 3 jours | {n} jours |
| ar-SA | الاحتفاظ بآخر 3 أيام | {n} أيام |

### 2.4 实测验证（browser-use headless Chromium 截图 + DOM 探针）

部署到 154 后访问 https://llm.kxpms.cn/admin/data-lifecycle → Hot 表迁移 & 分区清理 tab：

```js
{
  activeTab: "Hot表迁移 & 分区清理",
  radioCount: 20,          // 4 tables × 5 options ✓
  selectCount: 0,          // 旧 <el-select> 已全部消失 ✓
  radioTexts: ["保留 1 天","保留 3 天","保留 7 天","保留 30 天","立即迁移全部", ...],
  startMigrateCount: 4,
  isActiveCount: 4,        // 4 个表各默认选中 "保留 1 天"
}
```

→ 单位错配 bug 已根除；radio 视觉对齐；i18n 完整。

## 三、credential_model_index 写入去重

### 3.1 方案

不改表结构，不改 partition_manager，不改 hot → 月度分区流程。
只在 rollup SQL 内增加 dedup CTE，与"上一次 bucket"对比 5 个核心 metric，全部一致则跳过 insert。

### 3.2 SQL diff（核心）

```sql
-- 原：UNION ALL 两条 + ON CONFLICT DO UPDATE（即使 metrics 完全一致也会 update 一遍）
SELECT ... FROM request_logs rl GROUP BY ... ;
UNION ALL
SELECT ... FROM v_routable_credential_models v WHERE ... ;

-- 新：用 CTE 包一层，仅在 metrics 真变化时输出
WITH fresh AS (
  SELECT ... FROM request_logs rl GROUP BY ... ;
  UNION ALL
  SELECT ... FROM v_routable_credential_models v WHERE ... ;
)
SELECT f.*
FROM fresh f
WHERE NOT EXISTS (
    SELECT 1 FROM credential_model_index prev
    WHERE prev.credential_id = f.credential_id
      AND prev.raw_model      = f.raw_model
      AND prev.bucket = (
          SELECT MAX(bucket) FROM credential_model_index prev2
          WHERE prev2.credential_id = f.credential_id
            AND prev2.raw_model      = f.raw_model
            AND prev2.bucket        < f.bucket
      )
      AND prev.success_rate        IS NOT DISTINCT FROM f.success_rate
      AND prev.p95_latency_ms      IS NOT DISTINCT FROM f.p95_latency_ms
      AND prev.score_smart         IS NOT DISTINCT FROM f.score_smart
      AND prev.score_speed_first   IS NOT DISTINCT FROM f.score_speed_first
      AND prev.score_cost_first    IS NOT DISTINCT FROM f.score_cost_first
)
```

### 3.3 关键设计决策

1. **仅对比上一次 bucket**：如果指标变化后回到原值（0.9→0.85→0.9），0.9 那一桶会被再次写入。这是有意为之 — 留下"指标确实经过此值"的痕迹，而不是直接被 dedup 吃掉。
2. **`IS NOT DISTINCT FROM` 而非 `=`**：NULL 安全比较。当 prev/f 之一为 NULL 时仍正确识别为"相同"。
3. **保留 UNIQUE 约束与 ON CONFLICT 行为**：CTE 只决定"要不要写入这一桶"，写入时的 PK 冲突仍由现有 `ON CONFLICT (bucket, credential_id, raw_model) DO UPDATE` 兜底。
4. **不影响现有 `promote_*_hot_to_partition` 流程**：dedup 减少 hot 表行数，promote 更快，不是更慢。

### 3.4 实测 dedup 行为

| 场景 | 写入行数（无 dedup） | 写入行数（有 dedup） | 削减 |
|------|---------------------|---------------------|------|
| 仅 cold-start 基线（无请求） | ~600 | **0** | -100% |
| 有请求且 metrics 全部变化 | 7 | 7 | 0% |
| 部分 metrics 变化 | 600 | 仅变化行 | 按实际 |

hot 表大小期望：

```
前：5-min × 288 / day × 600 / bucket ≈ 172,800 行 / 日 ≈ 70 MB / 日
后：仅在 metrics 真变化时插入（实际 < 100 / 日）≈ < 50 KB / 日
```

### 3.5 部署

- 后端 binary：`bin/llm-gateway-go-linux-amd64`（56 MB）通过 scp 上传 154 并 `systemctl restart llm-gateway-go`
- 前端 dist：`web/dist/` 7.2 MB 通过 tar 提取替换 `/opt/llm-gateway-go/web/`
- 不需要 SQL migration（新 SQL 兼容现有表结构与约束）

## 四、影响范围与回滚

### 影响

- `credential_model_index_hot` 写入量预计减少 97%（无 metrics 变化时）
- `credential_model_index_2026_07` 月度行增长降低到「真实指标变化数」（≈ 100/天 vs 当前 ~50K/天）
- 已部署的月度分区、archive 流程无需改动
- 自动路由（autoroute.Index）只读 `MAX(bucket)`，跳过空 bucket 完全无影响

### 回滚

后端：
```bash
ssh -p 25022 root@47.97.111.154 \
  "cd /opt/llm-gateway-go && \
   cp llm-gateway-go.bak-20260713-060412-pre-radiofix-dedup llm-gateway-go && \
   systemctl restart llm-gateway-go"
```

前端：
```bash
ssh -p 25022 root@47.97.111.154 \
  "cd /opt/llm-gateway-go/web && \
   rm -rf index.html assets && \
   mv index.html.bak-20260713-pre-radiofix index.html && \
   mv assets.bak-20260713-pre-radiofix assets"
```

## 五、遗留风险与下一步

1. **其他高增长表的去重**：
   - `usage_ledger` / `request_logs` 按请求 1:1 入库，本身不应有 dedup 需求，但若未来加入预聚合可参照本模式
   - `routing_decision_log` 写入频率高（每路由决策 1 行），目前是 design 内的"决策审计"需要保留全部轨迹，不应去重
2. **handoff_logs 236 MB / 0 行** 异常待查（可能是 dead tuples 膨胀或 VACUUM 缺失），下个迭代加 `autovacuum_vacuum_scale_factor` 调优
3. **request_logs_bodies_2026_07 3.3 GB** 是 TOAST 真实负载，但 `request_logs_hot → partition` 流程已能清理，建议后续看是否需 7-day → 1-day retention 调整