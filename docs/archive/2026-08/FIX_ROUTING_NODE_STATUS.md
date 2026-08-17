# 路由节点状态修复实施方案

> **创建日期**: 2026-08-13  
> **目标**: 修复 "No available provider. All 0 candidates" 问题  
> **预计完成**: 4-6 小时

---

## 修复清单

### Phase 1: P0 紧急修复 (2-3 小时)

#### 修复 1: URSM v2 Ready Gate Fail-Open

**文件**: `domains/ursm/v2/manager.go`

**位置**: 第 379-386 行

**修改内容**:
```diff
--- a/domains/ursm/v2/manager.go
+++ b/domains/ursm/v2/manager.go
@@ -376,13 +376,23 @@ func (m *Manager) filterAndScore(ctx context.Context, seeds []CandidateSeed, re
-	// A false readiness snapshot must always fall back, including when the
-	// process mirror contains every requested node. The mirror is only a
-	// read accelerator; it cannot bypass the recovery gate.
-	if !ready {
-		return nil, "", fmt.Errorf("ursm.v2: not ready")
-	}
+	// M2 Fail-Open Enhancement (2026-08-13): Allow LRU-only requests to
+	// succeed even when Ready=false, as long as EVERY seed resolved from
+	// the mirror. This is the "Redis unavailable → LRU mirror" fail-open
+	// path that prevents cascading failures when Redis has transient issues.
 	if len(missIndices) == 0 {
+		// All seeds resolved from mirror → serve from cache even if not ready
 		scoreAndSort(views, seeds, m.cfg.ScoringWeights)
-		return views, statesource.StateSourceNodeMirrorHit, nil
+		src := statesource.StateSourceNodeMirrorHit
+		if !ready {
+			// Distinguish "cache-only because Redis down" from "cache hit"
+			src = statesource.StateSourceNodeMirrorFallback
+			m.log.Warn("ursm.v2: serving from LRU cache while Redis unavailable",
+				"seed_count", len(seeds),
+				"cache_hit", len(seeds)-len(missIndices),
+			)
+		}
+		return views, src, nil
 	}
 
+	// Only reject when we NEED Redis but it's not ready
+	if !ready {
+		return nil, "", fmt.Errorf("ursm.v2: not ready (cache miss requires Redis)")
+	}
```

**验证**:
```bash
# 1. 编译
go build -o gateway cmd/gateway/main.go

# 2. 单元测试
go test ./domains/ursm/v2/... -v -run TestFilterAndScore

# 3. 集成测试：模拟 Redis 不可达
# 在测试环境注入 Redis 延迟，验证请求仍可成功
```

---

#### 修复 2: 提升 LRU 缓存命中率

**文件 A**: `domains/ursm/v2/config.go`

**修改内容**:
```diff
--- a/domains/ursm/v2/config.go
+++ b/domains/ursm/v2/config.go
@@ -45,8 +45,10 @@ func DefaultConfig() Config {
 		RedisKeyPrefix:    "ursm:v2:",
 		Mode:              api.ModeOff,
-		LRUMirrorSize:     100_000,
-		LRUMirrorSoftTTL:  30 * time.Second,
+		// 2026-08-13: Increase LRU capacity and TTL to improve hit rate
+		// from ~20% to 85%+ in high-concurrency scenarios.
+		LRUMirrorSize:     300_000,  // 10万 → 30万
+		LRUMirrorSoftTTL:  60 * time.Second,  // 30s → 60s
 		CanaryPercent:     0,
 		CanaryTenants:     nil,
```

**文件 B**: `domains/ursm/v2/cache/nodemirror.go`

**修改内容**（可选，根据实际需求）:
```diff
--- a/domains/ursm/v2/cache/nodemirror.go
+++ b/domains/ursm/v2/cache/nodemirror.go
@@ -50,7 +50,10 @@ type NodeMirror struct {
 
 // GetForTenant retrieves a node view for the given tenant, credential, and model.
 func (m *NodeMirror) GetForTenant(tenantID string, credID int, model string) (NodeView, bool) {
-	key := fmt.Sprintf("%s:%d:%s", tenantID, credID, model)
+	// 2026-08-13: Remove tenant dimension from cache key to improve hit rate.
+	// Node availability is not tenant-specific in URSM v2 (tenant isolation
+	// is handled at quota/billing level, not routing level).
+	key := fmt.Sprintf("%d:%s", credID, model)
 	m.mu.RLock()
 	defer m.mu.RUnlock()
```

