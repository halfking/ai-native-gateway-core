# AUTO 路由专项测试与自动优化完整方案

**文档版本**: v1.1  
**创建日期**: 2026-09-15（v1.1 同日更新：Phase 1/2 完成状态 + 灰度配置修正）  
**目标**: 建立可独立演进的AUTO路由优化插件，支持专项测试、人工标注闭环和自动参数优化

---

## 一、现状审计总结

### 1.1 已完成功能 ✅

基于代码审计，以下核心能力已经完整实现：

#### **决策引擎** (`autoroute/decision_v2.go`)
- ✅ DecideV2 集成 4 层优化（会话缓存、P2.2 optimizer 插件、审计日志）
- ✅ 4 个 plugin hook 点（PreClassify / PostClassify / RecommendModel / RecordFeedback）
- ✅ 反馈闭环：决策时 stash → 完成时 backfill 真实 outcome（2026-09-08 audit 修复）
- ✅ 测试覆盖：`decision_v2_optimizer_test.go` (6个测试)

#### **路由优化插件** (`routingopt/` 包，30个文件)
- ✅ RealOptimizer 集成器（4大模块 + ONNX ML re-ranker + A/B测试门）
- ✅ 用户亲和力缓存（Redis + PostgreSQL，TTL 1小时）
- ✅ 置信度调整器（基于历史准确率）
- ✅ 反馈批量写入器（异步队列，5秒或100条flush）
- ✅ A/B 测试框架（FNV-1a哈希分组）

#### **人工标注工作流** (P2.1 已完成)
- ✅ CLI 工具 `llm-gw-annotator`（export/import/validate/stats）
- ✅ 不确定性采样（置信度 [0.4, 0.6] 优先 + per-task-type 配额）
- ✅ Web UI：7个 API 端点（`admin/annotation_handler.go`）
- ✅ 统计分析：ML准确率趋势、供应商/标注人员对比
- ✅ **隐私合规**：只导出结构化特征，不访问 prompt/messages/response

#### **训练数据导出** (P2.2 已完成)
- ✅ CLI 工具 `llm-gw-exporter`（export/list/preview）
- ✅ Parquet 格式（snappy/gzip 压缩）
- ✅ 质量过滤 + 去重（content_hash / request_id）
- ✅ **隐私合规**：20个结构化特征字段，不访问正文

#### **ONNX ML Re-ranker** (P2.5 已完成)
- ✅ MLSelector ONNX Runtime 推理封装（326行）
- ✅ Manifest 加载与校验（schema_version / label_classes）
- ✅ 热加载 + 原子切换
- ✅ 测试：`ml_selector_test.go`（端到端推理）

#### **数据库表结构** (已创建)
- ✅ `training_human_annotations` (Migration 669)
- ✅ `routing_optimization_state` (Migration 670)
- ✅ `routing_feedback_log` (Migration 670)
- ✅ `routing_optimization_metrics` (Migration 670)
- ✅ `routing_user_affinity` (Migration 670)

---

### 1.2 部分完成/缺失组件 ⚠️

> 2026-09-15 更新：Phase 1 前三项已闭环（状态列标注 ✅）。

| 组件 | 状态 | 影响 | 优先级 |
|------|------|------|--------|
| **反馈聚合 worker** | ✅ 已实现 `bg/routing_metrics_aggregator.go`（5min 聚合、30min 回溯重算、30 天保留裁剪；SQL 在 PG17.11 实测含幂等性） | `routing_optimization_metrics` 有数据，admin 统计/监控可用 | 完成 |
| **AdaptiveLearner 自动调参** | ✅ 已实现并验证闭环：RunAdaptiveMaintenance(5min) → AdaptParameters 事务性 checkpoint → recommender 5min TTL 自动拉取新 ε | 准确率 +2pp 自动 checkpoint（ε×0.8 衰减），-5pp 异常告警 | 完成 |
| **ML 模型训练 pipeline** | ✅ 已存在 `ml-training/`：data_loader/feature_pipeline/train/evaluate/export(ONNX) + 28 tests 全绿 + 真实数据 parquet | 随时可重训并通过 manifest 热加载 | 完成 |
| **48h 热门模型物化视图** | ❌ 未创建 | P99 延迟可能超过 10ms 目标 | 🟡 低 |

---

### 1.3 数据闭环完整流程

