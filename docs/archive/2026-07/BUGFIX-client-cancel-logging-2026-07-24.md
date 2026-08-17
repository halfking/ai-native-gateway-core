# Bug Fix: 客户端取消/超时请求日志记录不完整

## 问题描述

通过 VSCode 的 Copilot 连接 llm.kxpms.cn 时，发现 Copilot 一问后就停止了。

查看请求日志发现：
- 可能的请求ID：`5eae6ee96b833027b04c9cef9e64d012`
- 错误的探测请求：`probe-client_cancel-cred21-1784899122602079383`
- **问题**：这个请求只有空的 JSON，没有任何详细信息（请求体、模型参数等）

## 根本原因

在 `domains/streaming/handler.go` 的 `buildClientDisconnectProbeEntry` 函数中，当客户端取消或超时时，系统会生成一个探测（probe）记录，但这个记录**只包含最基本的元数据**：

- RequestID
- TenantID  
- ClientModel
- CredentialID
- ErrorKind
- FailureStage

**缺失的关键信息**：
- ❌ RequestBody（请求体）
- ❌ ModelParams（模型参数）
- ❌ APIKeyID（API密钥ID）
- ❌ EndUserID（终端用户）
- ❌ LatencyMs（延迟时间）
- ❌ RequestPreview（请求预览）

这导致运维人员无法分析：
1. 为什么客户端会取消请求？
2. 请求是否太大导致超时？
3. 使用了什么参数？
4. 是哪个用户的请求？

## 修复方案

### 1. 增强 `buildClientDisconnectProbeEntry` 函数

**文件**: `domains/streaming/handler.go`

**修改内容**:
- 记录完整的 `RequestBody`（限制 64KB，避免数据库溢出）
- 提取关键参数到 `RequestPreview` 用于快速查看
- 记录 `APIKeyID`、`EndUserID`、`LatencyMs` 等关键字段
- 添加新函数 `buildRequestPreview` 提取关键模型参数和统计信息

**关键改进**:
```go
// 记录请求体
if len(logCtx.Body) > 0 {
    maxSize := 64 * 1024 // 64KB
    bodyText := string(logCtx.Body)
    if len(bodyText) > maxSize {
        bodyText = bodyText[:maxSize] + "...[truncated]"
    }
    requestBody = &bodyText
    
    // 提取关键参数到 request_preview
    var reqBodyParsed map[string]any
    if err := json.Unmarshal(logCtx.Body, &reqBodyParsed); err == nil {
        preview := buildRequestPreview(reqBodyParsed)
        if preview != "" {
            requestPreview = &preview
        }
    }
}
```

### 2. 新增 `buildRequestPreview` 函数

提取关键信息用于快速诊断：
- 模型参数：temperature, max_tokens, stream, tool_choice 等
- 统计信息：message_count, total_content_length, tool_count
- 帮助快速判断请求是否因为太大而超时

### 3. 更新测试用例

**文件**: `domains/streaming/handler_disconnect_probe_test.go`

增强 `TestBuildClientDisconnectProbeEntry_Cancel` 测试：
- 验证 `RequestBody` 被正确记录
- 验证 `RequestPreview` 包含模型参数
- 验证 `APIKeyID`、`EndUserID`、`LatencyMs` 等字段

## 影响范围

### 正面影响
1. ✅ **完整的请求信息记录**：每个客户端取消/超时事件都有完整上下文
2. ✅ **更好的可观测性**：运维可以分析为什么请求被取消
3. ✅ **问题诊断能力**：可以识别是否因请求过大、参数不当等导致超时
4. ✅ **向后兼容**：现有功能不受影响，只是增强了日志内容

### 性能影响
- 轻微增加内存使用（需要序列化请求体）
- 客户端取消场景较少，总体影响可忽略
- 增加的数据库存储：每条 probe 记录约增加几 KB

## 验证方法

### 1. 单元测试
```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3
go test -v ./domains/streaming/... -run "TestBuildClientDisconnectProbeEntry"
```

所有测试通过 ✅

### 2. 手动测试
模拟客户端取消请求，检查数据库中的 probe 记录：
```sql
SELECT request_id, request_body, request_preview, error_kind, latency_ms
FROM request_logs 
WHERE request_id LIKE 'probe-client_cancel-%' 
ORDER BY event_at DESC 
LIMIT 10;
```

预期结果：
- `request_body` 字段包含完整请求 JSON
- `request_preview` 字段包含关键参数摘要
- `latency_ms` 字段显示取消前的延迟时间

### 3. Copilot 场景复现
1. 通过 VSCode Copilot 连接 llm.kxpms.cn
2. 发送一个请求并中途取消
3. 查看生成的 probe 记录是否包含完整信息

## 相关文件

### 修改的文件
- `domains/streaming/handler.go` - 增强日志记录逻辑
- `domains/streaming/handler_disconnect_probe_test.go` - 更新测试用例

### 相关文档
- `admin/probe_request_info.go` - 探测请求信息提取
- `domains/hooks/observability/telemetry/client.go` - 请求日志结构定义

## 后续建议

1. **监控报警**：对频繁的客户端取消事件设置告警
2. **数据分析**：定期分析 probe 记录，识别系统瓶颈
3. **日志保留**：根据存储压力调整 probe 记录的保留策略
4. **文档更新**：更新运维手册，说明如何使用新增的字段进行问题诊断

## 提交信息

```
fix(streaming): record complete request info for client disconnect probes

问题：VSCode Copilot 连接后立即断开，probe 记录只有空 JSON
原因：buildClientDisconnectProbeEntry 只记录基本元数据，缺失请求体和参数
修复：增强日志记录，包含 request_body, request_preview, api_key_id 等完整信息

- 记录完整请求体（限制 64KB）
- 提取关键参数到 request_preview（temperature, max_tokens, message_count 等）
- 记录 api_key_id, end_user_id, latency_ms 用于诊断
- 新增 buildRequestPreview 函数提取关键统计信息
- 更新测试用例验证新字段

影响：客户端取消/超时事件现在有完整上下文，便于问题分析

Refs: probe-client_cancel-cred21-1784899122602079383
```

---

**修复时间**: 2026-07-24  
**修复人员**: AI Assistant  
**验证状态**: ✅ 测试通过  
**部署状态**: 待部署
