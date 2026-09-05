# LLM模型质量监控 - 特色模型监控指南

## 概述

针对网关实际使用的**特色模型**和**常用模型**进行质量监控，每个供应商+模型组合都有独立的评分历史，系统会自动检测中途变化。

## 核心特性

### 1. 特色模型自动发现
系统内置12个特色模型的监控配置：

**国际模型**
- OpenAI: GPT-4, GPT-4 Turbo, GPT-3.5 Turbo
- Anthropic: Claude 3 Opus, Claude 3 Sonnet

**国产特色模型**
- 智谱: GLM-4, GLM-4 Plus
- 通义千问: Qwen Plus, Qwen Turbo
- 文心一言: ERNIE 4.0
- 月之暗面: Kimi (Moonshot)
- 字节豆包: Doubao Pro

### 2. 供应商+模型维度评分
- 每个供应商的每个模型都有独立的评分记录
- 评分包含：准确率、稳定性、延迟、综合评分、等级
- 所有评分永久保存，可追溯历史

### 3. 历史追踪和变化检测
- **完整历史**：记录所有测试的评分历史
- **趋势分析**：分析最近N天的评分趋势（上升/下降/稳定）
- **波动检测**：检测评分波动性，识别不稳定的模型
- **变化告警**：自动检测质量下降，分级告警（Critical/High/Medium/Low）

## 使用场景

### 场景1: 查看某个模型的完整历史

```bash
# 查看OpenAI GPT-4的质量历史
./quality-monitor -mode=history -provider=openai -model=gpt-4 -days=30

# 输出示例：
# 历史记录统计:
#   总测试次数: 15
#   首次测试: 2026-07-10 02:00:00
#   最近测试: 2026-08-06 02:00:00
#
# 评分统计:
#   当前评分: 92.50 (A)
#   平均评分: 91.20
#   最高评分: 95.30 (A+) - 2026-07-25
#   最低评分: 87.40 (B+) - 2026-07-15
#
# 趋势分析 (30天):
#   趋势方向: up
#   波动性: 2.15 (标准差)
#   ✓ 波动正常，质量稳定
```

### 场景2: 检测所有模型的质量变化

```bash
# 检测所有特色模型的质量变化
./quality-monitor -mode=changes

# 输出示例：
# === 模型质量变化报告 ===
# 检测时间: 2026-08-06 16:30:00
# 检测到 2 个模型有质量变化
#
# ⚠️ 高风险变化 (High):
#   ↓ aliyun:qwen-plus
#     准确率: 88.00% → 76.00% (↓12.00%)
#     综合评分: 86.50 (B+) → 75.20 (C) (↓11.30)
#     延迟P95: 1200ms → 2800ms (+1600ms)
#
# ⚡ 中等变化 (Medium):
#   ↑ zhipu:glm-4
#     准确率: 82.00% → 88.00% (↑6.00%)
#     综合评分: 80.50 (B) → 86.20 (B+) (↑5.70)
```

### 场景3: 定期自动监控所有特色模型

```bash
# 每天凌晨2点执行（添加到crontab）
0 2 * * * cd /path/to/llm-gateway-go && ./quality-monitor -mode=monitor -interval=24h

# 监控器会自动：
# 1. 测试所有12个特色模型
# 2. 保存评分历史
# 3. 检测质量变化
# 4. 发送告警（如果检测到下降）
```

### 场景4: 新模型接入前的质量评估

```bash
# 测试新模型
./quality-monitor -mode=test -provider=deepseek -model=deepseek-v2 -benchmark=full

# 查看测试报告
cat data/model-quality/reports/deepseek_deepseek-v2_*.json

# 决策：
# - 评分 >= 85 (B+以上): 可以接入
# - 评分 70-84 (C-B): 谨慎接入，加强监控
# - 评分 < 70 (D及以下): 不建议接入
```

### 场景5: 异常时触发质量检查

在网关代码中集成：

