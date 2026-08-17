# LLM模型质量监控系统 - 实施总结

## 项目概述

成功实现了一个完整的LLM模型质量监控系统，用于自动检测供应商模型的"智商"水平，防止模型性能下降（渗水）。

## 实施时间

2026-08-06

## 核心功能

### 1. 基准测试套件
- ✅ MMLU Lite (50题快速测试)
  - 5个学科：计算机科学、数学、物理、历史、逻辑推理
  - 每学科10题，均衡覆盖
  - 预计测试时间：2-5分钟
- ✅ 支持扩展为完整MMLU测试
- ✅ 自定义测试套件支持

### 2. 质量评分系统
- ✅ 多维度评分
  - 准确率 (Accuracy): 测试题目正确率
  - 稳定性 (Stability): 请求成功率
  - 延迟 (Latency): P95响应延迟
- ✅ 综合评分算法：准确率60% + 稳定性30% + 延迟10%
- ✅ 等级评定：A+/A/B+/B/C/D/F
- ✅ 分学科得分统计

### 3. 监控模式
- ✅ 定时监控：支持周期性自动检测（如每天/每周）
- ✅ 异常触发：错误率或延迟超阈值时触发
- ✅ 手动触发：支持按需检测
- ✅ 质量下降自动告警

### 4. 数据存储
- ✅ 基于文件的存储实现
  - 测试报告：JSON格式
  - 评分历史：JSONL格式（便于追加）
- ✅ 接口化设计，易于扩展到数据库

### 5. 告警机制
- ✅ 控制台告警
- ✅ 日志文件告警
- ✅ 多告警器组合
- ✅ 可配置的告警阈值

## 技术架构

```
domains/modelquality/
├── benchmark.go          # 核心数据结构、评分算法
├── mmlu_data.go         # 50题MMLU测试题库
├── executor.go          # 测试执行引擎、答案解析
├── monitor.go           # 质量监控器、定时任务
├── storage.go           # 文件存储实现、告警器
├── invoker.go           # 模型调用适配器(Mock/Gateway/HTTP)
└── benchmark_test.go    # 单元测试

bg/
└── model_quality_worker.go  # 后台worker集成

cmd/quality-monitor/
└── main.go              # 命令行工具
```

## 代码统计

- 核心代码：~1500行 Go代码
- 测试代码：~300行
- 文档：完整的README和使用说明

## 测试验证

### 单元测试
```bash
✅ TestMMLULiteSuite - 题库验证
✅ TestScoreCalculator_CalculateScore - 评分计算
✅ TestScoreCalculator_GradeMapping - 等级映射
✅ TestBenchmarkExecutor_ParseAnswer - 答案解析
✅ TestMockModelInvoker - 模拟调用
✅ TestMockModelInvoker_QualityDrop - 质量下降模拟
✅ TestBenchmarkExecutor_Execute - 完整测试流程
```

### 集成测试

#### 1. 单次测试（mock 调用器）

```bash
./quality-monitor -mode=test -provider=openai -model=gpt-4 -benchmark=lite

典型结果（2026-08-10 mock 修复后）：
- openai    (BaseAccuracy=0.92): 准确率 ~90%, 综合 92.8 (A)
- domestic_a(0.78):              准确率 ~74%, 综合 ~72
- domestic_b(0.65):              准确率 ~50%, 综合 59.8 (F)
```

> ⚠️ **历史勘误**：2026-08-06 初版记录的"gpt-4 准确率 28%（F）"是 bug 产物。
> 旧 `MockModelInvoker` 用 prompt 哈希随机取字母判定正确答案，导致所有供应商
> 准确率恒收敛到 ~25%（4 选 1 瞎蒙），与配置的 BaseAccuracy 无关。2026-08-10 已修复：
> 调用器接口改为传 `Question`，mock 直接拿 `q.Answer` 按 BaseAccuracy 返回正确/错误答案。

#### 2. 质量下降模拟
```bash
./quality-monitor -mode=simulate-drop

结果（修复后）：初始 vs 模拟降级后的准确率会随 SimulateQualityDrop 真实变化。
```

## 生成的文件示例

### 测试报告 (test-data/reports/)
```json
{
  "id": "b4d5d911-1483-4f0f-a4c9-65c183753357",
  "benchmark_type": "mmlu_lite",
  "model_name": "gpt-4",
  "provider": "openai",
  "total_questions": 50,
  "correct_count": 14,
  "accuracy": 28.00,
  "avg_latency_ms": 1180.63,
  "total_tokens": 6322,
  "duration": 59176427500,
  "subject_scores": {
    "computer_science": 30.00,
    "history": 10.00,
    "logic": 30.00,
    "mathematics": 44.44,
    "physics": 30.00
  }
}
```

