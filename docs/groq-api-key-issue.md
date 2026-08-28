# Groq API Key 问题诊断和修复

## 问题描述

在 https://llmgo.kxpms.cn/free-pool 中添加 Groq 账户时报错：
- Base URL: `https://api.groq.com/openai/v1`
- API Key: `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`
- 错误信息：端点不可达或 Key 无效

## 根本原因

通过测试 Groq API 端点发现：

```bash
curl -H "Authorization: Bearer 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" \
  https://api.groq.com/openai/v1/models
```

返回：
```json
{"error":{"message":"Forbidden"}}
```

**结论：提供的 API Key 无效或已过期（403 Forbidden）**

## 代码改进

虽然 API Key 本身无效，但代码在处理 403 响应时可以提供更清晰的错误信息。

### 改进点 1: 读取 403/401 错误详情

**位置**: `admin/free_pool_extra.go:768-820`

**改进前**：
- 只读取 200 响应的 body
- 403/401 响应的错误信息被丢弃

**改进后**：
```go
if status == 200 {
    // ... 解析模型列表
} else {
    // 读取错误详情，便于诊断
    bodyBytes, _ := readLimitedBody(resp.Body, maxProbeErrorBytes)
    errorDetail = string(bodyBytes)
}
```

### 改进点 2: 区分 403 和 401 错误

**位置**: `admin/free_pool_extra.go:969-991`

**改进前**：
```go
if authValid, ok := probeResult["auth_valid"].(bool); ok && !authValid {
    writeJSON(w, http.StatusOK, map[string]any{
        "status": "probe_failed",
        "probe":  probeResult,
        "error":  "API Key 探活未通过，请检查 base_url 与 Key",
    })
    return
}
```

**改进后**：
```go
if authValid, ok := probeResult["auth_valid"].(bool); ok && !authValid {
    statusCode, _ := probeResult["status_code"].(int)
    errMsg := "API Key 探活未通过，请检查 base_url 与 Key"
    if statusCode == 403 {
        errMsg = "API Key 无效或已过期（403 Forbidden），请检查 Key 是否正确"
    } else if statusCode == 401 {
        errMsg = "API Key 认证失败（401 Unauthorized），请检查 Key 格式"
    }
    writeJSON(w, http.StatusOK, map[string]any{
        "status": "probe_failed",
        "probe":  probeResult,
        "error":  errMsg,
    })
    return
}
```

## 解决方案

1. **短期**：需要获取一个有效的 Groq API Key
   - 访问 https://console.groq.com/keys
   - 创建新的 API Key
   - 使用新 Key 重新添加

2. **长期**：代码改进已完成
   - 更清晰的错误信息（区分 403/401）
   - 包含 API 响应的错误详情
   - 便于用户快速定位问题

## 测试验证

可以通过以下命令验证新的 Groq API Key：

```bash
# 替换 YOUR_API_KEY 为实际的 Key
curl -H "Authorization: Bearer YOUR_API_KEY" \
  https://api.groq.com/openai/v1/models
```

成功响应应包含可用模型列表，例如：
```json
{
  "data": [
    {"id": "llama-3.3-70b-versatile", ...},
    {"id": "llama-3.1-8b-instant", ...}
  ]
}
```

## 相关文件

- `admin/free_pool_extra.go`: Free pool 探活逻辑
- `internal/upstreamurl/upstreamurl.go`: URL 候选生成逻辑
