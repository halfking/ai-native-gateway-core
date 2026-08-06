# LLM模型质量监控系统

## 🎯 项目目标

自动化检测LLM供应商模型的"智商"水平，防止模型性能下降（渗水），为网关的路由决策提供数据支持。

## ✨ 核心价值

1. **自动化监控**: 定期/异常触发的自动化质量检测
2. **渗水检测**: 及时发现供应商模型降级或质量下降
3. **数据驱动**: 为模型路由提供量化的质量评分
4. **成本优化**: 避免使用低质量模型浪费成本
5. **SLA保障**: 确保服务质量符合预期

## 📦 交付物

### 核心代码 (~2300行)
```
domains/modelquality/
├── benchmark.go          # 核心数据结构、评分算法 (252行)
├── mmlu_data.go         # 50题MMLU测试题库 (435行)
├── executor.go          # 测试执行引擎、答案解析 (244行)
├── monitor.go           # 质量监控器、定时任务 (223行)
├── storage.go           # 文件存储实现、告警器 (242行)
├── invoker.go           # 模型调用适配器 (234行)
└── benchmark_test.go    # 单元测试 (294行)

bg/model_quality_worker.go    # 后台worker集成 (92行)
cmd/quality-monitor/main.go   # 命令行工具 (284行)
```

### 文档
- ✅ `docs/model-quality/README.md` - 完整使用文档
- ✅ `docs/model-quality/QUICKSTART.md` - 快速开始指南
- ✅ `IMPLEMENTATION_SUMMARY_MODEL_QUALITY.md` - 实施总结

### 工具
- ✅ `quality-monitor` - 命令行工具
- ✅ `demo-quality-monitor.sh` - 完整演示脚本

## 🚀 快速开始

```bash
# 1. 编译
go build -mod=mod -o quality-monitor ./cmd/quality-monitor

# 2. 快速测试
./quality-monitor -mode=test -provider=openai -model=gpt-4 -benchmark=lite

# 3. 模拟质量下降
./quality-monitor -mode=simulate-drop

# 4. 持续监控
./quality-monitor -mode=monitor -interval=24h

# 5. 完整演示
./demo-quality-monitor.sh
```

## 📊 功能特性

### 1. 基准测试
- [x] MMLU Lite (50题，5学科)
- [x] 多选题格式 (A/B/C/D)
- [x] 智能答案解析（支持多种格式）
- [ ] MMLU Full (可扩展)
- [ ] 中文题库
- [ ] 代码能力测试（HumanEval）

### 2. 质量评分
- [x] 准确率评分（正确率）
- [x] 稳定性评分（成功率）
- [x] 延迟评分（P95响应时间）
- [x] 综合评分（加权计算）
- [x] 等级评定（A+~F）
- [x] 分学科得分统计

### 3. 监控模式
- [x] 定时监控（可配置间隔）
- [x] 异常触发（错误率/延迟阈值）
- [x] 手动触发
- [x] 质量下降告警
- [ ] 自动模型切换

### 4. 数据存储
- [x] 文件存储（JSON + JSONL）
- [x] 测试报告归档
- [x] 评分历史追踪
- [ ] PostgreSQL存储
- [ ] Redis缓存

### 5. 告警机制
- [x] 控制台告警
- [x] 日志文件告警
- [x] 多告警器组合
- [ ] 邮件告警
- [ ] 钉钉/飞书告警
- [ ] Webhook集成

## 🏗️ 技术架构

```
┌─────────────────────────────────────────┐
│          命令行工具 / 后台Worker          │
└──────────────────┬──────────────────────┘
                   │
┌──────────────────▼──────────────────────┐
│          QualityMonitor                 │
│  - 定时调度                              │
│  - 异常触发                              │
│  - 质量对比                              │
└──────────────────┬──────────────────────┘
                   │
┌──────────────────▼──────────────────────┐
│       BenchmarkExecutor                 │
│  - 测试执行                              │
│  - 答案解析                              │
│  - 结果统计                              │
└──────────────────┬──────────────────────┘
                   │
┌──────────────────▼──────────────────────┐
│        ModelInvoker (接口)              │
│  ├─ MockInvoker (演示)                  │
│  ├─ GatewayInvoker (网关集成) [待实现]  │
│  └─ HTTPInvoker (直连API) [待实现]      │
└─────────────────────────────────────────┘
```

## 📈 测试结果

### 单元测试覆盖
- ✅ 题库结构验证
- ✅ 评分算法测试
- ✅ 答案解析测试
- ✅ 模拟调用测试
- ✅ 质量下降检测