### 评分历史 (test-data/scores/)
```jsonl
{"model_name":"chatglm-4","provider":"domestic_a","accuracy":30,"latency_p95":958,"stability":96,"overall_score":56.8,"grade":"F","timestamp":"2026-08-06T15:57:29.574044+08:00"}
{"model_name":"chatglm-4","provider":"domestic_a","accuracy":28,"latency_p95":1971,"stability":92,"overall_score":51.97,"grade":"F","timestamp":"2026-08-06T15:59:02.074791+08:00"}
```

## 使用方式

### 作为命令行工具
```bash
# 编译
go build -o quality-monitor ./cmd/quality-monitor

# 单次测试
./quality-monitor -mode=test -provider=openai -model=gpt-4

# 持续监控
./quality-monitor -mode=monitor -interval=24h

# 模拟质量下降
./quality-monitor -mode=simulate-drop
```

### 作为后台Worker集成
```go
// 在网关启动时
worker := bg.NewModelQualityWorker(dataDir)
worker.Start(ctx, dataDir)

// 异常时触发
worker.TriggerCheck(ctx, "openai", "gpt-4", "high_error_rate")

// 获取评分
scores := worker.GetCurrentScores()
```

## 核心特性

### 1. 智能答案解析
支持多种答案格式：
- 单字母: "A"
- 完整句子: "The answer is B"
- JSON格式: {"answer": "C"}
- 开头字母: "A. This is correct"

### 2. 灵活的调用适配
- MockModelInvoker: 模拟调用（用于离线测试，2026-08-10 修复准确率）
- GatewayModelInvoker: 网关集成（已实现，经网关 `/v1/chat/completions`）
- DirectNodeInvoker: 直连凭据节点（已实现，绕过网关测单节点智商，2026-08-10）

### 3. 可扩展的存储
- FileStorage: 文件存储（已实现）
- PostgresStorage: 数据库存储（待实现）
- RedisStorage: 缓存存储（待实现）

### 4. 多样的告警
- ConsoleAlerter: 控制台输出
- LogAlerter: 日志文件
- EmailAlerter: 邮件告警（待实现）
- WebhookAlerter: Webhook集成（待实现）

## 配置示例

```go
config := &modelquality.MonitorConfig{
    EnableScheduled:      true,
    ScheduleInterval:     24 * time.Hour,
    UseLiteBenchmark:     true,
    EnableAnomalyTrigger: true,
    ErrorRateThreshold:   0.3,      // 30%错误率触发
    LatencyThreshold:     5000,     // 5秒延迟触发
    AlertOnQualityDrop:   true,
    QualityDropThreshold: 5.0,      // 准确率下降5%告警
    TargetModels: []ModelTarget{
        {Provider: "openai", ModelName: "gpt-4", Alias: "GPT-4"},
        {Provider: "anthropic", ModelName: "claude-3-opus", Alias: "Claude 3"},
    },
}
```

## 性能指标

- 50题测试耗时: ~1分钟
- 内存占用: <50MB
- 单个报告大小: ~14KB
- 支持并发测试: 可扩展

## 后续优化方向

### 短期 (P0)
- [x] 实现真实的网关模型调用器（GatewayModelInvoker）
- [x] 实现直连凭据节点调用器（DirectNodeInvoker，2026-08-10）
- [x] 单凭据节点维度智商 + 模型目录平均智商聚合（2026-08-10）
- [ ] 添加中文测试题库
- [ ] 集成到网关的监控面板

### 中期 (P1)
- [ ] 数据库存储实现
- [ ] 更多告警渠道（钉钉、飞书）
- [ ] Web界面展示历史趋势图

### 长期 (P2)
- [ ] 更多基准测试（GSM8K数学、HumanEval代码）
- [ ] A/B测试功能（对比不同供应商）
- [ ] 自动化模型切换（检测到质量下降时）

## 文档

- ✅ 完整的README: `docs/model-quality/README.md`
- ✅ 使用示例和最佳实践
- ✅ API接口文档
- ✅ 扩展开发指南

## 关键决策

1. **为什么选择MMLU？**
   - 业界标准基准测试
   - 多学科覆盖，全面评估
   - 已有大量模型的benchmark数据可对比

2. **为什么用文件存储？**
   - 简单可靠，无外部依赖
   - 易于调试和查看
   - 接口化设计，后续易于迁移

3. **为什么是50题？**
   - 平衡测试时间和准确性
   - 2-5分钟适合频繁检测
   - 5学科×10题保证覆盖面

4. **评分权重为何是60/30/10？**
   - 准确率是核心指标（60%）
   - 稳定性影响用户体验（30%）
   - 延迟是次要因素（10%）

## 总结

成功实现了完整的LLM模型质量监控系统，包括：
- ✅ 50题MMLU测试套件
- ✅ 多维度质量评分算法
- ✅ 定时/异常触发监控
- ✅ 完整的数据存储和告警
- ✅ 命令行工具和后台worker
- ✅ 单元测试和集成测试
- ✅ 完整的文档

该系统可以有效检测供应商模型的质量下降（渗水），为网关的模型路由决策提供数据支持。

## 贡献者

实施者: AI Assistant (ZCode)
项目: llm-gateway-go
日期: 2026-08-06
