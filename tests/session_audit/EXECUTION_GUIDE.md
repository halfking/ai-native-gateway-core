# 会话输出审计测试 - 执行指南

## 快速开始

### 1. 前置条件

- ✅ Go 1.21+
- ✅ PostgreSQL 客户端（psql）
- ✅ 可访问 252 服务器数据库（172.16.2.210:5432）

### 2. 一键执行

```bash
cd tests/session_audit
chmod +x run_test.sh
./run_test.sh
```

### 3. 手动执行（详细步骤）

#### 步骤 1: 初始化数据库表

```bash
export PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg"
psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway -f schema.sql
```

**验证**:
```sql
\dt audit_*
-- 应该看到:
-- audit_test_results
-- audit_test_runs
```

#### 步骤 2: 检查配置文件

```bash
cat 02_sensitive_words_test.yaml | head -50
```

配置包含:
- 政治敏感词（中英文）
- 色情暴力
- 违禁品
- PII 检测规则（身份证、手机号、邮箱等）
- Prompt Injection 检测规则
- Jailbreak 检测规则

#### 步骤 3: 编译测试程序

```bash
cd ../..
go build -o tests/session_audit/audit_test tests/session_audit/audit_test_all_in_one.go
cd tests/session_audit
```

#### 步骤 4: 运行测试

```bash
./audit_test
```

**预期输出**:
```
=== 会话输出审计完整测试 ===

[步骤 1/4] 数据提取...
  已处理: 100 条
  已处理: 200 条
  ...
✅ 提取完成: 10000 条记录

[步骤 2/4] 加载测试配置...
✅ 配置完成: 156 个敏感词

[步骤 3/4] 执行审计测试...
  进度: 100/10000 (1.0%)
  进度: 200/10000 (2.0%)
  ...
✅ 审计完成: 10000 条结果

[步骤 4/4] 保存结果...
✅ 结果已保存到数据库

=== 测试摘要 ===
测试批次 ID: test_a1b2c3d4
总记录数: 10000

决策分布:
  Pass:         9500 (95.0%)
  Warn:         400 (4.0%)
  Block:        50 (0.5%)
  NeedApproval: 50 (0.5%)

威胁检测:
  Prompt Injection: 30 (0.3%)
  PII Leak:         20 (0.2%)
  Jailbreak:        10 (0.1%)

性能指标:
  P50: 2 ms
  P95: 4 ms
  P99: 8 ms
  Max: 15 ms
  Min: 1 ms

💡 查询结果:
  psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway
  SELECT * FROM v_audit_performance_summary WHERE test_run_id = 'test_a1b2c3d4';
```

---

## 结果分析

### 1. 查看性能统计

```sql
-- 连接数据库
psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway

-- 查询最新的测试结果
SELECT 
    test_run_id,
    total_tests,
    avg_latency_ms,
    p50_latency_ms,
    p95_latency_ms,
    p99_latency_ms,
    throughput_per_sec,
    decision_pass,
    decision_warn,
    decision_approval,
    has_injection,
    has_pii,
    has_jailbreak
FROM v_audit_performance_summary
ORDER BY total_tests DESC
LIMIT 5;
```

### 2. 查看测试批次详情

```sql
SELECT 
    test_run_id,
    description,
    total_records,
    completed_records,
    avg_latency_ms,
    p95_latency_ms,
    decision_pass,
    decision_warn,
    threat_injection,
    threat_pii,
    threat_jailbreak,
    started_at,
    completed_at,
    duration_seconds
FROM audit_test_runs
ORDER BY started_at DESC
LIMIT 1;
```

### 3. 查看具体审计结果

```sql
-- 查看需要审批的案例
SELECT 
    request_id,
    LEFT(content, 100) AS content_preview,
    detect_score,
    decision,
    reason,
    sensitive_words,
    threats,
    detect_latency_ms
FROM audit_test_results
WHERE test_run_id = 'test_a1b2c3d4'
  AND decision = 'need_approval'
ORDER BY detect_score DESC
LIMIT 10;
```

### 4. 查看敏感词命中排行

```sql
SELECT 
    word,
    hit_count,
    unique_requests
FROM v_sensitive_words_ranking
WHERE test_run_id = 'test_a1b2c3d4'
ORDER BY hit_count DESC
LIMIT 20;
```

### 5. 分析性能瓶颈

```sql
-- 查看耗时最长的案例
SELECT 
    request_id,
    content_length,
    detect_latency_ms,
    detect_score,
    decision,
    sensitive_count,
    threat_count
FROM audit_test_results
WHERE test_run_id = 'test_a1b2c3d4'
ORDER BY detect_latency_ms DESC
LIMIT 20;

-- 分析内容长度与耗时的关系
SELECT 
    CASE 
        WHEN content_length < 100 THEN '短文本(<100)'
        WHEN content_length < 500 THEN '中等(100-500)'
        WHEN content_length < 2000 THEN '长文本(500-2000)'
        ELSE '超长(>2000)'
    END AS length_range,
    COUNT(*) AS count,
    AVG(detect_latency_ms) AS avg_latency,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY detect_latency_ms) AS p95_latency
FROM audit_test_results
WHERE test_run_id = 'test_a1b2c3d4'
GROUP BY length_range
ORDER BY avg_latency DESC;
```

---

## 人工标注指南

为了评估准确率，需要对部分结果进行人工标注。

### 1. 抽取标注样本

