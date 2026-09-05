# LLM模型质量监控 - 快速开始

## 5分钟快速体验

### 1. 编译工具

```bash
cd llm-gateway-go
go build -mod=mod -o quality-monitor ./cmd/quality-monitor
```

### 2. 运行快速测试

```bash
# 测试一个模型（模拟OpenAI GPT-4）
./quality-monitor -mode=test -provider=openai -model=gpt-4 -benchmark=lite

# 输出示例：
# === LLM模型质量检测 ===
# 供应商: openai
# 模型: gpt-4
# 总题数: 50
# 正确数: 46
# 准确率: 92.00%
# 综合评分: 88.50 (A)
```

### 3. 模拟质量下降场景

```bash
# 演示供应商模型降级的检测
./quality-monitor -mode=simulate-drop

# 系统会自动：
# 1. 进行初始质量测试
# 2. 模拟模型性能下降
# 3. 再次测试
# 4. 对比并生成告警
```

### 4. 查看结果

```bash
# 查看测试报告
ls -lh test-data/reports/

# 查看评分历史
cat test-data/scores/openai_gpt-4.jsonl | jq '.'

# 查看告警日志（如果有）
cat test-data/alerts.log | jq '.'
```

## 实际应用场景

### 场景1: 每日定时检测

```bash
# 创建cron任务，每天凌晨2点检测所有关键模型
0 2 * * * cd /path/to/llm-gateway-go && ./quality-monitor -mode=monitor -interval=24h
```

### 场景2: 集成到网关

```go
// 在网关启动时启动质量监控
import "github.com/kaixuan/llm-gateway-go/bg"

func main() {
    // ... 网关初始化代码 ...
    
    // 启动模型质量监控
    qualityWorker := bg.NewModelQualityWorker("./data")
    if err := qualityWorker.Start(ctx, "./data"); err != nil {
        log.Fatal(err)
    }
    defer qualityWorker.Stop()
    
    // ... 网关主逻辑 ...
}
```

### 场景3: 异常时触发检测

```go
// 在检测到高错误率时触发质量检查
if errorRate > 0.3 {
    err := qualityWorker.TriggerCheck(
        ctx, 
        "openai", 
        "gpt-4", 
        fmt.Sprintf("high_error_rate_%.2f", errorRate),
    )
    if err != nil {
        log.Printf("Quality check failed: %v", err)
    }
}
```

### 场景4: 查询当前评分

```go
// 获取所有模型的当前质量评分
scores := qualityWorker.GetCurrentScores()

for key, score := range scores {
    log.Printf("Model %s: Score=%.2f (%s), Accuracy=%.2f%%, Latency=%.0fms",
        key, score.OverallScore, score.Grade, score.Accuracy, score.Latency)
}
```

## 配置建议

### 生产环境

```go
config := &modelquality.MonitorConfig{
    EnableScheduled:      true,
    ScheduleInterval:     24 * time.Hour,  // 每天检测
    UseLiteBenchmark:     true,            // 使用快速测试
    EnableAnomalyTrigger: true,
    ErrorRateThreshold:   0.3,             // 30%错误率触发
    LatencyThreshold:     5000,            // 5秒延迟触发
    AlertOnQualityDrop:   true,
    QualityDropThreshold: 10.0,            // 准确率下降10%告警
    TargetModels: []ModelTarget{
        // 只监控关键模型
        {Provider: "openai", ModelName: "gpt-4", Alias: "GPT-4"},
        {Provider: "openai", ModelName: "gpt-3.5-turbo", Alias: "GPT-3.5"},
    },
}
```

### 开发环境

```go
config := &modelquality.MonitorConfig{
    EnableScheduled:      false,           // 不启用自动检测
    EnableAnomalyTrigger: true,
    ErrorRateThreshold:   0.5,             // 更宽松的阈值
    LatencyThreshold:     10000,
    AlertOnQualityDrop:   true,
    QualityDropThreshold: 5.0,
    TargetModels: []ModelTarget{
        // 测试所有模型
        {Provider: "openai", ModelName: "gpt-4"},
        {Provider: "anthropic", ModelName: "claude-3-opus"},
        {Provider: "domestic_a", ModelName: "chatglm-4"},
    },
}
```

## 常见问题

### Q1: 测试需要多长时间？

**A:** MMLU Lite (50题) 约1-2分钟，取决于模型响应速度。

### Q2: 如何添加自定义测试题？

**A:** 编辑 `domains/modelquality/mmlu_data.go`，添加新的Question结构体。

### Q3: 如何接入真实的模型API？

**A:** 实现 `ModelInvoker` 接口，参考 `invoker.go` 中的示例。

### Q4: 评分不准确怎么办？

**A:** 可以调整权重配置或增加测试题目数量。

### Q5: 如何集成到监控系统？

**A:** 读取 `test-data/scores/*.jsonl` 文件，解析JSON并上报到Prometheus/InfluxDB等。

## 高级用法

### 自定义评分算法

```go
// 修改权重
type CustomScoreCalculator struct {
    *ScoreCalculator
}

func (c *CustomScoreCalculator) CalculateScore(report *BenchmarkReport) *QualityScore {
    score := c.ScoreCalculator.CalculateScore(report)
    
    // 自定义综合评分: 准确率80% + 稳定性20%
    score.OverallScore = score.Accuracy*0.8 + score.Stability*0.2
    score.Grade = c.scoreToGrade(score.OverallScore)
    
    return score
}
```

### 并发测试多个模型

```go
var wg sync.WaitGroup
models := []struct{provider, name string}{
    {"openai", "gpt-4"},
    {"anthropic", "claude-3-opus"},
    {"domestic_a", "chatglm-4"},
}

for _, m := range models {
    wg.Add(1)
    go func(provider, name string) {
        defer wg.Done()
        suite := modelquality.GetMMLULiteSuite()
        report, _ := executor.Execute(ctx, name, provider, suite)
        // 处理结果...
    }(m.provider, m.name)
}

wg.Wait()
```

### 导出为Prometheus指标

```go
import "github.com/prometheus/client_golang/prometheus"

var (
    modelQualityScore = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llm_model_quality_score",
            Help: "LLM model quality score",
        },
        []string{"provider", "model", "grade"},
    )
)

func exportMetrics(score *QualityScore) {
    modelQualityScore.WithLabelValues(
        score.Provider,
        score.ModelName,
        score.Grade,
    ).Set(score.OverallScore)
}
```

## 下一步

1. 根据实际需求调整监控配置
2. 实现真实的模型调用器（替换MockInvoker）
3. 将评分数据集成到现有监控系统
4. 根据质量评分优化路由策略

## 获取帮助

- 文档: `docs/model-quality/README.md`
- 实施总结: `IMPLEMENTATION_SUMMARY_MODEL_QUALITY.md`
- 单元测试示例: `domains/modelquality/benchmark_test.go`
