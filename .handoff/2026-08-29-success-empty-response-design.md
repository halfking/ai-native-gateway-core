# §4.4 Success with Empty Response Body 观测性设计

**日期:** 2026-08-29  
**状态:** 设计草案（待评审）  
**问题来源:** `.handoff/2026-08-29-section4-execution.md` §2.2  
**优先级:** P2（观测性增强，非功能性阻塞）

---

## §1 问题陈述

### 1.1 当前状态

**现象:** 非流式响应路径中，存在 `success=true` 但 `response_body` 为 `nil` 或空的情况，当前系统**没有专门的检测、metric 或告警**。

**唯一相关信号:**
- `domains/hooks/observability/telemetry/client.go:1492` 在持久化失败时 log 了 `has_response_body` 布尔标志
- Prometheus metrics 中 `has_response_body` 作为 **label**（非 counter），无法直接设置告警阈值

**业务影响:**
- 用户收到 HTTP 200 但响应体为空，体验为"静默失败"
- 系统认为请求成功，计费、统计、重试逻辑全部基于 `success=true`
- 无法及时发现上游或网关的序列化/传输故障

### 1.2 根本原因分析

**可能触发场景:**

| 场景 | 触发路径 | success | response_body | 当前行为 |
|------|----------|---------|---------------|----------|
| **上游返回空 JSON** | Upstream 返回 `{}` 或 `{"choices":[]}` | true | `{}` | 被认为成功 |
| **流式空响应** | 上游发送 `[DONE]` 但无 content | false | nil/empty | 已检测：`empty_response` (line 5470-5478) |
| **非流式空响应** | executeOpenAI 返回 200 但 body 未填充 | true | nil | **未检测** ⚠️ |
| **响应截断** | 网络中断导致 body 读取失败 | varies | nil/partial | 可能被标记为失败 |
| **序列化失败** | JSON marshal error，body 未写入 | true | nil | **未检测** ⚠️ |

**差异:** 流式路径有显式的 `detectEmptyStreamResponse` 检测（line 5470），非流式路径**缺失对应的检测**。

---

## §2 设计目标

### 2.1 核心目标

1. **检测能力:** 在请求完成时同步检测 `success=true && response_body missing` 异常
2. **可观测性:** 通过结构化日志 + Prometheus metrics 暴露该失败模式
3. **隔离原则:** 检测失败**不改变** request settlement 或已发送给客户端的响应（遵循 §5.4 原则）
4. **覆盖缺口:** 补齐非流式路径与流式路径的检测对称性

### 2.2 非目标

- **不修复根因:** 本设计仅添加观测，不修复导致空响应的上游/序列化 bug
- **不重试:** 检测到异常后不触发自动重试（可能响应已发送给客户端）
- **不改变 success 标志:** 为避免影响计费和现有逻辑，暂不修改 `reqLog.Success = false`（待讨论）

---

## §3 设计方案

### 3.1 架构概览

```
┌────────────────────────────────────────────────────────────┐
│         Request Completion (handler.go)                     │
│                                                              │
│  1. executeOpenAI / executeStream → result                  │
│  2. Build reqLog (success, response_body, tokens, ...)      │
│  3. [NEW] detectEmptyNonStreamResponse(reqLog)              │
│       ↓                                                      │
│       if success && response_body missing:                  │
│         - slog.Warn(...)                                    │
│         - metrics.SuccessEmptyResponseTotal.Inc()           │
│         - (optional) reqLog.QualityFlags += "empty_body"    │
│  4. Persist reqLog (unchanged)                              │
│  5. emitTelemetry (unchanged)                               │
└────────────────────────────────────────────────────────────┘
```

### 3.2 同步检测路径（推荐）

#### 3.2.1 检测函数

