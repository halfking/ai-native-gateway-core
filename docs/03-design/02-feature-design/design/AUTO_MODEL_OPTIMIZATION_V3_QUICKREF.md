# AUTO_MODEL V3 快速参考

## 🚀 快速开始

### 运行迁移
```bash
cd sql
./run_migrations.sh local              # 本地测试
./run_migrations.sh postgres-252       # 开发环境
```

### 验证迁移
```bash
psql -h localhost -U postgres -d llm_gateway \
  -f migrations/validate_v3_migrations.sql
```

### 回滚（如需）
```bash
cd sql
./rollback_migrations.sh local
```

---

## 📊 10类任务分类速查

| 类别 | 层级 | 关键词示例 |
|------|------|-----------|
| architecture | A | system design, 架构, design doc |
| audit | A | review, audit, 代码审查, security |
| debugging | A | debug, error, 不工作, stack trace |
| coding | B | implement, 实现, create, build |
| refactoring | B | refactor, optimize, 重构, 优化 |
| testing | B | test, 单元测试, coverage, mock |
| devops | C | deploy, docker, k8s, 部署 |
| documentation | C | document, 注释, README, explain |
| summary | C | summarize, 总结, TLDR, overview |
| dependency | C | upgrade, 依赖, package, npm |

---

## 🎯 层级选择优先级

```
1. X-Gw-Model-Tier header      ← 最高优先级（用户显式控制）
2. Agent depth >= 2            ← 强制 tier-c（嵌套子代理）
3. Tenant config               ← 租户特定配置
4. Global config               ← 全局默认配置
5. In-memory default           ← 内存fallback
```

---

## 💰 成本节约

| 状态 | 计算 | 成本 |
|------|------|------|
| **当前** | 100% × $20/1M | $20/1M |
| **优化后** | 20% × $30 + 40% × $10 + 40% × $2 | $10.80/1M |
| **节约** | - | **46%** ↓ |

---

## 🔍 常用监控查询

### 层级分布
```sql
SELECT task_tier, COUNT(*), 
       ROUND(COUNT(*) * 100.0 / SUM(COUNT(*)) OVER (), 2) as pct
FROM request_logs
WHERE is_terminal = TRUE AND ts >= NOW() - INTERVAL '24 hours'
  AND task_tier IS NOT NULL
GROUP BY task_tier;
```

### 分类准确率
```sql
SELECT (auto_decision->>'task_type') as task_type,
       COUNT(*) as count,
       AVG((auto_decision->>'confidence')::NUMERIC) as avg_confidence
FROM request_logs
WHERE request_type = 'client' AND ts >= NOW() - INTERVAL '24 hours'
GROUP BY task_type ORDER BY count DESC;
```

### 成本分析
```sql
SELECT task_tier,
       SUM(total_tokens * unit_price_in / 1000000) as cost_usd
FROM request_logs
WHERE is_terminal = TRUE AND ts >= NOW() - INTERVAL '24 hours'
GROUP BY task_tier;
```

---

## 📁 关键文件

### 已创建文件
```
sql/migrations/
├── 202609_01_add_request_type_fields.sql
├── 202609_02_create_task_type_tier_config.sql
├── 202609_03_add_tier_to_provider_models.sql
└── validate_v3_migrations.sql

sql/rollback/
├── 202609_01_rollback_request_type_fields.sql
├── 202609_02_rollback_task_type_tier_config.sql
└── 202609_03_rollback_tier_to_provider_models.sql

autoroute/
├── task_types_v3.go       ← 10类定义 + 关键词
├── classifier_v3.go       ← V3分类算法
└── tier_selector.go       ← 层级选择逻辑
```

### 待更新文件 (Phase 3)
```
autoroute/
├── recommend_v2.go        ← 添加层级过滤
├── scoring.go             ← 添加层级加权
└── decision.go            ← 集成V3分类器
```

---

## 🎛️ 特性开关

```go
// Phase 1-2 (已完成)
FeatureEnhancedClassification = "auto_v3_enhanced_classification"
FeatureTierSelection         = "auto_v3_tier_selection"

// Phase 3-8 (待实施)
FeatureTierBasedRouting      = "auto_v3_tier_routing"
FeatureRequestTypeSeparation = "auto_v3_request_separation"
FeatureDepthTracking         = "auto_v3_depth_tracking"
FeatureClientQueue           = "auto_v3_client_queue"
FeatureTierQueueSegmentation = "auto_v3_tier_queue_segmentation"
FeatureQualityGates          = "auto_v3_quality_gates"
FeatureAutoEscalation        = "auto_v3_auto_escalation"
```

---

## 🔧 代码集成示例

```go
// 创建V3分类器
keywords := autoroute.DefaultV3Keywords()
thresholds := autoroute.DefaultHeuristicThresholds()
classifier := autoroute.NewV3Classifier(keywords, thresholds, true)

// 创建层级选择器
tierSelector := autoroute.NewTierSelector(db, true)

// 分类
classification, _ := classifier.Classify(ctx, signals)

// 选择层级
tierInput := autoroute.TierSelectionInput{
    TaskType:   classification.Primary,
    Confidence: classification.Confidence,
    AgentDepth: getDepthFromHeader(r),
    TenantID:   getTenantID(r),
    HeaderTier: r.Header.Get("X-Gw-Model-Tier"),
}
tierResult, _ := tierSelector.SelectTier(ctx, tierInput)

// 使用 tierResult.Tier 进行模型过滤
```

---

## ⚠️ 重要提醒

### 迁移前
- [ ] 备份数据库
- [ ] 在非高峰时段执行
- [ ] 准备回滚脚本

### 部署顺序
1. ✅ 数据库迁移
2. ✅ 代码部署 (特性开关关闭)
3. ⏳ 影子模式 (双写，不路由)
4. ⏳ 灰度发布 (10% → 50% → 100%)

### 监控指标
- 分类准确率 >= 85%
- 升级率 < 15%
- 会话继续率降低 < 5%
- P95延迟 < 2s

---

## 📞 获取帮助

- **设计文档**: `AUTO_MODEL_OPTIMIZATION_V3_PLAN.md`
- **实施总结**: `AUTO_MODEL_OPTIMIZATION_V3_SUMMARY.md`
- **执行跟踪**: `AUTO_MODEL_OPTIMIZATION_V3_EXECUTION.md`
- **技术细节**: `AUTO_MODEL_OPTIMIZATION_V3_README.md`

---

**更新时间**: 2026-09-02  
**当前进度**: Phase 1-2 完成 (2/8)  
**下一步**: 测试迁移 → 应用到postgres-252 → Phase 3实施
