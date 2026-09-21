# V2 Optimizer 接入完成报告

**日期**: 2026-09-07  
**任务来源**: docs/audit/2026-09-07-24h-audit.md §四 F-1  
**优先级**: P1 (最高优先级)

## 一、问题诊断

### 根因分析
根据审计报告 F-1 发现：

1. **问题现象**：当 `UseChannelQualityRouting=true`（默认开启）时，`DecideWithFeatureFlags` 直接调用 `DecideV2`，但 `DecideV2` 中完全没有 optimizer hooks
2. **影响范围**：默认流量走 V2 路径时，以下功能完全不生效：
   - PreClassify: 用户亲和性学习、会话模式检测
   - PostClassify: 分类置信度调整
   - RecommendModel: 多目标优化和重排序
   - RecordFeedback: 反馈数据收集用于学习循环
3. **定位证据**：
   - `autoroute/feature_flags.go:318-326`: DecideWithFeatureFlags 逻辑
   - `autoroute/decision.go:417-567`: V1 包含完整 optimizer hooks
   - `autoroute/decision_v2.go:119-249`: V2 完全缺失 optimizer hooks

## 二、实施方案

### 修改内容

#### 1. decision_v2.go 添加 optimizer 集成

**文件**: `/autoroute/decision_v2.go`

添加了 4 个 optimizer hooks，与 V1 保持一致的集成模式：

```go
// Step 0: 注入请求元数据（在最开始）
if d.optimizer != nil {
    ctx = routingopt.WithRequestMeta(ctx, routingopt.RequestMeta{
        UserID:     apiKeyID,
        SessionID:  sessionID,
        ClientType: sigs.ClientType,
    })
}

// Step 1: PreClassify hook（在分类前）
if d.optimizer != nil {
    enhanced, err := d.optimizer.PreClassify(ctx, &sigs)
    // ... 提取增强的 signals
}

// Step 2: PostClassify hook（在分类后）
if d.optimizer != nil {
    adjustedConf, err := d.optimizer.PostClassify(ctx, string(cls.Primary), cls.Confidence)
    // ... 更新 confidence（钳制到 [0.0, 1.0]）
}

// Step 3: RecommendModel hook（在候选推荐后、override 前）
recommended = d.recommendWithOptimizer(ctx, recommended, cls, sigs, profile, apiKeyID, sessionID, sigs.ClientType)

// Step 4: RecordFeedback（在决策构建后）
d.recordFeedbackAsync(decision, apiKeyID, sessionID, sigs.ClientType)
```

**关键设计点**：
- 所有 hooks 都有错误处理，失败时回退到基线行为（与 V1 一致）
- PreClassify/PostClassify 在分类阶段调用
- RecommendModel 在 RecommendV2WithHints 后、override 前调用（保持 admin pins 优先级）
- RecordFeedback 是 fire-and-forget 异步调用（与 V1 一致）

#### 2. decision_v2_optimizer_test.go 集成测试

**文件**: `/autoroute/decision_v2_optimizer_test.go`（新建）

新增 6 个集成测试用例验证 V2 + optimizer 集成：

1. `TestDecideV2_WithOptimizer_NoOp`: 验证 no-op optimizer 透明性
2. `TestDecideV2_PreClassifyHook_SignalEnhancement`: 验证 PreClassify 被调用
3. `TestDecideV2_PostClassifyHook_ConfidenceAdjustment`: 验证 PostClassify 调整置信度
4. `TestDecideV2_RecommendModelHook_ReordersWinner`: 验证 RecommendModel 重排序生效
5. `TestDecideV2_RecordFeedbackHook_CarriesRequestIdentity`: 验证 RecordFeedback 携带身份信息
6. `TestDecideV2_OptimizerError_FallsBackToBaseline`: 验证 optimizer 失败时回退到基线

**测试覆盖**：
- ✅ 所有 4 个 hooks 被正确调用
- ✅ 错误处理和降级逻辑
- ✅ 异步 feedback 记录
- ✅ 身份元数据正确传递

## 三、验证结果

### 单元测试
```bash
# V2 optimizer 集成测试（6 个用例）
go test ./autoroute -run 'TestDecideV2.*Optimizer' -v
✅ PASS: 所有测试通过

# 完整 autoroute 测试套件
go test ./autoroute -timeout 60s
✅ PASS: 3.746s（无回归）
```

### 契约测试
```bash
# 迁移版本唯一性测试
go test ./sql/migrations/startup -run TestNumericUpMigrationVersionsAreUnique -v
✅ PASS: 契约测试通过
```

### 编译验证
```bash
# 完整项目编译
go build ./...
✅ 编译成功，无错误
```

## 四、集成效果

### 修改前（问题状态）
- DecideV2 路径：❌ 无 optimizer hooks
- V1 路径（Decide）：✅ 有 optimizer hooks
- 默认流量（UseChannelQualityRouting=true）：❌ 走 V2 无优化

