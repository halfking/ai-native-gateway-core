# P2任务实施计划 - AUTO路由优化中期任务

## 任务概述

完成P0和P1（立即/近期任务）后，现在进入P2中期任务阶段。这些任务将构建在结构化特征基础上，实现完整的ML训练和人工标注流程。

---

## P2.1 人工标注工作流实现

### 目标
基于结构化特征（无需访问原始prompt）实现人工标注系统，用于校验自动分类质量和收集训练数据。

### 核心组件

#### 1. 标注任务队列表（annotation_tasks）
```sql
CREATE TABLE annotation_tasks (
  id BIGSERIAL PRIMARY KEY,
  selection_id BIGINT NOT NULL, -- FK to auto_route_selections
  trigger_reason TEXT NOT NULL, -- low_confidence, model_conflict, anomaly_reward, etc.
  priority INTEGER NOT NULL DEFAULT 50, -- 0-100
  status TEXT NOT NULL DEFAULT 'pending', -- pending, assigned, completed, skipped
  assigned_to TEXT,
  assigned_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  quality_score INTEGER, -- 1-5
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_annotation_tasks_status ON annotation_tasks(status, priority DESC, created_at);
CREATE INDEX idx_annotation_tasks_selection ON annotation_tasks(selection_id);
```

#### 2. 标注结果表（annotations）
```sql
CREATE TABLE annotations (
  id BIGSERIAL PRIMARY KEY,
  task_id BIGINT NOT NULL REFERENCES annotation_tasks(id),
  annotator_id TEXT NOT NULL,
  annotated_task_type TEXT NOT NULL,
  recommended_model TEXT,
  confidence INTEGER NOT NULL CHECK (confidence BETWEEN 1 AND 5),
  notes TEXT,
  review_status TEXT DEFAULT 'pending', -- pending, approved, rejected
  reviewer_id TEXT,
  reviewed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

#### 3. 触发策略实现（annotation_trigger.go）
```go
package bg

// AnnotationTrigger 决定哪些auto_route_selections行需要人工标注
type AnnotationTrigger struct {
    // 低置信度阈值
    LowConfidenceThreshold float64 // < 0.7
    // 异常reward阈值
    AnomalyRewardThreshold float64 // < 0.3 or > 0.95
    // 采样率（分层随机）
    SamplingRate map[string]float64 // task_type -> rate
}

func (t *AnnotationTrigger) ShouldAnnotate(sel AutoSelection) (bool, string) {
    // 规则1: 低置信度
    if sel.Confidence < t.LowConfidenceThreshold {
        return true, "low_confidence"
    }
    
    // 规则2: 异常reward（已结算的行）
    if sel.Reward != nil && (*sel.Reward < 0.3 || *sel.Reward > 0.95) {
        return true, "anomaly_reward"
    }
    
    // 规则3: 分层随机抽样
    rate := t.SamplingRate[sel.TaskType]
    if rand.Float64() < rate {
        return true, "stratified_sampling"
    }
    
    return false, ""
}
```

#### 4. Admin API端点
```go
// GET /admin/annotations/queue
// - 返回待标注任务列表（只含结构化特征，无prompt）
// - 按优先级和时间排序

// POST /admin/annotations/{task_id}/assign
// - 分配任务给标注员

// POST /admin/annotations/{task_id}/complete
// - 提交标注结果

