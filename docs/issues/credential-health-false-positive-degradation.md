# Credential Health False Positive Degradation

**日期**: 2026-07-16  
**优先级**: P0 (Critical)  
**影响**: 客户端错误导致 credential 被错误标记 degraded 15 分钟，造成 "No available provider" 错误

## 问题描述

当前 credential health check 机制存在严重缺陷：**客户端问题被当作 credential 失败**，导致可用的 credential 被错误标记为 degraded。

### 真实场景（2026-07-16）

用户报告 VSCode Copilot 报错：
```
Rate limit exceeded
{"code":"upstream_provider_rate_limit","message":"No available provider for model 'minimax-m3'. All 0 candidates failed."}
```

但直连 minimax-m3 API **完全正常**。

### 根本原因

**journal log 证据**：
```json
{"time":"2026-07-16T14:52:28.133","msg":"sync_retry_stopped","model":"minimax-m3","reason":"client_disconnect","elapsed_ms":2178,"tried":2,"retried":2}
× 9 次

{"time":"2026-07-16T14:53:34.058","level":"WARN","msg":"credential binding marked degraded due to continuous failures","credential_id":19,"model":"minimax-m3","failure_rate":1,"sample_size":15,"error_kinds":{"transient":15},"recover_at":"2026-07-16T15:08:34.055015297+08:00"}
```

**问题链**：
1. 客户端 disconnect（可能是 curl timeout、VSCode 取消请求、网络抖动）
2. gateway 重试 2 次，仍然 client_disconnect
3. **这些 transient 错误被记录为 credential 失败**
4. 15 次 transient 失败 → credential 标记 degraded 15 分钟
5. 所有后续请求报 "No available provider"

## 架构缺陷

### 1. 错误分类不合理

**当前逻辑** (`credentialhealth/checker.go:85-117`):
```go
for _, e := range entries {
    // 跳过 network/stream_timeout/client_bugs
    if e.ErrorKind == "network" ||
        e.ErrorKind == string(errorsx.KindStreamTimeout) ||
        errorsx.IsClientBug(errorsx.ErrorKind(e.ErrorKind)) {
        continue
    }
    total++
    if !e.Success {
        failed++  // ← client_disconnect 被算作失败！
    }
}
```

**问题**：
- `client_disconnect` / `client_cancel` / `timeout` 等**客户端问题**被当作 credential 失败
- `transient` 错误（如临时网络抖动、上游短暂503）也被计入失败率

### 2. 没有主动探测验证

**当前流程**：
```
15 次失败 → 直接标记 degraded 15 分钟
```

**缺少的环节**：
```
15 次失败 → 主动探测 credential（如 GET /v1/models） → 确认真的不可用 → 才标记 degraded
```

### 3. 恢复机制太慢

- `recover_at`: 15 分钟后才自动恢复
- 期间所有请求都报 "No available provider"
- 即使 credential 一直正常工作，也要等 15 分钟

## 影响范围

**受影响场景**：
1. ✅ VSCode Copilot 取消请求 → credential degraded
2. ✅ curl timeout → credential degraded
3. ✅ 客户端网络抖动 → credential degraded
4. ✅ 上游短暂 503（5 秒后恢复）→ credential degraded 15 分钟
5. ✅ 大量并发请求触发上游 rate limit → 15 次后 credential degraded

**误杀率估算**：
- 15 次 transient 失败的窗口：1 小时
- 正常 credential 在高峰期遇到 15 次客户端 disconnect 的概率：> 50%
- **误杀率：保守估计 30-50%**

## 解决方案

### Phase 1: 临时缓解（已部署）

1. ✅ 修复 `model_offers` 视图缺少 `unavailable_recover_at` 列（migration 2027）
2. ✅ timeout 120s → 300s（减少客户端 timeout 概率）
3. ⚠️ 仍然存在误杀风险

### Phase 2: 错误分类修正（P0, 2 天）

**修改 `credentialhealth/checker.go:85-117`**:
```go
for _, e := range entries {
    // 扩展跳过列表
    if e.ErrorKind == "network" ||
        e.ErrorKind == string(errorsx.KindStreamTimeout) ||
        e.ErrorKind == string(errorsx.KindCanceled) ||       // ← 新增
        e.ErrorKind == string(errorsx.KindTimeout) ||        // ← 新增
        e.ErrorKind == string(errorsx.KindTransient) ||      // ← 新增
        errorsx.IsClientBug(errorsx.ErrorKind(e.ErrorKind)) {
        continue
    }
    // 同时检查 detail 是否包含 client_disconnect/client_cancel
    if strings.Contains(e.Detail, "client_disconnect") ||
        strings.Contains(e.Detail, "client_cancel") {
        continue
    }
    total++
    if !e.Success {
        failed++
    }
}
```