```go
// 检测到某供应商错误率异常
if errorRate > 0.3 {
    // 触发质量检查
    worker.TriggerCheck(ctx, "aliyun", "qwen-plus", 
        fmt.Sprintf("high_error_rate_%.2f", errorRate))
}

// 检测到延迟异常
if avgLatency > 5000 {
    worker.TriggerCheck(ctx, "zhipu", "glm-4", 
        fmt.Sprintf("high_latency_%dms", avgLatency))
}
```

## 数据存储结构

```
data/model-quality/
├── reports/                          # 测试报告
│   ├── openai_gpt-4_mmlu_lite_20260806_020000.json
│   ├── zhipu_glm-4_mmlu_lite_20260806_020130.json
│   └── aliyun_qwen-plus_mmlu_lite_20260806_020300.json
│
├── scores/                           # 评分历史 (JSONL格式)
│   ├── openai_gpt-4.jsonl           # 每行一条评分记录
│   ├── zhipu_glm-4.jsonl
│   └── aliyun_qwen-plus.jsonl
│
└── alerts.log                        # 告警日志
```

### 评分历史格式 (JSONL)

```jsonl
{"model_name":"gpt-4","provider":"openai","accuracy":92.0,"latency_p95":1200,"stability":98,"overall_score":91.8,"grade":"A","timestamp":"2026-08-01T02:00:00+08:00","benchmark_id":"..."}
{"model_name":"gpt-4","provider":"openai","accuracy":90.0,"latency_p95":1350,"stability":96,"overall_score":89.4,"grade":"A","timestamp":"2026-08-02T02:00:00+08:00","benchmark_id":"..."}
{"model_name":"gpt-4","provider":"openai","accuracy":94.0,"latency_p95":1100,"stability":99,"overall_score":93.7,"grade":"A","timestamp":"2026-08-03T02:00:00+08:00","benchmark_id":"..."}
```

每行是一次测试的评分，可以方便地追加、查询和分析。

## 历史分析能力

### 1. 趋势分析

系统会分析指定天数内的评分趋势：

- **up (上升)**: 质量持续改善
- **down (下降)**: 质量持续下降，需要关注
- **stable (稳定)**: 质量保持稳定

```bash
# 分析最近7天的趋势
./quality-monitor -mode=history -provider=openai -model=gpt-4 -days=7

# 分析最近30天的趋势
./quality-monitor -mode=history -provider=zhipu -model=glm-4 -days=30
```

### 2. 波动性检测

系统会计算评分的标准差来判断波动性：

- **标准差 < 3**: 非常稳定
- **标准差 3-5**: 正常波动
- **标准差 > 5**: 波动剧烈，质量不稳定

波动剧烈的模型需要特别关注，可能供应商在频繁调整模型。

### 3. 变化检测

系统会对比相邻两次测试的评分：

**变化阈值**:
- 综合评分变化 < 2分: 视为稳定，不告警
- 综合评分变化 2-5分: 轻微变化 (Low)
- 综合评分变化 5-10分: 中等变化 (Medium)
- 综合评分变化 10-15分: 高风险变化 (High)
- 综合评分变化 > 15分: 严重变化 (Critical)

**变化类型**:
- **improvement (改进)**: 评分上升，好事
- **degradation (下降)**: 评分下降，需要告警
- **stable (稳定)**: 无显著变化

## 告警策略

### 告警级别

1. **Critical (严重)**: 评分下降 > 15分
   - 立即告警
   - 建议停用该模型
   - 联系供应商确认

2. **High (高风险)**: 评分下降 10-15分
   - 紧急告警
   - 降低该模型的路由权重
   - 密切监控

3. **Medium (中等)**: 评分下降 5-10分
   - 正常告警
   - 增加监控频率
   - 记录备案

4. **Low (轻微)**: 评分下降 2-5分
   - 记录日志
   - 正常监控

### 告警触发条件

```go
config := &modelquality.MonitorConfig{
    AlertOnQualityDrop:   true,   // 启用质量下降告警
    QualityDropThreshold: 5.0,    // 下降5分触发告警
}
```

## 集成到网关

### 方式1: 作为后台Worker

```go
// 在网关启动时
worker := bg.NewModelQualityWorker("./data")
if err := worker.Start(ctx, "./data"); err != nil {
    log.Fatal(err)
}

// 异常时触发检测
if errorRate > 0.3 {
    worker.TriggerCheck(ctx, provider, modelName, "high_error_rate")
}

// 获取所有模型评分
scores := worker.GetCurrentScores()
```

