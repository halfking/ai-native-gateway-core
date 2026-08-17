# 模型质量监控 - 真实网关调用配置指南

## 概述

模型质量监控系统已集成到gateway主程序，支持通过真实的网关API进行MMLU质量测试。

## 启用方式

### 环境变量配置

模型质量监控通过以下环境变量控制：

```bash
# 1. 启用模型质量监控 (必须)
export LLM_GATEWAY_MODEL_QUALITY_ENABLED=true

# 2. 数据存储目录 (可选，默认 ./data)
export LLM_GATEWAY_MODEL_QUALITY_DATA_DIR=/path/to/data

# 3. 网关基础URL (可选，默认 http://localhost:8787)
export LLM_GATEWAY_MODEL_QUALITY_BASE_URL=http://localhost:8787

# 4. 系统API Key (从系统自动获取)
# 自动使用 selfCheckAPIKey，无需单独配置
```

### 完整启动示例

```bash
# 设置环境变量
export LLM_GATEWAY_MODEL_QUALITY_ENABLED=true
export LLM_GATEWAY_MODEL_QUALITY_DATA_DIR=./data/model-quality

# 启动gateway (worker会自动启动)
./gateway
```

## 工作原理

### 1. Worker启动流程

```
Gateway启动
  └─> 检查 LLM_GATEWAY_MODEL_QUALITY_ENABLED
       └─> true: 启动 ModelQualityWorker
            ├─> 创建 GatewayModelInvoker (使用系统API key)
            ├─> 初始化存储 (./data/model-quality/)
            ├─> 启动定时监控 (默认24小时)
            └─> 记录日志: "model_quality_worker started"
       └─> false: 跳过
```

### 2. 调用链路

```
ModelQualityWorker
  └─> GatewayModelInvoker
       └─> HTTP POST /v1/chat/completions
            ├─> Header: Authorization: Bearer {systemAPIKey}
            ├─> Header: X-Gateway-Quality-Test: true
            └─> Body: { model, messages, max_tokens, ... }
```

### 3. 真实vs模拟调用

```go
// bg/model_quality_worker.go 中的逻辑
if w.apiKey != "" {
    // 有API key，使用真实网关调用
    invoker = modelquality.NewGatewayModelInvoker(w.baseURL, w.apiKey)
} else {
    // 没有API key，使用Mock调用器（测试用）
    invoker = modelquality.NewMockModelInvoker()
}
```

## 数据存储

### 目录结构

```
./data/model-quality/
├── reports/                          # 测试报告
│   ├── openai_gpt-4_mmlu_lite_20260806_020000.json
│   ├── zhipu_glm-4_mmlu_lite_20260806_020130.json
│   └── ...
├── scores/                           # 评分历史
│   ├── openai_gpt-4.jsonl
│   ├── zhipu_glm-4.jsonl
│   └── ...
└── alerts.log                        # 告警日志
```

### 评分历史格式

每个模型一个JSONL文件，每行一条评分记录：

```jsonl
{"model_name":"gpt-4","provider":"openai","accuracy":92.0,"latency_p95":1200,"stability":98,"overall_score":91.8,"grade":"A","timestamp":"2026-08-06T02:00:00+08:00","benchmark_id":"..."}
{"model_name":"gpt-4","provider":"openai","accuracy":90.0,"latency_p95":1350,"stability":96,"overall_score":89.4,"grade":"A","timestamp":"2026-08-07T02:00:00+08:00","benchmark_id":"..."}
```

## 监控的模型

系统默认监控12个特色模型：

**国际模型** (5个):
1. OpenAI GPT-4
2. OpenAI GPT-4 Turbo
3. OpenAI GPT-3.5 Turbo
4. Anthropic Claude 3 Opus
5. Anthropic Claude 3 Sonnet

**国产特色模型** (7个):
6. 智谱 GLM-4
7. 智谱 GLM-4 Plus
8. 通义千问 Plus
9. 通义千问 Turbo
10. 文心一言 4.0
11. 月之暗面 Kimi
12. 字节豆包 Pro

## 监控周期