```go
// domains/streaming/handler.go

// detectEmptyNonStreamResponse checks for the anomalous pattern where a
// non-streaming request is marked as successful but has no response body.
// This mirrors the empty-response detection for streams (line 5470-5478).
//
// Per §5.4 principle (audit-24h-20260828), detection does NOT modify
// settlement or already-sent responses; it only logs and emits metrics.
func detectEmptyNonStreamResponse(reqLog *telemetry.LogEntry) bool {
	if reqLog == nil {
		return false
	}
	
	// Only check non-streaming successful requests
	if !reqLog.Success {
		return false
	}
	if reqLog.Streaming != nil && *reqLog.Streaming {
		return false // Streaming has its own detector
	}
	
	// Missing or empty response_body
	if reqLog.ResponseBody == nil || *reqLog.ResponseBody == "" || *reqLog.ResponseBody == "{}" {
		return true
	}
	
	return false
}
```

#### 3.2.2 集成位置

**选项 A: 在 `emitTelemetry` 之前（推荐）**

```go
// domains/streaming/handler.go (around line 5500, after detectUpstreamContextLoss)

// [NEW] Detect empty response body on non-streaming success paths.
// Mirrors the streaming empty-response detector (line 5470-5478).
if !isStream && reqLog.Success {
	if detectEmptyNonStreamResponse(reqLog) {
		slog.Warn("success_with_empty_response_body",
			"request_id", reqLog.RequestID,
			"tenant_id", reqLog.TenantID,
			"model", reqLog.Model,
			"client_model", reqLog.ClientModel,
			"provider", reqLog.Provider,
			"has_response_body", reqLog.ResponseBody != nil,
			"response_body_len", func() int {
				if reqLog.ResponseBody == nil {
					return 0
				}
				return len(*reqLog.ResponseBody)
			}(),
		)
		
		// Emit Prometheus metric
		if h.metrics != nil {
			h.metrics.SuccessEmptyResponseTotal.WithLabelValues(
				reqLog.TenantID,
				reqLog.Model,
				reqLog.Provider,
			).Inc()
		}
		
		// (Optional) Add quality flag for downstream analysis
		reqLog.QualityFlags = append(reqLog.QualityFlags, QualityFlagEmptyResponseBody)
		
		// Decision point: should we flip success to false?
		// Option 1 (conservative): Keep success=true, only observe
		// Option 2 (strict): Mark as failure similar to streaming path
		//   reqLog.Success = false
		//   reqLog.RequestStatus = strPtr(telemetry.RequestStatusFailure)
		//   reqLog.ErrorKind = strPtr("empty_response")
		//   reqLog.FailureStage = strPtr("response_serialization")
	}
}
```

**选项 B: 在 persistence 后（非阻塞）**

如果不想在主路径增加延迟，可以在 `emitTelemetry` 内部异步检测，但这样无法修改 `reqLog` 字段。

#### 3.2.3 Prometheus Metrics

**新增 counter:**

```go
// domains/hooks/observability/metrics/metrics.go

type Metrics struct {
	// ... 现有 metrics ...
	
	// SuccessEmptyResponseTotal counts non-streaming requests marked as
	// successful but with missing or empty response_body.
	SuccessEmptyResponseTotal *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	return &Metrics{
		// ... 现有初始化 ...
		
		SuccessEmptyResponseTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_success_empty_response_total",
				Help: "Non-streaming successful requests with empty response body",
			},
			[]string{"tenant_id", "model", "provider"},
		),
	}
}

func (m *Metrics) Register(reg prometheus.Registerer) error {
	// ... 现有注册 ...
	if err := reg.Register(m.SuccessEmptyResponseTotal); err != nil {
		return err
	}
	return nil
}
```

**Dashboard query (PromQL):**

```promql
# 每分钟空响应率
rate(llm_gateway_success_empty_response_total[5m])

# 空响应占比（vs 所有成功请求）
rate(llm_gateway_success_empty_response_total[5m])
/ 
rate(llm_gateway_requests_total{status="success"}[5m])

# 按 provider 聚合
sum by (provider) (rate(llm_gateway_success_empty_response_total[5m]))
```