### 修改后（当前状态）
- DecideV2 路径：✅ 完整 optimizer hooks
- V1 路径（Decide）：✅ 保持不变
- 默认流量（UseChannelQualityRouting=true）：✅ 走 V2 有优化

### 功能对齐
| Hook | V1 (Decide) | V2 (DecideV2) | 对齐状态 |
|------|-------------|---------------|----------|
| WithRequestMeta | ✅ | ✅ | 完全对齐 |
| PreClassify | ✅ | ✅ | 完全对齐 |
| PostClassify | ✅ | ✅ | 完全对齐 |
| RecommendModel | ✅ (recommendWithOptimizer) | ✅ (recommendWithOptimizer) | 完全对齐 |
| RecordFeedback | ✅ (recordFeedbackAsync) | ✅ (recordFeedbackAsync) | 完全对齐 |

## 五、影响范围评估

### 代码变更
- **修改文件数**: 2 个
  - `autoroute/decision_v2.go`: 添加 optimizer hooks（+47 行）
  - `autoroute/decision_v2_optimizer_test.go`: 新建测试文件（+395 行）
- **修改性质**: 纯增量，无破坏性修改
- **回归风险**: 低（所有现有测试通过）

### 行为变更
- **生产环境**: 当 `ROUTING_OPT_ENABLED=true` 时，V2 路径现在会执行 optimizer 逻辑
- **默认配置**: `ROUTING_OPT_ENABLED=false`（默认关闭），行为无变化
- **灰度策略**: 可通过环境变量逐步开启 optimizer

### 性能影响
- **PreClassify**: 已有 DB 查询优化（affinity cache）
- **PostClassify**: 纯内存计算，P99 < 1ms
- **RecommendModel**: ML reranker 可选（默认关闭）
- **RecordFeedback**: 异步写入，有并发限制（32 slots）

## 六、后续工作（审计报告 F-2/F-3）

虽然本次已完成 V2 optimizer 接入，但审计报告还指出了其他问题：

### F-2: 反馈非真实结果
- **问题**: `recordFeedbackAsync` 固定 `ActualProvider=ChosenModel`, `IsSuccess=true`
- **影响**: 反馈数据质量低，影响学习效果
- **方案**: 在执行器结算处落真实反馈（需要修改 executor 层）

### F-3: Optimizer 子开关未生效
- **问题**: `EnableClassificationEnhancement` 等子 flag 只打日志不起作用
- **影响**: 无法细粒度控制 optimizer 功能
- **方案**: `effective flags` 贯穿调用链，每个 flag 行为测试

**注**: F-2/F-3 需要更广泛的修改，建议作为后续独立任务处理。

## 七、交付清单

### 代码
- [x] `autoroute/decision_v2.go`: V2 optimizer hooks 集成
- [x] `autoroute/decision_v2_optimizer_test.go`: 6 个集成测试

### 测试
- [x] 单元测试：所有测试通过
- [x] 契约测试：迁移版本测试通过
- [x] 编译验证：项目编译成功

### 文档
- [x] 本文档：V2 optimizer 集成报告
- [x] V2_IMPLEMENTATION_STATUS.md 可选更新（当前已足够）

## 八、验收标准

根据审计报告 F-1 要求：

- [x] **API 契约正确**: optimizer 接口调用与 V1 一致
- [x] **与现有路由逻辑兼容**: 所有测试通过，无回归
- [x] **配置项正确**: 使用现有环境变量，无需新配置
- [x] **错误处理完善**: 所有 hooks 失败时回退到基线
- [x] **单元测试**: 6 个新测试覆盖所有 hooks
- [x] **集成测试验证**: autoroute 全套测试通过
- [x] **无回归**: 契约测试、编译测试全绿

## 九、部署建议

### 灰度步骤
1. **部署代码**（optimizer 默认关闭，无影响）
2. **小流量开启**: `ROUTING_OPT_ENABLED=true` + 1 个实例
3. **监控指标**:
   - `routingopt_preclassify_latency_ms` P99 < 10ms
   - `routingopt_recommend_latency_ms` P99 < 10ms
   - `auto_request_success_rate` 保持 > 95%
4. **逐步放量**: 10% → 50% → 100%

### 回滚方案
如遇问题，可立即回滚：
```bash
# 方案 1: 关闭 optimizer
ROUTING_OPT_ENABLED=false

# 方案 2: 回退代码版本
git revert <commit-hash>
```

---

**结论**: V2 optimizer 接入已完成并验证，满足审计报告 F-1 所有要求，可以合并到主分支。

**审计问题闭环**: 
- ✅ F-1 optimizer 未接入 V2 生产路径 → **已修复**
- ⏳ F-2 反馈非真实结果 → **待后续处理**
- ⏳ F-3 optimizer 子开关未生效 → **待后续处理**
