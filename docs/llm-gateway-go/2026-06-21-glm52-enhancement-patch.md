# GLM-5.2 混合格式防护增强补丁

## 概述

本补丁在现有的三层防护基础上，添加第四层：**早期字符串粗筛**。

## 变更文件

1. **relay/openai_format_detector.go** - 新增检测器（已创建）
2. **relay/openai_format_detector_test.go** - 检测器测试（已创建）
3. **relay/anthropic_to_openai_stream.go** - 集成到流处理（待修改）

## 修改详情

### 文件：relay/anthropic_to_openai_stream.go

**位置**：Line 292 之前（在 `json.Unmarshal` 之前）

**原代码**：
```go
// Line 292
var ev sseAnthropicEvent
if err := json.Unmarshal(data, &ev); err != nil {
    slog.Warn("anthropic_to_openai: malformed event JSON",
        "event_type", eventType, "error", err, "request_id", requestID)
    continue
}
```

**新代码**：
```go
// 2026-06-21 enhancement: Early coarse filter to drop OpenAI-format
// data before JSON parsing. Complements the existing fine-grained
// checks (Line 317 and Line 326) with a fast string-based pre-filter.
// See openai_format_detector.go for the detection logic.
if isOpenAIFormatData(data) {
    slog.Warn("anthropic_to_openai: detected OpenAI-format data, dropping",
        "event_type", eventType,
        "data_preview", truncateForLog(string(data), 100),
        "request_id", requestID)
    continue
}

// Existing code continues
var ev sseAnthropicEvent
if err := json.Unmarshal(data, &ev); err != nil {
    slog.Warn("anthropic_to_openai: malformed event JSON",
        "event_type", eventType, "error", err, "request_id", requestID)
    continue
}
```

## 防护层级

修改后的完整防护机制（4 层）：

```
Layer 0 (NEW): 字符串粗筛
  ↓ 检测 "choices":[  / "created":123 / "object":"chat.completion"
  ↓ 快速丢弃 OpenAI 格式数据
  ↓
Layer 1: JSON 解析
  ↓ 解析为 sseAnthropicEvent
  ↓ 失败则跳过
  ↓
Layer 2: 事件类型白名单
  ↓ 检查 isKnownAnthropicEventType()
  ↓ 非标准事件类型则丢弃
  ↓
Layer 3: OpenAI 格式精细检测
  ↓ 检查 ev.Type == "" && oaiCheck.Choices != nil
  ↓ 确认是 OpenAI 格式则丢弃
  ↓
正常处理
```

## 性能影响

**基准测试结果**：

```bash
go test -bench=BenchmarkIsOpenAIFormatData ./relay
```

**预期性能**：
- 每次检测 < 1μs（字符串匹配非常快）
- 对整体吞吐量影响 < 0.1%
- 早期过滤可能提升性能（避免无效 JSON 解析）

## 测试覆盖

**新增测试**：
- `TestIsOpenAIFormatData` - 14 个测试用例
- `TestTruncateForLog` - 5 个测试用例
- `BenchmarkIsOpenAIFormatData` - 性能基准

**运行测试**：
```bash
# 运行新增测试
go test -v ./relay -run "TestIsOpenAIFormat|TestTruncate"

# 运行基准测试
go test -bench=. ./relay -run=^$ -benchmem

# 运行所有 GLM-5.2 相关测试
go test -v ./relay -run GLM52
```

## 部署步骤

### 1. 应用补丁

```bash
cd __LOCAL_PATH_1__

# 1. 已创建的文件（无需操作）
# - relay/openai_format_detector.go
# - relay/openai_format_detector_test.go

# 2. 修改 anthropic_to_openai_stream.go
# 在 Line 292 之前添加上述代码块
```

### 2. 测试

```bash
# 运行所有测试
go test ./relay -v

# 确认无回归
go test ./... -short
```

### 3. 构建

```bash
# 构建 Linux 版本（用于 71 服务器）
GOOS=linux GOARCH=amd64 go build -o llm-gateway-go-linux ./cmd/gateway
```

### 4. 部署到 71（灰度）

```bash
# 使用现有部署脚本
./scripts/deploy-llm-gateway-71.sh

# 或手动部署
scp llm-gateway-go-linux __SSH_TARGET_2__:/tmp/
ssh __SSH_TARGET_2__ 'systemctl stop llm-gateway-go && \
  mv /tmp/llm-gateway-go-linux /usr/local/bin/llm-gateway-go && \
  systemctl start llm-gateway-go'
```

### 5. 验证

```bash
# 查看日志中是否有新的警告
ssh __SSH_TARGET_2__
docker logs llm-gateway-go -f | grep "detected OpenAI-format data"

# 运行诊断脚本
export GLM_API_KEY="your-key"
./scripts/diagnose-glm52.sh -v
```

### 6. 监控（7 天）

关注以下指标：
- 新警告日志的频率
- 用户是否仍报告"混乱"问题
- 整体错误率是否下降

## 回滚方案

如果出现问题，回滚步骤：

```bash
# 1. 恢复到上一个版本
ssh __SSH_TARGET_2__
git checkout <previous-commit>
go build -o llm-gateway-go ./cmd/gateway
systemctl restart llm-gateway-go

# 2. 或者只移除新增代码
# 删除 anthropic_to_openai_stream.go 中的 Line 292-299
# 保留 openai_format_detector.go（不影响现有逻辑）
```

## 预期效果

**如果上游确实混合格式**：
- ✅ 日志中会出现 "detected OpenAI-format data" 警告
- ✅ 客户端不再收到空 choices 数组
- ✅ 用户不再报告"混乱"问题

**如果上游没有混合格式**：
- ✅ 不会有新的警告日志
- ✅ 功能完全正常
- ✅ 性能无影响

## 后续改进

如果补丁有效，后续可以：

1. **添加 Prometheus metrics**：
   ```go
   var droppedOpenAIFormatTotal = prometheus.NewCounterVec(
       prometheus.CounterOpts{
           Name: "llm_gateway_dropped_openai_format_total",
           Help: "Total OpenAI-format events dropped from Anthropic stream",
       },
       []string{"model"},
   )
   ```

2. **添加结构化日志字段**：
   ```go
   slog.Warn("anthropic_to_openai: detected OpenAI-format data",
       "model", clientModel,
       "has_choices", strings.Contains(string(data), `"choices":`),
       "has_created", strings.Contains(string(data), `"created":`),
       "request_id", requestID)
   ```

3. **创建告警规则**：
   ```yaml
   - alert: HighOpenAIFormatLeakRate
     expr: rate(llm_gateway_dropped_openai_format_total[5m]) > 0.1
     annotations:
       summary: "Upstream leaking OpenAI format at high rate"
   ```

## 验证清单

部署后验证：

- [ ] 运行 `go test ./relay` 全部通过
- [ ] 构建成功（无编译错误）
- [ ] 部署到 71 成功
- [ ] 服务启动正常（healthz 200）
- [ ] 日志中无 panic 或 fatal
- [ ] 运行 `diagnose-glm52.sh` 测试通过
- [ ] 监控 24 小时无异常

## 相关文件

- **检测器**: `relay/openai_format_detector.go`
- **测试**: `relay/openai_format_detector_test.go`
- **集成点**: `relay/anthropic_to_openai_stream.go:292`
- **文档**: `docs/llm-gateway-go/2026-06-21-glm52-enhancement-patch.md` (本文件)

---

**创建日期**: 2026-06-21  
**作者**: AI Assistant  
**审核状态**: 待审核  
**优先级**: P1  
**风险等级**: 低（防御性增强，无破坏性变更）
