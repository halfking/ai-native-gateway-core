# llm-gateway-go Provider 587 测试验证报告

**日期**: 2026-06-20 23:20  
**Provider**: 587 (apiclaude.cc)  
**测试 Key**: sk-1R7I...KZw7  
**状态**: 🟡 **部分工作** - API 返回正常，但数据库记录不完整

---

## 🧪 测试结果

### 测试 1: 简单请求

**请求**:
```json
{
  "model": "claude-opus-4-8",
  "messages": [{"role": "user", "content": "只回复一个字：好"}],
  "max_tokens": 10
}
```

**响应**: ✅ **成功**
```json
{
  "id": "msg_C9IiHHIXgnqfKOBK7l68UOrm",
  "model": "claude-opus-4-8",
  "choices": [{
    "message": {"content": "好", "role": "assistant"},
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 15,
    "completion_tokens": 1,
    "total_tokens": 16
  }
}
```

### 测试 2: 稍长请求

**请求**:
```json
{
  "model": "claude-opus-4-8",
  "messages": [{
    "role": "user",
    "content": "请用简短的一句话解释：为什么天空是蓝色的？"
  }],
  "max_tokens": 100
}
```

**响应**: ✅ **成功**
```json
{
  "id": "msg_OLbwgFwipvDVf0r8pEyiXixI",
  "content": "阳光中的蓝色光波被大气分子散射得比其他颜色更强，所以我们看到天空呈现蓝色。",
  "usage": {
    "prompt_tokens": 29,
    "completion_tokens": 40,
    "total_tokens": 69
  }
}
```

**Reasoning Content**: ❌ 无（模型未返回 thinking blocks）

---

## 🔍 关键发现

### 发现 1: API 层面工作正常 ✅

**证据**:
- 两个测试请求都成功返回了内容
- 返回的内容准确且完整
- Token 计数正确
- Finish reason 为 "stop"（正常结束）

### 发现 2: 数据库记录不完整 ❌

**查询结果**:
```sql
SELECT 
  request_id,
  success,
  completion_tokens,
  response_body IS NOT NULL as has_response
FROM request_logs 
WHERE client_model = 'claude-opus-4-8'
  AND ts > now() - interval '10 minutes';
```

**结果**:
```
request_id: fdec36bc44fad374...
success: true
completion_tokens: 0  ❌
response_body: NULL  ❌
```

### 发现 3: 测试请求未找到 ❌

**我们的测试请求 ID**:
- `C9IiHHIXgnqfKOBK7l68UOrm` (测试 1)
- `OLbwgFwipvDVf0r8pEyiXixI` (测试 2)

**数据库搜索**: 0 rows

**说明**: 这两个请求可能：
1. 还没有写入数据库（延迟）
2. 使用了不同的 request_id 格式
3. 被某种策略跳过了记录

### 发现 4: 日志显示 outbound 为空 ❌

**日志**:
```json
{
  "msg": "audit: request completed",
  "request_id": "fdec36bc44fad374...",
  "model": "claude-opus-4-8",
  "outbound": "",  ❌
  "provider": 587,
  "credential": 17,
  "success": true
}
```

**说明**: 即使 API 返回了内容，日志中的 `outbound` 仍然是空字符串。

---

## 🎯 根本原因分析

### 原因 1: 响应体保存策略 (最可能 🔴)

可能有某种配置或代码逻辑导致：
- API 正常返回给客户端
- 但响应体不保存到数据库
- 只保存元数据（success, latency_ms 等）

**证据**:
- `response_body IS NULL`
- 但 `success = true` 且有 `latency_ms`
- `attempt_logged: true` 表示日志记录函数被调用了

### 原因 2: 异步写入或批处理

可能的写入策略：
- 响应立即返回给客户端
- 日志异步写入数据库
- 写入过程中响应体被丢弃或跳过

### 原因 3: 内存或性能优化

可能为了节省存储：
- 只保存失败请求的详细信息
- 成功请求只保存统计数据
- 响应体可选或有大小限制

### 原因 4: 代码路径问题

