# P2 ML路由优化 - 项目总览

**项目代号**: P2 (Phase 2)  
**目标**: 将AUTO模型路由从规则驱动升级为数据驱动 + 持续学习  
**状态**: P2.1 ✅ 已完成 | P2.2 📋 设计就绪

---

## 快速导航

### P2.1 人工标注工作流 (已完成)
- **[实施报告](./p2.1-implementation-report.md)** - Web界面实现细节
- **[完成总结](../planning/p2.1-completion-summary.md)** - CLI工具与数据库Schema
- **[部署指南](../deployment/p2.1-human-annotation-workflow.md)** - 完整使用手册

### P2.2 路由优化插件 (设计就绪)
- **[完整设计方案](./p2.2-routing-optimization-plugin-design.md)** ⭐ 核心文档
- **[实施路线图](./p2.2-implementation-roadmap.md)** - 3周开发计划
- **[OmniRoute经验借鉴](./p2.2-omniroute-learnings.md)** - 业界最佳实践

---

## 一、项目背景

### 当前痛点
1. **AUTO路由准确率不足**: 启发式规则准确率~70%，有提升空间
2. **缺乏学习机制**: 无法从错误中自动改进
3. **人工标注未利用**: P2.1收集的标注数据未反馈到系统
4. **缺乏个性化**: 所有用户使用相同的路由策略

### P2目标
- ✅ **P2.1**: 建立人工标注基础设施 (已完成)
- 🎯 **P2.2**: 建立持续优化闭环 (设计就绪)
- 🔮 **P2.3+**: ML模型训练与部署 (未来)

---

## 二、P2.1 成果总结

### 交付物 (2026-09-06完成)

#### 1. Web标注界面
- **主标注页**: `/routing-v2/annotations`
  - 样本筛选 (日期、置信度、状态)
  - 单条/批量标注
  - 实时表单验证
- **统计分析页**: `/routing-v2/annotations/stats`
  - ML准确率趋势
  - 供应商准确率对比
  - 标注人员统计
  - 原因分布分析

#### 2. 后端API (Go)
```go
POST   /api/admin/annotations              // 创建标注
GET    /api/admin/annotations/samples      // 获取待标注样本
GET    /api/admin/annotations/stats        // 统计分析
DELETE /api/admin/annotations/:id          // 删除标注
```

#### 3. CLI工具
```bash
llm-gw-annotator export   # 导出低置信度样本到CSV
llm-gw-annotator import   # 导入标注结果
llm-gw-annotator validate # 验证CSV格式
llm-gw-annotator stats    # 显示统计报告
```

#### 4. 数据库Schema
- `training_human_annotations` - 人工标注主表
- `ml_routing_annotations` - Web标注数据
- 4个统计视图 (准确率、供应商、标注人、原因)

### 关键指标
- ✅ 编译成功，无错误
- ✅ 所有单元测试通过
- ✅ 隐私合规 (不导出prompt/messages/response)
- ✅ 双语支持 (中文/英文)

---

## 三、P2.2 设计概览

### 核心理念
> **插件化、数据驱动、持续学习**

### 架构设计

```
┌─────────────────────────────────────────────────────────┐
│                   HTTP Request                          │
└────────────────────┬────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────┐
│            autoroute.Decider (现有)                      │
│  ┌──────────────────────────────────────────────────┐  │
│  │  🔌 RoutingOptimizer Plugin (新增)               │  │
│  │  ├─ ClassificationEnhancer  (特征增强)           │  │
│  │  ├─ ModelRecommender        (智能推荐)           │  │
│  │  ├─ FeedbackIntegrator      (反馈闭环)           │  │
│  │  └─ AdaptiveLearner         (在线学习)           │  │
│  └──────────────────────────────────────────────────┘  │
└────────────────────┬────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────┐
│   存储层: PostgreSQL (4新表) + Redis (热数据缓存)       │
└─────────────────────────────────────────────────────────┘
```

### 4个核心模块

#### 1. ClassificationEnhancer (特征增强)
**功能**:
- 用户亲和力注入 (历史task type分布)
- 会话模式识别 (IDE/CLI/Web)
- 时间上下文 (高峰期/非高峰期)