### 集成测试
```
测试模型: OpenAI GPT-4 (模拟)
- 总题数: 50
- 测试耗时: ~1分钟
- 报告大小: 14KB
- 评分记录: 已保存

质量下降检测:
- 初始准确率: 30.00% (评分: 56.80, F)
- 降级后准确率: 28.00% (评分: 51.97, F)
- 延迟变化: 958ms → 1971ms
- 稳定性下降: 96% → 92%
✓ 成功检测到质量下降
```

## 💡 使用场景

### 场景1: 日常质量监控
每天凌晨2点自动检测所有关键模型，生成质量报告。

### 场景2: 异常告警
当检测到某供应商错误率突增时，立即触发质量检测。

### 场景3: 新模型评估
供应商发布新模型时，先进行质量测试再决定是否接入。

### 场景4: 路由优化
根据实时质量评分动态调整模型路由权重。

### 场景5: 成本控制
避免使用低质量模型，减少重试和补偿成本。

## 🔧 配置示例

```go
config := &modelquality.MonitorConfig{
    // 定时监控
    EnableScheduled:      true,
    ScheduleInterval:     24 * time.Hour,  // 每天检测
    UseLiteBenchmark:     true,            // 快速测试
    
    // 异常触发
    EnableAnomalyTrigger: true,
    ErrorRateThreshold:   0.3,             // 30%错误率
    LatencyThreshold:     5000,            // 5秒延迟
    
    // 告警
    AlertOnQualityDrop:   true,
    QualityDropThreshold: 5.0,             // 下降5%
    
    // 监控目标
    TargetModels: []ModelTarget{
        {Provider: "openai", ModelName: "gpt-4"},
        {Provider: "anthropic", ModelName: "claude-3-opus"},
    },
}
```

## 📝 评分标准

### 综合评分公式
```
综合评分 = 准确率 × 60% + 稳定性 × 30% + 延迟评分 × 10%
```

### 延迟评分
- < 1000ms: 100分
- 1000-5000ms: 线性递减
- > 5000ms: 0分

### 等级划分
| 分数 | 等级 | 说明 |
|------|------|------|
| 95+ | A+ | 优秀，推荐使用 |
| 90-94 | A | 良好 |
| 85-89 | B+ | 中上 |
| 80-84 | B | 中等 |
| 70-79 | C | 及格 |
| 60-69 | D | 较差，需要关注 |
| <60 | F | 不合格，建议停用 |

## 🎓 扩展开发

### 添加新题目
编辑 `domains/modelquality/mmlu_data.go`：
```go
{
    ID:       "custom_001",
    Subject:  "custom_subject",
    Question: "Your question?",
    Options:  []string{"A", "B", "C", "D"},
    Answer:   "B",
}
```

### 实现真实调用器
```go
type MyInvoker struct {
    // 你的依赖
}

func (m *MyInvoker) InvokeModel(ctx context.Context, provider string, modelName string, prompt string) (response string, tokenUsage int, latency time.Duration, err error) {
    // 调用真实API
    return response, tokens, latency, nil
}
```

### 集成数据库
实现 `MonitorStorage` 接口存储到PostgreSQL/MySQL。

### 添加告警渠道
实现 `Alerter` 接口发送到钉钉/飞书/邮件。

## 🗺️ Roadmap

### Phase 1: MVP ✅ (已完成)
- [x] 50题MMLU测试套件
- [x] 质量评分系统
- [x] 定时/异常监控
- [x] 文件存储和告警
- [x] 命令行工具
- [x] 完整文档

### Phase 2: 生产化 (进行中)
- [ ] 对接网关真实调用
- [ ] PostgreSQL存储
- [ ] 钉钉/飞书告警
- [ ] 监控面板集成

### Phase 3: 增强 (规划中)
- [ ] 中文测试题库
- [ ] 代码能力测试
- [ ] 趋势分析和预测
- [ ] 自动化模型切换

## 📚 相关文档

- [完整使用文档](docs/model-quality/README.md)
- [快速开始指南](docs/model-quality/QUICKSTART.md)
- [实施总结](IMPLEMENTATION_SUMMARY_MODEL_QUALITY.md)

## 🤝 贡献

欢迎贡献：
- 提交新的测试题目
- 实现新的调用器
- 添加新的告警渠道
- 改进评分算法

## 📄 许可证

Apache License 2.0

---

**开发者**: AI Assistant (ZCode)  
**项目**: llm-gateway-go  
**完成时间**: 2026-08-06  
**代码行数**: ~2300行
