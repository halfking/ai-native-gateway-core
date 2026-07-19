# LLM Gateway 错误处理流程完整梳理

**日期**: 2026-07-19  
**目的**: 梳理智谱AI超限、商汤限流等问题的完整错误处理链路  
**范围**: 从请求失败 → 错误分类 → 状态更新 → 路由过滤 → 探测恢复

---

## 一、错误处理生命周期概览

```
┌─────────────────────────────────────────────────────────────────────┐
│                     错误处理生命周期（7个阶段）                        │
└─────────────────────────────────────────────────────────────────────┘

1. 上游响应错误
   ↓
2. 错误分类（errorsx.ClassifyError）
   ↓
3. 状态写入判断（IsClientBug / IsRetryable）
   ↓
4. 内存状态更新（credentialstate.Manager.UpdateOnFailure）
   ↓
5. 数据库状态更新（credentialhealth.Checker.CheckAndUpdate）
   ↓
6. 路由过滤（router.PlanCandidates → filterAvailable）
   ↓
7. 探测与恢复（bg.NodeProbeWorker / credentialhealth.RecoverExpired）
```

---

## 二、详细流程分解

### 阶段1: 上游响应错误

**触发点**: `domains/streaming/executors/executor_chat.go`

```go
// Line ~600: 上游返回非2xx响应
resp, err := upstreamClient.Do(req)
if err != nil {
    // 网络错误、超时等
    errKind = errorsx.ClassifyError(err, nil)
} else if resp.StatusCode >= 400 {
    // HTTP 4xx/5xx错误
    bodyBytes, _ := io.ReadAll(resp.Body)
    errKind = errorsx.ClassifyError(nil, bodyBytes)
}
```

**可能的上游错误示例**:
- 智谱AI超限: `HTTP 429` + `{"error": {"message": "超过限额"}}` 或 `model not found`
- 商汤限流: `HTTP 429` + `{"error": "rate limit exceeded"}`
- 模型不存在: `HTTP 404` + `{"error": "model not found"}`
- 认证失败: `HTTP 401` + `{"error": "invalid api key"}`

---

### 阶段2: 错误分类（errorsx.ClassifyError）

**文件**: `errorsx/classify.go`

#### 2.1 分类优先级

```go
func ClassifyError(err error, body []byte) ErrorKind {
    // 优先级1: 检查HTTP状态码（如果可用）
    if resp != nil {
        switch resp.StatusCode {
        case 401, 403:
            return KindAuth
        case 429:
            return KindRateLimit  // ← 注意：429永远返回KindRateLimit
        case 402:
            return KindQuotaBalance
        // ...
        }
    }
    
    // 优先级2: 检查响应body中的错误模式
    msg := string(body)
    if authFailedRe.MatchString(msg) {
        return KindAuth
    }
    if modelNotFoundRe.MatchString(msg) {
        return KindModelNotFound  // ← 问题：可能与429冲突
    }
    if rateLimitRe.MatchString(msg) {
        return KindRateLimit
    }
    
    // 优先级3: 默认分类
    return KindTransient
}
```

#### 2.2 当前存在的问题

**问题A: 状态码与body不一致时的误判**

```go
// 智谱AI可能返回：
HTTP 429
Body: {"error": {"message": "model not found"}}

// 当前逻辑：
// 1. 先检查状态码 → 429 → return KindRateLimit ✓ 正确
// 2. 如果代码逻辑有误，可能先检查body → return KindModelNotFound ✗ 错误
```

**需要确认的代码路径**:
```bash
grep -n "ClassifyError" ./domains/streaming/executors/executor_chat.go
```

检查调用点是否传递了正确的 `statusCode` 参数。

---

### 阶段3: 状态写入判断

**文件**: `domains/streaming/executors/executor.go`

```go
// Line ~2100: 决定是否写入状态
func (e *Executor) shouldWriteCredentialState(errKind errorsx.ErrorKind) bool {
    // 1. 客户端错误不写入
    if errorsx.IsClientBug(errKind) {
        return false
    }
    
    // 2. 取消操作不写入
    if errKind == errorsx.KindCanceled {
        return false
    }
    
    // 3. 瞬态错误暂不写入（等待确认）
    if errKind == errorsx.KindTransient {
        return false
    }
    
    return true
}
```

#### 3.1 IsClientBug 判断

**文件**: `errorsx/classify.go:571`