**预期提升**: 分类准确率 +3-5%

#### 2. ModelRecommender (智能推荐)
**功能**:
- Multi-objective优化 (质量/成本/延迟/可用性)
- 动态权重调整 (profile/时间/任务类型)
- ε-greedy探索策略 (5%探索率)
- 多级fallback链 (3层降级)

**预期提升**: 路由准确率 +5-7%

#### 3. FeedbackIntegrator (反馈闭环)
**功能**:
- 实时反馈收集 (成功/失败/延迟/成本)
- 人工标注数据加载 (P2.1数据)
- 加权准确率计算 (人工标注权重×2)
- 5分钟滚动聚合

**预期提升**: 数据质量 +100% (引入ground truth)

#### 4. AdaptiveLearner (在线学习)
**功能**:
- 准确率滑动窗口 (最近1000次)
- 参数自动优化 (Bayesian Optimization)
- 异常检测 (准确率下降>5%告警)
- 自动回滚 (新版本不如旧版本)

**预期提升**: 持续改进，长期准确率 +2-3%

### 业务价值

| 指标 | 当前 (Baseline) | P2.2目标 | 提升 |
|------|----------------|---------|------|
| 准确率 | 70% | 80%+ | +10% |
| 错误路由率 | 25% | 15% | -40% |
| 成本节省 | - | 10-15% | 新增 |
| 插件延迟 | - | P99<10ms | 可控 |

### 技术特性

#### ✅ 插件化
- 通过 `ROUTING_OPT_ENABLED` 开关控制
- 不侵入现有代码
- 超时降级保护 (10ms超时)
- 支持热插拔

#### ✅ 数据驱动
- 基于P2.1的人工标注数据
- 实时反馈聚合
- 历史模式学习
- 用户行为建模

#### ✅ 持续学习
- 在线参数优化
- 准确率自动监控
- 异常自动检测
- 参数自动回滚

#### ✅ 可观测
- Prometheus指标导出
- Grafana Dashboard
- 告警规则配置
- A/B测试支持

---

## 四、实施计划

### Week 1: 基础框架 (5天)
- Day 1-2: 插件接口与注入点
- Day 3: 数据库Schema (4个新表)
- Day 4-5: 基础模块实现

**Milestone 1**: 插件框架可运行 ✅

### Week 2: 智能优化 (5天)
- Day 6: 用户亲和力增强
- Day 7: 会话模式识别
- Day 8: Multi-objective推荐
- Day 9: 人工标注集成
- Day 10: 在线学习框架

**Milestone 2**: 智能优化功能完整 ✅

### Week 3: 监控上线 (4天)
- Day 11: 后端统计API
- Day 12: 前端Dashboard
- Day 13: 集成测试与压力测试
- Day 14: 文档与部署准备

**Milestone 3**: 生产就绪 ✅

### Week 4-8: 渐进式部署
- Week 4: Staging环境验证 (100%启用)
- Week 5: 生产A/B测试 (10%流量)
- Week 6-7: 渐进式放量 (25%→50%→100%)
- Week 8: 启用在线学习

---

## 五、关键设计决策

### 决策1: 为什么选择插件化架构？
**原因**:
- ✅ 降低风险 (可随时禁用)
- ✅ 独立升级 (不影响现有代码)
- ✅ 易测试 (单独测试插件)
- ✅ 支持A/B测试

**替代方案**: 直接修改Decider代码
**缺点**: 侵入性强，回滚困难

### 决策2: 为什么选择在线学习而非离线训练？
**原因**:
- ✅ 实时响应 (准确率下降立即调整)
- ✅ 轻量级 (仅优化参数，不训练模型)
- ✅ 低延迟 (无需等待离线训练周期)
- ✅ 可解释 (参数变化可追溯)

**替代方案**: 定期离线训练XGBoost/LightGBM
**缺点**: 部署复杂，延迟高

### 决策3: 为什么人工标注权重×2？
**原因**:
- ✅ Ground truth (人工判断更可靠)
- ✅ 稀缺数据 (人工标注成本高)
- ✅ 实证有效 (参考Active Learning文献)

**替代方案**: 权重×3或×5
**缺点**: 过度拟合人工标注

