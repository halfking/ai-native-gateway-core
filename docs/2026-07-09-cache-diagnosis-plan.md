# LLM 端缓存命中率基线诊断方案

**版本**: v1.0  
**日期**: 2026-07-09  
**环境**: 本地 r112 + 当前可访问部署环境  
**诊断目标**: 确认缓存命中率基线  
**诊断方式**: 被动监控（不改代码）

---

## 1. 诊断背景

### 1.1 关键澄清（来自审计调研）

本仓库存在 **8 个层级的"缓存"概念**，诊断时必须区分：

| 层 | 名称 | 与本诊断关系 |
|---|---|---|
| **C1** | **上游 LLM prompt cache**（Anthropic ephemeral / OpenAI checkpoint） | ✅ **核心诊断对象** |
| **C2** | **gateway prefix 稳定化**（`cache/prefix.Stabilize`） | ✅ **诊断对象**（中间产物） |
| **C3** | **gateway cache_control 注入**（`session.CacheInjector`） | ✅ **诊断对象** |
| C4 | Redis 可用性缓存（`llmgw:avail:*`） | ❌ 仅参考 |
| C5 | 3 层 SessionCache（L1 mem / L2 Redis / L3 PG） | ❌ 与本诊断无关 |
| C6 | `cache/{kv,semantic,delta}` 包（死代码） | ❌ 无运行时实例 |
| C7 | URSM fingerprint slot | ❌ 路由用 |
| C8 | monitor-summary 30s 内存缓存 | ❌ 自身缓存 |

### 1.2 诊断三大问题

1. **C1 当前命中率是多少？**（按 provider / 模型 / 会话长度分布）
2. **C2 触发率是多少？**（prefix 稳定化是否生效）
3. **C3 注入覆盖率是多少？**（cache_control 标记是否到位）

---

## 2. 诊断 SQL 脚本模板

### 2.1 全局 7 天基线（首选）

```sql
-- 文件: scripts/cache-baseline/01-global-7d.sql
-- 用途: 全局缓存命中率基线（C1）
SELECT
  'request_count' AS metric,
  COUNT(*)::text AS value
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
UNION ALL
SELECT 'cache_read_tokens',
  COALESCE(SUM(cache_read_tokens), 0)::text
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
UNION ALL
SELECT 'prompt_tokens',
  COALESCE(SUM(prompt_tokens), 0)::text
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
UNION ALL
SELECT 'cache_write_tokens',
  COALESCE(SUM(cache_write_tokens), 0)::text
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
UNION ALL
SELECT 'cache_hit_ratio_7d',
  CASE
    WHEN SUM(COALESCE(cache_read_tokens,0)) + SUM(COALESCE(prompt_tokens,0)) > 0
    THEN ROUND(
      SUM(cache_read_tokens)::numeric /
      (SUM(cache_read_tokens) + SUM(prompt_tokens)), 4
    )::text
    ELSE '0'
  END
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
  AND cache_read_tokens IS NOT NULL OR prompt_tokens IS NOT NULL;
```

### 2.2 按模型分组（识别高价值优化点）

```sql
-- 文件: scripts/cache-baseline/02-by-model.sql
-- 用途: 识别哪些模型缓存命中率最高/最低
SELECT
  outbound_model AS model,
  COUNT(*) AS request_count,
  SUM(COALESCE(cache_read_tokens, 0)) AS cache_read,
  SUM(COALESCE(prompt_tokens, 0)) AS prompt,
  CASE
    WHEN SUM(COALESCE(cache_read_tokens,0)) + SUM(COALESCE(prompt_tokens,0)) > 0
    THEN ROUND(
      SUM(cache_read_tokens)::numeric /
      NULLIF(SUM(cache_read_tokens) + SUM(prompt_tokens), 0), 4
    )
    ELSE 0
  END AS hit_ratio,
  ROUND(SUM(COALESCE(cache_write_tokens, 0))::numeric /
        NULLIF(COUNT(*), 0), 0) AS avg_cache_write_per_req
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
  AND success = true
  AND prompt_tokens > 100  -- 过滤心跳/超短请求
GROUP BY outbound_model
ORDER BY cache_read DESC
LIMIT 30;
```

### 2.3 按 provider 分组（识别 provider 级缓存支持差异）