// GET /admin/annotations/stats
// - 标注统计：完成率、一致性、按类别分布
```

### 隐私保证
- ❌ 标注UI **不显示** prompt、messages、response原文
- ✅ 只显示结构化特征：语言、长度桶、复杂度、内容类型指标
- ✅ 如需查看原文：通过既有审计系统按权限临时访问（不缓存）

### 验收标准
- [ ] 表结构创建并测试
- [ ] 触发策略实现并覆盖4种规则
- [ ] Admin API端点实现（CRUD）
- [ ] 单元测试覆盖率 > 80%
- [ ] 隐私测试：标注队列API不返回敏感内容

---

## P2.2 训练数据导出管道

### 目标
从 `auto_route_selections_all` 导出结构化特征+标签，生成ML训练数据集（不含prompt正文）。

### 核心组件

#### 1. 导出配置（training_export_config）
```sql
CREATE TABLE training_export_configs (
  id SERIAL PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  feature_version TEXT NOT NULL, -- v1, v2
  time_range_start DATE NOT NULL,
  time_range_end DATE NOT NULL,
  quality_filters JSONB, -- {"min_confidence": 0.7, "exclude_explore": true}
  dedup_strategy TEXT NOT NULL DEFAULT 'content_hash', -- content_hash, request_id
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

#### 2. 导出执行记录（training_exports）
```sql
CREATE TABLE training_exports (
  id SERIAL PRIMARY KEY,
  config_id INTEGER NOT NULL REFERENCES training_export_configs(id),
  status TEXT NOT NULL DEFAULT 'pending', -- pending, running, completed, failed
  row_count_raw INTEGER,
  row_count_deduped INTEGER,
  output_path TEXT, -- s3://bucket/exports/2026-09-05-v1.parquet
  started_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  error_message TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

#### 3. 导出器实现（training_exporter.go）
```go
package ml

type TrainingExporter struct {
    db *sql.DB
    s3Client *s3.Client
}

func (e *TrainingExporter) Export(ctx context.Context, configID int) error {
    config := e.loadConfig(configID)
    
    // 1. 查询auto_route_selections_all（只选择feature列）
    query := `
        SELECT 
            task_type, profile, classifier, confidence,
            detected_language, prompt_length_bucket, context_length_bucket,
            turn_count_bucket, has_code_indicator, has_math_indicator,
            has_table_indicator, has_multimedia_indicator, intent_category,
            domain_hint, complexity_bucket, latency_sensitive, cost_sensitive,
            feature_version, content_hash,
            chosen_model, success, latency_ms, cost_usd, reward
        FROM auto_route_selections_all
        WHERE ts BETWEEN $1 AND $2
          AND feature_version = $3
          AND confidence >= $4
    `
    
    // 2. 去重（基于content_hash）
    rows := e.queryAndDedupe(query, config)
    
    // 3. 导出为Parquet（高效列存储格式）
    outputPath := e.exportToParquet(rows, config)
    
    // 4. 记录元数据
    e.recordExport(configID, len(rows), outputPath)
    
    return nil
}
```

#### 4. 数据格式（Parquet Schema）
```
message TrainingDataset {
  required binary task_type (UTF8);
  required binary profile (UTF8);
  required double confidence;
  
  // 结构化特征（15个字段）
  optional binary detected_language (UTF8);
  optional binary prompt_length_bucket (UTF8);
  optional binary context_length_bucket (UTF8);
  optional binary turn_count_bucket (UTF8);
  optional boolean has_code_indicator;
  optional boolean has_math_indicator;
  optional boolean has_table_indicator;
  optional boolean has_multimedia_indicator;
  optional binary intent_category (UTF8);
  optional binary domain_hint (UTF8);
  optional binary complexity_bucket (UTF8);
  optional boolean latency_sensitive;
  optional boolean cost_sensitive;
  required binary feature_version (UTF8);
  required binary content_hash (UTF8);
  
  // 标签（outcome）
  required binary chosen_model (UTF8);
  optional boolean success;
  optional int32 latency_ms;
  optional double reward;
}
```

#### 5. CLI命令
```bash
# 导出最近30天的数据
llm-gw-exporter export \
  --start-date 2026-08-01 \
  --end-date 2026-09-01 \
  --feature-version v1 \
  --min-confidence 0.7 \
  --output s3://ml-training/exports/2026-09-01-v1.parquet

# 查看导出历史
llm-gw-exporter list

# 验证导出（检查无prompt泄露）
llm-gw-exporter validate s3://ml-training/exports/2026-09-01-v1.parquet
```

### 验收标准
- [ ] 导出配置表和执行记录表创建
- [ ] TrainingExporter实现（query + dedup + parquet）
- [ ] CLI命令实现（export, list, validate）
- [ ] 隐私测试：导出文件不包含prompt/messages字段
- [ ] 性能测试：100万行导出 < 5分钟

---

## P2.3 监控特征分布和去重率

### 目标
实时监控结构化特征的分布和质量，检测异常（如特征集中、去重率下降）。

### 核心组件

#### 1. 特征分布统计表（feature_distribution_stats）
```sql
CREATE TABLE feature_distribution_stats (
  id BIGSERIAL PRIMARY KEY,
  stat_date DATE NOT NULL,
  feature_name TEXT NOT NULL, -- detected_language, prompt_length_bucket, etc.
  feature_value TEXT NOT NULL, -- zh, en, s, m, l, etc.
  row_count INTEGER NOT NULL,
  percentage NUMERIC(5,2) NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX uq_feature_stats ON feature_distribution_stats(stat_date, feature_name, feature_value);
```

#### 2. 去重率统计表（dedup_stats）
```sql
CREATE TABLE dedup_stats (
  id BIGSERIAL PRIMARY KEY,
  stat_date DATE NOT NULL,
  total_rows INTEGER NOT NULL,
  unique_hashes INTEGER NOT NULL,
  dedup_rate NUMERIC(5,2) NOT NULL, -- (1 - unique/total) * 100
  top_duplicate_hashes JSONB, -- [{"hash": "abc...", "count": 123}, ...]
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX uq_dedup_stats ON dedup_stats(stat_date);
```

#### 3. 统计计算worker（feature_stats_worker.go）
```go
package bg

type FeatureStatsWorker struct {
    db *sql.DB
    interval time.Duration // 默认1小时
}

func (w *FeatureStatsWorker) Run(ctx context.Context) {
    ticker := time.NewTicker(w.interval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            w.computeStats()
        }
    }
}

func (w *FeatureStatsWorker) computeStats() {
    today := time.Now().Truncate(24 * time.Hour)
    
    // 1. 特征分布统计
    features := []string{
        "detected_language", "prompt_length_bucket", "context_length_bucket",
        "turn_count_bucket", "intent_category", "domain_hint", "complexity_bucket",
    }
    
    for _, feature := range features {
        w.computeFeatureDistribution(today, feature)
    }
    
    // 2. 去重率统计
    w.computeDedupRate(today)
    
    // 3. 检测异常
    w.detectAnomalies(today)
}

func (w *FeatureStatsWorker) detectAnomalies(date time.Time) {
    // 异常1: 某个特征值占比 > 90%（特征集中）
    // 异常2: 去重率 < 50%（大量重复请求）
    // 异常3: 某个特征完全缺失（feature_value = NULL占比 > 50%）
}
```

#### 4. Grafana Dashboard

**Panel 1: 语言分布（饼图）**
```sql
SELECT 
  detected_language AS metric,
  row_count AS value
FROM feature_distribution_stats
WHERE stat_date = CURRENT_DATE
  AND feature_name = 'detected_language'
ORDER BY row_count DESC;
```

**Panel 2: 长度桶分布（柱状图）**
```sql
SELECT 
  feature_value AS bucket,
  row_count AS count
FROM feature_distribution_stats
WHERE stat_date = CURRENT_DATE
  AND feature_name = 'prompt_length_bucket'
ORDER BY 
  CASE feature_value
    WHEN 'xs' THEN 1
    WHEN 's' THEN 2
    WHEN 'm' THEN 3
    WHEN 'l' THEN 4
    WHEN 'xl' THEN 5
    WHEN 'xxl' THEN 6
  END;
```

**Panel 3: 去重率趋势（时序图）**
```sql
SELECT 
  stat_date AS time,
  dedup_rate AS value
FROM dedup_stats
WHERE stat_date > CURRENT_DATE - INTERVAL '30 days'
ORDER BY stat_date;
```

**Panel 4: 特征填充率（表格）**
```sql
SELECT 
  feature_name,
  SUM(row_count) FILTER (WHERE feature_value != 'NULL') AS filled,
  SUM(row_count) AS total,
  ROUND(100.0 * SUM(row_count) FILTER (WHERE feature_value != 'NULL') / SUM(row_count), 2) AS fill_rate
FROM feature_distribution_stats
WHERE stat_date = CURRENT_DATE
GROUP BY feature_name
ORDER BY fill_rate;
```

#### 5. 告警规则
```yaml
# prometheus alerts
- alert: FeatureConcentration
  expr: |
    max by (feature_name) (
      feature_distribution_percentage{feature_name!="detected_language"}
    ) > 90
  for: 1h
  annotations:
    summary: "特征 {{ $labels.feature_name }} 过度集中"
    description: "某个值占比超过90%，可能导致模型训练偏差"

- alert: LowDedupRate
  expr: dedup_rate < 50
  for: 2h
  annotations:
    summary: "去重率过低"
    description: "当前去重率 {{ $value }}%，大量重复请求"

- alert: MissingFeatures
  expr: feature_fill_rate < 80
  for: 30m
  annotations:
    summary: "特征 {{ $labels.feature_name }} 缺失率高"
    description: "填充率只有 {{ $value }}%"
```

### 验收标准
- [ ] 统计表创建并测试
- [ ] FeatureStatsWorker实现并集成到bg package
- [ ] Grafana dashboard创建（4个panel）
- [ ] Prometheus告警规则配置
- [ ] 异常检测测试：注入异常数据，验证告警触发

---

## 实施顺序

### 阶段1: 基础设施（第1-2周）
- P2.1.1: 创建标注表结构
- P2.2.1: 创建导出配置表
- P2.3.1: 创建统计表

### 阶段2: 核心逻辑（第3-4周）
- P2.1.2: 实现触发策略
- P2.2.2: 实现导出器
- P2.3.2: 实现统计worker

### 阶段3: UI和监控（第5-6周）
- P2.1.3: Admin API和UI
- P2.2.3: CLI命令
- P2.3.3: Grafana dashboard

### 阶段4: 测试和上线（第7-8周）
- 集成测试
- 隐私合规测试
- 性能测试
- 灰度上线

---

## 依赖关系

```
P2.1 (人工标注) ──┐
                   ├──> P2.2 (训练导出) ──> ML模型训练
P2.3 (监控) ──────┘
```

- P2.3 可独立并行实施（无依赖）
- P2.1 和 P2.2 可并行，但最终都需要P2.3的监控数据

---

## 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| 标注UI意外显示prompt | 🔴 高 | 强制隐私测试，API层级过滤 |
| 导出文件包含敏感数据 | 🔴 高 | Parquet schema白名单，验证命令 |
| 统计计算影响性能 | 🟡 中 | 异步worker，只查询历史分区 |
| 特征分布异常未检测 | 🟡 中 | 多层告警（Prometheus + 日报） |

---

## 交付检查清单

### P2.1 人工标注
- [ ] Migration: annotation_tasks, annotations表
- [ ] 代码: annotation_trigger.go, annotation_api.go
- [ ] 测试: 隐私测试（API不返回prompt）
- [ ] 文档: 标注员操作手册

### P2.2 训练导出
- [ ] Migration: training_export_configs, training_exports表
- [ ] 代码: training_exporter.go, exporter CLI
- [ ] 测试: 隐私测试（parquet不含prompt字段）
- [ ] 文档: 导出流程文档

### P2.3 监控
- [ ] Migration: feature_distribution_stats, dedup_stats表
- [ ] 代码: feature_stats_worker.go
- [ ] 配置: Grafana dashboard JSON, Prometheus rules
- [ ] 文档: 监控指标说明

---

## 下一步行动

1. **立即**: 创建P2任务跟踪issue（Jira/GitHub）
2. **本周**: 实施P2.3（监控）— 最简单，无依赖，快速见效
3. **下周**: 并行启动P2.1和P2.2
4. **2周后**: 第一次演示（监控dashboard + 标注队列原型）