**只计入明确的 credential 问题**：
- `auth` / `auth_revoked`
- `quota` / `quota_balance` / `quota_permanent`
- `model_not_found`（当模型确实不存在时）
- `rate_limit`（当上游真的限流时，而非客户端取消）

### Phase 3: 主动探测机制（P0, 3 天）

**在标记 degraded 前验证**:
```go
func (c *Checker) CheckAndMarkDegraded(ctx context.Context, credentialID int, model string) error {
    // 1. 检查失败率
    if failureRate >= c.failureThreshold {
        // 2. 主动探测（新增）
        if c.prober != nil {
            probeResult := c.prober.ProbeCredential(ctx, credentialID, model)
            if probeResult.Success {
                slog.Info("credential健康探测通过，不标记degraded", 
                    "credential_id", credentialID, 
                    "model", model,
                    "failure_rate", failureRate)
                return nil  // 探测通过，不标记
            }
        }
        
        // 3. 探测失败，标记 degraded
        return c.markDegraded(ctx, credentialID, model, recoverAt)
    }
    return nil
}
```

**Prober 实现**:
```go
type CredentialProber interface {
    // ProbeCredential 用简单请求验证 credential 可用性
    // 例如：GET /v1/models（OpenAI/Anthropic）
    //      GET /info（minimax）
    ProbeCredential(ctx context.Context, credentialID int, model string) ProbeResult
}

type ProbeResult struct {
    Success   bool
    ErrorKind errorsx.ErrorKind
    Latency   time.Duration
}
```

### Phase 4: 快速恢复（P1, 1 天）

**当前**：degraded → 15 分钟后自动恢复  
**改进**：degraded → 每 30 秒探测一次 → 成功立即恢复

```go
func (c *Checker) RecoverExpired(ctx context.Context) (int, error) {
    // 当前逻辑：只恢复 recover_at < now() 的
    // 新逻辑：每次都主动探测 degraded credential
    degradedBindings := c.getDegradedBindings(ctx)
    for _, binding := range degradedBindings {
        probeResult := c.prober.ProbeCredential(ctx, binding.CredentialID, binding.Model)
        if probeResult.Success {
            c.restoreBinding(ctx, binding.CredentialID, binding.Model)
            slog.Info("degraded credential 探测恢复", 
                "credential_id", binding.CredentialID,
                "model", binding.Model,
                "degraded_duration", time.Since(binding.UnavailableAt))
        }
    }
}
```

### Phase 5: 告警与可观测性（P2, 1 天）

1. **Prometheus metrics**:
   - `credential_degraded_total{reason="false_positive|real_failure"}`
   - `credential_probe_success_rate`
   - `credential_recovery_duration_seconds`

2. **告警规则**:
   - credential 被标记 degraded（Slack / Lark）
   - credential 误杀率 > 20%（需要调整阈值）

3. **Dashboard**:
   - 实时 credential 健康状态
   - degraded 原因分布（client_disconnect vs auth vs quota）
   - 恢复时长分布

## 验证计划

### 单元测试
```go
func TestChecker_IgnoreClientDisconnect(t *testing.T) {
    // 15 次 client_disconnect → 不应标记 degraded
}

func TestChecker_ProbeBeforeDegraded(t *testing.T) {
    // 15 次失败 + 探测成功 → 不标记 degraded
}

func TestChecker_FastRecovery(t *testing.T) {
    // degraded + 探测成功 → 立即恢复
}
```

### 集成测试（生产模拟）
1. 模拟 15 次客户端 cancel → credential 应保持可用
2. 模拟 credential auth 失败 → 应立即标记 degraded
3. degraded credential 恢复后发送成功请求 → 应立即恢复

## 工作量估算

| Phase | 工作量 | 优先级 |
|---|---|---|
| Phase 2: 错误分类 | 2 天 | P0 |
| Phase 3: 主动探测 | 3 天 | P0 |
| Phase 4: 快速恢复 | 1 天 | P1 |
| Phase 5: 可观测性 | 1 天 | P2 |
| **总计** | **7 天** | |

## 相关代码

- `credentialhealth/checker.go:85-117` - 错误统计逻辑
- `credentialhealth/checker.go:208` - 标记 degraded
- `domains/streaming/executors/executor.go` - shouldWriteCredentialState
- `errorsx/classify.go` - ErrorKind 定义

## 参考

- 用户报告：VSCode Copilot "No available provider" (2026-07-16)
- Journal log：154 网关 14:52-14:53
- 类似问题：AWS ELB health check false positive (需要主动探测)

---

**下一步**: 优先实施 Phase 2（错误分类）+ Phase 3（主动探测）