```sql
-- 创建标注样本表
CREATE TEMP TABLE annotation_samples AS
SELECT 
    request_id,
    content,
    detect_score,
    decision,
    sensitive_words,
    threats
FROM audit_test_results
WHERE test_run_id = 'test_a1b2c3d4'
  AND (
    -- Pass 类型：抽取可能漏报的
    (decision = 'pass' AND content_length > 500) OR
    -- Warn 类型：抽取可能误报的
    (decision = 'warn' AND sensitive_count < 3) OR
    -- NeedApproval 类型：全部标注
    decision = 'need_approval'
  )
ORDER BY RANDOM()
LIMIT 300;

-- 导出为 CSV
\copy annotation_samples TO 'annotation_samples.csv' CSV HEADER;
```

### 2. 标注规则

对每条样本标注以下字段:
- **manual_label**: `safe` / `sensitive` / `malicious`
- **manual_notes**: 标注说明（可选）

**标注标准**:
- `safe`: 无任何敏感或恶意内容，机器误报
- `sensitive`: 包含敏感词或 PII，但无恶意
- `malicious`: 包含 Prompt Injection、Jailbreak 或恶意攻击

### 3. 更新标注结果

```sql
-- 批量更新（使用临时表）
CREATE TEMP TABLE annotation_results (
    request_id TEXT,
    manual_label TEXT,
    manual_notes TEXT
);

-- 导入标注结果
\copy annotation_results FROM 'annotation_results.csv' CSV HEADER;

-- 更新到审计结果表
UPDATE audit_test_results ar
SET 
    manual_label = anr.manual_label,
    manual_notes = anr.manual_notes,
    is_false_positive = CASE 
        WHEN ar.decision IN ('warn', 'block', 'need_approval') 
             AND anr.manual_label = 'safe' 
        THEN true 
        ELSE false 
    END,
    is_false_negative = CASE 
        WHEN ar.decision = 'pass' 
             AND anr.manual_label IN ('sensitive', 'malicious') 
        THEN true 
        ELSE false 
    END,
    updated_at = NOW()
FROM annotation_results anr
WHERE ar.request_id = anr.request_id;
```

### 4. 查看准确率指标

```sql
SELECT 
    test_run_id,
    total_labeled,
    true_positive,
    false_positive,
    true_negative,
    false_negative,
    recall_rate,      -- 召回率 (TPR)
    precision_rate,   -- 精确率
    false_positive_rate,  -- 误报率 (FPR)
    -- 计算 F1 分数
    2.0 * (precision_rate * recall_rate) / NULLIF(precision_rate + recall_rate, 0) AS f1_score
FROM v_audit_accuracy_analysis
WHERE test_run_id = 'test_a1b2c3d4';
```

---

## 优化建议生成

基于测试结果，生成优化建议报告：

```bash
# 查询测试结果
psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway -c \
  "SELECT * FROM v_audit_performance_summary WHERE test_run_id = 'test_a1b2c3d4';" \
  > test_results.txt

# 手动填充报告模板
cp OPTIMIZATION_REPORT_TEMPLATE.md OPTIMIZATION_REPORT_test_a1b2c3d4.md
# 编辑报告，替换占位符 {test_run_id}, {avg_latency} 等
```

---

## 故障排查

### 问题 1: 数据库连接失败

**症状**:
```
❌ 连接数据库失败: dial tcp 172.16.2.210:5432: i/o timeout
```

**解决方案**:
1. 检查网络连接: `ping 172.16.2.210`
2. 检查端口可达: `nc -zv 172.16.2.210 5432`
3. 检查数据库密码是否正确
4. 确认防火墙规则

### 问题 2: 表不存在

**症状**:
```
ERROR: relation "audit_test_results" does not exist
```

**解决方案**:
```bash
psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway -f schema.sql
```

### 问题 3: 编译失败

**症状**:
```
cannot find package "github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
```

**解决方案**:
```bash
# 确保在项目根目录
cd /path/to/swift-cabin

# 更新依赖
go mod tidy

# 重新编译
go build -o tests/session_audit/audit_test tests/session_audit/audit_test_all_in_one.go
```

### 问题 4: 配置文件缺失

**症状**:
```
❌ 配置加载失败: open 02_sensitive_words_test.yaml: no such file or directory
```

**解决方案**:
```bash
# 确保在 tests/session_audit 目录
cd tests/session_audit
ls -la 02_sensitive_words_test.yaml
```

---

## 性能基准

### 预期性能指标

| 环境 | P50 | P95 | P99 | 吞吐量 |
|------|-----|-----|-----|--------|
| 单核 (MacBook Pro M1) | 2ms | 4ms | 8ms | 500 条/秒 |
| 单核 (Linux 5.x) | 3ms | 5ms | 10ms | 400 条/秒 |
| 10 核并发 | 2ms | 5ms | 12ms | 4000 条/秒 |

### 影响因素

1. **内容长度**: 内容越长，检测越慢
2. **敏感词数量**: 敏感词越多，Trie 树扫描越慢
3. **正则数量**: 正则越多，匹配越慢
4. **CPU 性能**: CPU 越快，检测越快

---

## 下一步

1. ✅ 运行测试，获取性能基线
2. ⏳ 人工标注 300 条样本，评估准确率
3. ⏳ 根据优化报告，实施优化方案
4. ⏳ 回归测试，对比优化效果
5. ⏳ 持续迭代，提升性能和准确率

---

## 联系方式

如有问题，请联系：
- 开发团队
- GitHub Issues

---

**文档版本**: v1.0  
**最后更新**: 2026-07-11  
**维护者**: official-deploy 团队