### 决策4: 为什么选择ε-greedy而非UCB/Thompson Sampling？
**原因**:
- ✅ 简单易懂 (工程师易理解)
- ✅ 可控探索率 (ε=5%可配置)
- ✅ 实证有效 (业界广泛使用)
- ✅ 易调试 (探索行为可预测)

**替代方案**: UCB1, Thompson Sampling
**优点**: 理论更优
**缺点**: 复杂度高，调参难

---

## 六、风险管理

### 技术风险

| 风险 | 影响 | 概率 | 缓解措施 | 责任人 |
|------|------|------|---------|--------|
| 插件延迟过高 | 用户体验下降 | 中 | Redis缓存、超时降级 | 后端Dev |
| 准确率未提升 | 白做功 | 低 | A/B测试提前验证 | Tech Lead |
| 数据库写入瓶颈 | 服务不稳定 | 中 | 批量写入、异步持久化 | DBA |
| 在线学习过拟合 | 准确率波动 | 中 | 滑动窗口、自动回滚 | 后端Dev |

### 业务风险

| 风险 | 影响 | 概率 | 缓解措施 | 责任人 |
|------|------|------|---------|--------|
| 人工标注不足 | 学习效果差 | 高 | 降低标注门槛、激励 | PM |
| 用户反感探索 | 投诉 | 低 | ε=5%低探索率 | PM |
| 模型选择偏差 | 锁定次优 | 中 | 定期强制探索 | Data Scientist |

---

## 七、OmniRoute经验借鉴

### 直接采纳
1. ✅ **多级fallback链** - 预计算top-3候选，失败时无缝降级
2. ✅ **动态权重调整** - 根据profile/时间/任务类型调整权重
3. ✅ **ε-greedy探索** - 平衡exploitation vs exploration
4. ✅ **熔断器模式** - provider连续失败后短期禁用

### 差异化优势
- 🆕 **人工标注增强** - OmniRoute没有
- 🆕 **在线学习** - 更系统化
- 🆕 **插件化架构** - 更易维护
- 🆕 **用户建模** - 个性化路由

### 不适用特性
- ❌ **Free-tier聚合** - 我们focus on企业付费用户
- ❌ **压缩优化** - 独立特性，不在P2范围
- ❌ **290+ Provider** - 当前provider数量已够用

---

## 八、监控与告警

### Prometheus指标

```
# 准确率指标
routing_opt_accuracy_overall                        # 整体准确率
routing_opt_accuracy_by_task{task_type}            # 按任务类型
routing_opt_accuracy_by_provider{provider}         # 按供应商

# 性能指标
routing_opt_latency_ms{module}                     # 插件延迟
routing_opt_cache_hit_rate{cache_type}             # 缓存命中率
routing_opt_fallback_triggered_total               # Fallback次数

# 学习指标
routing_opt_parameter_updates_total                # 参数更新次数
routing_opt_human_annotations_used_total           # 人工标注使用次数
routing_opt_exploration_requests_total             # 探索请求次数
```

### Grafana Dashboard

```
Row 1: 核心指标
  - 准确率趋势图 (7天)
  - 插件延迟分布 (P50/P99)
  - 请求量对比 (启用/禁用)

Row 2: 分类准确率
  - 各task type准确率柱状图
  - 各provider准确率表格

Row 3: 学习指标
  - 参数更新时间线
  - 人工标注贡献饼图
  - 探索请求占比

Row 4: 异常监控
  - 准确率下降告警
  - 插件错误率
  - 超时降级次数
```

### 告警规则

```yaml
# 准确率告警
- alert: RoutingOptimizerAccuracyDrop
  expr: routing_opt_accuracy_overall < 0.7
  for: 5m
  severity: warning

# 性能告警
- alert: RoutingOptimizerHighLatency
  expr: histogram_quantile(0.99, routing_opt_latency_ms) > 50
  for: 3m
  severity: warning

# 异常告警
- alert: RoutingOptimizerHighErrorRate
  expr: rate(routing_opt_plugin_errors_total[5m]) > 0.01
  for: 2m
  severity: critical
```

---

## 九、成功标准

### Phase 1: Staging验证 (Week 4)
- ✅ 准确率: 70% → 75%+
- ✅ P99延迟: <10ms
- ✅ 错误率: <1%
- ✅ 数据库QPS: <1000 writes/s

