# Groq API Key 问题诊断和修复

## 问题描述

在 https://llmgo.kxpms.cn/free-pool 中添加 Groq 账户时报错：
- Base URL: `https://api.groq.com/openai/v1`
- API Key: `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`
- 错误信息：端点不可达或 Key 无效

## 根本原因

有两种可能的原因：

### 1. API Key 无效或已过期

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

### 2. 网络环境限制（GFW）

如果云主机部署在国内环境，可能无法访问 Groq API（`api.groq.com`），导致探活失败。即使 API Key 有效，也会因为网络不可达而报错。

## 解决方案

### 方案 1: 获取有效的 API Key（推荐）

1. 访问 https://console.groq.com/keys
2. 创建新的 API Key
3. 使用新 Key 重新添加

验证新 Key 的方法：
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

### 方案 2: 强制跳过探活（用于 GFW 环境）

**新增功能**：如果确认 API Key 有效，但因网络环境（如 GFW）无法探活，可以使用 `force_skip_probe` 参数强制保存。

#### API 使用方法

在调用 `/api/free-pool/quick-entry` 时，添加 `force_skip_probe: true`：

```json
{
  "platform_id": "groq",
  "base_url": "https://api.groq.com/openai/v1",
  "api_key": "gsk_...",
  "probe_first": true,
  "save": true,
  "force_skip_probe": true
}
```

#### UI 使用方法

在前端添加"强制跳过探活"复选框，当探活失败时：
1. 显示错误信息，提示可能是网络环境问题
2. 提供"强制跳过探活"选项
3. 勾选后重新提交，设置 `force_skip_probe: true`

#### 工作原理

- 当 `force_skip_probe: false`（默认）：探活失败则拒绝保存
- 当 `force_skip_probe: true`：跳过探活检查，直接保存配置
- 探活结果仍会记录到 `probe` 字段，便于后续诊断

#### 注意事项

⚠️ 使用 `force_skip_probe` 时需要确保：
1. Base URL 正确
2. API Key 有效
3. Models 列表正确（手动指定或使用平台默认）

如果配置错误，该凭据将无法正常工作，需要手动修正或删除。

## 代码改进

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

**位置**: `admin/free_pool_extra.go:969-998`

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
if !req.ForceSkipProbe {
    if authValid, ok := probeResult["auth_valid"].(bool); ok && !authValid {
        statusCode, _ := probeResult["status_code"].(int)
        errMsg := "API Key 探活未通过，请检查 base_url 与 Key"
        if statusCode == 403 {
            errMsg = "API Key 无效或已过期（403 Forbidden），请检查 Key 是否正确。如果确认 Key 有效但因网络环境无法探活（如 GFW），可勾选\"强制跳过探活\"直接保存"
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
}
```

### 改进点 3: 新增 force_skip_probe 参数

**位置**: `admin/free_pool_extra.go:920-936`

新增请求参数：
```go
var req struct {
    // ... 其他字段
    ForceSkipProbe bool `json:"force_skip_probe"` // 跳过探活检查，强制保存（用于 GFW 环境）
}
```

当 `ForceSkipProbe: true` 时，探活失败不会阻止保存，配置会直接入库。

## 使用示例

### 正常流程（探活成功）

```bash
curl -X POST https://llmgo.kxpms.cn/api/free-pool/quick-entry \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <admin_token>" \
  -d '{
    "platform_id": "groq",
    "api_key": "gsk_valid_key",
    "probe_first": true,
    "save": true
  }'
```

### GFW 环境（强制跳过探活）

```bash
curl -X POST https://llmgo.kxpms.cn/api/free-pool/quick-entry \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <admin_token>" \
  -d '{
    "platform_id": "groq",
    "base_url": "https://api.groq.com/openai/v1",
    "api_key": "gsk_your_key",
    "models": ["llama-3.3-70b-versatile", "llama-3.1-8b-instant"],
    "probe_first": true,
    "save": true,
    "force_skip_probe": true
  }'
```

## 相关文件

- `admin/free_pool_extra.go`: Free pool 探活逻辑
- `internal/upstreamurl/upstreamurl.go`: URL 候选生成逻辑