```sql
-- 文件: scripts/cache-baseline/03-by-provider.sql
SELECT
  p.display_name AS provider,
  COUNT(*) AS request_count,
  SUM(COALESCE(cache_read_tokens, 0)) AS cache_read,
  CASE
    WHEN SUM(COALESCE(cache_read_tokens,0)) + SUM(COALESCE(prompt_tokens,0)) > 0
    THEN ROUND(
      SUM(cache_read_tokens)::numeric /
      NULLIF(SUM(cache_read_tokens) + SUM(prompt_tokens), 0), 4
    )
    ELSE NULL  -- NULL 表示该 provider 不支持缓存
  END AS hit_ratio
FROM request_logs r
JOIN providers p ON p.id = r.provider_id
WHERE r.ts >= NOW() - INTERVAL '7 days'
  AND r.success = true
GROUP BY p.display_name
ORDER BY request_count DESC;
```

### 2.4 按会话长度分组（识别长对话缓存效果）

```sql
-- 文件: scripts/cache-baseline/04-by-session-length.sql
-- 用途: 多轮对话 vs 单轮请求的缓存效果差异
WITH session_stats AS (
  SELECT
    session_id,
    COUNT(*) AS turn_count,
    SUM(COALESCE(cache_read_tokens, 0)) AS cache_read,
    SUM(COALESCE(prompt_tokens, 0)) AS prompt
  FROM request_logs
  WHERE ts >= NOW() - INTERVAL '7 days'
    AND success = true
    AND session_id IS NOT NULL
    AND session_id != ''
  GROUP BY session_id
)
SELECT
  CASE
    WHEN turn_count = 1 THEN '1_turn'
    WHEN turn_count BETWEEN 2 AND 5 THEN '2-5_turns'
    WHEN turn_count BETWEEN 6 AND 10 THEN '6-10_turns'
    WHEN turn_count BETWEEN 11 AND 20 THEN '11-20_turns'
    ELSE '21+_turns'
  END AS session_length_bucket,
  COUNT(*) AS session_count,
  ROUND(AVG(cache_read), 0) AS avg_cache_read,
  CASE
    WHEN SUM(COALESCE(cache_read,0)) + SUM(COALESCE(prompt,0)) > 0
    THEN ROUND(
      SUM(cache_read)::numeric /
      NULLIF(SUM(cache_read) + SUM(prompt), 0), 4
    )
    ELSE 0
  END AS hit_ratio
FROM session_stats
GROUP BY 1
ORDER BY 1;
```

### 2.5 按 prompt 长度分组（前缀长度对缓存的影响）

```sql
-- 文件: scripts/cache-baseline/05-by-prompt-length.sql
SELECT
  CASE
    WHEN prompt_tokens < 500 THEN '0-500'
    WHEN prompt_tokens < 2000 THEN '500-2K'
    WHEN prompt_tokens < 8000 THEN '2K-8K'
    WHEN prompt_tokens < 32000 THEN '8K-32K'
    ELSE '32K+'
  END AS prompt_length_bucket,
  COUNT(*) AS request_count,
  ROUND(AVG(COALESCE(cache_read_tokens, 0))::numeric, 0) AS avg_cache_read,
  CASE
    WHEN SUM(COALESCE(cache_read_tokens,0)) + SUM(COALESCE(prompt_tokens,0)) > 0
    THEN ROUND(
      SUM(cache_read_tokens)::numeric /
      NULLIF(SUM(cache_read_tokens) + SUM(prompt_tokens), 0), 4
    )
    ELSE 0
  END AS hit_ratio
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days' AND success = true
GROUP BY 1
ORDER BY 1;
```

### 2.6 按租户分组（识别租户级缓存差异）

```sql
-- 文件: scripts/cache-baseline/06-by-tenant.sql
SELECT
  tenant_id,
  COUNT(*) AS request_count,
  ROUND(SUM(COALESCE(cache_read_tokens, 0))::numeric /
        NULLIF(SUM(COALESCE(prompt_tokens, 0)), 0), 4) AS read_per_prompt_ratio,
  CASE
    WHEN SUM(COALESCE(cache_read_tokens,0)) + SUM(COALESCE(prompt_tokens,0)) > 0
    THEN ROUND(
      SUM(cache_read_tokens)::numeric /
      NULLIF(SUM(cache_read_tokens) + SUM(prompt_tokens), 0), 4
    )
    ELSE 0
  END AS hit_ratio
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days' AND success = true
GROUP BY tenant_id
HAVING COUNT(*) > 100  -- 过滤小流量租户
ORDER BY request_count DESC
LIMIT 30;
```

---

## 3. Admin API 快速查询

### 3.1 缓存经济学（首选入口）

