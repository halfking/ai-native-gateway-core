# 2026-11-13 凭据监控摘要端点优化方案

> **背景**：`/api/credentials/monitor-summary` 在 245/154 上持续 500（`duration_ms=15007`），真凶已定位为 `total_requests` 的两臂 COUNT。
> 本文档给出**四条路径**的代价与口径影响分析，供运营/产品裁决。

## 一、问题定性

### 1.1 现象
- **端点**：`GET /api/credentials/monitor-summary`（admin/credential_monitor.go:687）
- **症状**：245/154 上 **500**（`duration_ms=15007`，context 15 s）
- **真凶**：`total_requests` 的两臂 COUNT（`session_turns_hot` ∪ `session_turns` 母表按 `credential_id` 计数）
- **数据量**：252 库母表 **1,411,419 行 / 7 分区**（实测 2026-11-12）

### 1.2 根因（逐段二分实测）
| 段 | 252 库实测 | 判读 |
|---|---|---|
| `recent_success_rate` 单次调用 | **0.815 ms** | ★ 谓词面已修好（迁移 962） |
| 整条查询去掉 `total_requests` 后的其余部分（74 凭据 × 1965 次 LATERAL 调用） | **882 ms** | ★ 函数面彻底修好 |
| ★★ `total_requests` 的两臂 COUNT（**单凭据**） | ★★ **12 s 超时** | ★★ **真凶在这里** |
| 换 `GROUP BY` 聚合形态（替代 74 次相关子查询） | **23.4 s** | **形状不是问题** |

### 1.3 为什么「同口径」无法解决
- **索引已命中**：计划显示 `Bitmap Index Scan` / `Parallel Index Scan`，`Index Cond` 就是那个 CASE 表达式 ⇒ **不是**「索引没被用上」。
- **但回表数量大**：单凭据在 2026_09 / 2026_10 分区各有 **8.9 万 / 4.2 万行**匹配 ⇒ `Bitmap **Heap** Scan` 要回表十几万行才能数 ⇒ **计数语义要求全量遍历**。
- ⇒ ★★ **通用式**：**「索引被用上了」与「快」是两件事：计数语义要求全量遍历时，谓词同形救不了** ⇒ 计数类查询的优化方向不是「让索引可用」，而是「别数那么多」。

## 二、四条路径的代价与口径影响

### 路径 ① 口径变更：改成近窗计数

#### 定义
把 `total_requests` 从「该凭据在 `session_turns` 母表全历史的总计数」改成「近 N 天/小时窗口的计数」。

#### 实施
```sql
-- 现状（全历史）
SELECT COUNT(*)
  FROM session_turns
 WHERE (CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END) = <cred_id>
UNION ALL
SELECT COUNT(*)
  FROM session_turns_hot
 WHERE (CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END) = <cred_id>;

-- 路径① 形态（近 7 天窗口）
SELECT COUNT(*)
  FROM session_turns
 WHERE (CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END) = <cred_id>
   AND created_at >= NOW() - INTERVAL '7 days'
UNION ALL
SELECT COUNT(*)
  FROM session_turns_hot
 WHERE (CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END) = <cred_id>
   AND created_at >= NOW() - INTERVAL '7 days';
```

#### 252 库实测（2026-11-12 数据）
| 窗口 | 单凭据耗时 | 74 凭据总耗时（估算） | 匹配行数（cred 99009 样本） |
|---|---|---|---|
| 全历史（现状） | **12 s** 超时 | **必越 15 s** | 2026_09: 89,123 / 2026_10: 42,067 |
| 近 7 天 | **待实测** | **待实测** | **待实测** |
| 近 3 天 | **待实测** | **待实测** | **待实测** |
| 近 24 小时 | **待实测** | **待实测** | **待实测** |

#### 代价
| 维度 | 代价 |
|---|---|
| **工程量** | ★ 低（单行 SQL 加 `AND created_at >= ...` + 前端/文档同步改口径说明） |
| **口径影响** | ★★★★ **高**：数字含义变化 ⇒ **admin 面板口径变更** ⇒ 需运营/产品明确：① 这个数字给谁看 ② 看它的人要它回答什么问题 ③ 窗口定多大 |
| **向后兼容** | ★★★ 否（历史对比会断层：旧数是全历史、新数是近窗） |
| **数据完整性** | ★ 无损（底层数据不动） |
| **回滚容易度** | ★ 易（改回去即可） |

#### 适用场景
- **运营确认**「这个数字只需要看近期趋势，不需要全历史累计」。
- **窗口足够小**（≤ 7 天）时，匹配行数从十几万降到千级/百级 ⇒ 可能进 15 s。