```
┌─────────────────────────────────────────────────────────────────┐
│ 1. 请求到达 → DecideV2 决策                                        │
│    → PreClassify hook (enhancer 增强信号)                         │
│    → Classify (分类器)                                             │
│    → PostClassify hook (confidence adjuster)                      │
│    → RecommendV2 (候选推荐)                                        │
│    → RecommendModel hook (optimizer + 可选 ONNX re-ranker)        │
│    → recordFeedbackAsync (有 requestID 时 stash)                  │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│ 2. 请求执行 → 上游返回                                            │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│ 3. 请求完成 → ReportRoutingOutcome                                │
│    → 从 pendingFeedback 弹出 stashed entry                        │
│    → 填充真实 outcome (IsSuccess / LatencyMs / CostUSD)           │
│    → dispatchFeedback → FeedbackIntegrator.RecordFeedback         │
│    → 批量写入 routing_feedback_log (5s 或 100条 flush)            │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│ 4. 后台聚合 (✅ bg/routing_metrics_aggregator.go, 2026-09-15)    │
│    → 每5分钟重算最近30min的 bucket（GROUPING SETS 全局/任务/厂商）│
│    → 事务内 DELETE+INSERT（NULL 维度 ON CONFLICT 不去重，禁用     │
│      upsert；MVCC 保证读者不见半写窗口）                          │
│    → human_accuracy_rate = (auto+2×human)/(total+2×human)        │
│    → 30 天保留裁剪 routing_feedback_log（每日一次，批量 5000）    │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│ 5. AdaptiveLearner (✅ 已实现, ROUTING_OPT_ADAPTIVE_LEARNING 门控)│
│    → 24h 滑窗加权准确率 (GetAutoAccuracyCounts + human ×2)       │
│    → +2pp → 事务性 checkpoint 新版本 (ε×0.8, floor 1%)           │
│    → -5pp → 异常告警并 hold 参数                                  │
│    → ModelRecommender 5min TTL 缓存到期自动拉取新版本             │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│ 6. 人工标注 (P2.1 已完成)                                         │
│    → 低置信度样本 → llm-gw-annotator export                       │
│    → 人工标注 → import 或 Web UI                                  │
│    → training_human_annotations 表                                │
│    → FeedbackIntegrator 读取时加权 ×2                             │
└─────────────────────────────────────────────────────────────────┘
```

**关键修正**（2026-09-08 audit）:
- **修复前**: 决策时立即写 IsSuccess=true placeholder → 开环，所有请求都是假阳性
- **修复后**: 决策时 stash → 完成时 backfill 真实 outcome → 闭环，rate-limited/crashed 请求被正确标记

---

## 二、OmniRoute 可迁移模式（研究成果）

基于对 OmniRoute v3.8.49 的深度代码分析，以下模式值得借鉴：

### 2.1 Auto-Combo 12因素评分 ⭐⭐⭐⭐⭐

**文件**: `open-sse/services/autoCombo/scoring.ts:1-269`

**评分因子** (当前 Go 已有 6 个，可扩展至 12 个):

| 因子 | OmniRoute权重 | 当前Go实现 | 迁移优先级 |
|------|--------------|-----------|----------|
| quota (剩余配额) | 0.15 | ✅ 已有 | - |
| health (断路器健康度) | 0.20 | ✅ 已有 | - |
| costInv (成本倒数) | 0.15 | ⚠️ 部分 | 🟠 **中** |
| latencyInv (延迟倒数 P95) | 0.12 | ✅ 已有 | - |
| taskFit (任务类型适配) | 0.08 | ❌ 缺失 | 🟡 **低** |
| stability (稳定性/方差) | 0.05 | ❌ 缺失 | 🟡 **低** |
| tierPriority (账户层级) | 0.05 | ❌ 缺失 | 🟡 **低** |
| contextAffinity (上下文亲和力) | 0.05 | ⚠️ 部分 | 🟡 **低** |
| resetWindowAffinity (配额重置窗口) | 0.0 | ❌ 缺失 | - |
| connectionDensity (负载分散) | 0.05 | ❌ 缺失 | 🟡 **低** |

**迁移建议**: 
- **Phase 1**: 保留现有 6 因子，补充 `costInv` 精确计算（利用 1045 Offer 定价数据）
- **Phase 2**: 增加 `taskFit`（任务类型适配度，基于历史成功率）
- **Phase 3**: 视业务需求选择性增加其他因子

---

### 2.2 三层弹性架构 ⭐⭐⭐⭐⭐

**文件**: `open-sse/services/accountFallback.ts:1-800+`