```bash
# 最近 7 天
curl -s -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" \
  "http://<host>:8781/api/admin/usage/cache-economics?days=7" | jq .

# 最近 30 天
curl -s -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" \
  "http://<host>:8781/api/admin/usage/cache-economics?days=30" | jq .

# 自定义时间窗
curl -s -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" \
  "http://<host>:8781/api/admin/usage/cache-economics?start=2026-07-01&end=2026-07-08" | jq .
```

**响应字段说明**（`admin/usage_enhanced.go:445-459`）：

| 字段 | 含义 | 解读 |
|---|---|---|
| `cache_hit_ratio` | `cache_read / (cache_read + prompt)` | **核心基线指标** |
| `dollars_saved` | 缓存命中的成本节省 | 业务价值 |
| `savings_rate` | 节省比例（`total_saved / dollars_spent`） | ROI |
| `compressed_requests` | 启用压缩的请求数 | 与缓存的交互效果 |

### 3.2 数据生命周期（验证数据深度）

```bash
curl -s -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" \
  "http://<host>:8781/api/admin/data-lifecycle/metrics" | jq '.request_logs'
```

确认 `request_logs` 在期望时间窗（默认 90 天）内有数据。

---

## 4. C2 prefix 稳定化触发率诊断

### 4.1 响应头采样

```bash
# 验证 C2 是否真的改写了消息顺序
# tail 在前、system 在后 → 应该被改写
curl -i -X POST \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "messages": [
      {"role": "user", "content": "tail-first"},
      {"role": "system", "content": "stable system prompt"}
    ]
  }' \
  "http://<host>:8781/v1/chat/completions" | grep -i 'X-Gw-Prefix'
```

预期响应头：`X-Gw-Prefix-Stabilized: reordered by stability class`

### 4.2 启动日志验证

```bash
# 确认 C2 / C3 启动状态
grep -E 'prompt-cache (prefix stabilization|cache-control injection) (enabled|disabled)' \
  /var/log/llm-gateway/gateway.log

# 或 stderr（取决于 LLM_GATEWAY_LOG_FILE 配置）
journalctl -u llm-gateway -g 'prompt-cache' | tail -10
```

**生产环境推断配置**（基于代码默认值 + 文档）：
- `LLM_GATEWAY_PROMPT_CACHE_STABILIZE = true` ✅ 已启用
- `LLM_GATEWAY_PROMPT_CACHE_INJECT = false` ❌ 默认关闭

### 4.3 C2 改写率统计 SQL（通过 proxy 层日志聚合）

如果日志已收集到 `X-Gw-Prefix-Stabilized` 响应头，可通过网关日志统计：

```bash
# 从网关访问日志统计改写率
grep 'X-Gw-Prefix-Stabilized' /var/log/llm-gateway/access.log | wc -l  # 触发次数
wc -l /var/log/llm-gateway/access.log  # 总请求数
```

---

## 5. C3 cache_control 注入覆盖率诊断

### 5.1 启动状态确认

```bash
grep 'prompt-cache-control injection' /var/log/llm-gateway/gateway.log
# 期望: "enabled" → C3 已配置生效
# 期望: "disabled" 或缺失 → C3 未启用（需要检查 env vars）
```

### 5.2 通过 SQL 估算覆盖率（间接指标）

```sql
-- 文件: scripts/cache-baseline/07-inject-coverage.sql
-- 用途: 估算 cache_control 注入的潜在覆盖率
SELECT
  outbound_model,
  COUNT(*) FILTER (WHERE cache_read_tokens > 0) AS req_with_cache_read,
  COUNT(*) AS total_req,
  ROUND(
    100.0 * COUNT(*) FILTER (WHERE cache_read_tokens > 0) /
    NULLIF(COUNT(*), 0), 2
  ) AS cache_read_pct
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
  AND success = true
GROUP BY outbound_model
HAVING COUNT(*) > 100
ORDER BY cache_read_pct DESC;
```

> 含义：如果某模型 `cache_read_pct` 高，说明上游缓存**已被有效利用**（即使 C3 未启用，C2 稳定化也起作用了）；如果所有模型 `cache_read_pct` 都很低，说明 C3 关闭是关键瓶颈。

---

## 6. 会话粘性 vs 缓存亲和性诊断

### 6.1 利用 `affinity_hit` 字段

数据库表已有 `affinity_hit boolean` 字段（`request_logs` schema），记录请求是否命中粘性凭据。

