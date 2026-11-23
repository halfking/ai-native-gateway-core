# 2026-11-23 503 Service Unavailable Errors - Invalid Provider Keys

## 时间线（Timeline）

**发生时间**: 2026-11-23 06:26:21 - 06:26:23 UTC
**恢复时间**: 2026-11-23 06:26:24 UTC
**持续时长**: ~3 秒
**影响请求数**: 3 个请求返回 503

## 问题概述（Summary）

在 06:26:21-23 期间，网关返回了 3 个 503 Service Unavailable 错误。根因是**多个上游提供商的 API 密钥失效或过期**，导致候选模型选择（candidate resolution）失败。

## 根本原因（Root Cause）

### 核心问题
候选模型解析返回 `candidates_count=0`，系统无法找到任何可用的提供商来处理请求。

### 具体失败的提供商密钥

1. **custom/community/Marcus-Mok-GH/pt-harbor**
   - 错误: HTTP 502 - Authentication failed
   - 详情: `API error 401: A valid API key is required`

2. **custom/community/Catniti/gemini-4-hydrogen**
   - 错误: HTTP 502 - Authentication failed
   - 详情: `API error 401: A valid API key is required`

3. **custom/minimax/minimax-h3**
   - 错误: HTTP 502 - Authentication failed
   - 详情: `API error 401: A valid API key is required`

4. **其他模型**
   - 部分模型没有配置任何 API 密钥
   - 部分模型的密钥处于速率限制状态

### 日志证据

```json
{
  "timestamp": "2026-11-23T06:26:21.096676316Z",
  "msg": "candidates_resolved",
  "request_id": "[REDACTED:49c5773a]",
  "candidates_count": 0,
  "severity_text": "INFO"
}
```

```json
{
  "timestamp": "2026-11-23T06:26:21.266339581Z",
  "msg": "node probe failed: node/upstream fault (ladder applies)",
  "err_code": "http_503",
  "detail": "No candidate model has a configured, usable provider key. Add provider API keys in the dashboard. All models exhausted. Add more API keys or wait for rate limits to reset. Soonest reset ~2m. 1 model skipped: no key configured for their platform."
}
```

```json
{
  "timestamp": "2026-11-23T06:26:21.868050805Z",
  "msg": "node probe failed: node/upstream fault (ladder applies)",
  "err_code": "http_502",
  "detail": "All 1 attempted provider key(s) failed authentication... custom/community/Marcus-Mok-GH/pt-harbor key1: auth. Last error: Custom (OpenAI-compatible) API error 401: A valid API key is required"
}
```

## 系统行为分析（System Behavior）

### ✅ 系统正常工作的部分

1. **错误检测**: 系统正确检测到无效的 API 密钥
2. **错误日志**: 提供了清晰、可操作的错误消息
3. **阶梯路由**: 尝试了多个候选提供商（ladder routing）
4. **自动重验证**: 标记失效的密钥进行自动重新验证
5. **配置热重载**: 在 06:26:23-24 检测到配置更新并自动应用
6. **快速恢复**: 在密钥更新后立即恢复服务

### 恢复过程

```json
{
  "timestamp": "2026-11-23T06:26:23.868080892Z",
  "msg": "auto route listener: refresh requested",
  "payload": "credential_model_bindings:UPDATE:99023"
}
```

```json
{
  "timestamp": "2026-11-23T06:26:24.125375012Z",
  "msg": "auto route listener: refresh requested",
  "payload": "credentials:UPDATE:99023"
}
```

```json
{
  "timestamp": "2026-11-23T06:26:24.029Z",
  "msg": "http_request",
  "status": 200,
  "status_text": "OK"
}
```

## 影响范围（Impact）

- **受影响请求**: 3 个请求（全部来自 127.0.0.1:35516 和 127.0.0.1:57874）
- **错误类型**: HTTP 503 Service Unavailable
- **客户端**: Go-http-client/1.1（内部服务调用）
- **持续时间**: ~3 秒
- **恢复方式**: 配置更新后自动恢复

## 结论（Conclusion）

### 问题性质
这是一个**运维/配置问题**，不是代码缺陷。网关按设计正常工作：
- 正确检测无效密钥
- 记录详细错误信息
- 在无可用候选时返回适当的 HTTP 503
- 自动应用配置更新
- 快速恢复服务

### 代码层面
**无需代码修复**。系统按预期运行。

### 运维层面
需要确保：
1. 所有提供商 API 密钥保持有效
2. 为关键模型配置多个备用密钥
3. 监控密钥认证失败率
4. 建立密钥轮换和更新流程

## 建议（Recommendations）

### 短期措施（Immediate）

1. ✅ **验证密钥已修复** - 从 06:26:24 开始请求已成功
2. **检查所有提供商密钥状态** - 在管理面板验证所有密钥有效性
3. **审查失败的密钥配置**:
   - custom/community/Marcus-Mok-GH/pt-harbor
   - custom/community/Catniti/gemini-4-hydrogen
   - custom/minimax/minimax-h3

### 长期措施（Long-term）

1. **密钥管理**:
   - 建立密钥轮换计划
   - 为关键模型配置多个备用密钥
   - 设置密钥过期提醒

2. **监控告警**:
   - 为 `candidates_count=0` 场景添加专门的告警
   - 监控密钥认证失败率
   - 设置提供商健康度仪表板

3. **文档化**:
   - 记录各提供商的密钥获取和更新流程
   - 建立密钥失效的应急响应流程

### 可选的代码改进（Optional Code Enhancements）

虽然当前系统运行正常，但可以考虑以下增强：

1. **主动密钥验证**: 定期探测密钥有效性，而不是等到请求失败
2. **更细粒度的指标**: 添加 Prometheus 指标跟踪 "zero candidates" 事件
3. **密钥健康度仪表板**: 在管理面板显示每个密钥的健康状态和最后验证时间

## 验证（Verification）

```bash
# 检查最近的 503 错误数量
ssh 211 "docker logs llm-gateway-211-b 2>&1 | tail -n 1000" | grep '"status":503' | wc -l
# 结果: 仅有 3 个（都在 06:26:21-23 时间窗口）

# 检查最近的成功请求
ssh 211 "docker logs llm-gateway-211-b 2>&1 | tail -n 100" | grep '"status":200'
# 结果: 大量成功请求，系统已恢复正常
```

## 附加观察（Additional Observations）

### 其他警告（Non-Critical）

1. **会话摘要生成超时**:
   ```
   session_summary_worker: GenerateSummary failed
   error: "Post \"https://llm.kxpms.cn/v1/chat/completions\": context deadline exceeded"
   ```
   - 这是异步后台任务，不影响主请求路径
   - 建议单独跟踪和优化

2. **模型绑定歧义**:
   ```
   modelbinding: ambiguous normalized model binding, using first candidate
   requested_model: "minimax-m3.1-flash"
   candidates: ["MiniMax-M3.1-Flash","MiniMax-M3.1-Flash-Preview"]
   ```
   - 系统正确选择了第一个候选
   - 可考虑优化模型名称规范化逻辑

## 文件信息

- **创建时间**: 2026-11-23
- **分析者**: AI Assistant
- **数据源**: 192.168.31.211 docker logs (llm-gateway-211-b)
- **当前版本**: 2.6.1-42ae8b70-20261123-211
- **Build Seq**: 3241
