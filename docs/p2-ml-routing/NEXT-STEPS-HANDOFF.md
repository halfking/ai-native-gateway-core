# P2.2 下一步任务 Handoff 提示词

**创建日期**: 2026-09-06  
**前置条件**: P2.2设计方案已完成并通过文档审计  
**下一阶段**: P2.2实施 - Week 1基础框架开发

> **⚠️ 注意**: 本文档中的任务提示词适用于 P2.2 **实施阶段**（代码开发），目前 P2.2 仍处于设计阶段。  
> 当前工作区还有未提交的 P2.1 相关改动（annotation/, web/ 等），启动 P2.2 实施前需先明确这些改动的归属与处理方式。

---

## 🚀 任务1: Week 1 Day 1-2 - 插件接口与注入点

### 提示词 (Prompt)

```
你好！我需要开始P2.2 AUTO路由优化插件的开发工作。

**任务**: Week 1 Day 1-2 - 实现插件接口与注入点

**背景**:
- P2.2设计方案已完成: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md
- 需要创建插件框架的基础代码

**具体要求**:

1. **创建插件接口** (routingopt/plugin.go)
   - 定义 RoutingOptimizer 接口 (5个方法)
   - 定义相关类型: EnhancedSignals, ModelCandidate, RoutingContext, RoutingFeedback, OptimizerStats
   - 参考设计文档 §2.2 插件接口定义

2. **创建默认实现** (routingopt/default_impl.go)
   - 实现 DefaultOptimizer (no-op版本)
   - 所有方法直接返回输入, 不做修改
   - 添加日志记录

3. **修改Decider注入点** (autoroute/index.go)
   - 在 Decider 结构体中添加 optimizer 字段
   - 实现 SetOptimizer() 方法
   - 在 Decide() 方法中调用插件 (可选, 基于optimizer是否为nil)

4. **单元测试** (routingopt/plugin_test.go)
   - 测试 DefaultOptimizer 所有方法
   - 测试 Decider.SetOptimizer() 注入
   - 测试插件调用链

5. **Feature Flags配置** (settings/routing_opt_feature_flags.go)
   - 定义 RoutingOptFeatureFlags 结构体
   - 支持环境变量: ROUTING_OPT_ENABLED, ROUTING_OPT_MAX_PLUGIN_LATENCY_MS 等
   - 参考设计文档 §6 配置管理

**验收标准**:
- [ ] routingopt/plugin.go 编译通过
- [ ] routingopt/default_impl.go 实现完整
- [ ] autoroute/index.go 注入点添加成功
- [ ] 单元测试全部通过: go test ./routingopt/... -v
- [ ] 现有测试不受影响: go test ./autoroute/... -v

**参考文档**:
- 设计文档: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md (§2.2, §6)
- 快速指南: docs/p2-ml-routing/QUICKSTART.md (Day 1部分)
- 实施路线图: docs/p2-ml-routing/p2.2-implementation-roadmap.md (Week 1)

请开始实现，完成后运行测试并报告结果。
```

---

## 🗄️ 任务2: Week 1 Day 3 - 数据库Schema

### 提示词 (Prompt)

```
你好！我需要实现P2.2路由优化插件的数据库Schema。

**任务**: Week 1 Day 3 - 创建4个数据库表和DAO层

**背景**:
- 插件接口已完成 (Day 1-2)
- 需要创建数据存储层

**具体要求**:

1. **创建Migration** (sql/migrations/startup/670_routing_optimization.sql)
   - 创建4个表:
     a) routing_optimization_state (参数版本控制)
     b) routing_feedback_log (实时反馈日志, 按月分区)
     c) routing_optimization_metrics (5分钟聚合指标)
     d) routing_user_affinity (用户历史偏好)
   - 参考设计文档 §3 数据库Schema (完整SQL)

2. **运行Migration**
   - 在开发/测试数据库上执行
   - 验证表创建成功: \dt routing_*
   - 验证索引创建: \d routing_optimization_state

3. **创建DAO层** (routingopt/dao.go)
   - OptimizationStateDAO (CRUD操作)
   - FeedbackLogDAO (插入、查询)
   - MetricsDAO (聚合查询)
   - UserAffinityDAO (CRUD操作)

4. **单元测试** (routingopt/dao_test.go)
   - 测试每个DAO的CRUD操作
   - 使用测试数据库或内存数据库

**验收标准**:
- [ ] Migration SQL执行成功, 4个表创建
- [ ] 索引和分区配置正确
- [ ] DAO层实现完整
- [ ] 单元测试通过: go test ./routingopt/dao_test.go -v

**参考文档**:
- 设计文档: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md (§3)
- 快速指南: docs/p2-ml-routing/QUICKSTART.md (Day 3部分)

请开始实现，完成后报告数据库表结构和DAO测试结果。
```

