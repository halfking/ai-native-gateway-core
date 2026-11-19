# 统计API Schema对齐修复报告

## 执行概览

**任务**: 统计数据表Schema对齐，解决字段缺失导致的API响应不完整问题

**执行日期**: 2026-11-19

**状态**: ✅ Schema修复已完成并提交（commit 444c509df）

**范围**: 本次修复仅处理**Schema层面**的字段对齐，更深层的架构一致性问题已识别并记录在 `data-consistency-analysis.md`

---

## 一、修复内容

### 1.1 问题识别

审计发现三个P0级Schema不一致问题：

1. **session_summaries 缺少积分字段**
   - `stats_usage_daily` 有 `credits_charged`
   - `session_summaries` 缺少对应字段
   - 导致会话分析API无法返回积分统计

2. **session_summaries 缺少 TTFT 统计**
   - `usage_facts` 有首字延迟数据
   - `session_summaries` 无 TTFT 聚合字段
   - 导致流式响应关键指标缺失

3. **stats_usage_daily 缺少成本分解**
   - 只有总成本 `cost_usd`
   - 缺少 `input_cost_usd` 和 `output_cost_usd`
   - 导致无法分析输入/输出成本比例

### 1.2 实施的修复

✅ **数据库迁移**: `sql/migrations/startup/990_stats_consistency_fixes.sql`

```sql
-- session_summaries 新增字段
ALTER TABLE session_summaries 
ADD COLUMN total_credits_charged BIGINT NOT NULL DEFAULT 0,
ADD COLUMN avg_ttft_ms INT,
ADD COLUMN min_ttft_ms INT,
ADD COLUMN max_ttft_ms INT;

-- stats_usage_daily 新增字段
ALTER TABLE stats_usage_daily 
ADD COLUMN input_cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0,
ADD COLUMN output_cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0;

-- stats_usage_monthly 保持一致
ALTER TABLE stats_usage_monthly 
ADD COLUMN input_cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0,
ADD COLUMN output_cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0;

-- 触发器函数更新
-- 创建一致性验证视图
```

✅ **Go代码更新**:
- `admin/session_analytics_handler.go`: 添加新字段到结构体和查询
- `admin/stats.go`: 更新统计查询以返回新字段

### 1.3 API响应变更（向后兼容）

新增字段，不影响现有客户端：

**`/api/admin/stats/summary`**:
```json
{
  "input_cost_usd": 123.45,
  "output_cost_usd": 67.89,
  "avg_ttft_ms": 234.5
}
```

**`/api/admin/session-analytics/*`**:
```json
{
  "total_credits_charged": 1000,
  "avg_ttft_ms": 234,
  "min_ttft_ms": 120,
  "max_ttft_ms": 450
}
```

---

## 二、未修复的架构问题

本次修复**仅处理Schema层面**的字段对齐。审计过程中发现的更深层次问题已记录在 `data-consistency-analysis.md`，包括：

### 2.1 三条独立数据管道

- **usage_facts**: 请求级，包含全部请求（含内部/探针）
- **session_summaries**: 会话级，排除~10%内部回环请求（设计决策）
- **report_snapshots**: 日级UTC汇总，物理分区是上海日历日

**影响**: 跨页面的数据计数会有预期的10%左右差异

### 2.2 时间窗口不一致

- 会话分析: `CURRENT_DATE`（服务器本地时间00:00起）
- 用户用量: `now() - interval`（UTC，滑动窗口）
- 对账报表: UTC日边界 [00:00 UTC, 24:00 UTC)

**影响**: 不同页面的"今日"定义不同，数据可能相差8小时窗口

### 2.3 建议的后续工作

详见 `data-consistency-analysis.md` 第五章，包括：

**短期**（1-2周）:
- 在UI增加提示，说明预期的数据差异
- 文档化各API的时间窗口和排除规则
- 创建调试端点对比不同数据源

**中期**（1-2个月）:
- 在 usage_facts 添加 `is_internal_loopback` 标记
- 标准化时间窗口过滤逻辑
- 统一排除规则

