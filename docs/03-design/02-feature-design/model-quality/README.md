# LLM 模型质量监控系统

## 概述

自动化的LLM模型质量监控系统，用于定期检测供应商模型的智商水平，防止模型"渗水"（性能下降）。

## 功能特性

### 1. 基准测试
- **MMLU Lite**: 50题快速测试（5个学科，预计2-5分钟）
- **MMLU Full**: 完整测试（可扩展更多题目）
- 支持自定义测试套件

### 2. 质量评分
- **准确率**: 测试题目正确率
- **稳定性**: 请求成功率
- **延迟**: P95响应延迟
- **综合评分**: 加权计算（准确率60% + 稳定性30% + 延迟10%）
- **等级评定**: A+/A/B+/B/C/D/F

### 3. 监控模式
- **定时监控**: 周期性自动检测（如每天/每周）
- **异常触发**: 检测到异常时立即触发质量检查
- **手动触发**: 支持按需手动检测

### 4. 告警机制
- 质量下降自动告警
- 准确率下降阈值可配置
- 支持多种告警方式（控制台/日志/外部系统）

## 快速开始

### 安装依赖

```bash
cd llm-gateway-go
go mod tidy
```

### 编译

```bash
# 编译质量监控工具
go build -o quality-monitor ./cmd/quality-monitor

# 编译网关（集成后台worker）
go build -o gateway ./cmd/gateway
```

### 单次测试

```bash
# 测试OpenAI GPT-4
./quality-monitor -mode=test -provider=openai -model=gpt-4 -benchmark=lite

# 完整测试
./quality-monitor -mode=test -provider=openai -model=gpt-4 -benchmark=full
```

### 模型目录平均智商

```bash
# 汇总所有已测评分，按模型打印平均智商 / 标准差 / 各供应商均分
./quality-monitor -mode=catalog-iq
```

### 单凭据节点智商

```bash
# 汇总所有带 CredentialID 的评分，按 (供应商, 凭据节点) 打印平均智商
./quality-monitor -mode=node-iq
```

### 直连单个凭据节点测试

直连节点（绕过网关）测量该节点的模型智商。两种取节点方式：

```bash
# 方式A：直接指定节点（无需 DB）
./quality-monitor -mode=node-test \
  -node-base-url=https://api.openai.com \
  -node-api-key=sk-xxx \
  -raw-model=gpt-4o \
  -credential-id=42

# 方式B：从 DB 自动发现并解密（需 -dsn + -fernet-key）
./quality-monitor -mode=node-test \
  -dsn="postgres://..." -fernet-key=<hex> \
  -credential-id=42 [-raw-model=...]
```

> ⚠️ 直连测试会产生真实 token 费用。`node-test` 把 `CredentialID` 写入评分，
> 供 `node-iq` 聚合区分同一供应商下不同 key 的智商差异（发现"渗水"的具体节点）。

### 持续监控

```bash
# 启动持续监控（每24小时检测一次）
./quality-monitor -mode=monitor -interval=24h

# 自定义监控间隔（每6小时）
./quality-monitor -mode=monitor -interval=6h
```

### 模拟质量下降

```bash
# 演示质量下降检测和告警
./quality-monitor -mode=simulate-drop
```

## 架构设计

```
domains/modelquality/
├── benchmark.go        # 核心数据结构和评分算法
├── mmlu_data.go       # MMLU测试题库
├── executor.go        # 测试执行器
├── monitor.go         # 质量监控器
├── storage.go         # 数据存储层
└── invoker.go         # 模型调用适配器

bg/
└── model_quality_worker.go  # 后台worker集成

cmd/quality-monitor/
└── main.go            # 命令行工具
```

## 配置说明

### MonitorConfig 结构

