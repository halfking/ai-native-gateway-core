# P0-4: 缓存Token口径修复效果验证

## 状态：✅ 代码已修复，待数据验证

**修复日期**: 2026-10-06（代码注释）  
**验证日期**: 2026-11-26  
**验证人**: AI Assistant

---

## 修复位置确认

### 代码位置
`domains/streaming/handler.go:8075`

```go
reqLog.CostUSD, reqLog.CostDisplay, reqLog.CostCurrency = AssignRequestCost(CostPriceInput{
    PromptTokens:     reqLog.PromptTokens,
    CompletionTokens: reqLog.CompletionTokens,
    CacheReadTokens:  reqLog.CacheReadTokens,
    CacheWriteTokens: reqLog.CacheWriteTokens,
    PriceIn:          result.Candidate.PriceInPer1M,
    PriceOut:         result.Candidate.PriceOutPer1M,
    CacheReadPrice:   result.Candidate.CacheReadPricePer1M,
    CacheWritePrice:  result.Candidate.CacheWritePricePer1M,
    Currency:         result.Candidate.Currency,
    Convention:       CacheTokenConventionForProtocol(result.Candidate.Protocol), // ✅ 修复
})
```

### 修复说明（代码注释L8059-8064）
> ★ 2026-10-06 接线：口径由**上游协议**带下来，不再是「必须人工声明」。  
> result.Candidate.Protocol 与四个价字段来自候选 SQL 的**同一行**。  
> 之前这里不传 Convention ⇒ 永远是零值 ⇒ 一律按 OpenAI 口径扣 cache  
> ⇒ Anthropic 上游（input_tokens 不含 cache）被扣成负数。  
> 非 anthropic 协议返回 Unset，行为与改动前**逐字节相同**。

---

## 验证清单

### 验证1: 负成本检查（最关键）

**预期**: 2026-10-06之后应该没有负成本记录

```sql
-- 检查修复前后的负成本行数
SELECT 
    DATE(created_at) AS date,
    COUNT(*) FILTER (WHERE cost_usd < 0) AS negative_cost_count,
    COUNT(*) AS total_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE cost_usd < 0) / COUNT(*), 2) AS negative_pct
FROM session_turns
WHERE created_at >= '2026-09-01'  -- 修复前1个月
  AND created_at < '2026-12-01'   -- 修复后2个月
  AND cost_usd IS NOT NULL
GROUP BY DATE(created_at)
ORDER BY date;
```

**预期结果**:
- 2026-10-06之前：`negative_cost_count > 0`
- 2026-10-06之后：`negative_cost_count = 0`

---

### 验证2: Anthropic协议的cache>prompt场景

**原问题**: cache_read_tokens > prompt_tokens时计算成负数

```sql
-- 检查Anthropic流量中cache>prompt的情况
SELECT 
    DATE(created_at) AS date,
    COUNT(*) AS cache_gt_prompt_count,
    COUNT(*) FILTER (WHERE cost_usd < 0) AS negative_cost_count,
    COUNT(*) FILTER (WHERE cost_usd IS NULL) AS null_cost_count,
    AVG(COALESCE(cost_usd, 0)) AS avg_cost_usd,
    SUM(COALESCE(cache_read_tokens, 0)) AS total_cache_tokens,
    SUM(COALESCE(prompt_tokens, 0)) AS total_prompt_tokens
FROM session_turns
WHERE created_at >= '2026-10-01'
  AND created_at < '2026-12-01'
  AND cache_read_tokens > prompt_tokens  -- 关键条件
  AND protocol LIKE '%anthropic%'        -- Anthropic协议
GROUP BY DATE(created_at)
ORDER BY date;
```

**预期结果**:
- 2026-10-06之前：`negative_cost_count > 0` 或 `null_cost_count > 0`
- 2026-10-06之后：`negative_cost_count = 0` 且 `avg_cost_usd > 0`

---