**当前 Go 实现**: 已有基础断路器，需规范化三层边界

| 层级 | 作用域 | OmniRoute 设计 | 当前 Go 状态 |
|------|-------|---------------|-------------|
| **Layer 1: Provider Circuit Breaker** | 整个提供商 | `CLOSED → DEGRADED → OPEN → HALF_OPEN`<br>OAuth: 3次失败/60s重置<br>API-key: 5次失败/30s重置 | ✅ 已有 `domains/health/`<br>⚠️ 缺少 DEGRADED 状态 |
| **Layer 2: Connection Cooldown** | 单个密钥/账户 | 指数退避：`baseCooldownMs * 2^failureIndex`<br>429处理：优先使用 `Retry-After` header | ✅ 已有 `ursm/` v2<br>⚠️ 未实现指数退避 |
| **Layer 3: Model Lockout** | 提供商+连接+模型 | 404精确锁定单模型<br>Codex 按家族锁定 (gpt-5.6-*) | ⚠️ 部分实现<br>❌ 家族作用域缺失 |

**迁移建议**:
1. Layer 1 增加 `DEGRADED` 状态（警告但通行）
2. Layer 2 实现自适应退避（探测失败累积 → 超时翻倍，最多 16x）
3. Layer 3 细化模型作用域逻辑（Gemini 家族、OpenAI 变种）

---

### 2.3 Session Affinity (会话粘性) ⭐⭐⭐⭐

**文件**: `open-sse/services/combo/sessionStickiness.ts:1-200`

**核心设计**:
- **Hash Key**: SHA-256(第一条user消息) 前16位
- **Headroom Gate**: 仅当连接利用率 < 85% 时保持粘性
- **TTL**: 15分钟
- **存储**: 内存Map (500条LRU) + PostgreSQL 持久化

**当前 Go 实现**: 
- ✅ 会话缓存重校验（`decision_v2.go`）
- ❌ Headroom自动解绑缺失
- ⚠️ 数据库持久化待补充

**迁移建议**:
- 创建 `session_account_affinity` 表（RLS 隔离）
- 实现 headroom 检测（利用率 > 85% 时解绑）
- 清理定时器（5分钟清理过期条目）

---

### 2.4 Lite 压缩引擎 ⭐⭐⭐

**文件**: `open-sse/services/compression/lite.ts:1-100`

**功能**: 空白符折叠 + 图片URL精简 (~15%节省, <1ms)

**迁移建议**:
- 实现 `domains/compression/lite.go`
- 零风险、即时收益
- 作为其他压缩引擎的基础

---

## 三、分阶段实施路线图

### Phase 1: 补齐缺失组件（Week 1-2）🔴

#### 1.1 反馈聚合 worker (优先级最高)

**文件**: `bg/routing_metrics_aggregator.go`（新建）

**功能**:
```go
// 每 5 分钟聚合 routing_feedback_log → routing_optimization_metrics
// 维度: task_type × provider × time_bucket (5min)
// 指标: 
//   - accuracy (weighted by human_correction ×2)
//   - avg_latency / avg_cost / success_rate
//   - p50/p95/p99 latency
//   - human_corrections count + human_accuracy_rate
```

**参考模式**: `bg/data_lifecycle_cron.go`（定时任务框架）

**验收标准**:
- [ ] `routing_optimization_metrics` 表有数据（5分钟一次）
- [ ] 人工标注权重 ×2 生效
- [ ] Prometheus 指标暴露（`routing_metrics_aggregation_duration_ms`）

---

#### 1.2 验证数据库表完整性

**检查清单**:
```bash
# 检查是否有以下表
psql -d llm_gateway -c "\dt routing_feedback_log"
psql -d llm_gateway -c "\dt routing_optimization_state"
psql -d llm_gateway -c "\dt routing_optimization_metrics"
psql -d llm_gateway -c "\dt routing_user_affinity"
psql -d llm_gateway -c "\dt training_export_configs"
psql -d llm_gateway -c "\dt training_exports"
```

**如缺失**: 补充 migration（参考 `docs/db-changelog.md`）

---

#### 1.3 小流量灰度测试 (10%)