#### 风险
- **窗口不够小** ⇒ 仍会超时（需实测验证窗口与耗时的关系）。
- **口径变更未同步** ⇒ 前端/报告/文档仍说「总请求数」⇒ 用户误读。

---

### 路径 ② 换预聚合源：复用既有物化视图

#### 定义
仓内已有 `public.credential_model_call_history`（物化视图，每分钟刷新，`total_calls` 按 `credential_id` / `model_id` / `bucket_time` 聚合），把 `total_requests` 改成从这里读。

#### 实施
```sql
-- 路径② 形态（复用 credential_model_call_history）
SELECT COALESCE(SUM(total_calls), 0) AS total_requests
  FROM credential_model_call_history
 WHERE credential_id = <cred_id>
   AND bucket_time >= NOW() - INTERVAL '24 hours';
```

#### 252 库实测（2026-11-12 数据）
| 形态 | 单凭据耗时 | 匹配行数（cred 99009 样本） | 判读 |
|---|---|---|---|
| 从 MV 读（24h 窗） | **待实测** | **待实测** | MV 已按分钟桶聚合 ⇒ 匹配行数 = O(模型数 × 1440) |

#### 代价
| 维度 | 代价 |
|---|---|
| **工程量** | ★ 低（改 SQL + 验证 MV 刷新频率是否满足实时性要求） |
| **口径影响** | ★★★ **中高**：① 该 MV 的窗口是 **24 h**（见 `bg/materialized_view_refresher.go`）⇒ **不是全历史** ⇒ 数字含义变化；② 实时性延迟 ≤ 1 min（MV 刷新频率） |
| **向后兼容** | ★★★ 否（同路径①：历史对比会断层） |
| **数据完整性** | ★ 无损（底层数据不动） |
| **回滚容易度** | ★ 易（改回去即可） |

#### 适用场景
- **运营确认**「24 h 窗口满足需求」。
- **MV 的刷新频率（半小时一次，实测 2.4 s）** 不成为新瓶颈。

#### 风险
- **窗口固定 24 h** ⇒ 若需要「7 天」或「全历史」，此路径不适用。
- **MV 的 `total_calls` 口径与 `session_turns` 的行数口径是否等价** ⇒ 需逐表核对（实测：`session_turns` 一行 = 一次 turn，`credential_model_call_history.total_calls` 来自 `stage_events` 的 `call_count` ⇒ **可能不等价**，需实证）。

---

### 路径 ③ 异步物化：后台 job 写汇总表、读面 O(1)

#### 定义
新增后台定时任务，每 N 分钟算一次每个凭据的 `total_requests`（全历史或近窗），写进汇总表 `credential_summary`；读面从汇总表取 ⇒ **O(1)**。

#### 实施
```sql
-- 新增汇总表
CREATE TABLE credential_summary (
    credential_id     BIGINT PRIMARY KEY,
    total_requests    BIGINT NOT NULL DEFAULT 0,
    last_updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX ON credential_summary (last_updated_at);

-- 后台 job 伪码
func RefreshCredentialSummary(ctx context.Context) {
    for _, credID := range activeCredentials {
        count := countTotalRequests(credID)  // 跑一次那个慢查询
        upsertCredentialSummary(credID, count)
    }
}

-- 读面改成
SELECT total_requests FROM credential_summary WHERE credential_id = <cred_id>;
```

#### 代价
| 维度 | 代价 |
|---|---|
| **工程量** | ★★★★ **高**：① 新表 + 迁移；② 新后台 job（定时触发 + 并发控制 + 失败重试）；③ 读面改查汇总表；④ 汇总表的 retention / 清理策略；⑤ 监控（job 健康度 / 汇总表新鲜度） |
| **口径影响** | ★ **零**：`total_requests` 的语义不变（仍是全历史或指定窗口），只是「实时」变成「准实时（延迟 ≤ N min）」 |
| **向后兼容** | ★ 是（数字含义不变） |
| **数据完整性** | ★ 无损（底层数据不动） |
| **回滚容易度** | ★★ 中（job 可停、表可删，但回滚后读面仍慢） |

#### 适用场景
- **运营/产品要求**「数字含义不变」（全历史累计）且「可接受准实时（延迟 ≤ N min）」。
- **愿意投入工程量**换取「读面 O(1)」。

#### 实施细节
1. **刷新频率**：建议 **5~10 分钟**（过频会加重写压力；过疏会让「准实时」变「半小时前」）。
2. **并发控制**：74 个凭据并行刷新 ⇒ 单个慢查询 12 s ⇒ 串行需 **888 s（14.8 min）** ⇒ 需**并发**（10 并发 ⇒ 88 s）。
3. **失败处理**：单凭据刷新超时（30 s statement_timeout）⇒ 记日志 + 保留旧值 ⇒ 不阻塞其他凭据。
4. **冷启动**：首次刷新可能需要几分钟 ⇒ 读面要有「汇总表无数据时 fallback 到实时查询」的兜底（或返回 `null`）。