```go
func IsClientBug(kind ErrorKind) bool {
    // 2026-07-03: Removed KindModelNotFound from this list.
    switch kind {
    case KindToolCallIdMismatch, 
         KindUnsupportedFeature, 
         KindCanceled:
        return true
    default:
        return false
    }
}
```

**关键发现**: `KindModelNotFound` 已在 2026-07-03 从 `IsClientBug` 中移除，意味着：
- `model_not_found` 错误**会写入状态**
- **会触发冷却机制**

但为什么智谱AI没有冷却？需要检查：
1. 错误是否被正确分类为 `KindModelNotFound`
2. 还是被错误分类为其他类型（如 `KindRateLimit`）

---

### 阶段4: 内存状态更新（credentialstate.Manager）

**文件**: `domains/credentialstate/manager.go:223`

```go
func (m *Manager) UpdateOnFailure(ctx context.Context, credID int, model string, 
                                    errKind errorsx.ErrorKind, requestID, tenantID, billingMode string) {
    // 4.1 过滤不应计入统计的错误
    if errKind == errorsx.KindCanceled || errorsx.IsClientBug(errKind) {
        return  // ← model_not_found 不会被跳过（已从 IsClientBug 移除）
    }
    
    // 4.2 更新内存状态
    state.ConsecutiveFails++
    state.LastError = string(errKind)
    state.LastFailureAt = &now
    
    // 4.3 判断是否进入冷却
    isTransient := errKind == errorsx.KindRateLimit ||
                   errKind == errorsx.KindUpstreamDown ||
                   errKind == errorsx.KindTimeout ||
                   errKind == errorsx.KindStreamTimeout
    
    // 4.4 免费凭证特殊处理
    if billingMode == "free" && isTransient {
        state.Available = true  // ← 免费凭证瞬态错误不硬剔
    } else {
        state.Available = false  // ← 进入冷却
    }
    
    // 4.5 触发探测
    if state.ConsecutiveFails >= m.activeProbeThreshold {
        m.activeProbeSubmitter(credID, model, tenantID, requestID)
    }
}
```

#### 4.1 关键判断逻辑

| 错误类型 | IsClientBug | 是否写状态 | 是否冷却 | 是否触发探测 |
|---------|------------|----------|---------|------------|
| `KindCanceled` | ✓ | ✗ | ✗ | ✗ |
| `KindToolCallIdMismatch` | ✓ | ✗ | ✗ | ✗ |
| `KindModelNotFound` | ✗ | ✓ | ✓ | ✓ |
| `KindRateLimit` | ✗ | ✓ | ✓ | ✓ |
| `KindAuth` | ✗ | ✓ | ✓ | ✓ |
| `KindTimeout` | ✗ | ✓ | ✓ (免费凁证除外) | ✓ |
| `KindTransient` | ✗ | ✗ (暂不写) | ✗ | ✗ |

---

### 阶段5: 数据库状态更新（credentialhealth.Checker）

**文件**: `credentialhealth/checker.go`

#### 5.1 持续失败检测

```go
func (c *Checker) CheckAndUpdate(ctx context.Context, credentialID int, model string) error {
    // 5.1.1 获取最近1小时的失败记录
    entries, err := c.recorder.GetRecent(ctx, credentialID, model, since)
    
    // 5.1.2 计算失败率（排除网络错误、客户端错误等）
    var total, failed int
    for _, e := range entries {
        // 跳过不应计入的错误
        if e.ErrorKind == "network" ||
           e.ErrorKind == string(errorsx.KindCanceled) ||
           e.ErrorKind == string(errorsx.KindTransient) ||
           e.ErrorKind == string(errorsx.KindEmptyResponse) ||
           errorsx.IsClientBug(errorsx.ErrorKind(e.ErrorKind)) {
            continue  // ← model_not_found 不会被跳过
        }
        total++
        if !e.Success {
            failed++
        }
    }
    
    failureRate := float64(failed) / float64(total)
    
    // 5.1.3 超过阈值（默认80%）→ 标记为 degraded
    if failureRate >= c.failureThreshold {
        // 5.1.4 探测验证（如果配置了prober）
        if c.prober != nil {
            result := c.prober.ProbeCredential(ctx, credentialID, model)
            if result.Success {
                return nil  // ← 探测成功，不标记degraded
            }
        }
        
        return c.markDegraded(ctx, credentialID, model, failureRate, errorKinds, total)
    }
}
```