### 验证3: 修复前后成本对比

**对比修复前后的成本统计**

```sql
-- 对比修复前后30天的成本
WITH periods AS (
    SELECT 
        CASE 
            WHEN created_at < '2026-10-06' THEN 'before_fix'
            ELSE 'after_fix'
        END AS period,
        COUNT(*) AS request_count,
        SUM(COALESCE(prompt_tokens, 0)) AS total_prompt_tokens,
        SUM(COALESCE(completion_tokens, 0)) AS total_completion_tokens,
        SUM(COALESCE(cache_read_tokens, 0)) AS total_cache_tokens,
        SUM(COALESCE(cost_usd, 0)) AS total_cost_usd,
        COUNT(*) FILTER (WHERE cost_usd < 0) AS negative_cost_count,
        COUNT(*) FILTER (WHERE cost_usd IS NULL) AS null_cost_count
    FROM session_turns
    WHERE created_at >= '2026-09-06'  -- 修复前30天
      AND created_at < '2026-11-06'   -- 修复后30天
      AND protocol LIKE '%anthropic%'
    GROUP BY period
)
SELECT 
    period,
    request_count,
    total_cache_tokens,
    total_prompt_tokens,
    total_cost_usd,
    negative_cost_count,
    null_cost_count,
    ROUND(total_cost_usd / NULLIF(request_count, 0), 6) AS avg_cost_per_request
FROM periods
ORDER BY period;
```

**预期结果**:
- `before_fix`: `negative_cost_count > 0`, `avg_cost_per_request` 偏低
- `after_fix`: `negative_cost_count = 0`, `avg_cost_per_request` 更合理（包含cache成本）

---

### 验证4: 口径识别函数检查

**验证 `CacheTokenConventionForProtocol` 函数是否正确**

需要查看该函数的实现，确认：
- Anthropic协议返回 `AnthropicConvention`
- OpenAI协议返回 `OpenAIConvention`
- 其他协议返回 `Unset` 或 `OpenAIConvention`（默认）

```bash
# 搜索函数定义
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
grep -rn "func CacheTokenConventionForProtocol" domains/streaming/
```

---

### 验证5: 实际数据抽样检查

**随机抽取修复后的Anthropic请求，手工验证成本计算**

```sql
-- 抽取修复后的样本
SELECT 
    request_id,
    created_at,
    protocol,
    prompt_tokens,
    completion_tokens,
    cache_read_tokens,
    cache_write_tokens,
    cost_usd
FROM session_turns
WHERE created_at >= '2026-10-06'
  AND created_at < '2026-11-26'
  AND protocol LIKE '%anthropic%'
  AND cache_read_tokens > prompt_tokens  -- 关键场景
ORDER BY RANDOM()
LIMIT 10;
```

**手工验证**:
对每条记录，根据Anthropic口径手工计算：
```
expected_cost = (prompt_tokens * price_in + 
                 completion_tokens * price_out + 
                 cache_read_tokens * cache_read_price) / 1_000_000
```

对比 `cost_usd` 是否与 `expected_cost` 一致（误差 < 1%）

---

## 执行验证

### 步骤1: 连接数据库
```bash
# 连接到生产只读副本（避免影响主库）
psql -h <readonly-host> -U <user> -d <database>
```

### 步骤2: 执行验证SQL
依次执行验证1-5的SQL查询，记录结果。

### 步骤3: 分析结果
根据查询结果，填写下方的验证报告。

---

## 验证报告模板

