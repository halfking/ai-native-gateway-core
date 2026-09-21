# P2.2 快速启动指南

**目标受众**: 开发团队  
**预计阅读时间**: 5分钟  
**前置条件**: P2.1已部署，有≥100条人工标注数据

---

## 立即行动 (Day 1)

### 1. 审查设计方案 (30分钟)
```bash
# 阅读核心文档
cat docs/p2-ml-routing/README.md                                    # 项目总览
cat docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md   # 完整设计 ⭐
cat docs/p2-ml-routing/p2.2-implementation-roadmap.md               # 3周计划
```

**重点关注**:
- 架构图 (设计文档 §2.1)
- 插件接口定义 (设计文档 §2.2)
- 4个核心模块职责 (设计文档 §2.3)

### 2. 技术预研 (1小时)
```bash
# 检查现有代码结构
ls -la autoroute/
cat autoroute/index.go | head -100                    # Decider结构
cat autoroute/classifier.go | head -100               # Classifier接口
cat autoroute/classification_feedback.go              # 反馈聚合器

# 检查P2.1基础设施
psql $DATABASE_URL -c "\d training_human_annotations"
psql $DATABASE_URL -c "SELECT COUNT(*) FROM training_human_annotations"

# 检查Redis可用性
redis-cli PING
```

**验证清单**:
- [ ] Decider代码可读，知道在哪注入插件
- [ ] 人工标注表存在，有数据
- [ ] Redis可用

### 3. 团队对齐 (30分钟会议)
**议程**:
1. 设计方案讲解 (10min)
2. 技术栈确认 (5min): Go + PostgreSQL + Redis + Prometheus
3. 资源分配 (5min): 1后端Dev × 3周 + 0.5前端Dev × 2天
4. 风险讨论 (5min): 延迟、准确率、数据库
5. Q&A (5min)

**输出**: 启动/不启动决策

---

## Week 1: 基础框架 (5天)

### Day 1: 插件接口定义

**创建文件**:
```bash
mkdir -p routingopt
touch routingopt/plugin.go
touch routingopt/default_impl.go
touch routingopt/plugin_test.go
```

**实现内容** (参考设计文档 §2.2):
```go
// routingopt/plugin.go
type RoutingOptimizer interface {
    PreClassify(ctx context.Context, signals *autoroute.ClassificationSignals) (*EnhancedSignals, error)
    PostClassify(ctx context.Context, taskType autoroute.TaskType, confidence float64) (float64, error)
    RecommendModel(ctx context.Context, candidates []ModelCandidate, context RoutingContext) ([]ModelCandidate, error)
    RecordFeedback(ctx context.Context, feedback RoutingFeedback) error
    GetStats(ctx context.Context) (*OptimizerStats, error)
}
```

**修改现有文件**:
```go
// autoroute/index.go
type Decider struct {
    // ... 现有字段
    optimizer routingopt.RoutingOptimizer  // 新增
}

func (d *Decider) SetOptimizer(opt routingopt.RoutingOptimizer) {
    d.optimizer = opt
}
```

**测试**:
```bash
go test ./routingopt/... -v
go test ./autoroute/... -v  # 确保没破坏现有功能
```

**交付标准**: 插件接口编译通过，单元测试通过

---

### Day 2: Feature Flag配置

**创建配置文件**:
```bash
touch settings/routing_opt_feature_flags.go
```

**实现内容**:
```go
type RoutingOptFeatureFlags struct {
    Enabled                         bool    `env:"ROUTING_OPT_ENABLED" default:"false"`
    EnableClassificationEnhancement bool    `default:"true"`
    EnableModelRecommendation       bool    `default:"true"`
    EnableFeedbackIntegration       bool    `default:"true"`
    EnableAdaptiveLearning          bool    `default:"false"`  // 初期禁用
    MaxPluginLatencyMs              int     `default:"10"`
    ExplorationRate                 float64 `default:"0.05"`
}
```

**环境变量示例**:
```bash
# .env.example
ROUTING_OPT_ENABLED=false                           # 默认禁用
ROUTING_OPT_CLASSIFICATION_ENHANCEMENT=true
ROUTING_OPT_MODEL_RECOMMENDATION=true
ROUTING_OPT_FEEDBACK_INTEGRATION=true
ROUTING_OPT_ADAPTIVE_LEARNING=false
ROUTING_OPT_MAX_PLUGIN_LATENCY_MS=10
ROUTING_OPT_EXPLORATION_RATE=0.05
```

**交付标准**: 配置加载成功，可通过环境变量控制

---

### Day 3: 数据库Schema

**创建Migration**:
```bash
touch sql/migrations/startup/670_routing_optimization.sql
```

**复制内容** (从设计文档 §3):
```sql
-- 1. routing_optimization_state (优化参数版本控制)
CREATE TABLE routing_optimization_state (...);

-- 2. routing_feedback_log (实时反馈日志)
CREATE TABLE routing_feedback_log (...);

-- 3. routing_optimization_metrics (5分钟聚合指标)
CREATE TABLE routing_optimization_metrics (...);

-- 4. routing_user_affinity (用户历史偏好)
CREATE TABLE routing_user_affinity (...);
```

**运行Migration**:
```bash
psql $DATABASE_URL -f sql/migrations/startup/670_routing_optimization.sql
```

**验证**:
```bash
psql $DATABASE_URL -c "\dt routing_*"
psql $DATABASE_URL -c "\d routing_optimization_state"
```

**创建DAO层**:
```bash
touch routingopt/dao.go
touch routingopt/dao_test.go
```