**告警规则示例:**

```yaml
- alert: HighSuccessEmptyResponseRate
  expr: |
    (
      rate(llm_gateway_success_empty_response_total[5m])
      / 
      rate(llm_gateway_requests_total{status="success"}[5m])
    ) > 0.01  # 1% 阈值
  for: 5m
  labels:
    severity: warning
    component: llm_gateway
  annotations:
    summary: "High rate of successful requests with empty response body"
    description: "{{ $labels.provider }}/{{ $labels.model }} has {{ $value | humanizePercentage }} empty responses in the last 5 minutes"
```

---

### 3.3 异步扫描路径（补充）

**目的:** 捕获同步路径遗漏的边缘情况（如持久化后才发现的空响应）

#### 3.3.1 扫描 Job

```go
// internal/jobs/empty_response_scanner.go

type EmptyResponseScanner struct {
	db      *pgxpool.Pool
	metrics *metrics.Metrics
}

// ScanEmptyResponses runs a daily aggregation query to detect success=true
// but response_body is NULL/empty in the request_logs table.
func (s *EmptyResponseScanner) ScanEmptyResponses(ctx context.Context) error {
	query := `
		SELECT 
			tenant_id,
			model,
			provider,
			COUNT(*) as count,
			DATE(ts) as date
		FROM request_logs
		WHERE success = true
		  AND ts >= NOW() - INTERVAL '24 hours'
		  AND (
		      response_body IS NULL 
		      OR response_body = '' 
		      OR response_body = '{}'
		  )
		GROUP BY tenant_id, model, provider, DATE(ts)
	`
	
	rows, err := s.db.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("scan empty responses: %w", err)
	}
	defer rows.Close()
	
	for rows.Next() {
		var tenant, model, provider string
		var count int
		var date time.Time
		
		if err := rows.Scan(&tenant, &model, &provider, &count, &date); err != nil {
			slog.Warn("scan row error", "error", err)
			continue
		}
		
		slog.Warn("daily_empty_response_aggregation",
			"tenant_id", tenant,
			"model", model,
			"provider", provider,
			"count", count,
			"date", date.Format("2006-01-02"),
		)
		
		// Emit aggregated metric (gauge, not counter)
		if s.metrics != nil {
			s.metrics.DailyEmptyResponseCount.WithLabelValues(
				tenant, model, provider,
			).Set(float64(count))
		}
	}
	
	return rows.Err()
}
```

#### 3.3.2 调度

```go
// cmd/gateway/main.go

// Schedule daily scan at 02:00 UTC
go func() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	
	// Initial run after 2 hours
	time.Sleep(2 * time.Hour)
	
	for {
		if err := emptyResponseScanner.ScanEmptyResponses(context.Background()); err != nil {
			slog.Error("empty response scan failed", "error", err)
		}
		<-ticker.C
	}
}()
```

**注意:** 异步扫描的 metric 是 **gauge**（快照值），与同步路径的 **counter** 互补。

---

## §4 关键决策点

### 4.1 是否修改 `success` 标志？

**选项 A: 保持 `success=true`（保守，推荐短期）**

**理由:**
- 响应可能已发送给客户端，修改 `success` 无法撤回
- 避免影响现有的计费、统计、dashboard 逻辑
- 仅作为观测增强，不改变核心业务逻辑

**实现:**
```go
// 仅 log + metric，不修改 reqLog.Success
slog.Warn("success_with_empty_response_body", ...)
metrics.SuccessEmptyResponseTotal.Inc()
reqLog.QualityFlags = append(reqLog.QualityFlags, QualityFlagEmptyResponseBody)
```

**选项 B: 修改为 `success=false`（严格，对齐流式路径）**

**理由:**
- 与流式路径的 `detectEmptyStreamResponse` 行为一致（line 5473: `reqLog.Success = false`）
- 更准确地反映实际结果：空响应不应该被计为成功
- 触发重试逻辑（如果 failover 策略支持）