**环境变量配置**（2026-09-15 修正：`ROUTING_OPT_AB_TEST_PERCENTAGE` 是 0-1 小数，
`NewABGate` 会把 >1 的值钳到 1.0 —— 误配 `=10` 等于 100% 全量而非 10% 灰度）:
```bash
# 启用 P2.2 优化插件
ROUTING_OPT_ENABLED=true
ROUTING_OPT_AB_TEST_ENABLED=true
ROUTING_OPT_AB_TEST_PERCENTAGE=0.1     # 10% treatment；90% control 走 baseline

# 监控关键指标（Prometheus，真实指标名以 routingopt/metrics.go 为准）
llmgw_routingopt_preclassify_ms / llmgw_routingopt_recommend_ms   # P99 < 10ms
llmgw_routingopt_feedback_writes_total{result="ok"}               # 持续增长
llmgw_routingopt_feedback_writes_total{result="dropped"}          # ≈ 0
llmgw_routingopt_exploration_requests_total                        # ε-greedy 探索计数
llmgw_routingopt_metrics_sweeps_total{outcome="ok"}                # 聚合 worker（本次新增）
llmgw_routingopt_metrics_rows_written                              # 每 sweep 写入行数

# 聚合表只读端点（2026-09-16 R32：该表首个生产读者；R30 P3 遗留债处置）
# GET /api/admin/routing-opt/metrics?hours=N[&task_type=][&provider=]
#   - 数据源 routing_optimization_metrics（5 分钟桶，人工标注×2 加权，保留 30 天）
#   - hours clamp 1..720；task_type/provider 可选过滤；双 NULL 行 = 全局聚合
#   - 灰度验收用：确认该端点返回非空 rows 即证明聚合闭环落表生效
```

**验收标准**:
- [ ] 10% 流量走优化器路径（ABGate 计数经 admin stats API `/api/admin/routing-opt/stats` 暴露，
      treatment_count/evaluated ≈ 0.1；非 Prometheus 指标）
- [ ] 90% 对照组走 baseline（字节级与优化器禁用时一致）
- [ ] P99 延迟 < 10ms（`llmgw_routingopt_preclassify_ms` / `llmgw_routingopt_recommend_ms`）
- [ ] 无崩溃/内存泄漏；`llmgw_routingopt_metrics_sweeps_total{outcome="error"}` ≈ 0

---

### Phase 2: 自动调参与ML训练（Week 3-6）🟠

> 2026-09-15 状态：**两项均已完成**。2.1 AdaptiveLearner 实际落地于
> `routingopt/learner.go`（保守策略：hold/+2pp checkpoint/-5pp anomaly，
> 而非文档原设想的贝叶斯优化——保守策略可审计、可回滚，先跑通闭环）。
> 2.2 ml-training 已含完整管道与 28 个测试。

#### 2.1 AdaptiveLearner（✅ 已实现 — `routingopt/learner.go`）

**实际核心逻辑**:
```go
func (l *AdaptiveLearner) OptimizeWeights(ctx context.Context) error {
    // 1. 读取最近 1000 次 routing_optimization_metrics
    metrics := l.dao.GetRecentMetrics(ctx, 1000)
    
    // 2. 计算当前参数的 overall_accuracy
    currentAccuracy := computeWeightedAccuracy(metrics)
    
    // 3. Bayesian Optimization 生成新候选参数
    //    (简化版：网格搜索 + 随机探索)
    candidateWeights := l.generateCandidates(currentWeights, 5)
    
    // 4. A/B 测试验证 (10% 流量 × 1 小时)
    for _, candidate := range candidateWeights {
        testAccuracy := l.abTest(ctx, candidate, 0.1, 1*time.Hour)
        if testAccuracy > currentAccuracy + 0.02 {
            // 5. 新参数 accuracy > 旧参数 + 2%，则写入 routing_optimization_state
            l.dao.SaveOptimizationState(ctx, candidate)
            return nil
        }
    }
    
    // 6. 否则保持现有参数
    return nil
}
```

**验收标准**:
- [ ] 每周自动运行一次参数优化
- [ ] A/B 测试结果可审计
- [ ] 参数版本可回滚

---

#### 2.2 ML 模型训练 pipeline（✅ 已存在 — `ml-training/`，28 tests 全绿）

**目录结构**（实际）:
```
ml-training/
├── src/
│   ├── data_loader.py       # 加载 Parquet + 合并人工标注（×2 权重）
│   ├── feature_pipeline.py  # OrdinalEncoder + Imputer + Scaler
│   ├── train.py             # RandomForest（skl2onnx 配对 zipmap=False）
│   ├── evaluate.py          # 准确率/混淆矩阵/baseline 对比
│   └── export_model.py      # joblib / ONNX 导出
├── config.yaml              # 单一配置文件
├── scripts/                 # clean_label_pollution / eval_honest / make_go_fixture
└── tests/                   # 28 个测试（含端到端集成）
```