---

## 🧩 任务3: Week 1 Day 4-5 - 基础模块骨架

### 提示词 (Prompt)

```
你好！我需要实现P2.2路由优化插件的4个核心模块的骨架版本。

**任务**: Week 1 Day 4-5 - 实现4个模块的no-op版本

**背景**:
- 插件接口已完成 (Day 1-2)
- 数据库Schema已创建 (Day 3)
- 需要实现模块骨架, 确保流程跑通

**具体要求**:

1. **创建4个模块文件**:
   - routingopt/classifier_enhancer.go
   - routingopt/model_recommender.go
   - routingopt/feedback_integrator.go
   - routingopt/adaptive_learner.go

2. **实现no-op版本** (先不做真正的优化逻辑):
   - ClassificationEnhancer: 直接返回原始signals
   - ModelRecommender: 直接返回原始candidates顺序
   - FeedbackIntegrator: 只写入数据库, 不做聚合
   - AdaptiveLearner: 不做参数更新, 只读取当前参数

3. **集成到插件** (routingopt/plugin.go)
   - 在 RoutingOptimizerImpl 中组装4个模块
   - 实现完整的插件方法调用链

4. **集成测试** (routingopt/integration_test.go)
   - 测试场景1: 完整请求流程 (Decider → Plugin → 返回决策)
   - 测试场景2: 插件禁用时流程不变
   - 测试场景3: 插件超时时降级到baseline

5. **端到端验证**:
   - 启动服务 (插件禁用): ROUTING_OPT_ENABLED=false
   - 启动服务 (插件启用): ROUTING_OPT_ENABLED=true
   - 发送测试请求, 确认行为一致

**验收标准**:
- [ ] 4个模块文件创建, 结构体和方法定义完整
- [ ] no-op实现编译通过
- [ ] 集成测试通过: go test ./routingopt/integration_test.go -v
- [ ] 端到端测试: 插件启用/禁用都能正常工作
- [ ] 确认: 插件对现有路由结果无影响 (no-op)

**参考文档**:
- 设计文档: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md (§2.3, §5)
- 快速指南: docs/p2-ml-routing/QUICKSTART.md (Day 4-5部分)

**Milestone 1验证**:
完成后, 应该达到 Week 1 Milestone: "插件框架可运行, 但未启用优化逻辑"

请开始实现，完成后运行集成测试并演示端到端流程。
```

---

## 🧠 任务4: Week 2 - 智能优化实现

### 提示词 (Prompt)

```
你好！我需要实现P2.2路由优化插件的智能优化逻辑。

**任务**: Week 2 Day 6-10 - 实现4个模块的真正优化逻辑

**前置条件**:
- Week 1已完成: 插件框架可运行, 4个模块骨架已创建

**具体要求**:

### Day 6: 用户亲和力增强
1. **实现ClassificationEnhancer真正逻辑**:
   - 查询用户最近100次请求 (从request_logs)
   - 计算task type分布
   - Redis缓存用户亲和力 (TTL: 1小时)
   - 注入到 EnhancedSignals.UserAffinities

2. **测试**:
   - 单元测试: 模拟用户历史数据
   - 集成测试: 验证Redis缓存生效

### Day 7: 会话模式识别
1. **扩展ClassificationEnhancer**:
   - 识别会话模式 (IDE/CLI/Web)
   - 添加时间上下文 (高峰期/非高峰期)
   - 注入到 RoutingContext

2. **测试**:
   - 不同模式下的特征差异验证

### Day 8: Multi-objective推荐
1. **实现ModelRecommender核心逻辑**:
   - Multi-objective评分: Score = w1*Quality - w2*Cost + w3*Latency + w4*Availability
   - 动态权重调整 (profile/时间/任务类型)
   - ε-greedy探索策略 (5%探索率)
   - 多级fallback链 (预计算top-3)
   - 熔断器保护 (provider健康检查)

2. **测试**:
   - 不同场景下的权重验证
   - 探索策略验证
   - Fallback链验证

### Day 9: 人工标注集成
1. **实现FeedbackIntegrator人工标注加载**:
   - 关联查询: routing_feedback_log + training_human_annotations
   - 加权准确率计算 (人工标注×2)
   - 5分钟滚动聚合

2. **测试**:
   - 验证人工标注改变推荐结果
   - 验证加权准确率计算正确

### Day 10: 在线学习框架
1. **实现AdaptiveLearner真正逻辑**:
   - 准确率滑动窗口 (最近1000次)
   - 参数自动优化触发 (准确率提升>2%)
   - 异常检测 (准确率下降>5%告警)
   - 自动回滚 (新版本<旧版本)

2. **测试**:
   - 参数更新流程验证
   - 异常检测验证
   - 回滚保护验证

**验收标准**:
- [ ] 4个模块的真正优化逻辑实现完整
- [ ] 所有单元测试通过
- [ ] 集成测试通过
- [ ] 准确率在测试数据上有提升

**Milestone 2验证**:
完成后, 应该达到 Week 2 Milestone: "智能优化功能完整"

**参考文档**:
- 设计文档: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md (§5)
- 实施路线图: docs/p2-ml-routing/p2.2-implementation-roadmap.md (Week 2)

请按照Day 6→7→8→9→10的顺序逐步实现，每天完成后报告进度。
```