默认每24小时对每个模型进行一次MMLU Lite测试（50题）：

- **测试耗时**: 约1-2分钟/模型
- **Token消耗**: 约3000-5000 tokens/模型
- **总耗时**: 约12-24分钟（12个模型）

## 日志输出

### 启动日志

```
INFO CHECKPOINT: system_health_worker started
INFO CHECKPOINT: model_quality_worker started data_dir=./data/model-quality base_url=http://localhost:8787
```

### 测试日志

```
INFO model quality worker: using real gateway invoker base_url=http://localhost:8787
INFO model quality worker started models=12 interval=24h0m0s storage=./data/model-quality
```

### 告警日志

当检测到质量下降时：

```json
{
  "time": "2026-08-06T10:30:00+08:00",
  "level": "WARN",
  "provider": "aliyun",
  "model": "qwen-plus",
  "alert_type": "quality_drop",
  "current_score": 75.2,
  "previous_score": 86.5,
  "change": -11.3,
  "severity": "high"
}
```

## 查看结果

### 1. 使用命令行工具

```bash
# 查看历史
./quality-monitor -mode=history -provider=openai -model=gpt-4 \
  -data-dir=./data/model-quality

# 检测变化
./quality-monitor -mode=changes -data-dir=./data/model-quality
```

### 2. 直接查看文件

```bash
# 查看评分历史
cat ./data/model-quality/scores/openai_gpt-4.jsonl | jq '.'

# 查看最新报告
ls -t ./data/model-quality/reports/ | head -1 | xargs cat | jq '.'

# 查看告警
cat ./data/model-quality/alerts.log | jq '.'
```

## 性能影响

### Token消耗

- 每次完整测试（12个模型）: ~40,000-60,000 tokens
- 每天一次: ~1.2M-1.8M tokens/月
- 建议使用专用测试账户

### 系统负载

- CPU: 测试期间略有增加（HTTP请求+JSON解析）
- 内存: < 100MB
- 网络: 每次测试约5-10MB数据
- 磁盘: 每次测试约15-20KB存储

## 常见问题

### Q1: 如何验证worker是否启动？

查看日志：
```bash
grep "model_quality_worker" /path/to/gateway.log
```

### Q2: 如何临时禁用？

```bash
# 重启gateway时不设置环境变量
unset LLM_GATEWAY_MODEL_QUALITY_ENABLED
./gateway
```

### Q3: 如何修改测试周期？

目前硬编码为24小时，未来会支持配置。

### Q4: 测试会影响生产流量吗？

不会。测试请求：
- 使用专用的系统API key
- 标记了 `X-Gateway-Quality-Test: true`
- 独立的监控和统计
- 不计入生产指标

### Q5: 如何添加自定义模型？

编辑 `domains/modelquality/discovery.go` 中的 `GetDefaultMonitorModels()` 函数。

## 故障排查

### 问题1: Worker未启动

**症状**: 日志中看不到 "model_quality_worker started"

**排查**:
1. 检查环境变量: `echo $LLM_GATEWAY_MODEL_QUALITY_ENABLED`
2. 检查系统API key是否可用
3. 查看错误日志

### 问题2: 测试失败

**症状**: 报告中显示大量错误

**排查**:
1. 检查网关URL是否正确
2. 检查API key权限
3. 检查模型名称是否正确
4. 查看 `./data/model-quality/alerts.log`

### 问题3: 历史数据丢失

**症状**: JSONL文件不完整

**排查**:
1. 检查磁盘空间
2. 检查目录权限
3. 检查是否有多个gateway实例写同一目录

## 下一步

1. **监控集成**: 将评分数据导出到Prometheus/Grafana
2. **路由优化**: 根据质量评分动态调整路由权重
3. **告警集成**: 接入钉钉/飞书告警
4. **可视化**: 在Web面板展示历史趋势

## 相关文档

- 完整文档: `docs/model-quality/README.md`
- 快速开始: `docs/model-quality/QUICKSTART.md`
- 特色模型指南: `docs/model-quality/FEATURED_MODELS_GUIDE.md`
- 命令行工具: `cmd/quality-monitor/`