可能在代码中：
```go
// 伪代码
func logRequest(req, resp) {
    log := RequestLog{
        RequestID: req.ID,
        Success: true,
        LatencyMS: latency,
        // 响应体可能在某些条件下不保存
        ResponseBody: nil,  // ← 这里可能有问题
    }
    db.Insert(log)
}
```

---

## 🔬 进一步诊断步骤

### 步骤 1: 检查源代码中的日志写入逻辑

```bash
cd services/llm-gateway-go

# 搜索 response_body 写入逻辑
grep -rn "response_body" relay/ db/ --include="*.go" | grep -i "insert\|save\|write"

# 搜索 audit 日志函数
grep -rn "audit: request completed" relay/ --include="*.go" -A 10 -B 10
```

### 步骤 2: 检查环境变量配置

```bash
# 在 184 上检查
kubectl -n pms-test get deployment llm-gateway-go-deployment -o yaml | \
  grep -E "SAVE_RESPONSE|RESPONSE_BODY|LOG_BODY" -i

# 检查 secret
kubectl -n pms-test get secret llm-gateway-secret -o yaml
```

### 步骤 3: 启用详细日志

```bash
# 临时设置日志级别为 debug
kubectl -n pms-test set env deployment/llm-gateway-go-deployment \
  LLM_GATEWAY_LOG_LEVEL=debug

# 重新测试并观察日志
kubectl -n pms-test logs -f deployment/llm-gateway-go-deployment
```

### 步骤 4: 直接测试数据库写入

创建一个简单的测试脚本：
```go
// test-db-write.go
package main

import (
    "context"
    "database/sql"
    _ "github.com/lib/pq"
)

func main() {
    db, _ := sql.Open("postgres", "...")
    _, err := db.Exec(`
        INSERT INTO request_logs (
            request_id, success, completion_tokens, response_body
        ) VALUES (
            'test-123', true, 10, '{"test": "data"}'::jsonb
        )
    `)
    if err != nil {
        panic(err)
    }
    println("Write successful")
}
```

---

## ✅ 临时解决方案

### 方案 1: 确认功能性

虽然数据库记录不完整，但：
- ✅ API 正常工作
- ✅ 用户可以正常使用
- ✅ Provider 587 可以继续使用

**结论**: 可以继续使用，但需要修复日志记录问题

### 方案 2: 使用其他观测方式

如果需要追踪请求：
- 使用客户端日志
- 使用 API 响应中的 usage 信息
- 使用其他监控工具

### 方案 3: 修复代码

找到并修复响应体不保存的根本原因

---

## 📊 对比总结

| 方面 | 状态 | 说明 |
|------|------|------|
| **API 响应** | ✅ 正常 | 内容完整，token 计数正确 |
| **Provider 587** | ✅ 工作 | 第三方中转服务可用 |
| **Credential 17** | ✅ 有效 | 认证成功，请求通过 |
| **数据库记录** | ❌ 不完整 | success=true 但 response_body=NULL |
| **日志写入** | ⚠️ 部分 | 元数据写入，响应体缺失 |
| **可观测性** | ❌ 受影响 | 无法追溯用户看到的内容 |

---

## 🎯 推荐行动

### 立即 (P0)

1. ✅ **功能验证完成** - Provider 587 可以正常使用
2. ⏳ **通知用户** - API 工作正常，但日志记录不完整
3. ⏳ **开始修复** - 定位响应体不保存的代码位置

### 今天 (P1)

1. 检查源代码中的日志写入逻辑
2. 检查是否有环境变量控制响应体保存
3. 修复代码并重新部署

### 本周 (P2)

1. 添加监控和告警
2. 端到端测试
3. 回填历史数据（如果可能）

---

## 🔴 关键结论

**Provider 587 (apiclaude.cc) 现在可以正常工作**，但存在日志记录不完整的问题：

✅ **可以继续使用**:
- API 返回正常
- 用户体验不受影响
- Token 计数准确

❌ **需要修复**:
- response_body 不保存到数据库
- 影响可观测性和调试能力
- 无法追溯用户实际看到的内容

---

**报告人**: AI Assistant  
**报告时间**: 2026-06-20 23:20  
**状态**: Provider 587 功能正常，日志记录需要修复