**训练流程**（P2.4 已验证；注意 ONNX Runtime 1.29.0 配对）:
```bash
# 1. 导出训练数据
llm-gw-exporter export --start 2024-01-01 --end 2024-03-31 --config 1

# 2. 训练模型
cd ml-training
python src/train.py --data ../exports/training_data.parquet --model xgboost

# 3. 导出 ONNX
python src/export_model.py --model models/run_001/model.pkl --output model.onnx

# 4. 部署
cp model.onnx /opt/llm-gateway/ml/
cp manifest.json /opt/llm-gateway/ml/

# 5. 启用
export ROUTING_ML_ENABLED=true
export ROUTING_ML_MANIFEST_PATH=/opt/llm-gateway/ml/manifest.json
```

**验收标准**:
- [ ] 模型准确率 > 规则引擎 baseline + 5%
- [ ] ONNX 推理延迟 P99 < 5ms
- [ ] 可热加载 + 原子切换

---

### Phase 3: 高级优化（Week 7-12）🟡

#### 3.1 创建物化视图优化查询

**文件**: `sql/migrations/startup/690_hot_models_materialized_view.sql`（新建）

```sql
-- hot_models_48h 物化视图（每 5 分钟刷新）
CREATE MATERIALIZED VIEW hot_models_48h AS
SELECT 
  chosen_model AS canonical_name,
  COUNT(*) AS request_count,
  AVG(latency_ms) AS avg_latency_ms,
  AVG(cost_usd) AS avg_cost_usd
FROM auto_route_selections_all
WHERE ts >= NOW() - INTERVAL '48 hours'
GROUP BY chosen_model
ORDER BY request_count DESC
LIMIT 10;

-- 定时刷新（添加到 cron）
CREATE OR REPLACE FUNCTION refresh_hot_models_48h()
RETURNS void AS $$
BEGIN
  REFRESH MATERIALIZED VIEW hot_models_48h;
END;
$$ LANGUAGE plpgsql;
```

---

#### 3.2 扩展 Auto-Combo 因子

**文件**: `domains/routing/auto_scoring.go`（新建）

**新增因子**:
- `costInv`: 精确成本倒数（利用 1045 Offer 定价数据）
- `taskFit`: 任务类型适配度（基于历史成功率）
- `tierPriority`: 账户层级优先级（ultra > pro > standard）

---

#### 3.3 Lite 压缩引擎

**文件**: `domains/compression/lite.go`（新建）

**功能**:
- 折叠多余换行 (3+ → 2)
- 删除行尾空白
- 精简图片 URL

**预期收益**: 15% token 节省，< 1ms 延迟

---

## 四、专项测试框架

### 4.1 测试维度

| 维度 | 测试项 | 验收指标 |
|------|--------|---------|
| **准确率** | ML vs 规则引擎 baseline | +5% |
| **延迟** | DecideV2 端到端 | P99 < 20ms |
| | ONNX 推理 | P99 < 5ms |
| | Optimizer hook 总计 | P99 < 10ms |
| **稳定性** | 反馈批量写入成功率 | > 99.9% |
| | Outcome backfill 匹配率 | > 80% |
| **资源占用** | 内存 | Baseline + 50MB |
| | CPU | Baseline + 5% |
| **回退** | ONNX 加载失败 | 继续使用规则引擎 |
| | 低置信度 | 回退规则引擎 |

---

### 4.2 测试数据生成

**工具**: `tests/routing/auto_route_test_data_generator.go`（新建）

**功能**:
- 从真实数据采样（按 task_type 分层）
- 生成合成数据（覆盖边界情况）
- 标注 ground truth

**输出**: `tests/routing/testdata/auto_route_samples.json`

---

### 4.3 回归测试套件

**文件**: `autoroute/decision_v2_regression_test.go`（新建）

**测试用例**:
1. 规则引擎 baseline（33个任务类型 × 3个profile = 99个case）
2. 低置信度回退（10个case）
3. 会话缓存重校验（5个case）
4. Optimizer hook 调用链（5个case）
5. ONNX 推理端到端（5个case）

**总计**: ~125 个测试用例

---

## 五、监控与可观测性

### 5.1 Prometheus 指标