**注意**: 去除租户维度需要评估业务影响，建议先只增加容量和 TTL。

**验证**:
```bash
# 1. 编译
go build -o gateway cmd/gateway/main.go

# 2. 启动服务，观察缓存命中率
# 在 Prometheus 监控：llmgw_ursm_v2_cache_hit_rate

# 3. 预期：命中率从 20% 提升到 60%+ (仅增加容量+TTL)
#          或提升到 85%+ (同时去除租户维度)
```

---

#### 修复 3: 降级模式总是触发

**文件**: `domains/streaming/executors/router.go`

**位置**: 第 274 行

**修改内容**:
```diff
--- a/domains/streaming/executors/router.go
+++ b/domains/streaming/executors/router.go
@@ -271,15 +271,21 @@ func (r *Router) PlanCandidatesWithContext(
 
-		// 2026-07-24 Phase 1: 在 authoritative 模式下也保留降级模式。
-		// 降级模式是保护机制，用于处理瞬态故障导致的完全失败。
-		// URSM v2 authoritative 模式下的冷却决策仍在生效，降级只是最后的保护。
-		if len(candidates) <= 2 {
+		// 2026-08-13 Enhancement: Always try degraded mode when no candidates
+		// are available, regardless of the original candidate count. The
+		// previous `len(candidates) <= 2` guard was too conservative —
+		// a model with 5 credentials could have all 5 marked as cooling
+		// (len(available)==0) but would not trigger degraded mode.
+		if len(available) == 0 {
 			degradedCandidates := r.tryDegradedMode(queryCtx, candidates)
 			if len(degradedCandidates) > 0 {
-				slog.Warn("router: degraded mode activated, using transiently unavailable candidates",
+				slog.Warn("router: degraded mode activated (zero available candidates)",
 					"total_candidates", len(candidates),
 					"degraded_count", len(degradedCandidates),
 					"reasons", reasonCounts,
 					"state_backend", stateBackend.Name(),
+					// Log the original unavailability reasons for audit
+					"sample_reasons", sampleReasons,
 				)
 				return degradedCandidates
 			}
```

**验证**:
```bash
# 1. 编译
go build -o gateway cmd/gateway/main.go

# 2. 单元测试
go test ./domains/streaming/executors/... -v -run TestDegradedMode

# 3. 集成测试：模拟所有候选节点冷却
# 验证降级模式被触发
```

---

### Phase 2: P1 重要修复 (2-3 小时)

#### 修复 4: 分级冷却策略

**文件**: `domains/ursm/v2/reducer/reducer.go` (需要先定位该文件)

**新增内容**:
```go
// FailureMode classifies failure types for graduated cooling.
type FailureMode int

const (
	FailureTransient  FailureMode = iota // Timeout, network jitter
	FailurePersistent                     // Consecutive failures, quota exhausted
	FailureFatal                          // Auth failure, model not found
)

// ClassifyFailure determines the failure mode based on error kind and streak.
func ClassifyFailure(errorKind string, failStreak int) FailureMode {
	switch errorKind {
	case "timeout", "network_error", "connection_reset":
		if failStreak < 3 {
			return FailureTransient
		}
		return FailurePersistent
	case "rate_limit", "quota_exceeded":
		return FailurePersistent
	case "auth_failed", "invalid_api_key", "model_not_found":
		return FailureFatal
	default:
		if failStreak < 2 {
			return FailureTransient
		}
		return FailurePersistent
	}
}

// CalculateCoolingDuration returns graduated cooling duration based on failure mode.
func CalculateCoolingDuration(mode FailureMode, failStreak int) time.Duration {
	switch mode {
	case FailureTransient:
		// Transient: 30s - 2min (progressive)
		base := 30 * time.Second
		increment := time.Duration(failStreak) * 30 * time.Second
		max := 2 * time.Minute
		if d := base + increment; d < max {
			return d
		}
		return max
	case FailurePersistent:
		// Persistent: 5min - 15min (progressive)
		base := 5 * time.Minute
		increment := time.Duration(failStreak) * 5 * time.Minute
		max := 15 * time.Minute
		if d := base + increment; d < max {
			return d
		}
		return max
	case FailureFatal:
		// Fatal: 1 hour (fixed)
		return 1 * time.Hour
	default:
		return 5 * time.Minute // fallback
	}
}
```

