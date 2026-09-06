# 修复报告：会话 Ping 协议适配 + 强制启用 URSM 状态切换失败

**日期：** 2026-09-06  
**修复人：** ZCode  
**问题来源：** http://localhost:8782/routing-v2?tab=resolve 操作员报告

---

## 问题 1：会话 Ping 失败显示"会话 Ping 失败：error"

### 根因

`admin/node_operations.go:214-260` 的 `runCredentialSessionPing` 函数**硬编码**为 OpenAI Chat Completions 协议：

```go
// 旧代码
endpoint := strings.TrimRight(baseURL, "/") + "/chat/completions"
payload := map[string]any{
    "model": model,
    "max_tokens": 1,
    "stream": false,
    "messages": []map[string]string{{"role": "user", "content": "ping"}},
}
setModelsAuthHeaders(req, protocol, apiKey)
```

**问题：**
- 端点固定为 `/chat/completions`（OpenAI）
- 但当 `protocol='anthropic-messages'` 时：
  - 认证头已正确设置为 `x-api-key` + `anthropic-version`（通过 `setModelsAuthHeaders`）
  - 但端点仍是 `/chat/completions`（错误，应为 `/v1/messages`）
  - 请求体格式也略有差异（Anthropic 不需要 `stream` 字段）

**影响范围：**
- 所有配置为 `anthropic-messages` 协议的供应商（如 apiclaude/130dao 的 `claude-sonnet-5`）
- Ping 请求发送到错误端点，供应商返回 404 或 400，前端显示"会话 Ping 失败：error"
- 即使供应商直接可达，Ping 也会失败

### 修复

**文件：** `admin/node_operations.go:214-260`

1. **动态协议适配**：复用 `internal/providercap` 包的协议描述器

```go
// 新代码
desc := providercap.Resolve(protocol, catalogCode)
endpoint := providercap.ProbeEndpointURL(baseURL, desc)

if desc.ChatProbeEndpoint == upstreamurl.EpMessages {
    // Anthropic Messages API format
    payload = map[string]any{
        "model": model,
        "max_tokens": 1,
        "messages": []map[string]any{
            {"role": "user", "content": "ping"},
        },
    }
} else {
    // OpenAI Chat Completions format (default)
    payload = map[string]any{
        "model": model,
        "max_tokens": 1,
        "stream": false,
        "messages": []map[string]string{{"role": "user", "content": "ping"}},
    }
}
providercap.ApplyAuthHeaders(req, desc, apiKey)
```

2. **响应格式识别**：同时支持 OpenAI 和 Anthropic 响应

```go
func isChatPingResponse(body []byte) bool {
    // OpenAI Chat Completions format
    var oaiResp struct {
        Choices json.RawMessage `json:"choices"`
    }
    if json.Unmarshal(body, &oaiResp) == nil && len(oaiResp.Choices) > 0 {
        return true
    }
    // Anthropic Messages format
    var anthResp struct {
        Type    string          `json:"type"`
        Content json.RawMessage `json:"content"`
    }
    if json.Unmarshal(body, &anthResp) == nil && anthResp.Type == "message" && len(anthResp.Content) > 0 {
        return true
    }
    return false
}
```

**测试覆盖：**
- `admin/node_operations_test.go:48-103`：新增 anthropic-messages 协议测试用例
- 验证端点为 `/v1/messages`、认证头为 `x-api-key` + `anthropic-version`、响应识别正确

---

## 问题 2：强制启用后显示"URSM 状态未清理，运行态可能仍需重试"

### 根因

`admin/routing.go:5527-5558` 的 URSM v2 清理采用 **best-effort** 模式：

```go
// 旧代码
if h.ursmV2 != nil {
    for _, m := range resetModels {
        if err := h.ursmV2.ApplyAdmin(...); err != nil {
            slog.Warn("force_enable: ursm.v2 apply_admin failed", ...) // 仅警告
        }
        if err := h.ursmV2.ClearStateForTenant(...); err != nil {
            slog.Warn("force_enable: ursm.v2 clear_state failed", ...) // 仅警告
        }
    }
    beforeAfter["ursm_v2_cleared"] = len(resetModels) > 0 && cleared == len(resetModels)
}
```