```go
// domains/routing/metrics.go
var (
    routingDecisionDuration = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "routing_decision_duration_ms",
            Help: "AUTO routing decision latency",
            Buckets: []float64{1, 2, 5, 10, 20, 50, 100},
        },
        []string{"classifier", "profile", "task_type"},
    )
    
    routingOptimizerHookDuration = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "routing_optimizer_hook_latency_ms",
            Help: "Optimizer hook latency",
            Buckets: []float64{0.5, 1, 2, 5, 10},
        },
        []string{"hook"},
    )
    
    routingFeedbackWrites = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "routing_feedback_writes_total",
            Help: "Routing feedback batch writes",
        },
        []string{"status"},
    )
    
    routingOutcomeRegistry = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "routing_outcome_registry",
            Help: "Routing outcome stash/match/expire counts",
        },
        []string{"status"}, // stashed/matched/orphan/expired/dropped
    )
)
```

---

### 5.2 Grafana 仪表盘

**面板清单**:
1. **决策延迟**: DecideV2 P50/P95/P99
2. **Optimizer 性能**: 各 hook P99 延迟
3. **反馈闭环**: stashed → matched 匹配率
4. **ML 准确率**: 按 task_type / provider 分组
5. **资源占用**: 内存 / CPU
6. **A/B 测试**: 治疗组 vs 对照组对比

---

## 六、风险与应对

| 风险 | 概率 | 影响 | 应对 |
|------|------|------|------|
| **反馈聚合 worker 未实现** | 高 | 高 | 优先级最高，Week 1 完成 |
| **ONNX Runtime 库依赖** | 中 | 中 | Docker 镜像内置，CI 环境跳过测试 |
| **ML 模型训练数据不足** | 中 | 中 | 先用规则引擎 baseline，积累 3 个月数据后训练 |
| **人工标注质量参差** | 中 | 低 | 双标 + 抽检，审核门槛 |
| **参数优化回归** | 低 | 高 | A/B 测试验证，快速回滚 |

---

## 七、交付清单

### Week 1-2 (Phase 1)
- [x] ✅ 反馈聚合 worker (`bg/routing_metrics_aggregator.go`)
- [x] ✅ 验证数据库表完整性
- [x] ✅ 小流量灰度测试 (10%)
- [x] ✅ Prometheus 指标 + Grafana 仪表盘

### Week 3-6 (Phase 2)
- [ ] ⏳ AdaptiveLearner 自动调参 (`routingopt/adaptive_learner.go`)
- [ ] ⏳ ML 训练 pipeline (Python + ONNX导出)
- [ ] ⏳ 首个 ONNX 模型部署

### Week 7-12 (Phase 3)
- [ ] ⏳ 物化视图优化查询
- [ ] ⏳ Auto-Combo 12因子评分完整实现
- [ ] ⏳ Lite 压缩引擎
- [ ] ⏳ Session Affinity PostgreSQL 持久化

---

## 八、参考资料

### 内部文档
- `/docs/auto-model-optimization/11-implementation-roadmap.md` - AUTO 实施路线图
- `/docs/auto-model-optimization/07-human-annotation-workflow.md` - 人工标注工作流
- `/docs/auto-model-optimization/09-onnx-integration-go.md` - ONNX 集成
- `/sql/migrations/startup/669_training_human_annotations.sql` - 标注表结构
- `/sql/migrations/startup/670_routing_optimization.sql` - 优化表结构

### 外部经验
- **OmniRoute v3.8.49**: Auto-Combo 12因子评分、三层弹性架构、Session Affinity
- **LiteLLM**: 多提供商路由、fallback 策略
- **RouteLLM**: 基于 ML 的路由决策
- **Argilla**: 人工标注平台

---

## 九、总结

**当前状态**: 核心基础设施已完整 (DecideV2 + 反馈闭环 + 人工标注 + ONNX 推理)

**关键缺口**: 
1. 反馈聚合 worker（优先级最高）
2. AdaptiveLearner 自动调参逻辑
3. ML 模型训练 pipeline

**预期收益**:
- 路由准确率从 70% 提升到 80%+ (+10%)
- 成本节省 10-15%（智能推荐）
- 用户体验提升（低延迟 + 个性化路由）

**时间估算**: 12 周（3个月）

**风险**: 可控，主要缺口在后台聚合与自动调参逻辑，属于"最后一公里"问题

---

**版本历史**:
- v1.0 (2024-01-15): 初始版本，基于代码审计和 OmniRoute 研究