---

## 📊 任务5: Week 3 - 监控界面与上线准备

### 提示词 (Prompt)

```
你好！我需要完成P2.2路由优化插件的监控界面和上线准备工作。

**任务**: Week 3 Day 11-14 - 监控Dashboard + 测试 + 文档

**前置条件**:
- Week 1-2已完成: 插件完整功能已实现

**具体要求**:

### Day 11: 后端统计API
1. **创建API端点** (admin/routing_opt_handler.go):
   - GET /api/admin/routing-opt/stats (整体准确率、模块状态)
   - GET /api/admin/routing-opt/accuracy?days=7 (准确率趋势)
   - GET /api/admin/routing-opt/parameters (当前参数版本)
   - GET /api/admin/routing-opt/ab-test (A/B测试对比)

2. **路由注册** (admin/handler.go):
   - 注册4个API端点

3. **测试**:
   - API集成测试

### Day 12: 前端Dashboard
1. **创建Vue组件** (web/src/views/RoutingOptStatsView.vue):
   - 准确率趋势图 (ECharts折线图, 7天)
   - 各task type准确率对比表
   - 参数版本时间线
   - 人工标注贡献占比饼图
   - 实时指标卡片

2. **路由配置** (web/src/router.ts):
   - 添加 /routing-v2/optimization 路由

3. **国际化** (web/src/locales/*/index.ts):
   - 中英文翻译

4. **编译验证**:
   - npm run build
   - 确认无编译错误

### Day 13: 集成测试与压力测试
1. **端到端测试套件**:
   - go test ./routingopt/... -v -tags=integration
   - 覆盖所有核心功能

2. **压力测试**:
   - 使用 ab 或 wrk 工具
   - 目标: P99延迟增加<10ms, 吞吐量不下降
   - 生成性能报告

3. **A/B测试框架验证**:
   - 测试10%流量分流
   - 验证control vs treatment指标收集

### Day 14: 文档与部署准备
1. **补充文档**:
   - docs/p2-ml-routing/p2.2-deployment-guide.md (部署指南)
   - docs/p2-ml-routing/p2.2-monitoring-playbook.md (监控手册)
   - docs/p2-ml-routing/p2.2-troubleshooting.md (故障排查)

2. **Release notes**:
   - 功能清单
   - 配置说明
   - 已知问题

3. **部署脚本**:
   - deploy-staging.sh (Staging部署)
   - deploy-production.sh (生产部署)

**验收标准**:
- [ ] 4个API端点实现并测试通过
- [ ] 前端Dashboard编译通过, 功能正常
- [ ] 端到端测试全部通过
- [ ] 压力测试P99延迟<10ms
- [ ] 部署文档完整

**Milestone 3验证**:
完成后, 应该达到 Week 3 Milestone: "生产就绪"

**参考文档**:
- 设计文档: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md (§7, §8, §9)
- 实施路线图: docs/p2-ml-routing/p2.2-implementation-roadmap.md (Week 3)

请按照Day 11→12→13→14的顺序逐步完成，最后提供完整的交付报告。
```

---

## 🚢 任务6: Week 4-8 - 渐进式部署

### 提示词 (Prompt)

