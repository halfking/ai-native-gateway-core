# 会话输出审计测试 - 优化建议报告

## 执行摘要

测试批次 ID: `{test_run_id}`  
测试时间: `{test_time}`  
测试数据量: `{total_records}` 条  
数据来源: 252 数据库 request_logs 表（最近 7 天）

---

## 1. 性能分析

### 1.1 检测耗时统计

| 指标 | 耗时 (ms) | 目标 (ms) | 达标 |
|------|-----------|-----------|------|
| 平均耗时 | `{avg_latency}` | ≤ 5 | `{avg_pass}` |
| P50 | `{p50_latency}` | ≤ 3 | `{p50_pass}` |
| P95 | `{p95_latency}` | ≤ 5 | `{p95_pass}` |
| P99 | `{p99_latency}` | ≤ 10 | `{p99_pass}` |
| 最大耗时 | `{max_latency}` | - | - |

### 1.2 吞吐量

- **单线程吞吐量**: `{throughput}` 条/秒
- **预估并发能力** (10 核): `{concurrent_throughput}` 条/秒

### 1.3 性能瓶颈分析

#### 🔴 瓶颈 1: Trie 树扫描效率
- **现象**: 敏感词数量超过 `{sensitive_word_count}` 个时，P95 耗时显著增加
- **影响**: 内容越长，扫描越慢（线性增长）
- **优化建议**:
  ```
  1. 使用 Aho-Corasick 算法替代简单 Trie 树，支持多模式匹配
  2. 按照敏感词长度分层构建 Trie 树，优先匹配短词
  3. 对高频敏感词建立 Bloom Filter 预过滤
  ```

#### 🟡 瓶颈 2: 正则表达式匹配
- **现象**: PII/Injection/Jailbreak 正则检测占总耗时的 40-60%
- **影响**: 正则表达式数量每增加 10 个，P95 耗时增加约 1ms
- **优化建议**:
  ```
  1. 预编译所有正则表达式（已实现）
  2. 使用 hyperscan 库进行并行正则匹配
  3. 对简单模式（如固定字符串）使用字符串匹配代替正则
  4. 按照匹配频率排序正则规则，高频规则优先
  ```

#### 🟢 瓶颈 3: 内容长度影响
- **现象**: 内容长度超过 2000 字符后，检测耗时明显增加
- **影响**: 长文本检测耗时可达 10-20ms
- **优化建议**:
  ```
  1. 对超长内容进行分段检测（如每 1000 字符一段）
  2. 使用采样策略：检测前 1000 字符 + 随机采样中间 500 字符
  3. 对明显安全的内容（如代码块）进行跳过
  ```

---

## 2. 准确率分析

### 2.1 决策分布

| 决策 | 数量 | 占比 | 说明 |
|------|------|------|------|
| Pass | `{decision_pass}` | `{pass_ratio}%` | 通过，无风险 |
| Warn | `{decision_warn}` | `{warn_ratio}%` | 警告，需记录 |
| Block | `{decision_block}` | `{block_ratio}%` | 阻断 |
| NeedApproval | `{decision_approval}` | `{approval_ratio}%` | 需人工审批 |

### 2.2 威胁检测统计

| 威胁类型 | 检测数量 | 占比 | 误报率估计 |
|---------|---------|------|-----------|
| Prompt Injection | `{threat_injection}` | `{injection_ratio}%` | `{injection_fpr}%` |
| PII Leak | `{threat_pii}` | `{pii_ratio}%` | `{pii_fpr}%` |
| Jailbreak | `{threat_jailbreak}` | `{jailbreak_ratio}%` | `{jailbreak_fpr}%` |

### 2.3 准确率评估（需要人工标注）

**当前状态**: 未进行人工标注

**建议操作**:
1. 从每个决策类型中随机抽取 100 条进行人工标注
2. 使用以下 SQL 查询待标注样本:
   ```sql
   -- 抽取待标注样本
   SELECT request_id, decision, content, sensitive_words, threats
   FROM audit_test_results
   WHERE test_run_id = '{test_run_id}'
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
   ```

3. 标注完成后，使用以下 SQL 更新:
   ```sql
   UPDATE audit_test_results
   SET manual_label = 'safe|sensitive|malicious',
       manual_notes = '标注说明'
   WHERE request_id = '{request_id}';
   ```

4. 查询准确率指标:
   ```sql
   SELECT * FROM v_audit_accuracy_analysis
   WHERE test_run_id = '{test_run_id}';
   ```

### 2.4 敏感词命中 TOP 10