#### 5.2 标记为 degraded

```go
func (c *Checker) markDegraded(ctx context.Context, credentialID int, model string, 
                                rate float64, kinds map[string]int, sampleSize int) error {
    recoverAt := time.Now().Add(c.degradedCooldown)  // 默认15分钟
    
    // 5.2.1 更新 credential_model_bindings
    tag, err := c.db.Exec(ctx, `
        UPDATE credential_model_bindings cmb
        SET available = FALSE,
            unavailable_reason = 'continuous_failure',
            unavailable_at = now(),
            unavailable_recover_at = $3,  -- ← 关键：设置恢复时间
            updated_at = now()
        FROM provider_models pm
        WHERE pm.id = cmb.provider_model_id
          AND cmb.credential_id = $1
          AND pm.canonical_raw_name = $2
          AND cmb.available = TRUE
          AND COALESCE(cmb.admin_protected, FALSE) = FALSE
    `, credentialID, model, recoverAt)
    
    // 5.2.2 同步到 model_offers
    // 5.2.3 清空候选缓存
    if c.invalidateCache != nil {
        c.invalidateCache(credentialID)
    }
}
```

---

### 阶段6: 路由过滤（router.PlanCandidates）

**文件**: `domains/streaming/executors/router.go`

#### 6.1 过滤流程

```go
func (r *Router) PlanCandidates(candidates []provider.Candidate, ...) []provider.Candidate {
    // 6.1.1 使用状态管理器过滤
    var available []provider.Candidate
    if r.StateManager != nil && r.StateManager.Enabled() {
        ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
        defer cancel()
        available = r.filterAvailableWithStateManager(ctx, candidates)
    } else {
        available = filterAvailable(candidates)  // ← 回退到数据库状态
    }
    
    // 6.1.2 所有候选者都不可用？
    if len(available) == 0 {
        // 6.1.3 尝试降级模式（单候选者场景）
        if len(candidates) <= 2 {
            degradedCandidates := r.tryDegradedMode(queryCtx, candidates)
            if len(degradedCandidates) > 0 {
                return degradedCandidates  // ← 问题：可能强制使用限流的凭证
            }
        }
        return nil  // ← 返回空，触发 "no available providers"
    }
    
    // 6.1.4 继续路由选择...
    return available
}
```

#### 6.2 降级模式判断（关键问题所在）

```go
func (r *Router) tryDegradedMode(ctx context.Context, candidates []provider.Candidate) []provider.Candidate {
    var degradedCandidates []provider.Candidate
    
    for _, c := range candidates {
        reason := c.UnavailableReason()  // ← 从数据库读取
        
        // 6.2.1 如果数据库没有标记，查询内存状态
        if reason == "" && r.StateManager != nil && r.StateManager.Enabled() {
            if _, smReason := r.StateManager.IsAvailable(ctx, c.CredentialID, c.RawModel); smReason != "" {
                reason = "state:" + smReason
            }
        }
        
        // 6.2.2 判断是否为瞬态原因
        if isTransientUnavailableReason(reason) {
            degradedCandidates = append(degradedCandidates, c)  // ← 降级使用
        }
    }
    
    return degradedCandidates
}
```

#### 6.3 瞬态原因判断（核心问题）

```go
func isTransientUnavailableReason(reason string) bool {
    switch reason {
    case "availability:cooling",
         "availability:rate_limited",      // ← 问题：限流被视为瞬态
         "availability:suspended":
        return true
    case "state:" + string(errorsx.KindTimeout),
         "state:" + string(errorsx.KindStreamTimeout),
         "state:" + string(errorsx.KindRateLimit),  // ← 问题：内存态限流也被视为瞬态
         "state:" + string(errorsx.KindUpstreamDown),
         "state:" + string(errorsx.KindEmptyResponse),
         "state:probe_direct_timeout":
        return true
    default:
        return false
    }
}
```

**问题分析**:
- `availability:rate_limited` 和 `state:rate_limit` 都被视为"瞬态"
- 单候选者场景下，降级模式会**强制使用限流的凭证**
- 导致智谱AI超限后仍在发送请求

---

### 阶段7: 探测与恢复

#### 7.1 主动探测（bg.NodeProbeWorker）

**文件**: `bg/node_probe.go:510`