```
你好！我需要执行P2.2路由优化插件的渐进式部署。

**任务**: Week 4-8 - Staging → A/B → 全量 → 在线学习

**前置条件**:
- Week 1-3已完成: 代码开发完成, 测试通过, 文档齐全

**具体要求**:

### Week 4: Staging验证 (100%启用插件)
1. **部署到Staging**:
   ```bash
   export ROUTING_OPT_ENABLED=true
   export ROUTING_OPT_AB_TEST_ENABLED=false
   export ROUTING_OPT_ADAPTIVE_LEARNING=false  # 初期禁用
   ./deploy-staging.sh
   ```

2. **观察7天，监控指标**:
   - 准确率趋势 (目标: >75%)
   - P99延迟 (目标: <10ms)
   - 错误率 (目标: <1%)
   - 数据库负载 (写入QPS, 表大小)

3. **生成Staging验证报告**

### Week 5: 生产A/B测试 (10%流量)
1. **部署到生产**:
   ```bash
   export ROUTING_OPT_AB_TEST_ENABLED=true
   export ROUTING_OPT_AB_TEST_PERCENTAGE=0.1
   ./deploy-production.sh
   ```

2. **观察3天，对比指标**:
   - Control组 vs Treatment组准确率
   - 延迟对比 (P50, P99)
   - 成本对比 (avg cost per request)

3. **决策**: 如Treatment组显著更好，继续放量；否则回滚

### Week 6-7: 渐进式放量
1. **放量计划**:
   - Day 1-2: 10% → 观察
   - Day 3-4: 25% → 观察
   - Day 5-6: 50% → 观察
   - Day 7-8: 75% → 观察
   - Day 9-10: 100% → 完全启用

2. **每阶段验证**:
   - 准确率无回归
   - 延迟无显著增加
   - 错误率稳定

### Week 8: 启用在线学习
1. **启用自适应学习**:
   ```bash
   export ROUTING_OPT_ADAPTIVE_LEARNING=true
   ```

2. **观察参数自动更新**:
   - 更新频率 (预期: 每周1-2次)
   - 准确率变化 (目标: 持续提升)
   - 异常检测告警 (确保无误报)

3. **生成最终部署报告**

**验收标准**:
- [ ] Staging验证通过 (准确率>75%, 延迟<10ms, 错误率<1%)
- [ ] A/B测试Treatment组胜出 (准确率+3%+)
- [ ] 100%流量稳定运行
- [ ] 在线学习正常工作
- [ ] 准确率达到80%+目标

**参考文档**:
- 实施路线图: docs/p2-ml-routing/p2.2-implementation-roadmap.md (部署策略)
- 部署指南: docs/p2-ml-routing/p2.2-deployment-guide.md (待创建)

请按照Week 4→5→6-7→8的顺序执行，每周提供部署报告和数据分析。
```

---

## 📋 任务优先级建议

### 优先级1 (必须): 核心开发
- ✅ 任务1: Week 1 Day 1-2 (插件接口) - **最高优先级**
- ✅ 任务2: Week 1 Day 3 (数据库Schema)
- ✅ 任务3: Week 1 Day 4-5 (模块骨架)
- ✅ 任务4: Week 2 (智能优化)
- ✅ 任务5: Week 3 (监控界面)

### 优先级2 (重要): 部署上线
- ✅ 任务6: Week 4-8 (渐进式部署)

### 优先级3 (可选): 优化增强
- P2.3: ML模型训练 (需要3-6个月数据积累)
- P3: 强化学习 (长期规划)

---

## 📞 支持资源

### 文档资源
- **项目总览**: docs/p2-ml-routing/README.md
- **完整设计**: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md
- **实施路线图**: docs/p2-ml-routing/p2.2-implementation-roadmap.md
- **快速启动**: docs/p2-ml-routing/QUICKSTART.md
- **审计报告**: docs/p2-ml-routing/AUDIT-REPORT.md

### 代码参考
- **现有代码**: autoroute/index.go, autoroute/classifier.go
- **P2.1基础**: annotation/, admin/annotation_handler.go
- **数据库**: sql/migrations/startup/664_training_human_annotations.sql

### 团队支持
- **技术负责人**: AUTO Route Team
- **问题反馈**: GitHub Issues / 内部Slack #auto-routing

---

## ✅ 使用建议

### 如何使用这些提示词
1. **按顺序执行**: 任务1 → 2 → 3 → 4 → 5 → 6
2. **每个任务独立会话**: 复制对应提示词到新的AI会话
3. **验证后再进入下一步**: 确保当前任务验收标准全部通过
4. **保留上下文**: 每个任务完成后，将关键输出记录到文档

### 并行执行建议
- **Day 11 + Day 12可并行**: 后端API和前端Dashboard可由不同人开发
- **其他任务串行**: 有依赖关系，必须顺序执行

### 遇到问题时
1. **先查阅设计文档**: 90%的问题已在设计文档中说明
2. **参考QUICKSTART**: 提供Day 1-5的详细步骤
3. **查看审计报告**: 了解设计完整性和注意事项
4. **联系团队**: 如遇到设计之外的问题

---

**创建时间**: 2026-09-06  
**维护者**: AUTO Route Team  
**版本**: v1.0  
**状态**: ✅ 准备就绪
