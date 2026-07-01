# GLM-5.2 修复 - 根因重新分析

> **日期**: 2026-06-22 00:35  
> **状态**: ❌ 第一次修复无效  
> **原因**: 路由路径判断错误

---

## 🔍 关键发现

### 实际 SSE 流格式

捕获的原始流：
```
data: {"object":"chat.completion.chunk","created":1782059311,"model":"glm-5.2","choices":[...]}
data: {"choices":[...],"created":1782059311,"id":"...","model":"glm-5.2","usage":null}
data: {"choices":[],"created":1782059311,"id":"...","model":"glm-5.2","usage":{...}}  ← 问题块
data: [DONE]
```

**结论**: 上游返回的是 **OpenAI 格式**，不是 Anthropic 格式！

### 路径错误

**我们的假设（错误）**:
- glm-5.2 走 Q3 路径（OpenAI client → Anthropic upstream）
- 在 `anthropic_to_openai_stream.go` 中添加过滤

**实际情况**:
- glm-5.2 走 Q1 路径（OpenAI client → OpenAI upstream）
- 或者上游本身就是 OpenAI 协议
- **不经过 anthropic_to_openai_stream.go**

### 为什么代码没生效

```
客户端 (OpenAI 格式)
    ↓
Gateway 判断路由
    ↓
Q1 路径: OpenAI → OpenAI (直通或简单转换)
    ↓
上游 glm-5.2 (返回 OpenAI 格式)
    ↓
Gateway (不经过 anthropic_to_openai_stream.go)
    ↓
客户端收到空 choices
```

---

## ✅ 正确的修复位置

### 应该在哪里过滤

需要在 **OpenAI 流式响应处理** 中过滤，可能的位置：

1. **`relay/openai_stream.go`** - OpenAI 格式的流处理
2. **`routing/executor_*.go`** - 执行器层面
3. **通用流处理层** - 在所有流式响应的公共路径

### 查找正确文件

```bash
# 查找 OpenAI 流式响应处理
grep -r "chat.completion.chunk" services/llm-gateway-go/relay/
grep -r "StreamReader" services/llm-gateway-go/relay/
grep -r "data: \[DONE\]" services/llm-gateway-go/relay/
```

---

## 🎯 修复方案 V2

### 方案 A: 在 OpenAI 流处理中过滤

**位置**: `relay/openai_stream.go` 或类似文件

**逻辑**:
```go
// 在写入客户端前检查
if isOpenAIFormatData(data) {
    // 检查是否空 choices
    if hasEmptyChoices(data) {
        slog.Warn("dropping empty choices block", ...)
        continue  // 跳过这个块
    }
}
```

### 方案 B: 在执行器层面过滤

**位置**: `routing/executor_chat.go` 或 `routing/executor_openai.go`

**优点**: 
- 覆盖所有 OpenAI 格式的响应
- 不影响其他路径

### 方案 C: 添加通用后处理

**位置**: 流式响应的公共写入点

**优点**: 
- 一次修复，覆盖所有路径
- 最安全

---

## 📊 验证步骤

### 确认路由路径

1. 查看日志中的 provider 信息
2. 检查 `glm-5.2` 模型配置
3. 确认使用的执行器类型

### 找到正确的代码位置

```bash
# 在 relay/ 中搜索 OpenAI 流处理
cd services/llm-gateway-go
find relay -name "*.go" -exec grep -l "chat.completion.chunk" {} \;
find relay -name "*.go" -exec grep -l "StreamReader" {} \;
find routing -name "*.go" -exec grep -l "stream" {} \;
```

---

## 💡 教训

1. **先验证路径，再写代码**
   - 应该先捕获原始流，确认格式
   - 再决定在哪里添加过滤

2. **测试驱动开发**
   - 部署前应该有集成测试
   - 验证代码确实被执行

3. **代码注释可能过时**
   - 代码注释提到 "anthropic-compatible upstreams"
   - 但实际上 glm-5.2 不走 Anthropic 协议

---

## 🚀 下一步

1. **回滚当前修复**（可选，不影响功能）
2. **找到正确的 OpenAI 流处理代码**
3. **在正确位置添加过滤**
4. **测试验证**

---

**创建时间**: 2026-06-22 00:35  
**状态**: 需要重新定位修复位置  
**关键发现**: glm-5.2 走 OpenAI 路径，不走 Anthropic 路径