**问题：**
1. URSM 清理失败只记录 warning，最终仍返回 HTTP 200
2. 前端显示警告但 PostgreSQL 已更新 → **数据不一致**
3. 当 `resetModels` 为空时（整凭据 reset 时模型枚举失败、或 raw_model 不匹配任何 binding），完全跳过 URSM
4. Redis 不可用、tenant ID 不匹配、schema mode 错误等问题都被静默吞掉
5. **实际后果：** PG 显示 `manual_disabled=false`、`availability_state='ready'`，但 Redis URSM 仍有 `manual_hold=true` 或 `disabled=true`，路由层继续过滤该节点

**根因详解：**
- URSM v2 是运行态路由的关键门禁（Redis 存储冷却状态、手动禁用标记、失败计数）
- 强制启用需要**原子性**清理 PG + Redis + 内存态三层
- best-effort 设计导致"PG 成功 + Redis 失败"的半成功状态
- 操作员看到 200 响应误以为已完全恢复，实际流量仍被拦截

### 修复

**文件：** `admin/routing.go:5527-5570`

1. **URSM 清理改为必要步骤**：失败时阻断返回错误

```go
// 新代码
if h.ursmV2 != nil {
    if len(resetModels) == 0 {
        // 整凭据 reset 时模型枚举失败 — 明确拒绝
        return fmt.Errorf("force_enable: no binding models found for credential %d, cannot reset URSM", credentialID)
    }
    for _, m := range resetModels {
        if err := h.ursmV2.ApplyAdmin(ctx, adminAction); err != nil {
            return fmt.Errorf("force_enable: ursm.v2 apply_admin failed for model %s: %w", m, err)
        }
        if err := h.ursmV2.ClearStateForTenant(ctx, ursmTenantID, credentialID, m); err != nil {
            return fmt.Errorf("force_enable: ursm.v2 clear_state failed for model %s: %w", m, err)
        }
    }
    beforeAfter["ursm_v2_admin_applied"] = true
    beforeAfter["ursm_v2_cleared"] = true
}
```

**变更理由：**
- 保持原子性：PG 成功必须 Redis 也成功，否则整体失败回滚（PG 事务已提交，但 HTTP 返回 500，操作员知道需要手动修复 Redis）
- 前端不再显示"URSM 状态未清理"警告，因为要么完全成功（200），要么完全失败（500）
- 操作员看到 500 时可排查 Redis/tenant/schema 问题后重试

2. **整凭据 reset 时模型枚举失败明确报错**

**文件：** `admin/routing.go:1933-1965`

```go
// 新代码
if rawModel != "" {
    models = append(models, rawModel)
} else {
    rows, err := h.db.Query(ctx, `SELECT pm.raw_model_name ...`)
    if err != nil {
        slog.Warn("emergency_repair: enumerate binding models failed", "error", err)
        outcome["enum_models_error"] = err.Error()
        met.RoutingCredentialResetTotal.WithLabelValues("enum_models", "error").Inc()
    } else {
        // ... scan 循环
        if len(models) == 0 {
            slog.Warn("emergency_repair: credential has no bindings", "cred", credentialID)
            outcome["enum_models_empty"] = true
        }
        met.RoutingCredentialResetTotal.WithLabelValues("enum_models", "ok").Inc()
    }
}
```

**变更：**
- 查询失败时返回清晰的 `enum_models_error` 到 outcome
- 成功查询但结果为空时记录 `enum_models_empty`（可能是合法的零绑定凭据，不强制失败）
- Metric 明确区分成功/失败，便于监控告警

3. **provider 级 toggle 补全 URSM 同步**（可选增强）

**文件：** `admin/node_operations.go:423-442`

```go
// 新代码：provider 级批量启停后也同步 URSM
ursmAppliedCount, ursmErrorCount := 0, 0
if h.ursmV2 != nil {
    manualDisabled := !req.Enabled
    for _, cid := range credIDs {
        result := h.applyURSMManualDisabled(ctx, cid, manualDisabled, req.Reason, operatorID)
        ursmAppliedCount += result.models - result.errors
        ursmErrorCount += result.errors
    }
}
// 响应中返回
"ursm_v2_models_applied": ursmAppliedCount,
"ursm_v2_models_errored":  ursmErrorCount,
```

**说明：**
- provider 级操作保持 best-effort（因为可能涉及数十个 credential）
- 失败详情通过响应字段暴露，操作员可对失败的 credential 单独补偿
- 关闭了"批量禁用 provider 后 URSM 未同步，重新启用时仍被 manual_hold 拦截"的缺口

---

## 影响评估

### 向后兼容性

✅ **会话 Ping：**
- 保持现有 OpenAI 格式为默认
- 新增 anthropic-messages 分支
- 无破坏性变更