```sql
-- 文件: scripts/cache-baseline/08-affinity-vs-cache.sql
-- 用途: 关联粘性命中与缓存命中
SELECT
  CASE WHEN affinity_hit THEN 'affinity_hit' ELSE 'affinity_miss' END AS affinity,
  COUNT(*) AS request_count,
  SUM(COALESCE(cache_read_tokens, 0)) AS cache_read,
  CASE
    WHEN SUM(COALESCE(cache_read_tokens,0)) + SUM(COALESCE(prompt_tokens,0)) > 0
    THEN ROUND(
      SUM(cache_read_tokens)::numeric /
      NULLIF(SUM(cache_read_tokens) + SUM(prompt_tokens), 0), 4
    )
    ELSE 0
  END AS hit_ratio
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days' AND success = true
GROUP BY affinity_hit;
```

**解读**：
- 如果 `affinity_hit` 命中率显著高于 `affinity_miss` → **说明凭据稳定性确实影响缓存**
- 如果两者相近 → **说明缓存主要依赖前缀稳定性而非凭据稳定性**（这正是审计的核心结论）

### 6.2 利用 session_credential_rotations 表

```sql
-- 文件: scripts/cache-baseline/09-rotation-vs-cache.sql
-- 用途: 评估凭据轮换对缓存的影响
WITH rotation_counts AS (
  SELECT
    session_id,
    COUNT(*) AS rotation_count
  FROM session_credential_rotations
  WHERE started_at >= NOW() - INTERVAL '7 days'
  GROUP BY session_id
)
SELECT
  CASE
    WHEN COALESCE(rotation_count, 0) = 0 THEN '0_rotations'
    WHEN rotation_count = 1 THEN '1_rotation'
    WHEN rotation_count BETWEEN 2 AND 5 THEN '2-5_rotations'
    ELSE '6+_rotations'
  END AS rotation_bucket,
  COUNT(DISTINCT r.session_id) AS session_count,
  ROUND(AVG(r.cache_read_tokens), 0) AS avg_cache_read
FROM request_logs r
LEFT JOIN rotation_counts rc ON rc.session_id = r.session_id
WHERE r.ts >= NOW() - INTERVAL '7 days'
  AND r.success = true
  AND r.session_id IS NOT NULL
GROUP BY 1
ORDER BY 1;
```

---

## 7. 凭据健康度对缓存的影响

### 7.1 切换率统计

```sql
-- 文件: scripts/cache-baseline/10-credential-switch-rate.sql
SELECT
  switch_reason,
  COUNT(*) AS rotation_count,
  COUNT(DISTINCT session_id) AS affected_sessions,
  ROUND(AVG(EXTRACT(EPOCH FROM (
    COALESCE(ended_at, NOW()) - started_at
  ))), 0) AS avg_duration_sec
FROM session_credential_rotations
WHERE started_at >= NOW() - INTERVAL '7 days'
GROUP BY switch_reason
ORDER BY rotation_count DESC;
```

### 7.2 健康度变化 vs 缓存

```sql
-- 文件: scripts/cache-baseline/11-health-vs-cache.sql
SELECT
  c.label AS credential,
  c.status,
  c.trust_level,
  COUNT(r.id) AS request_count,
  ROUND(AVG(r.cache_read_tokens), 0) AS avg_cache_read,
  ROUND(AVG(r.affinity_hit::int), 4) AS affinity_hit_rate
FROM request_logs r
JOIN credentials c ON c.id = r.credential_id
WHERE r.ts >= NOW() - INTERVAL '7 days' AND r.success = true
GROUP BY c.id, c.label, c.status, c.trust_level
ORDER BY request_count DESC
LIMIT 30;
```

---

## 8. 执行步骤

### 步骤 1：环境准备

```bash
# 选项 A：本地 r112（结构验证 + 受控流量验证）
PGPASSWORD='kxpass' psql -h localhost -p 15432 -U kxuser -d llm_gateway -f <query>

# 选项 B：真实运行环境
# 前提：目标机器上已经实际运行 llm-gateway-go，且 request_logs / usage_ledger 有真实流量数据。
# 如果目标环境当前不承载 llm-gateway-go（例如仅是开发机或 legacy gateway 主机），
# 则不适合执行本计划中的真实缓存基线采集。
```

### 步骤 2：跑基线 SQL

```bash
# 创建诊断目录
mkdir -p scripts/cache-baseline/
# 复制上面 11 个 SQL 文件到该目录
# 在真实运行环境逐个执行

# 示例
PGPASSWORD="$PG_LLM_GATEWAY_PASS" psql \
  -h "$PG_LLM_GATEWAY_HOST" \
  -U "$PG_LLM_GATEWAY_USER" \
  -d llm_gateway \
  -f scripts/cache-baseline/01-global-7d.sql
```