```go
func (w *NodeProbeWorker) ProbeSync(ctx context.Context, 
                                     candidates []credentialstate.NoCandidatesCandidate,
                                     tenantID string, parentReqID string) bool {
    // 7.1.1 对每个候选者进行探测
    for _, c := range candidates {
        key := fmt.Sprintf("%d|%s", c.CredentialID, c.RawModel)
        
        // 7.1.2 直连上游探测
        res.direct = w.probeDirect(ctx, c.CredentialID, c.RawModel)
        
        if res.direct.ok {
            // 7.1.3 探测成功 → 立即恢复
            w.updateBindingAvailability(ctx, c.CredentialID, c.RawModel, true, "")
            w.updateCredentialHealth(ctx, c.CredentialID)
            
            // 7.1.4 再通过网关探测
            res.gateway = w.probeGateway(ctx, c.CredentialID, c.RawModel)
        } else {
            // 7.1.5 探测失败 → 延长冷却
            w.updateBindingAvailability(ctx, c.CredentialID, c.RawModel, false, res.direct.errCode)
            recoverAt := time.Now().Add(5 * time.Minute)
            w.updateObservedState(ctx, c.CredentialID, c.RawModel, false, res.direct.errCode, recoverAt)
        }
    }
}
```

#### 7.2 探测使用的模型（回答问题3）

```go
func (w *NodeProbeWorker) probeDirect(ctx context.Context, credID int, model string) nodeProbeRoundResult {
    // 7.2.1 查询模型配置
    plain, outboundModel, baseURL, protocol, providerID, err := w.resolveDirectTarget(ctx, credID, model)
    
    // 7.2.2 使用 outbound_model_name（如果有），否则使用 raw_model_name
    bodyModel := outboundModel
    if bodyModel == "" {
        bodyModel = model  // ← 使用传入的 model 参数（即失败请求的模型）
    }
    
    // 7.2.3 构造探测请求
    body := directProbeBody(bodyModel, protocol)
    // ...
}
```

**SQL查询**:
```sql
SELECT c.secret_ciphertext,
       COALESCE(NULLIF(pm.outbound_model_name, ''), pm.raw_model_name, ''),
       p.base_url, COALESCE(p.protocol, 'openai-completions'), p.id
FROM credentials c
JOIN providers p ON p.id = c.provider_id
JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE c.id = $1 AND pm.raw_model_name = $2  -- ← 使用失败请求的模型名
```

**结论**: 探测机制**确实使用原请求失败的模型**，逻辑正确 ✓

#### 7.3 自动恢复（credentialhealth.RecoverExpired）

**文件**: `credentialhealth/checker.go:307`

```go
func RecoverExpired(ctx context.Context, db DBQuerier) (int, error) {
    // 7.3.1 查询过期的冷却记录
    cmbTag, err := db.Exec(ctx, `
        UPDATE credential_model_bindings cmb
        SET available = TRUE,
            unavailable_reason = NULL,
            unavailable_at = NULL,
            unavailable_recover_at = NULL,
            updated_at = now()
        FROM provider_models pm
        WHERE pm.id = cmb.provider_model_id
          AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
          AND cmb.unavailable_reason <> 'model_probe_broken'
          AND COALESCE(cmb.admin_protected, FALSE) = FALSE
          AND COALESCE(cmb.unavailable_recover_at,
                       cmb.unavailable_at + INTERVAL '30 seconds') < now()
    `)
    
    // 7.3.2 同步恢复 model_offers 和 credentials.availability_state
    // ...
}
```

**问题**: 
- 恢复时**没有探测验证**
- 如果上游仍在限流，会立即再次失败 → 再次冷却 → 循环往复

---

## 三、问题根因总结

### 问题1: 智谱AI超限仍在发送请求

**根因链**:
```
1. 智谱AI返回: HTTP 429 或包含"model not found"的响应
   ↓
2. 错误分类: 
   - 如果是标准429 → KindRateLimit ✓
   - 如果body包含"model not found" → KindModelNotFound（可能误判）
   ↓
3. 状态更新: 
   - KindRateLimit → 进入冷却（Available=false）
   - KindModelNotFound → 进入冷却（Available=false）
   ↓
4. 路由过滤: 
   - filterAvailable 过滤掉该凭证
   ↓
5. 降级模式: 
   - 单候选者 + reason="availability:rate_limited" → isTransientUnavailableReason=true
   - 强制使用该凭证 ← 问题！
```