**实现:**
```go
reqLog.Success = false
reqLog.RequestStatus = strPtr(telemetry.RequestStatusFailure)
reqLog.ErrorKind = strPtr("empty_response")
reqLog.FailureStage = strPtr("response_serialization")
reqLog.FailureDetailCode = strPtr("success_empty_body")
```

**风险:**
- 如果响应已写入客户端（HTTP 200 已发送），客户端看到的是成功但内部标记为失败，造成指标不一致
- 计费逻辑可能受影响（如果基于 `success` 标志）

**推荐:** 短期采用选项 A，观察数据后再决定是否切换到选项 B。

---

### 4.2 检测粒度：何为"空"？

**定义选项:**

| 定义 | 检测条件 | 误报风险 |
|------|----------|----------|
| **Strict (严格)** | `response_body == nil` | 低 — 只捕获真正的 nil |
| **Moderate (中等)** | `response_body == nil OR == ""` | 中 — 捕获空字符串 |
| **Loose (宽松)** | `response_body == nil OR == "" OR == "{}"` | 高 — 可能误报合法的空 JSON |

**推荐:** 采用 **Moderate**（中等），检测 `nil` 和空字符串，但**保留 `{}` 判断为业务决策**。

**实现:**

```go
func detectEmptyNonStreamResponse(reqLog *telemetry.LogEntry) bool {
	if reqLog == nil || !reqLog.Success {
		return false
	}
	if reqLog.Streaming != nil && *reqLog.Streaming {
		return false
	}
	
	// Moderate: nil or empty string
	if reqLog.ResponseBody == nil || *reqLog.ResponseBody == "" {
		return true
	}
	
	// Optional (strict mode): also check for "{}" or "[]"
	// Disabled by default to avoid false positives for legitimately empty arrays
	/*
	body := strings.TrimSpace(*reqLog.ResponseBody)
	if body == "{}" || body == "[]" {
		return true
	}
	*/
	
	return false
}
```

---

### 4.3 Label 基数控制

**问题:** Prometheus labels 会产生高基数，影响性能

**当前方案:**
```go
SuccessEmptyResponseTotal.WithLabelValues(tenant_id, model, provider)
```

**Label 基数估算:**
- `tenant_id`: ~1000（高基数）
- `model`: ~50
- `provider`: ~20
- 组合: ~1,000,000 个 time series

**优化选项:**

**选项 1: 移除 `tenant_id` label（推荐）**

```go
SuccessEmptyResponseTotal.WithLabelValues(model, provider)
```

基数降至 ~1000，tenant 级别的分析通过日志实现。

**选项 2: Tenant ID 采样**

只对特定 tenant 或采样 10% 的 tenant 启用 label：

```go
if shouldSampleTenant(reqLog.TenantID) {
	metrics.SuccessEmptyResponseTotal.WithLabelValues(tenant, model, provider).Inc()
} else {
	metrics.SuccessEmptyResponseTotal.WithLabelValues("_sampled_out", model, provider).Inc()
}
```

**选项 3: 使用 exemplars**

Prometheus exemplars 允许在不增加 label 基数的情况下关联日志：

```go
metrics.SuccessEmptyResponseTotal.WithLabelValues(model, provider).Inc()
// Exemplar 包含 tenant_id + request_id，但不作为 label
```

**推荐:** 短期采用选项 1（移除 tenant label），长期考虑 exemplars。

---

## §5 实施计划

### Phase 1: 同步检测（1-2 天）

1. **实现检测函数** `detectEmptyNonStreamResponse`
2. **集成到 handler.go** 在 `emitTelemetry` 前调用
3. **添加结构化日志** `slog.Warn("success_with_empty_response_body", ...)`
4. **添加 Prometheus metric** `llm_gateway_success_empty_response_total`
5. **单元测试** 验证检测逻辑