```
# 缓存Token口径修复验证报告

## 执行信息
- 验证日期: YYYY-MM-DD
- 验证人: <name>
- 数据库: <host/database>
- 数据范围: 2026-09-01 至 2026-11-26

## 验证结果

### 1. 负成本检查
- 修复前（2026-09-06 至 2026-10-05）:
  - 总请求数: <count>
  - 负成本行数: <count>
  - 负成本比例: <percentage>%

- 修复后（2026-10-06 至 2026-11-26）:
  - 总请求数: <count>
  - 负成本行数: <count> ✅ 预期为0
  - 负成本比例: <percentage>% ✅ 预期为0

### 2. Anthropic cache>prompt场景
- 修复前:
  - cache>prompt请求数: <count>
  - 其中负成本: <count>
  - 平均成本: $<amount>

- 修复后:
  - cache>prompt请求数: <count>
  - 其中负成本: <count> ✅ 预期为0
  - 平均成本: $<amount> ✅ 预期>0

### 3. 修复前后成本对比
| 指标 | 修复前 | 修复后 | 变化 |
|------|--------|--------|------|
| 请求数 | <count> | <count> | - |
| Cache Token总量 | <count> | <count> | - |
| 总成本 | $<amount> | $<amount> | +X% |
| 平均单价 | $<amount> | $<amount> | +X% |
| 负成本行数 | <count> | 0 ✅ | -100% |

### 4. 口径识别函数
- ✅ 函数存在且逻辑正确
- ✅ Anthropic协议返回正确口径
- ✅ OpenAI协议返回正确口径

### 5. 实际数据抽样
抽取10条记录，手工验证：
- ✅ 10/10条成本计算正确
- ✅ 误差范围 < 1%

## 结论
- [ ] ✅ 修复生效，负成本问题已解决
- [ ] ⚠️ 部分生效，仍有<count>条负成本（需进一步调查）
- [ ] ❌ 修复未生效，问题依然存在

## 建议
（根据验证结果填写）

## 附录
（附上关键SQL查询的原始输出）
```

---

## 如果验证失败

### 场景1: 修复后仍有负成本

**可能原因**:
1. 部署未完成（某些节点仍在运行旧代码）
2. `CacheTokenConventionForProtocol` 函数逻辑错误
3. 某些边缘协议未正确识别

**排查步骤**:
```sql
-- 查看负成本请求的协议分布
SELECT 
    protocol,
    COUNT(*) AS negative_count,
    ARRAY_AGG(request_id ORDER BY created_at DESC LIMIT 5) AS sample_request_ids
FROM session_turns
WHERE created_at >= '2026-10-06'
  AND cost_usd < 0
GROUP BY protocol
ORDER BY negative_count DESC;
```

**修复建议**:
- 检查代码部署状态
- 验证 `CacheTokenConventionForProtocol` 函数
- 添加缺失协议的口径映射

### 场景2: 修复后成本为NULL

**可能原因**:
1. `CalcCost` 函数在某些条件下返回错误
2. 价格数据缺失（`PriceInPer1M` 等为NULL）

**排查步骤**:
```sql
-- 查看NULL成本的原因
SELECT 
    request_id,
    protocol,
    prompt_tokens,
    cache_read_tokens,
    cost_usd
FROM session_turns
WHERE created_at >= '2026-10-06'
  AND cost_usd IS NULL
  AND (prompt_tokens > 0 OR completion_tokens > 0)
ORDER BY created_at DESC
LIMIT 20;
```

---

## 相关文档

- [第一轮审计报告](../audit/2026-11-26-pricing-system-audit-report.html) - 识别了此问题
- [usage.go](../../domains/streaming/usage.go) - CalcCost函数实现
- [handler.go:8075](../../domains/streaming/handler.go#L8075) - 修复位置

---

## 总结

**修复状态**: ✅ 代码已修复（2026-10-06）  
**验证状态**: ⏳ 待执行数据验证  
**预期效果**: 负成本行数归零，4.5M cache token正确计费

**下一步**:
1. 在生产数据库执行验证SQL
2. 填写验证报告
3. 如验证通过，标记P0-4为完成
4. 如验证失败，根据排查步骤定位问题

---

**创建日期**: 2026-11-26  
**创建人**: AI Assistant  
**状态**: 验证脚本已准备，待执行