### 问题2: 商汤限流但仍派发请求

**根因链**:
```
1. 商汤返回: HTTP 429
   ↓
2. 错误分类: KindRateLimit ✓
   ↓
3. 标记冷却: unavailable_recover_at = now() + 15min
   ↓
4. 15分钟后:
   - RecoverExpired 自动恢复（无探测）
   ↓
5. 新请求到达:
   - 路由选择商汤
   ↓
6. 再次429 → 循环往复
```

### 问题3: 火山引擎 glm-5.2 配置缺失

**根因**: 
- 数据库 `provider_models` 表中没有火山引擎的 glm-5.2 记录
- 已通过 SQL 脚本修复

---

## 四、关键配置参数

| 参数 | 位置 | 默认值 | 说明 |
|------|------|--------|------|
| `degradedCooldown` | `credentialhealth.Checker` | 15分钟 | 持续失败后的冷却期 |
| `failureThreshold` | `credentialhealth.Checker` | 0.80 (80%) | 失败率阈值 |
| `minSampleSize` | `credentialhealth.Checker` | 5 | 最小样本数 |
| `activeProbeThreshold` | `credentialstate.Manager` | 2 | 触发主动探测的连续失败次数 |
| `memCacheTTL` | `credentialstate.Manager` | 10秒 | 内存缓存TTL |
| `redisCacheTTL` | `credentialstate.Manager` | 5分钟 | Redis缓存TTL |

---

## 五、待验证的关键问题

### 5.1 智谱AI的实际响应格式

**需要抓取真实的错误响应**:
```bash
# 查询最近的智谱AI失败请求
SELECT 
    request_id,
    credential_id,
    client_model,
    error_kind,
    upstream_status_code,
    upstream_response_preview,
    created_at
FROM request_logs
WHERE provider_code = 'zhipuai'
  AND request_status = 'failure'
  AND created_at > now() - interval '24 hours'
ORDER BY created_at DESC
LIMIT 20;
```

**关键验证点**:
1. 超限时返回的 HTTP 状态码是什么？（429 还是 400/404？）
2. body 中的错误信息是什么？（是否包含"model not found"？）
3. error_kind 被分类为什么？（KindRateLimit 还是 KindModelNotFound？）

### 5.2 智谱AI的模型配置

```sql
-- 查询智谱AI的所有模型配置
SELECT 
    p.provider_code,
    pm.raw_model_name,
    pm.canonical_name,
    pm.outbound_model_name,
    COUNT(cmb.id) as binding_count
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
LEFT JOIN credential_model_bindings cmb ON cmb.provider_model_id = pm.id
WHERE p.provider_code = 'zhipuai'
GROUP BY p.provider_code, pm.raw_model_name, pm.canonical_name, pm.outbound_model_name
ORDER BY pm.raw_model_name;
```

**验证**:
- 是否有 `glm-5.2` 配置？
- `outbound_model_name` 是否正确？
- 是否有绑定到凭证？

### 5.3 降级模式触发频率

```sql
-- 查询降级模式使用的日志
-- (需要在代码中添加metric或日志)
```

**需要添加的监控**:
```go
// domains/streaming/executors/router.go:139
if len(degradedCandidates) > 0 {
    slog.Warn("router: degraded mode activated",
        "total_candidates", len(candidates),
        "degraded_count", len(degradedCandidates),
        "reasons", reasonCounts,
    )
    // 添加metric
    degradedModeActivations.WithLabelValues(
        candidates[0].ProviderCode,
        candidates[0].RawModel,
    ).Inc()
    return degradedCandidates
}
```

---

## 六、下一步行动

### 立即执行（P0）
1. ✅ 执行 SQL 脚本添加火山引擎 glm-5.2 配置
2. ⏳ 抓取智谱AI的真实错误响应（验证5.1）
3. ⏳ 检查智谱AI的模型配置（验证5.2）

### 待确认后执行（P1）
4. ⏳ 根据验证结果决定是否需要：
   - 修改错误分类逻辑（优先级调整）
   - 修改降级模式判断（移除限流）
   - 修改恢复逻辑（增加探测）

### 优化改进（P2）
5. ⏳ 添加降级模式监控metric
6. ⏳ 添加自动恢复前的探测机制
7. ⏳ 优化限流状态的持久化（使用Retry-After头）

---

**结论**: 不要急着改代码，先验证实际数据，确认根因后再精准修复。