### 方式2: 定时任务 (Cron)

```bash
# /etc/crontab
# 每天凌晨2点检测所有特色模型
0 2 * * * /path/to/quality-monitor -mode=monitor -interval=24h

# 每6小时检测关键模型
0 */6 * * * /path/to/quality-monitor -mode=changes
```

### 方式3: 手动触发

```bash
# 测试单个模型
./quality-monitor -mode=test -provider=openai -model=gpt-4

# 查看历史
./quality-monitor -mode=history -provider=openai -model=gpt-4

# 检测所有变化
./quality-monitor -mode=changes
```

## 配置模型列表

### 方式1: 使用默认列表

系统内置12个特色模型，无需配置：

```go
models := modelquality.GetDefaultMonitorModels()
```

### 方式2: 自定义列表

```go
models := []modelquality.ModelTarget{
    {Provider: "openai", ModelName: "gpt-4", Alias: "OpenAI GPT-4"},
    {Provider: "zhipu", ModelName: "glm-4", Alias: "智谱 GLM-4"},
    // 添加更多模型...
}

discovery := modelquality.NewStaticModelDiscovery(models)
```

### 方式3: 从网关自动发现 (TODO)

```go
// 未来实现：从网关配置自动发现所有活跃模型
discovery := modelquality.NewGatewayModelDiscovery()
models, _ := discovery.DiscoverModels(ctx)
```

## 最佳实践

### 1. 监控频率

- **生产环境**: 每天检测1次（凌晨低峰期）
- **关键模型**: 每6小时检测1次
- **新接入模型**: 前2周每天检测2次

### 2. 数据保留

- **评分历史**: 永久保留（JSONL格式，占用空间小）
- **测试报告**: 保留最近90天
- **告警日志**: 保留最近30天

### 3. 告警响应

- **Critical**: 立即响应，停用模型
- **High**: 1小时内响应，降低权重
- **Medium**: 当天响应，增加监控
- **Low**: 记录备案

### 4. 质量标准

建议的质量分级标准：

| 评分 | 等级 | 路由策略 |
|------|------|---------|
| 95+ | A+ | 优先路由 |
| 90-94 | A | 正常路由 |
| 85-89 | B+ | 正常路由 |
| 80-84 | B | 降低权重 |
| 70-79 | C | 备用路由 |
| 60-69 | D | 不推荐使用 |
| <60 | F | 停用 |

## 常见问题

### Q1: 如何添加新的监控模型？

编辑 `domains/modelquality/discovery.go`，在 `GetDefaultMonitorModels()` 中添加：

```go
{
    Provider:  "your-provider",
    ModelName: "your-model",
    Alias:     "显示名称",
}
```

### Q2: 历史数据存储在哪里？

默认存储在 `./data/model-quality/` 目录，可通过 `-data-dir` 参数自定义。

### Q3: 如何查看某个模型的所有历史记录？

```bash
# 方式1: 使用history命令
./quality-monitor -mode=history -provider=openai -model=gpt-4

# 方式2: 直接查看JSONL文件
cat data/model-quality/scores/openai_gpt-4.jsonl | jq '.'
```

### Q4: 如何导出历史数据进行分析？

```bash
# 导出为CSV
cat data/model-quality/scores/openai_gpt-4.jsonl | \
  jq -r '[.timestamp, .overall_score, .accuracy, .latency_p95, .grade] | @csv' > openai_gpt-4.csv

# 使用Excel/Python进行进一步分析
```

### Q5: 质量下降后如何处理？

1. 查看历史趋势，确认是偶发还是持续下降
2. 检查供应商是否有模型更新公告
3. 增加监控频率（如每小时一次）
4. 降低该模型的路由权重
5. 准备备用模型
6. 如持续下降，考虑停用

## 下一步

1. 将MockInvoker替换为真实网关调用
2. 从网关配置自动发现模型
3. 集成到监控面板
4. 添加更多告警渠道（钉钉、飞书）
5. 根据质量评分自动调整路由权重