**Commit 消息示例:**
```
feat(observability): detect success with empty response body (§4.4)

- Add detectEmptyNonStreamResponse to mirror streaming empty detection
- Emit slog.Warn + Prometheus counter when success && response_body missing
- Add QualityFlagEmptyResponseBody to reqLog for downstream analysis
- Preserves success=true (conservative approach, observability only)

Addresses .handoff/2026-08-29-section4-execution.md §2.2
Design: .handoff/2026-08-29-success-empty-response-design.md
```

### Phase 2: Dashboard & Alerts（1 天）

1. **Grafana dashboard** 添加 panel 显示空响应率
2. **告警规则** 设置 1% 阈值（基于历史数据调优）
3. **告警路由** 配置 Slack/PagerDuty 通知

### Phase 3: 异步扫描（可选，2 天）

1. **实现扫描 job** `EmptyResponseScanner`
2. **调度器集成** 每日 02:00 UTC 运行
3. **日聚合 metric** `llm_gateway_daily_empty_response_count` (gauge)

### Phase 4: 数据驱动优化（持续）

1. **观察 1 周数据** 确定空响应的实际占比和模式
2. **根因分析** 识别高发的 provider/model 组合
3. **决策点复审:**
   - 是否需要修改 `success=false`？（§4.1）
   - 是否需要收紧检测条件？（§4.2）
   - 是否需要触发自动重试？

---

## §6 测试计划

### 6.1 单元测试

```go
// domains/streaming/handler_test.go

func TestDetectEmptyNonStreamResponse(t *testing.T) {
	tests := []struct {
		name     string
		reqLog   *telemetry.LogEntry
		wantTrue bool
	}{
		{
			name: "success_with_nil_body",
			reqLog: &telemetry.LogEntry{
				Success:      true,
				Streaming:    ptrBool(false),
				ResponseBody: nil,
			},
			wantTrue: true,
		},
		{
			name: "success_with_empty_string_body",
			reqLog: &telemetry.LogEntry{
				Success:      true,
				Streaming:    ptrBool(false),
				ResponseBody: ptrStr(""),
			},
			wantTrue: true,
		},
		{
			name: "success_with_valid_body",
			reqLog: &telemetry.LogEntry{
				Success:      true,
				Streaming:    ptrBool(false),
				ResponseBody: ptrStr(`{"choices":[{"message":{"content":"hello"}}]}`),
			},
			wantTrue: false,
		},
		{
			name: "failure_with_nil_body",
			reqLog: &telemetry.LogEntry{
				Success:      false,
				Streaming:    ptrBool(false),
				ResponseBody: nil,
			},
			wantTrue: false, // Not detected because success=false
		},
		{
			name: "streaming_with_nil_body",
			reqLog: &telemetry.LogEntry{
				Success:      true,
				Streaming:    ptrBool(true),
				ResponseBody: nil,
			},
			wantTrue: false, // Not detected because streaming=true (has own detector)
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectEmptyNonStreamResponse(tt.reqLog)
			if got != tt.wantTrue {
				t.Errorf("detectEmptyNonStreamResponse() = %v, want %v", got, tt.wantTrue)
			}
		})
	}
}
```

### 6.2 集成测试