**应用到 Reducer**:
```diff
--- a/domains/ursm/v2/reducer/reducer.go
+++ b/domains/ursm/v2/reducer/reducer.go
@@ -100,7 +100,12 @@ func (r *Reducer) processFailure(node *Node, errorKind string) {
 	node.FailStreak++
 	node.LastFailedAt = r.now()
 	
-	coolDuration := 5 * time.Minute  // Fixed cooling
+	// 2026-08-13: Graduated cooling based on failure mode
+	mode := ClassifyFailure(errorKind, node.FailStreak)
+	coolDuration := CalculateCoolingDuration(mode, node.FailStreak)
+	
+	// Log for audit
+	log.Info("node entering cooling", "mode", mode, "duration", coolDuration, "streak", node.FailStreak)
 	
 	node.CoolUntil = r.now().Add(coolDuration)
 	node.Available = false
```

**验证**:
```bash
# 1. 单元测试
go test ./domains/ursm/v2/reducer/... -v

# 2. 集成测试：模拟不同失败模式
# - 偶发超时 → 30s 冷却
# - 连续失败 → 5min 冷却
# - 认证失败 → 1hour 冷却
```

---

#### 修复 5: 错误类型细化

**文件**: `domains/streaming/handler.go`

**新增函数**:
```go
// classifyNoProviderError analyzes candidate unavailability reasons and
// returns a more specific error code and message.
func classifyNoProviderError(candidates []provider.Candidate, lastKind string) (code, msg string) {
	if len(candidates) == 0 {
		return "model_not_configured", "Model not configured or no credentials available"
	}
	
	// Count unavailability reasons
	reasonCounts := make(map[string]int)
	for _, c := range candidates {
		reason := c.UnavailableReason()
		if reason == "" {
			reason = "unknown"
		}
		reasonCounts[reason]++
	}
	
	total := len(candidates)
	
	// Majority vote: if >50% have the same reason, use that
	if reasonCounts["state:cooling"] >= total/2 {
		return "temporarily_unavailable", 
			fmt.Sprintf("All providers in cooling period (%d/%d)", reasonCounts["state:cooling"], total)
	}
	if reasonCounts["rate_limit"] > 0 {
		return "rate_limit_exceeded", 
			fmt.Sprintf("Providers rate limited (%d/%d)", reasonCounts["rate_limit"], total)
	}
	if reasonCounts["quota_exceeded"] > 0 {
		return "quota_exceeded", 
			fmt.Sprintf("Providers quota exhausted (%d/%d)", reasonCounts["quota_exceeded"], total)
	}
	if reasonCounts["state:transient"] >= total/2 {
		return "service_degraded", 
			fmt.Sprintf("Providers experiencing transient issues (%d/%d)", reasonCounts["state:transient"], total)
	}
	
	// Fallback
	return "no_available_provider", 
		fmt.Sprintf("No available provider (%d candidates checked)", total)
}
```

**应用到错误处理**:
```diff
--- a/domains/streaming/handler.go
+++ b/domains/streaming/handler.go
@@ -850,10 +850,13 @@ func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
 			// X-Gateway-Last-Kind header.
+			// 2026-08-13: Classify the error more precisely
+			errCode, errMsg := classifyNoProviderError(candidates, string(execErrTyped.LastKind))
+			
 			logCtx.failAndMark(errorKindOrFallback(realKind),
-				fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErrTyped.Tried),
+				fmt.Sprintf("%s for model '%s'.", errMsg, clientModel),
 				providerID, credentialID)
 			h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, tried, modelResolution, txResult, errCode, failTrace, int(time.Since(startTime).Milliseconds()))
 			markLogged()
```

---

## 部署流程

### Step 1: 本地编译测试
```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

# 1. 应用修复 (手动编辑文件)
# 修复 1: domains/ursm/v2/manager.go
# 修复 2: domains/ursm/v2/config.go
# 修复 3: domains/streaming/executors/router.go

# 2. 编译
make build
# 或
go build -o gateway cmd/gateway/main.go

# 3. 运行单元测试
go test ./domains/ursm/v2/... -v
go test ./domains/streaming/executors/... -v

# 4. 本地启动验证
./gateway --config=config.example.yaml
```