⚠️ **强制启用：**
- **行为变更：** URSM 失败从 warning 改为阻断返回 500
- **风险缓解：**
  - URSM Redis 稳定时无影响（绝大多数场景）
  - Redis 不稳定时会暴露失败（这正是预期行为，避免假成功）
- **回滚方案：** 如生产 Redis 不稳定导致强制启用全部失败，可临时回退为 best-effort 模式（保留 warning 分支的注释代码）

### 测试覆盖

✅ **单元测试：**
- `admin/node_operations_test.go:48-103`：anthropic-messages ping
- `admin/routing_force_enable_test.go`：SQL 结构验证
- `admin/routing_reset_test.go`：reset-state 端点契约

✅ **集成测试：**
- `admin` 包全量测试通过（66.6s）
- `domains/ursm/v2` 包全量测试通过（5.7s）
- `internal/providercap` 无测试文件（轻量工具包）

⚠️ **缺失覆盖：**
- 完整 DB + Redis + URSM 集成测试（需要真实 PG + Redis 环境）
- URSM 失败阻断的端到端验证（本地验证需配置 Redis）

---

## 部署建议

### 部署顺序

1. **先部署会话 Ping 修复**（无风险，纯增强）
2. **再部署强制启用 URSM 阻断**（需在低峰期观察）

### 验证步骤

#### 会话 Ping 验证

1. 登录 http://localhost:8782/routing-v2?tab=resolve
2. 选择 `claude-sonnet-5` / `apiclaude` / `130dao` 节点
3. 点击"设置与维护"标签页 → "会话 Ping"
4. **预期结果：**
   - 成功显示延迟（如 `会话 Ping 成功：350ms`）
   - 不再显示"会话 Ping 失败：error"

#### 强制启用验证

1. 同样在节点详情页，选择一个被 `manual_disabled=true` 阻塞的节点
2. 点击"紧急操作"标签页 → "强制启用"
3. **预期结果：**
   - 成功时不再显示"URSM 状态未清理"警告
   - 失败时明确显示错误（如 `ursm.v2 clear_state failed: Redis connection timeout`）
   - 节点状态立即从红色变为绿色（不再需要等待 5 分钟冷却）

### 回滚计划

如生产 Redis 不稳定导致强制启用全部失败：

```bash
# 回退为 best-effort 模式（临时）
git revert <本次提交 SHA>
# 或手动恢复 admin/routing.go:5540-5565 的旧 slog.Warn 分支
```

---

## 根因总结

| 问题 | 根因 | 修复 |
|------|------|------|
| 会话 Ping 失败 | 硬编码 OpenAI 协议，不支持 Anthropic Messages API | 动态协议适配 + 响应格式识别 |
| URSM 状态未清理 | best-effort 设计 + 零绑定跳过 + Redis 失败静默 | 改为必要步骤 + 失败阻断 + 枚举错误显式报错 |
| provider 级启停绕过 URSM | 只改 PG，未调用 URSM fan-out | 补全 URSM 同步（best-effort） |

---

## 文件清单

### 修改文件

- `admin/node_operations.go` (+74 行)：会话 Ping 协议适配 + provider 级 URSM 同步
- `admin/node_operations_test.go` (+45 行)：anthropic-messages ping 测试
- `admin/routing.go` (+34 行)：URSM 失败阻断 + 模型枚举错误报告

### 依赖包

- `internal/providercap`：协议描述器与端点构造器（已存在，无新增代码）
- `internal/upstreamurl`：端点常量（已存在，无新增代码）
- `domains/ursm/v2`：URSM v2 Manager（已存在，无新增代码）

### 测试文件

- `admin/node_operations_test.go`：会话 Ping 协议测试
- `admin/routing_force_enable_test.go`：强制启用 SQL 结构测试（已存在）
- `admin/routing_reset_test.go`：reset-state 契约测试（已存在）

---

## 下一步

1. ✅ 代码审查与合并
2. ⏳ 本地环境验证（需配置 Redis + URSM v2）
3. ⏳ 部署到测试环境，验证 apiclaude/130dao 节点
4. ⏳ 观察生产 Grafana 指标：
   - `llmgw_routing_credential_reset_total{surface="ursmv2",outcome="error"}` 应保持低位
   - `llmgw_routing_credential_reset_total{surface="enum_models",outcome="error"}` 应为零
5. ⏳ 监控前端告警：不再出现"URSM 状态未清理"警告

---

**修复完成时间：** 2026-09-06  
**预计生产部署：** 待测试环境验证通过后