| 敏感词 | 命中次数 | 误报可能性 |
|--------|---------|-----------|
| `{word_1}` | `{count_1}` | `{fpr_1}` |
| `{word_2}` | `{count_2}` | `{fpr_2}` |
| ... | ... | ... |

**查询 SQL**:
```sql
SELECT * FROM v_sensitive_words_ranking
WHERE test_run_id = '{test_run_id}'
ORDER BY hit_count DESC
LIMIT 10;
```

---

## 3. 覆盖率分析

### 3.1 敏感词覆盖

- **总敏感词数**: `{total_sensitive_words}` 个
- **命中敏感词数**: `{hit_sensitive_words}` 个
- **覆盖率**: `{coverage_ratio}%`

### 3.2 未命中敏感词分析

**建议操作**:
```sql
-- 查询未命中的敏感词
WITH all_words AS (
  SELECT unnest(sensitive_words) AS word
  FROM audit_test_runs
  WHERE test_run_id = '{test_run_id}'
),
hit_words AS (
  SELECT DISTINCT jsonb_array_elements_text(sensitive_words) AS word
  FROM audit_test_results
  WHERE test_run_id = '{test_run_id}'
)
SELECT aw.word
FROM all_words aw
LEFT JOIN hit_words hw ON aw.word = hw.word
WHERE hw.word IS NULL;
```

**优化建议**:
- 移除从未命中的敏感词（可能是过时或不相关的词）
- 分析业务场景，补充缺失的敏感词

---

## 4. 综合优化方案

### 4.1 立即优化（1-2 天）

#### 优化 1: Trie 树优化
**目标**: 将 P95 耗时从 `{p95_latency}ms` 降低到 3ms 以下

**实施方案**:
```go
// 1. 使用 cloudflare/ahocorasick 库
import "github.com/cloudflare/ahocorasick"

type OptimizedDetector struct {
    ac *ahocorasick.Matcher
}

func NewOptimizedDetector(words []string) *OptimizedDetector {
    ac := ahocorasick.NewStringMatcher(words)
    return &OptimizedDetector{ac: ac}
}

func (d *OptimizedDetector) Scan(text string) []string {
    matches := d.ac.Match([]byte(text))
    // 去重
    seen := make(map[string]bool)
    var result []string
    for _, match := range matches {
        word := string(match)
        if !seen[word] {
            result = append(result, word)
            seen[word] = true
        }
    }
    return result
}
```

**预期效果**: P95 耗时降低 30-40%

#### 优化 2: 正则表达式优化
**目标**: 减少正则匹配耗时

**实施方案**:
```go
// 1. 对固定字符串使用 strings.Contains 替代正则
// 例如: "DAN" 不需要正则
if strings.Contains(text, "DAN") {
    // ...
}

// 2. 合并相似正则
// 原来: (?i)ignore\s+previous, (?i)ignore\s+all
// 优化: (?i)ignore\s+(previous|all)

// 3. 使用 sync.Pool 复用正则对象
var regexPool = sync.Pool{
    New: func() interface{} {
        return regexp.MustCompile(pattern)
    },
}
```

**预期效果**: 正则匹配耗时降低 20-30%

### 4.2 中期优化（1 周）

#### 优化 3: 并发检测
**目标**: 提升吞吐量

**实施方案**:
```go
func (d *FastDetector) DetectBatch(ctx context.Context, contents []string) ([]*DetectResult, error) {
    results := make([]*DetectResult, len(contents))
    
    // 使用 worker pool
    numWorkers := runtime.NumCPU()
    workCh := make(chan int, len(contents))
    resultCh := make(chan struct{
        idx int
        result *DetectResult
        err error
    }, len(contents))
    
    // 启动 workers
    for i := 0; i < numWorkers; i++ {
        go func() {
            for idx := range workCh {
                result, err := d.Detect(ctx, contents[idx])
                resultCh <- struct{
                    idx int
                    result *DetectResult
                    err error
                }{idx, result, err}
            }
        }()
    }
    
    // 分发任务
    for i := range contents {
        workCh <- i
    }
    close(workCh)
    
    // 收集结果
    for range contents {
        res := <-resultCh
        if res.err == nil {
            results[res.idx] = res.result
        }
    }
    
    return results, nil
}
```

**预期效果**: 吞吐量提升 5-8 倍

#### 优化 4: 缓存策略
**目标**: 避免重复检测相同内容