### 步骤 3：跑 Admin API 拉数据

```bash
# 在真实运行环境
export LLM_GATEWAY_ADMIN_API_KEY="..."
curl -s -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" \
  "http://$TARGET_GATEWAY_HOST:8781/api/admin/usage/cache-economics?days=7" | tee /tmp/cache-economics-7d.json
```

### 步骤 4：响应头抽样

```bash
# 验证 C2 是否生效
curl -i -X POST \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-3-5-sonnet-20241022","messages":[
    {"role":"user","content":"test"},
    {"role":"system","content":"system prompt"}
  ]}' \
  "http://$TARGET_GATEWAY_HOST:8781/v1/chat/completions" | grep -i 'X-Gw-Prefix'
```

### 步骤 5：生成诊断报告

将 SQL 输出 + Admin API 输出整理为基线报告。

---

## 9. 诊断报告模板

```markdown
# 缓存命中率基线报告

**采集日期**: YYYY-MM-DD
**时间窗**: 最近 7 天
**数据源**: request_logs / usage_ledger / Admin API

## 1. 全局基线
- 总请求数: N
- 总 prompt tokens: N
- 总 cache_read tokens: N
- 缓存命中率: XX.XX%

## 2. 按模型（TOP 10）
| 模型 | 请求数 | 命中率 |
|---|---|---|
| claude-3-5-sonnet | X | Y% |
| gpt-4o | X | Y% |

## 3. 按 Provider
| Provider | 请求数 | 命中率 | 是否支持缓存 |
|---|---|---|---|
| Anthropic | X | Y% | ✅ |
| OpenAI | X | Y% | ✅ |
| DeepSeek | X | N/A | ❌ |

## 4. C2 Prefix 稳定化
- 启动状态: enabled
- 响应头触发率（抽样）: X%
- 评估: 是否需要调整 TailTurns 参数

## 5. C3 Cache Control 注入
- 启动状态: enabled/disabled
- 覆盖率（间接估算）: X%
- 建议: 是否需要启用

## 6. 凭据粘性 vs 缓存
- affinity_hit 命中率: X%
- affinity_miss 命中率: Y%
- 结论: 粘性对缓存的影响 [显著/不显著]

## 7. 凭据轮换影响
- 平均每会话轮换次数: X
- 主要轮换原因: ...
- 对缓存影响: [高/中/低]

## 8. 优化优先级建议
1. [P0] ...
2. [P1] ...
3. [P2] ...
```

---

## 10. 关键决策点

诊断完成后，根据结果决定：

### 场景 A: 全局命中率 < 30%
**根因假设**: C3 未启用或 C2 参数不当
**优化方向**: 启用 `LLM_GATEWAY_PROMPT_CACHE_INJECT`，调整 `TailTurns`

### 场景 B: 长对话（>10 turns）命中率 < 50%
**根因假设**: 会话上下文增长导致前缀漂移
**优化方向**: 检查 session 压缩策略、消息历史截断逻辑

### 场景 C: affinity_hit 命中率显著高于 affinity_miss（差 >10pp）
**根因假设**: 凭据切换导致上游缓存失效
**优化方向**: 优化粘性路由（延长 L1 TTL、降低健康检查误报率）
**注意**: 此场景下审计结论需要修正

### 场景 D: 不同 provider 命中率差异 >20pp
**根因假设**: Provider 缓存机制差异
**优化方向**: 按 provider 分别优化 prefix 策略

### 场景 E: 所有场景命中率均 < 10%
**根因假设**: 请求模式本身不适合缓存（短请求、单轮）
**优化方向**: 评估是否值得优化（ROI 低）

---

## 11. 风险与限制

1. **数据时间窗**: 默认 90 天保留，`log.request_retention_days` 可调
2. **采样偏差**: 高频租户主导平均值，建议按租户分层分析
3. **跨月分区**: `usage_ledger_with_current_month` 视图已处理，无需手动 UNION
4. **affinity_hit 字段**: 首次诊断需要确认此字段已正确埋点（看代码逻辑）

---

## 12. 下一步行动

诊断完成后，将进入审计报告中规划的：
- **阶段 1**: 前缀稳定性优化（基于诊断结果细化）
- **阶段 2**: 粘性路由微调（仅在场景 C 成立时）
- **阶段 3**: 效果验证与调优

---

**诊断方案 v1.0** — 等待用户在真实承载 llm-gateway-go 的环境执行后回填基线数据。