#### 风险
- **工程量大** ⇒ 引入新的故障面（job 挂了、汇总表漂移、新鲜度监控缺失）。
- **延迟可见性**：用户刷新页面看到的是「N 分钟前的数字」⇒ 需在前端标注「更新时间」。

---

### 路径 ④ 接受 500：不改

#### 定义
承认「该端点在当前数据量下已无法在 15 s 内返回」，不修复。

#### 代价
| 维度 | 代价 |
|---|---|
| **工程量** | ★ 零 |
| **口径影响** | ★ 零 |
| **用户体验** | ★★★★ **极差**：admin 面板核心页面不可用 |
| **运维成本** | ★★★ 高：每次 500 都会触发告警 / 日志噪声 |

#### 适用场景
- **该页面使用频率极低**（例：每月 < 10 次）。
- **有替代方案**（例：直接查库 / 导出 CSV）。

#### 风险
- **数据量继续涨** ⇒ 其他依赖 `session_turns` 计数的端点也会逐个 500。

---

## 三、推荐方案（按裁决面分层）

### 3.1 若「数字含义可以变」⇒ 路径 ① 或 ②
| 选项 | 适用条件 | 优先级 |
|---|---|---|
| **路径 ①**（近窗计数） | 运营确认「只看近 N 天趋势」+ 窗口 ≤ 7 天 | ★★★ 推荐（工程量最小） |
| **路径 ②**（复用 MV） | 运营确认「24 h 窗口满足需求」+ MV 口径等价 | ★★ 备选（需先实证口径等价性） |

### 3.2 若「数字含义不能变」⇒ 路径 ③
| 选项 | 适用条件 | 优先级 |
|---|---|---|
| **路径 ③**（异步物化） | 必须全历史累计 + 可接受准实时（≤ 10 min 延迟）+ 愿意投入工程量 | ★★★★ **强烈推荐**（口径零变化是唯一不触碰裁决面的路） |

### 3.3 若「该页面可以不修」⇒ 路径 ④
不推荐（除非使用频率 < 每月 10 次）。

---

## 四、实施前的必要实测（路径 ① / ②）

### 4.1 路径 ① 的窗口与耗时关系
在 **252 库**上实测（取 **3 个样本凭据**：高频 / 中频 / 低频）：

```sql
-- 样本凭据选取（按 credential_model_call_history 的 total_calls 排序）
SELECT credential_id, SUM(total_calls) AS total
  FROM credential_model_call_history
 WHERE bucket_time >= NOW() - INTERVAL '7 days'
 GROUP BY credential_id
 ORDER BY total DESC
 LIMIT 1;  -- 高频样本

-- 逐窗口实测
EXPLAIN (ANALYZE, BUFFERS)
SELECT COUNT(*)
  FROM session_turns
 WHERE (CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END) = <sample_cred_id>
   AND created_at >= NOW() - INTERVAL '<窗口>';
```

| 窗口 | 高频样本耗时 | 中频样本耗时 | 低频样本耗时 | 匹配行数 |
|---|---|---|---|---|
| 24 小时 | **待实测** | **待实测** | **待实测** | **待实测** |
| 3 天 | **待实测** | **待实测** | **待实测** | **待实测** |
| 7 天 | **待实测** | **待实测** | **待实测** | **待实测** |

### 4.2 路径 ② 的口径等价性验证
```sql
-- 取同一凭据同一窗口，比对两个源的数字
WITH mv_count AS (
    SELECT COALESCE(SUM(total_calls), 0) AS n
      FROM credential_model_call_history
     WHERE credential_id = <sample_cred_id>
       AND bucket_time >= NOW() - INTERVAL '24 hours'
),
raw_count AS (
    SELECT COUNT(*) AS n
      FROM session_turns
     WHERE (CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END) = <sample_cred_id>
       AND created_at >= NOW() - INTERVAL '24 hours'
    UNION ALL
    SELECT COUNT(*)
      FROM session_turns_hot
     WHERE (CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END) = <sample_cred_id>
       AND created_at >= NOW() - INTERVAL '24 hours'
)
SELECT 
    (SELECT n FROM mv_count) AS from_mv,
    (SELECT SUM(n) FROM raw_count) AS from_raw,
    (SELECT n FROM mv_count) - (SELECT SUM(n) FROM raw_count) AS delta;
```

**若 delta ≠ 0** ⇒ 两个源口径**不等价** ⇒ 路径 ② **不适用**（除非运营接受新口径）。

---

## 五、决策流程图