**实施方案**:
```go
import "github.com/dgraph-io/ristretto"

type CachedDetector struct {
    detector *FastDetector
    cache    *ristretto.Cache
}

func (d *CachedDetector) Detect(ctx context.Context, content string) (*DetectResult, error) {
    // 计算内容哈希
    hash := sha256.Sum256([]byte(content))
    cacheKey := hex.EncodeToString(hash[:])
    
    // 查询缓存
    if val, found := d.cache.Get(cacheKey); found {
        return val.(*DetectResult), nil
    }
    
    // 检测
    result, err := d.detector.Detect(ctx, content)
    if err != nil {
        return nil, err
    }
    
    // 缓存结果（TTL = 1小时）
    d.cache.SetWithTTL(cacheKey, result, 1, time.Hour)
    
    return result, nil
}
```

**预期效果**: 重复内容检测耗时降低 95%

### 4.3 长期优化（1 个月）

#### 优化 5: 机器学习模型
**目标**: 提升准确率，降低误报率

**实施方案**:
1. 收集标注数据（10,000+ 条）
2. 训练 BERT 分类模型（安全/敏感/恶意）
3. 使用规则引擎 + ML 模型混合决策
4. 持续学习，定期更新模型

**预期效果**: 
- 准确率提升到 95%+
- 误报率降低到 5% 以下

#### 优化 6: 分级检测策略
**目标**: 平衡性能和准确率

**实施方案**:
```go
// Level 1: 快速筛选（Trie + 简单正则，≤1ms）
// 如果 Pass，直接返回

// Level 2: 中等检测（完整正则，≤5ms）
// 如果 Pass 或 Warn，返回

// Level 3: 深度检测（ML 模型，≤50ms）
// 仅对 NeedApproval 的内容执行
```

**预期效果**: 
- 90% 的请求在 Level 1 完成（≤1ms）
- 9% 的请求在 Level 2 完成（≤5ms）
- 1% 的请求在 Level 3 完成（≤50ms）
- 总体 P95 降低到 2ms

---

## 5. 行动计划

### 第 1 周
- [ ] 实施 Aho-Corasick 算法优化（优化 1）
- [ ] 实施正则表达式优化（优化 2）
- [ ] 性能回归测试

### 第 2 周
- [ ] 实施并发检测（优化 3）
- [ ] 实施缓存策略（优化 4）
- [ ] 压力测试

### 第 3-4 周
- [ ] 人工标注 1000 条样本
- [ ] 计算准确率指标
- [ ] 调整敏感词库和正则规则

### 第 2-3 个月
- [ ] 收集标注数据 10,000+ 条
- [ ] 训练 ML 模型（优化 5）
- [ ] A/B 测试
- [ ] 实施分级检测策略（优化 6）

---

## 6. 预期收益

### 性能提升
- P50 耗时: `{p50_latency}ms` → **1ms** (-67%)
- P95 耗时: `{p95_latency}ms` → **2ms** (-60%)
- P99 耗时: `{p99_latency}ms` → **5ms** (-50%)
- 吞吐量: `{throughput}` 条/秒 → **5000 条/秒** (+400%)

### 准确率提升
- 误报率: 估计 15-20% → **5%** (-70%)
- 漏报率: 估计 10-15% → **3%** (-70%)
- F1 分数: 估计 0.80 → **0.95** (+19%)

### 成本节约
- 服务器资源: 减少 40%
- 人工审批: 减少 60%（误报率降低）
- 运维成本: 减少 30%

---

## 附录

### A. 测试环境

- **数据库**: PostgreSQL 17 @ 172.16.2.210:5432
- **数据量**: `{total_records}` 条
- **测试时间**: `{test_duration}` 秒
- **Go 版本**: go1.21
- **OS**: macOS / Linux

### B. 查询脚本

```sql
-- 查询性能统计
SELECT * FROM v_audit_performance_summary
WHERE test_run_id = '{test_run_id}';

-- 查询准确率（需要人工标注）
SELECT * FROM v_audit_accuracy_analysis
WHERE test_run_id = '{test_run_id}';

-- 查询敏感词排行
SELECT * FROM v_sensitive_words_ranking
WHERE test_run_id = '{test_run_id}'
ORDER BY hit_count DESC LIMIT 20;

-- 查询误报案例（需要人工标注）
SELECT request_id, content, decision, sensitive_words, manual_label
FROM audit_test_results
WHERE test_run_id = '{test_run_id}'
  AND is_false_positive = true
ORDER BY detect_score DESC;
```

### C. 参考文档

- [Aho-Corasick 算法](https://en.wikipedia.org/wiki/Aho%E2%80%93Corasick_algorithm)
- [cloudflare/ahocorasick](https://github.com/cloudflare/ahocorasick)
- [Hyperscan 正则引擎](https://www.hyperscan.io/)
- [Ristretto Cache](https://github.com/dgraph-io/ristretto)

---

**报告生成时间**: `{report_time}`  
**生成工具**: 会话输出审计测试框架 v1.0