### Step 2: 部署到 154
```bash
# 1. 备份当前版本
ssh root@8.136.114.154 -p 25022 "cp /opt/llm-gateway-go/gateway /opt/llm-gateway-go/gateway.backup.$(date +%Y%m%d_%H%M%S)"

# 2. 停止服务
ssh root@8.136.114.154 -p 25022 "systemctl stop llm-gateway-go"

# 3. 上传新版本
scp -P 25022 gateway root@8.136.114.154:/opt/llm-gateway-go/gateway

# 4. 启动服务
ssh root@8.136.114.154 -p 25022 "systemctl start llm-gateway-go"

# 5. 验证启动
ssh root@8.136.114.154 -p 25022 "systemctl status llm-gateway-go"

# 6. 观察日志
ssh root@8.136.114.154 -p 25022 "journalctl -u llm-gateway-go -f"
```

### Step 3: 监控验证
```bash
# 1. 检查指标（Prometheus）
# - llmgw_routing_state_source_total{source="node_mirror_fallback"} (新增，应 > 0)
# - llmgw_routing_no_candidates_total (应下降)
# - llmgw_request_error_total{code="503"} (应下降)
# - llmgw_ursm_v2_cache_hit_rate (应上升)

# 2. 检查日志
# 观察是否出现 "serving from LRU cache while Redis unavailable"
# 观察 "degraded mode activated" 触发频率

# 3. 对比修复前后
# - 成功率：应提升 5-10%
# - P99 延迟：应下降 30-50ms
# - "No available provider" 错误：应下降 80%+
```

---

## 回滚计划

如果修复后出现问题，立即回滚：

```bash
# 1. 停止服务
ssh root@8.136.114.154 -p 25022 "systemctl stop llm-gateway-go"

# 2. 恢复备份
ssh root@8.136.114.154 -p 25022 "cp /opt/llm-gateway-go/gateway.backup.* /opt/llm-gateway-go/gateway"

# 3. 启动服务
ssh root@8.136.114.154 -p 25022 "systemctl start llm-gateway-go"

# 4. 验证
ssh root@8.136.114.154 -p 25022 "systemctl status llm-gateway-go"
```

---

## 验收标准

### 功能验收
- [ ] Redis 短暂不可达时，LRU 缓存命中的请求仍可成功
- [ ] LRU 缓存命中率提升到 60%+ (仅容量+TTL) 或 85%+ (同时去租户维度)
- [ ] 所有候选节点不可用时，降级模式被触发
- [ ] 错误响应包含更详细的失败原因（cooling / rate_limit / quota_exceeded）

### 性能验收
- [ ] P99 延迟下降 30-50ms（减少 Redis 回源）
- [ ] "No available provider" 错误率下降 80%+
- [ ] 偶发故障场景成功率从 85% 提升到 95%+

### 监控验收
- [ ] Prometheus 指标 `llmgw_routing_state_source_total{source="node_mirror_fallback"}` 有数据
- [ ] LRU 缓存命中率指标上升
- [ ] 降级模式触发次数可观测

---

## 风险评估

| 风险 | 可能性 | 影响 | 缓解措施 |
|------|--------|------|----------|
| LRU 内存占用过高 | 中 | 中 | 监控内存使用，必要时降低容量 |
| 去租户维度影响业务 | 低 | 高 | 可选修改，先只增加容量+TTL |
| 降级模式返回不稳定节点 | 低 | 中 | 降级模式本身是保护机制，已有日志 |
| 分级冷却策略误判 | 低 | 低 | 保留详细日志，便于后续调优 |

---

## 后续优化

### Week 2
- [ ] 实现按模型维度探活
- [ ] 优化冷却期内定期探活机制

### Week 3
- [ ] 实现多级缓存 (L1 LRU + L2 Redis)
- [ ] 预测性节点恢复

### Week 4
- [ ] 全面压测验证
- [ ] 编写运维手册

---

**创建人**: LLM Gateway Team  
**审核人**: TBD  
**部署窗口**: 工作日 10:00-16:00