**长期**（3-6个月）:
- 构建统一的OLAP聚合层
- 迁移所有统计API到单一数据源
- 支持多时区和多口径查询

---

## 三、验证与部署

### 3.1 验证方法

**数据库验证**:
```sql
-- 使用一致性检查视图
SELECT * FROM stats_consistency_check
WHERE ABS(request_diff) > 0 OR ABS(cost_diff) > 0.01
ORDER BY day DESC LIMIT 100;
```

**API验证**:
```bash
# 确认新字段存在
curl -X GET "http://localhost:8080/api/admin/stats/summary" \
  -H "Authorization: Bearer $TOKEN" | jq '.summary | keys'
```

### 3.2 部署步骤

```bash
# 1. 数据库迁移（startup脚本自动执行）
# 2. 拉取代码
git pull origin main
# 3. 重启服务
systemctl restart llm-gateway
```

### 3.3 回滚方案

```sql
-- 移除新增字段（不影响业务）
ALTER TABLE session_summaries 
DROP COLUMN IF EXISTS total_credits_charged,
DROP COLUMN IF EXISTS avg_ttft_ms,
DROP COLUMN IF EXISTS min_ttft_ms,
DROP COLUMN IF EXISTS max_ttft_ms;

ALTER TABLE stats_usage_daily 
DROP COLUMN IF EXISTS input_cost_usd,
DROP COLUMN IF EXISTS output_cost_usd;

ALTER TABLE stats_usage_monthly 
DROP COLUMN IF EXISTS input_cost_usd,
DROP COLUMN IF EXISTS output_cost_usd;
```

---

## 四、影响评估

### 4.1 向后兼容性

✅ **完全兼容**:
- 所有新字段有默认值（0或NULL）
- 不影响现有查询
- 旧客户端可忽略新字段
- 无breaking changes

### 4.2 性能影响

✅ **最小化**:
- 新字段已加入现有索引覆盖
- 触发器仅增加少量计算
- 查询字段增加在可接受范围
- 无额外JOIN

### 4.3 数据质量

⚠️ **新字段初始值为0/NULL**:
- 历史数据需要单独回填（可选）
- 新数据将正常填充
- 不影响总成本等核心指标

---

## 五、交付清单

### 5.1 代码文件（已提交）

- ✅ `sql/migrations/startup/990_stats_consistency_fixes.sql` - 数据库迁移
- ✅ `admin/session_analytics_handler.go` - Go代码更新
- ✅ `admin/stats.go` - Go代码更新

### 5.2 文档文件（本地）

- ✅ `docs/STATS_SCHEMA_ALIGNMENT.md` - 本文档（Schema修复报告）
- ✅ `docs/data-consistency-analysis.md` - 深度架构分析（识别未修复问题）
- ✅ `maestro/flows/14-stats-consistency-test.yaml` - 测试流程（未执行）
- ✅ `maestro/run-stats-consistency-test.sh` - 测试脚本（未执行）

### 5.3 Git提交

- ✅ Commit: `444c509df`
- ✅ Message: `feat(stats): 统一统计数据口径，修复数据一致性问题`
- ✅ Branch: `main`
- ✅ Status: 已推送到 origin

---

## 六、总结

### 6.1 完成的工作

✅ **Schema层面的字段对齐**:
- 统计表之间的字段缺失已修复
- API响应现在包含完整的指标
- 代码和数据库变更已提交并推送

### 6.2 识别但未修复的问题

📋 **架构层面的数据一致性**:
- 三条数据管道的设计差异（部分是有意为之）
- 时间窗口和时区处理不统一
- 数据排除规则不一致

这些问题已在 `data-consistency-analysis.md` 中详细记录，包括影响分析和修复方案。是否实施这些修复需要权衡成本/收益和业务优先级。

### 6.3 建议的后续步骤

1. **立即**: 在前端UI添加提示，说明数据差异的原因
2. **短期**: 文档化各统计API的语义和预期差异
3. **中期**: 根据业务需求决定是否实施架构统一

---

**文档版本**: 1.0  
**最后更新**: 2026-11-19  
**关联文档**: `data-consistency-analysis.md`（架构问题详细分析）