```
用户打开 /api/credentials/monitor-summary
                ↓
        「total_requests 这个数字要回答什么问题?」
                ↓
        ┌───────┴───────┐
        ↓               ↓
    「全历史累计」   「近期趋势」
        ↓               ↓
    路径 ③         窗口 ≤ 7 天?
  (异步物化)        ↓
              ┌─────┴─────┐
              ↓           ↓
             是           否
              ↓           ↓
          路径 ①      路径 ② 或 ③
        (近窗计数)   (视 MV 口径而定)
```

---

## 六、下一步行动（待裁决）

| # | 动作 | 责任方 | 输出 |
|---|---|---|---|
| 1 | **明确口径需求**：`total_requests` 要回答什么问题？全历史 or 近期趋势？ | **运营/产品** | 书面确认（含窗口大小） |
| 2 | **实测窗口耗时**（路径 ①）：在 252 库上跑 §四.1 的实测脚本 | **本会话 or DBA** | 耗时表 + EXPLAIN 输出 |
| 3 | **实测口径等价性**（路径 ②）：在 252 库上跑 §四.2 的验证脚本 | **本会话 or DBA** | delta 数值 + 差异解释 |
| 4 | **拍板路径** | **运营/产品 + 技术负责人** | 路径编号 ① / ② / ③ / ④ |
| 5 | **实施** | **本会话** | 迁移 + 代码 + 测试 + 部署 |

---

## 七、附录：252 库实测命令（可直接执行）

### A. 获取样本凭据 ID
```bash
ssh acc-pg17 "psql -U llm_gateway -d llm_gateway_db -c \"
SELECT credential_id, SUM(total_calls) AS total
  FROM credential_model_call_history
 WHERE bucket_time >= NOW() - INTERVAL '7 days'
 GROUP BY credential_id
 ORDER BY total DESC
 LIMIT 3;
\""
```

### B. 实测单凭据各窗口耗时
```bash
SAMPLE_CRED_ID=99009  # 替换成上一步的高频样本
for window in '24 hours' '3 days' '7 days'; do
    echo "=== Window: $window ==="
    ssh acc-pg17 "psql -U llm_gateway -d llm_gateway_db -c \"
    EXPLAIN (ANALYZE, BUFFERS)
    SELECT COUNT(*)
      FROM session_turns
     WHERE (CASE WHEN credential_id ~ '^[0-9]+\\\$' THEN credential_id::bigint END) = $SAMPLE_CRED_ID
       AND created_at >= NOW() - INTERVAL '$window';
    \""
done
```

### C. 实测 MV 口径等价性
```bash
SAMPLE_CRED_ID=99009
ssh acc-pg17 "psql -U llm_gateway -d llm_gateway_db -c \"
WITH mv_count AS (
    SELECT COALESCE(SUM(total_calls), 0) AS n
      FROM credential_model_call_history
     WHERE credential_id = $SAMPLE_CRED_ID
       AND bucket_time >= NOW() - INTERVAL '24 hours'
),
raw_count AS (
    SELECT COUNT(*) AS n
      FROM session_turns
     WHERE (CASE WHEN credential_id ~ '^[0-9]+\\\$' THEN credential_id::bigint END) = $SAMPLE_CRED_ID
       AND created_at >= NOW() - INTERVAL '24 hours'
    UNION ALL
    SELECT COUNT(*)
      FROM session_turns_hot
     WHERE (CASE WHEN credential_id ~ '^[0-9]+\\\$' THEN credential_id::bigint END) = $SAMPLE_CRED_ID
       AND created_at >= NOW() - INTERVAL '24 hours'
)
SELECT 
    (SELECT n FROM mv_count) AS from_mv,
    (SELECT SUM(n) FROM raw_count) AS from_raw,
    (SELECT n FROM mv_count) - (SELECT SUM(n) FROM raw_count) AS delta;
\""
```

---

## 八、诚实边界

1. **本文档未包含路径 ③ 的详细设计**（后台 job 的并发模型 / 失败重试策略 / 汇总表 schema 细节）⇒ 若选路径 ③，需单独立项设计。
2. **窗口耗时实测尚未执行**（§四.1 的表格全部「待实测」）⇒ 路径 ① 的可行性**待实证**。
3. **MV 口径等价性尚未验证**（§四.2）⇒ 路径 ② 的可行性**待实证**。
4. **本文档不涉及前端改动**（口径变更后的文案 / 更新时间标注 / 加载状态）⇒ 前端改动需与产品对齐。
5. **15 s context 本身未改**（`admin/credential_monitor.go:687`）⇒ 即使优化后单查询降到 1 s，数据量再涨一个量级仍可能越线 ⇒ context 调整也是可选项（但改 context 不解决根本问题）。

---

**文档版本**：v1.0  
**作者**：本会话（Mavis）  
**日期**：2026-11-13  
**状态**：待裁决
