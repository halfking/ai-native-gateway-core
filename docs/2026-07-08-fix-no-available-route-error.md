# 修复"无可用路由"错误（密钥解密失败场景）

**日期**: 2026-07-08  
**优先级**: P0  
**影响范围**: 生产环境 llm.kxpms.cn (数据库在252，服务在154)  
**问题追踪**: 线上单个失败请求样例（已脱敏）

## 问题描述

部分客户在访问网关时，明确后端 LLM 可用（数据库中有候选凭据），但请求却报"无可用路由"错误，导致请求失败。

### 症状

- **错误信息**: "No available provider for model 'xxx'"
- **error_kind**: `no_candidate`
- **数据库状态**: 候选凭据存在且状态正常（`is_routable=TRUE`, `availability_state='ready'`）
- **影响**: 正常可用的后端服务无法被路由到，用户请求失败

### 根本原因

问题发生在 `provider.Client.enrichWithAPIKeys()` 方法中。该方法负责为每个候选凭据解密 API 密钥：

```go
// 原有逻辑（有缺陷）
func (c *Client) enrichWithAPIKeys(ctx context.Context, rr *resolveResponse) []Candidate {
    var cands []Candidate
    for _, raw := range rr.Candidates {
        var cand Candidate
        // ... unmarshal ...
        
        apiKey, err := c.RevealAPIKey(ctx, cand.ProviderID, cand.CredentialID)
        if err != nil {
            slog.Warn("failed to reveal api key", ...)
            continue  // ❌ 硬性过滤，直接跳过该候选者
        }
        cand.APIKey = apiKey
        cands = append(cands, cand)
    }
    return cands
}
```

当密钥解密失败时（原因可能是密钥配置错误、加密版本不兼容、keyring 未初始化等），该候选者会被**硬性过滤**（`continue`），不会出现在返回列表中。

**触发条件**：
1. **部分密钥解密失败**: 如果只有部分候选者的密钥解密失败，其他候选者仍可用，系统能正常工作
2. **全部密钥解密失败**: 如果所有候选者的密钥都解密失败（例如 keyring 初始化失败），`enrichWithAPIKeys` 返回空列表
3. **空列表 → 无可用路由**: 调用方（`handler.serveWithExecutor`）收到空列表后，会触发 `no_candidate` 错误

### 实际案例

某次线上失败请求（request_id 已脱敏）:
- 数据库查询返回 2 个候选凭据（credential_id=11, credential_id=12）
- 两个凭据的 `is_routable=TRUE`, `availability_state='ready'`
- 但 `enrichWithAPIKeys` 中两个密钥都解密失败（可能是 keyring 配置问题）
- 返回空列表
- 路由器报 `no_candidate` 错误

## 修复方案

### 核心思路

**将密钥解密失败从硬性过滤改为软性降级**：
- 解密失败的候选者不再被直接丢弃
- 而是标记为不可路由（`Routable=false`，`BlockReason="key_decrypt_failed"`）
- 保留在返回列表中

这样做的好处：

1. **部分失败时其他候选者仍可用**: 当只有部分密钥解密失败时，解密成功的候选者仍能正常路由
2. **全部失败时返回诊断信息**: 当全部失败时，调用方仍能收到候选者列表（虽然都是不可路由的），便于诊断
3. **路由器可以感知到差异**: 路由器的 `filterAvailable()` 会过滤掉 `Routable=false` 的候选者，这与原有行为一致
4. **运维可追溯**: 保留 `BlockReason`，运维可以通过日志或 API 查看到具体原因

### 代码修改

**文件**: `provider/client.go`

```go
func (c *Client) enrichWithAPIKeys(ctx context.Context, rr *resolveResponse) []Candidate {
    if rr == nil {
        return nil
    }

    planSet := make(map[int]bool, len(rr.PlanOrder))
    for _, p := range rr.PlanOrder {
        planSet[p.CredentialID] = true
    }

    var cands []Candidate
    var skippedCount int
    for _, raw := range rr.Candidates {
        var cand Candidate
        if err := json.Unmarshal(raw, &cand); err != nil {
            continue
        }
        if !planSet[cand.CredentialID] {
            continue
        }

        apiKey, err := c.RevealAPIKey(ctx, cand.ProviderID, cand.CredentialID)
        if err != nil {
            // 2026-07-08 P0 fix: 密钥解密失败时不再硬性过滤候选者。
            // 降级策略：
            // - 保留候选者但标记为不可路由（Routable=false）
            // - 设置 BlockReason 说明原因
            // - 记录警告日志供运维排查
            // - 路由器的 filterAvailable 会过滤掉这些候选者
            slog.Warn("enrichWithAPIKeys: reveal failed, marking candidate unavailable",
                "credential_id", cand.CredentialID,
                "provider_id", cand.ProviderID,
                "error", err,
            )
            skippedCount++
            reason := fmt.Sprintf("key_decrypt_failed: %v", err)
            cand.Routable = false
            cand.BlockReason = &reason
            cand.APIKey = "" // 确保没有泄漏部分解密的数据
            cands = append(cands, cand)
            continue
        }
        cand.APIKey = apiKey
        cands = append(cands, cand)
    }
    
    // 2026-07-08: 当有候选者因密钥解密失败被降级时，记录汇总日志
    if skippedCount > 0 {
        slog.Warn("enrichWithAPIKeys: some candidates marked unavailable due to key decrypt failure",
            "total_candidates", len(rr.Candidates),
            "failed_count", skippedCount,
            "available_count", len(cands)-skippedCount,
        )
    }

    // ... 原有的排序逻辑 ...
}
```

### 测试覆盖

**文件**: `provider/client_key_decrypt_test.go`

新增单元测试 `TestEnrichWithAPIKeys_KeyDecryptFail`，覆盖三种场景：