```go
func TestEmptyResponseMetricEmission(t *testing.T) {
	// Setup: 创建带 mock metrics 的 handler
	metrics := metrics.NewMetrics()
	registry := prometheus.NewRegistry()
	metrics.Register(registry)
	
	handler := &Handler{
		metrics: metrics,
		// ... other fields ...
	}
	
	// Simulate: 处理一个空响应的请求
	reqLog := &telemetry.LogEntry{
		RequestID:    "test-123",
		TenantID:     "tenant-a",
		Model:        "gpt-4",
		Provider:     "openai",
		Success:      true,
		Streaming:    ptrBool(false),
		ResponseBody: nil,
	}
	
	// Trigger detection (in real code this happens in emitTelemetry path)
	if detectEmptyNonStreamResponse(reqLog) {
		metrics.SuccessEmptyResponseTotal.WithLabelValues(
			reqLog.Model, reqLog.Provider,
		).Inc()
	}
	
	// Assert: metric incremented
	metricFamilies, _ := registry.Gather()
	found := false
	for _, mf := range metricFamilies {
		if mf.GetName() == "llm_gateway_success_empty_response_total" {
			found = true
			if len(mf.GetMetric()) == 0 {
				t.Fatal("metric registered but no samples")
			}
			if mf.GetMetric()[0].GetCounter().GetValue() != 1 {
				t.Errorf("metric value = %v, want 1", mf.GetMetric()[0].GetCounter().GetValue())
			}
		}
	}
	if !found {
		t.Fatal("metric llm_gateway_success_empty_response_total not found")
	}
}
```

---

## §7 风险与缓解

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|----------|
| **误报：合法的空响应** | 告警噪音 | 中 | 调优检测条件（§4.2），观察 1 周数据 |
| **高基数导致 Prometheus 过载** | 查询变慢，存储增长 | 中 | 移除 tenant_id label（§4.3） |
| **同步检测增加延迟** | 请求响应变慢 | 低 | 检测逻辑简单（<1ms），可忽略 |
| **与现有 success 语义冲突** | 计费/统计错乱 | 高 | 短期保持 success=true，仅观测（§4.1） |
| **异步扫描数据库负载** | DB 慢查询 | 低 | 限制扫描时间窗口（24h），添加索引 |

---

## §8 ADR 编写建议

如果本设计被 approve，建议编写正式 ADR：

**文件:** `docs/adr/2026-08-29-success-empty-response-body.md`

**内容大纲:**

1. **Status:** Proposed
2. **Context:** 非流式成功响应缺少空 body 检测
3. **Decision:**
   - 添加 `detectEmptyNonStreamResponse` 函数
   - 在 handler.go 集成，emit slog + Prometheus metric
   - 暂不修改 `success` 标志（保守观测）
   - Label 设计：`{model, provider}`（去掉 tenant_id）
4. **Consequences:**
   - 可观测性提升，可设置告警
   - 不影响现有计费/统计逻辑
   - 未来可升级为修改 success 标志或触发重试
5. **Alternatives considered:**
   - 仅异步扫描 → 延迟高，无法实时告警
   - 修改 success=false → 影响现有逻辑，风险大

---

## §9 待解决问题

### 9.1 Response body 何时为 nil？

**需要调查:**
- `executeOpenAI` 的哪些路径会返回 `result.ResponseBody == nil`？
- 是否有合法的空响应场景（如 204 No Content）？

**行动:** 在实施前先运行探索性查询：

```sql
SELECT 
	model, 
	provider, 
	COUNT(*) as count
FROM request_logs
WHERE success = true
  AND streaming = false
  AND (response_body IS NULL OR response_body = '')
  AND ts >= NOW() - INTERVAL '7 days'
GROUP BY model, provider
ORDER BY count DESC
LIMIT 20;
```

### 9.2 Telemetry owner 确认

**阻塞点（原 handoff 提到）:**
> 涉及 metric schema，需要先与 telemetry/alert 路由对齐

**行动:**
- 与 telemetry owner 确认 metric 命名规范
- 确认 Grafana dashboard 位置和更新权限
- 确认告警路由配置（Slack channel / PagerDuty）

---

## §10 参考

- **问题来源:** `.handoff/2026-08-29-section4-execution.md` §2.2
- **流式检测实现:** `domains/streaming/handler.go:5470-5478` (`detectEmptyStreamResponse`)
- **Telemetry 客户端:** `domains/hooks/observability/telemetry/client.go:1492`
- **§5.4 原则（不影响 settlement）:** `docs/adr/2026-08-28-requestjourney-journal-snapshot.md` §Decision point 5