### Phase 2: 生产A/B (Week 5)
- ✅ Treatment组准确率 > Control组 +3%
- ✅ 延迟增加: <5ms
- ✅ 成本对比: 持平或更低

### Phase 3: 全量上线 (Week 7)
- ✅ 准确率: 70% → 80%+
- ✅ 错误路由率: 25% → 15%
- ✅ 成本节省: 10-15%
- ✅ 插件可用性: 99.9%

### Phase 4: 在线学习 (Week 8+)
- ✅ 参数更新频率: 每周1-2次
- ✅ 准确率趋势: 持续提升
- ✅ 回滚触发次数: 0 (理想)
- ✅ 人工标注利用率: >80%

---

## 十、相关文档

### 设计文档 (本目录)
- [P2.1实施报告](./p2.1-implementation-report.md) - Web标注界面
- [P2.2完整设计](./p2.2-routing-optimization-plugin-design.md) ⭐ 核心
- [P2.2实施路线图](./p2.2-implementation-roadmap.md) - 3周计划
- [OmniRoute经验](./p2.2-omniroute-learnings.md) - 业界参考

### 历史文档 (planning/)
- [P2.1完成总结](../planning/p2.1-completion-summary.md) - CLI工具
- [P2.1验证报告](../planning/p2.1-verification-results.md) - 审计结果

### 部署文档 (deployment/)
- [P2.1部署指南](../deployment/p2.1-human-annotation-workflow.md) - 使用手册

### 代码位置
```
annotation/                  # P2.1: 标注数据层
  ├── types.go
  ├── exporter.go
  ├── importer.go
  └── stats.go

admin/
  ├── annotation_handler.go  # P2.1: Web API
  └── routing_opt_handler.go # P2.2: 优化统计API (待开发)

autoroute/
  ├── classifier.go          # 现有: 任务分类器
  ├── index.go               # 现有: 路由决策器 (待修改: 注入插件)
  └── classification_feedback.go  # 现有: 反馈聚合器

routingopt/                  # P2.2: 优化插件 (待开发)
  ├── plugin.go
  ├── classifier_enhancer.go
  ├── model_recommender.go
  ├── feedback_integrator.go
  └── adaptive_learner.go

web/src/views/
  ├── AnnotationView.vue           # P2.1: 标注主页
  ├── AnnotationStatsView.vue      # P2.1: 统计分析
  └── RoutingOptStatsView.vue      # P2.2: 优化Dashboard (待开发)
```

---

## 十一、FAQ

### Q1: P2.2什么时候开始开发？
**A**: 设计已就绪，可随时启动。建议先在Staging环境积累1周人工标注数据（P2.1），再开始P2.2开发。

### Q2: P2.2会影响现有功能吗？
**A**: 不会。插件通过 `ROUTING_OPT_ENABLED=false` 默认禁用，且有超时降级保护。

### Q3: 需要多少人工标注数据才有效？
**A**: 最少100条，推荐1000+条。P2.1已支持CSV批量导入。

### Q4: 如果准确率未提升怎么办？
**A**: A/B测试会提前验证效果，如无提升则不全量部署。插件可随时禁用回退。

### Q5: 在线学习会自动调整参数吗？
**A**: 会，但有严格保护：
- 仅当准确率提升>2%时才更新
- 异常检测（下降>5%）自动告警
- 新版本不如旧版本自动回滚

### Q6: P2.3会做什么？
**A**: ML模型训练（XGBoost/LightGBM），但需要P2.2运行3-6个月积累足够数据。

---

## 十二、联系方式

- **技术负责人**: AUTO Route Team
- **项目文档**: `/docs/p2-ml-routing/`
- **代码仓库**: `autoroute/`, `routingopt/`, `annotation/`
- **问题反馈**: GitHub Issues / 内部Slack #auto-routing

---

**项目启动**: 2026-08-25 (P2.1)  
**当前状态**: P2.1 ✅ 已完成 | P2.2 📋 设计就绪  
**下一里程碑**: P2.2 Week 1 (基础框架)  
**预计完成**: P2.2 Week 8 (全量上线)