```go
type MonitorConfig struct {
    // 定时检测
    EnableScheduled     bool          // 启用定时检测
    ScheduleInterval    time.Duration // 检测间隔
    UseLiteBenchmark    bool          // 使用精简测试
    
    // 异常触发
    EnableAnomalyTrigger bool    // 启用异常触发
    ErrorRateThreshold   float64 // 错误率阈值
    LatencyThreshold     int64   // 延迟阈值(ms)
    
    // 目标模型
    TargetModels []ModelTarget // 监控的模型列表
    
    // 告警
    AlertOnQualityDrop   bool    // 质量下降告警
    QualityDropThreshold float64 // 下降阈值(%)
}
```

### 监控目标配置

```go
TargetModels: []ModelTarget{
    {
        Provider:  "openai",
        ModelName: "gpt-4",
        Alias:     "OpenAI GPT-4",
    },
    {
        Provider:  "anthropic",
        ModelName: "claude-3-opus",
        Alias:     "Claude 3 Opus",
    },
    // ... 更多模型
}
```

## 使用示例

### 作为后台Worker集成

```go
// 在网关启动时
worker := bg.NewModelQualityWorker(dataDir)
if err := worker.Start(ctx, dataDir); err != nil {
    log.Fatal(err)
}
defer worker.Stop()

// 异常时触发检测
if errorRate > threshold {
    worker.TriggerCheck(ctx, "openai", "gpt-4", "high_error_rate")
}

// 获取当前评分
scores := worker.GetCurrentScores()
for key, score := range scores {
    fmt.Printf("%s: %.2f (%s)\n", key, score.OverallScore, score.Grade)
}
```

### 自定义模型调用器

```go
// 实现 ModelInvoker 接口
type MyInvoker struct {
    // 你的依赖
}

func (m *MyInvoker) InvokeModel(ctx context.Context, provider string, modelName string, prompt string) (response string, tokenUsage int, latency time.Duration, err error) {
    // 调用你的模型API
    // 返回答案、token使用量、延迟
}

// 使用自定义调用器
executor := modelquality.NewBenchmarkExecutor(myInvoker, 30*time.Second)
```

## 数据存储

所有数据存储在 `data/model-quality/` 目录：

```
data/model-quality/
├── reports/           # 测试报告
│   └── openai_gpt-4_mmlu_lite_20260806_143022.json
├── scores/            # 评分历史
│   └── openai_gpt-4.jsonl
└── alerts.log         # 告警日志
```

## 评分标准

### "智商"是什么

本文档中"智商 / IQ"指 `QualityScore.OverallScore`（综合智商分，0-100）。
该系统提供**两个聚合维度**查看模型智商：

| 维度 | CLI 模式 | 含义 |
|------|---------|------|
| **模型目录平均智商** | `catalog-iq` | 同一 canonical 模型跨所有提供它的凭据节点的平均智商（横向对比不同模型的聪明程度） |
| **单凭据节点智商** | `node-iq` | 按 `(供应商, 凭据节点ID)` 聚合该节点下所有模型的智商（纵向定位"渗水"的具体 key） |

> 经网关测试（`mode=test`，网关负载均衡选节点）产出的评分 `CredentialID=0`，只参与
> `catalog-iq` 聚合，不参与 `node-iq`（无节点维度）。要得到单节点智商，必须用
> `mode=node-test` 直连指定节点。

### 综合评分计算

```
综合评分 = 准确率 × 0.6 + 稳定性 × 0.3 + 延迟评分 × 0.1
```

### 延迟评分

- < 1000ms: 100分
- 1000-5000ms: 线性递减
- > 5000ms: 0分

### 等级划分

| 评分范围 | 等级 | 说明 |
|---------|------|------|
| 95-100  | A+   | 优秀 |
| 90-94   | A    | 良好 |
| 85-89   | B+   | 中上 |
| 80-84   | B    | 中等 |
| 70-79   | C    | 及格 |
| 60-69   | D    | 较差 |
| < 60    | F    | 不合格 |

## 告警示例

当检测到质量下降时，系统会自动发送告警：