**交付标准**: 4个表创建成功，DAO层CRUD测试通过

---

### Day 4-5: 基础模块骨架

**创建文件**:
```bash
touch routingopt/classifier_enhancer.go
touch routingopt/model_recommender.go
touch routingopt/feedback_integrator.go
touch routingopt/adaptive_learner.go
```

**实现策略**: 先写no-op版本，确保流程跑通
```go
// 示例: classifier_enhancer.go (no-op版本)
func (e *ClassificationEnhancer) Enhance(ctx context.Context, signals *autoroute.ClassificationSignals) (*EnhancedSignals, error) {
    // Day 4-5: 直接返回原始signals，不做增强
    return &EnhancedSignals{Original: signals}, nil
}

// Week 2会实现真正的增强逻辑
```

**集成测试**:
```bash
touch routingopt/integration_test.go
```

**测试场景**:
1. 请求 → Decider → 插件调用 → 返回决策
2. 插件禁用时，流程不变
3. 插件超时时，降级到baseline

**交付标准**: 端到端流程可运行，插件对现有逻辑无影响

---

## Week 2-3: 参考实施路线图

详见: [p2.2-implementation-roadmap.md](./p2.2-implementation-roadmap.md)

---

## 开发环境快速配置

### 1. 依赖检查
```bash
go version          # ≥1.21
psql --version      # ≥14
redis-cli --version # ≥6
```

### 2. 启动服务
```bash
# Terminal 1: PostgreSQL (如果本地)
# 或使用远程数据库

# Terminal 2: Redis
redis-server

# Terminal 3: 应用
export ROUTING_OPT_ENABLED=false  # 初期禁用
go run cmd/llm-gateway/main.go
```

### 3. 验证插件接口
```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "auto",
    "messages": [{"role": "user", "content": "测试"}]
  }'

# 检查日志，应无插件相关错误
grep "routing_opt" logs/llm-gateway.log
```

---

## 常见问题

### Q: 如何验证插件注入成功？
```go
// autoroute/index.go
func (d *Decider) Decide(...) {
    if d.optimizer != nil {
        log.Info("routing optimizer plugin enabled")
        // ...
    }
}
```

### Q: 插件超时如何处理？
```go
ctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
defer cancel()

enhanced, err := d.optimizer.PreClassify(ctx, &signals)
if err != nil {
    log.Warn("optimizer.PreClassify timeout, using original signals")
    // 降级: 使用原始signals
}
```

### Q: 如何本地测试人工标注数据？
```sql
-- 插入测试数据
INSERT INTO training_human_annotations (request_id, auto_label, human_label, is_correct, annotator)
VALUES 
  ('test-1', 'openai', 'anthropic', false, 'dev-tester'),
  ('test-2', 'anthropic', 'anthropic', true, 'dev-tester');

-- 验证查询
SELECT * FROM training_human_annotations WHERE annotator = 'dev-tester';
```

---

## 检查清单

### Day 1完成标准
- [ ] `routingopt/plugin.go` 创建，接口定义完成
- [ ] `autoroute/index.go` 修改，注入点添加
- [ ] 单元测试通过
- [ ] 编译无错误

### Day 2完成标准
- [ ] Feature flags配置文件创建
- [ ] 环境变量可控制插件启用/禁用
- [ ] 配置加载单元测试通过

### Day 3完成标准
- [ ] 4个表创建成功
- [ ] DAO层实现完成
- [ ] CRUD单元测试通过

### Day 4-5完成标准
- [ ] 4个模块骨架文件创建
- [ ] No-op实现完成
- [ ] 集成测试通过: 端到端流程可运行
- [ ] 确认: 插件禁用时，现有功能不受影响

### Week 1里程碑验证
```bash
# 运行完整测试套件
go test ./routingopt/... -v -race
go test ./autoroute/... -v -race

# 启动服务 (插件禁用)
export ROUTING_OPT_ENABLED=false
go run cmd/llm-gateway/main.go

# 启动服务 (插件启用, no-op版本)
export ROUTING_OPT_ENABLED=true
go run cmd/llm-gateway/main.go

# 发送测试请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "auto", "messages": [{"role": "user", "content": "测试"}]}'

# 检查日志
tail -f logs/llm-gateway.log | grep "routing_opt"
```

**预期结果**: 
- ✅ 插件禁用: 行为与之前完全一致
- ✅ 插件启用: 日志显示插件被调用，但不改变路由结果 (no-op)
- ✅ 无性能回归: 延迟增加<5ms

---

## 下一步

### 完成Week 1后
1. **Code Review**: 技术负责人审查插件框架代码
2. **Demo**: 向团队演示插件调用链
3. **文档更新**: 补充实际遇到的问题和解决方案
4. **启动Week 2**: 实现真正的优化逻辑

### 持续关注
- 每日站会: 同步进度和阻塞
- 每周复盘: 对比路线图，调整计划
- Slack #auto-routing: 技术讨论和问题求助

---

## 资源链接

- **完整设计**: [p2.2-routing-optimization-plugin-design.md](./p2.2-routing-optimization-plugin-design.md)
- **实施路线图**: [p2.2-implementation-roadmap.md](./p2.2-implementation-roadmap.md)
- **OmniRoute参考**: [p2.2-omniroute-learnings.md](./p2.2-omniroute-learnings.md)
- **项目总览**: [README.md](./README.md)

---

**创建时间**: 2026-09-06  
**适用版本**: P2.2  
**维护者**: AUTO Route Team