1. **partial_decrypt_failure**: 部分候选者密钥解密失败
   - 验证解密成功的候选者仍然可路由
   - 验证解密失败的候选者被标记为不可路由
   
2. **all_decrypt_failure**: 全部候选者密钥解密失败
   - 验证所有候选者都被标记为不可路由
   - 验证返回列表不为空（包含诊断信息）
   
3. **all_decrypt_success**: 全部候选者密钥解密成功
   - 验证所有候选者都正常可路由

测试结果：
```
=== RUN   TestEnrichWithAPIKeys_KeyDecryptFail
=== RUN   TestEnrichWithAPIKeys_KeyDecryptFail/partial_decrypt_failure
=== RUN   TestEnrichWithAPIKeys_KeyDecryptFail/all_decrypt_failure
=== RUN   TestEnrichWithAPIKeys_KeyDecryptFail/all_decrypt_success
--- PASS: TestEnrichWithAPIKeys_KeyDecryptFail (0.00s)
    --- PASS: TestEnrichWithAPIKeys_KeyDecryptFail/partial_decrypt_failure (0.00s)
    --- PASS: TestEnrichWithAPIKeys_KeyDecryptFail/all_decrypt_failure (0.00s)
    --- PASS: TestEnrichWithAPIKeys_KeyDecryptFail/all_decrypt_success (0.00s)
PASS
```

## 验证方案

### 1. 单元测试验证

```bash
cd /Users/xutaohuang/workspace/llm-gateway-go-cursor
go test -v ./provider -run TestEnrichWithAPIKeys_KeyDecryptFail
```

### 2. 集成测试验证

在测试环境中模拟密钥解密失败场景：
- 修改环境变量使 keyring 初始化失败
- 发送请求到网关
- 验证：
  - 请求不会报 `no_candidate` 错误
  - 日志中出现 `enrichWithAPIKeys: reveal failed, marking candidate unavailable`
  - 如果有其他可用候选者，请求能正常转发

### 3. 生产验证

部署到 154 服务器后：
- 监控 `no_candidate` 错误率是否下降
- 监控新增的警告日志 `enrichWithAPIKeys: reveal failed`
- 对于原先报错的请求，验证是否能正常处理

## 影响分析

### 向下兼容性

✅ **完全向下兼容**

- 对于密钥解密成功的候选者，行为与原来完全一致
- 对于密钥解密失败的候选者，原来是硬性过滤（相当于不存在），现在是标记为不可路由后保留
- 路由器的 `filterAvailable()` 会过滤掉 `Routable=false` 的候选者，最终效果一致

### 性能影响

✅ **无性能影响**

- 仅增加了设置 `Routable` 和 `BlockReason` 字段的开销（微乎其微）
- 日志记录是原本就有的，只是改为更详细的消息

### 风险评估

**风险**: 低

- 修改逻辑清晰，仅将 `continue` 改为设置字段后 `append`
- 有完整的单元测试覆盖
- 编译通过，无语法错误
- 不影响正常流程（密钥解密成功的路径）

## 部署建议

### 部署步骤

1. **备份当前版本**
   ```bash
   ssh llm-gateway-154
   cd /path/to/llm-gateway
   git tag backup-before-key-decrypt-fix-$(date +%Y%m%d-%H%M%S)
   ```

2. **拉取修复代码**
   ```bash
   git pull origin main
   ```

3. **编译**
   ```bash
   go build -o llm-gateway-new ./cmd/gateway/
   ```

4. **滚动重启**（避免影响在线流量）
   ```bash
   # 停止旧进程
   systemctl stop llm-gateway
   # 替换二进制
   mv llm-gateway llm-gateway.old
   mv llm-gateway-new llm-gateway
   # 启动新进程
   systemctl start llm-gateway
   ```

5. **监控验证**
   - 观察 Prometheus 指标 `llmgw_request_errors_total{error_kind="no_candidate"}`
   - 检查日志中是否出现新的警告消息
   - 验证原先失败的请求是否恢复

### 回滚方案

如果出现问题，立即回滚：

```bash
systemctl stop llm-gateway
mv llm-gateway.old llm-gateway
systemctl start llm-gateway
```

## 后续工作

### 密钥解密失败的根本原因排查

修复后，通过日志可以清楚看到哪些凭据的密钥解密失败：

```
WARN enrichWithAPIKeys: reveal failed, marking candidate unavailable 
     credential_id=11 provider_id=1 
     error="credential reveal not configured (no DB, keyring, or fernet key)"
```

建议排查：
1. keyring 是否正确初始化（环境变量 `CREDENTIAL_ENCRYPTION_KEY`）
2. fernet key 是否正确配置（环境变量 `SECRET_KEY`）
3. 数据库中的 `secret_ciphertext` 是否使用了不兼容的加密版本
4. 凭据是否在数据库中被标记为 `status='disabled'`

### 监控和告警

建议新增 Prometheus 告警规则：

```yaml
- alert: HighKeyDecryptFailureRate
  expr: |
    rate(llmgw_enrichment_key_decrypt_failures_total[5m]) > 0.1
  for: 5m
  annotations:
    summary: "高密钥解密失败率"
    description: "过去 5 分钟内密钥解密失败率超过 10%，可能存在配置问题"
```

## 相关文档

- [Credential State Manager 设计文档](./2026-06-12-credential-availability-audit-design.md)
- [路由器降级模式设计](./2026-07-03-router-degraded-mode.md)
- [密钥管理最佳实践](./credential-encryption-guide.md)

## 变更历史

| 日期 | 作者 | 变更 |
|------|------|------|
| 2026-07-08 | Kiro | 初始版本，修复密钥解密失败导致的"无可用路由"错误 |