```
========================================
🚨 ALERT [warning] - 2026-08-06 14:30:15
========================================
Title: LLM模型质量下降告警
----------------------------------------
模型 智谱 ChatGLM-4 质量下降检测!
准确率: 78.00% -> 63.00% (下降 15.00%)
综合评分: 76.50 (C) -> 62.30 (D)
建议: 检查供应商模型是否更新或降级
========================================
```

## 最佳实践

### 1. 定时检测策略

- **生产环境**: 每天检测一次（使用Lite版本）
- **关键业务**: 每6小时检测一次
- **开发环境**: 按需手动检测

### 2. 异常触发策略

在以下场景触发质量检查：
- 错误率突增（如从5%升至30%）
- 平均延迟突增（如从1s升至5s）
- 发现死循环或异常响应
- 用户投诉增加

### 3. 告警阈值设置

```go
QualityDropThreshold: 5.0   // 准确率下降5%告警
ErrorRateThreshold:   0.3   // 错误率超30%触发检测
LatencyThreshold:     5000  // 延迟超5000ms触发检测
```

### 4. 存储管理

- 定期归档历史报告（保留最近30天）
- 评分历史使用JSONL格式（便于追加和分析）
- 告警日志定期轮转

## 扩展开发

### 添加新的测试题目

编辑 `domains/modelquality/mmlu_data.go`：

```go
{
    ID:       "new_001",
    Subject:  "new_subject",
    Question: "Your question here?",
    Options:  []string{"A", "B", "C", "D"},
    Answer:   "B",
}
```

### 添加新的告警渠道

实现 `Alerter` 接口：

```go
type EmailAlerter struct {
    smtpConfig SMTPConfig
}

func (a *EmailAlerter) Alert(ctx context.Context, level string, title string, message string) error {
    // 发送邮件
}
```

### 集成数据库存储

实现 `MonitorStorage` 接口：

```go
type PostgresStorage struct {
    db *sql.DB
}

func (s *PostgresStorage) SaveReport(ctx context.Context, report *BenchmarkReport) error {
    // 存储到PostgreSQL
}
```

## 性能优化

### 并发测试

修改 `executor.go` 支持并发执行：

```go
// 使用worker pool并发测试
var wg sync.WaitGroup
results := make(chan *TestResult, len(suite.Questions))

for _, q := range suite.Questions {
    wg.Add(1)
    go func(question Question) {
        defer wg.Done()
        result, _ := e.ExecuteQuestion(ctx, modelName, provider, question)
        results <- result
    }(q)
}

wg.Wait()
close(results)
```

### 缓存题库

将题库加载到内存或Redis，避免重复解析。

## 故障排查

### 1. 测试失败

```bash
# 检查模型调用器配置
# 查看详细日志
./quality-monitor -mode=test -provider=openai -model=gpt-4 -benchmark=lite
```

### 2. 监控未启动

```bash
# 检查配置
# 查看日志文件
tail -f data/model-quality/alerts.log
```

### 3. 告警未触发

- 检查 `AlertOnQualityDrop` 是否启用
- 检查 `QualityDropThreshold` 阈值设置
- 确认有历史评分数据用于对比

## TODO

- [x] 实现真实的网关模型调用器（GatewayModelInvoker）
- [x] 支持HTTP直连调用（DirectNodeInvoker，2026-08-10）
- [x] 单凭据节点智商维度（CredentialID + node-test + node-iq，2026-08-10）
- [x] 模型目录平均智商聚合（catalog-iq，2026-08-10）
- [x] 修复 MockModelInvoker 假准确率（2026-08-10）
- [ ] 数据库存储实现（PostgreSQL）
- [ ] 更多告警渠道（Email、钉钉、飞书）
- [ ] Web界面展示历史趋势
- [ ] 导出测试报告（PDF/Excel）
- [ ] 更多基准测试（GSM8K、HumanEval等）
- [ ] 多语言支持（中文题库）
- [ ] A/B测试功能（对比不同供应商）

## 许可证

Apache License 2.0
