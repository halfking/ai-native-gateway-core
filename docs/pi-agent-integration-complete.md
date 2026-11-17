# PI 智能体完整集成方案

## 1. 已完成的组件

### 1.1 核心基础设施 ✅
- **数据库 Schema** (`sql/migrations/startup/971_pi_error_analysis_system.sql`)
  - ★ **订正（2026-11-18 复核）**：原写作 `sql/migrations/domain/641_pi_error_analysis_system.sql` ——
    该路径是**死文件区**（`sql/migrations/domain/` 无一件登记进 installer StartupFiles / deploy 通道），
    迁移**永不落地**；且它非幂等（裸 CREATE INDEX / CREATE TRIGGER），重放会 42P07/42710 ⇒ 部署 die。
    提交 `c6827623d` 已把它收编为正典通道 `971`（七点登记 + 幂等 + 双门接管）⇒ 此处按 971 更正。
  - `error_analysis_queue` - 错误队列
  - `error_analysis_results` - 分析结果
  - 统计视图和自动化函数

### 1.2 错误收集器 ✅
- **文件**: `domains/promptinjection/error_collector.go`
- **功能**: 自动收集错误、去重、优先级管理
- **测试**: 15/15 通过

### 1.3 LLM 分析器 ✅
- **文件**: `domains/promptinjection/error_analyzer.go`
- **功能**: 批量分析、提示词模板、结果解析
- **测试**: 13/13 通过

### 1.4 LLM 客户端实现 ✅
- **文件**: `domains/promptinjection/llm_client_impl.go`
- **功能**: 通过网关 API 调用 LLM 进行分析
- **特性**: HTTP 回环调用、超时控制、错误处理

---

## 2. 集成到网关主流程

### 2.1 在 Detector 中注入错误收集器

需要修改 `domains/promptinjection/detector.go`，添加错误收集器：

```go
// Detector 提示词注入检测器
type Detector struct {
	db              *sql.DB
	basicRules      []*Rule
	advancedRules   []*Rule
	heuristicEngine *HeuristicEngine
	llmEngine       *LLMDetectionEngine
	canaryDetector  *CanaryDetector
	vectorMatcher   *VectorMatcher
	llmCacheMu      sync.RWMutex
	llmCache        map[string]*DetectionResult
	
	// 新增：错误收集器
	errorCollector  *ErrorCollector  // 添加此字段
}

// SetErrorCollector 设置错误收集器
func (d *Detector) SetErrorCollector(collector *ErrorCollector) {
	d.errorCollector = collector
}
```

### 2.2 在检测失败时收集错误

修改 `Detect` 方法，在 LLM 检测失败时自动收集：

```go
// 在 detector.go 的 Detect 方法中
func (d *Detector) Detect(ctx context.Context, tenantID, sessionID, requestID, input string) (*DetectionResult, error) {
	// ... 现有代码 ...
	
	// Layer 5: LLM 判官检测
	if policy.EnableLLMDetection && policy.LLMEngineID != nil {
		llmResult, err := d.llmEngine.Detect(ctx, *policy.LLMEngineID, input, timeout)
		if err != nil {
			slog.Warn("prompt_injection: LLM detection failed", "error", err)
			
			// 🆕 收集 LLM 检测失败错误
			if d.errorCollector != nil {
				_ = d.errorCollector.CollectLLMDetectionFailure(
					ctx,
					tenantID,
					sessionID,
					requestID,
					input,
					err,
					policy,
				)
			}
			
			// 原有的 fail-closed/fail-open 逻辑
			if policy.LLMDetectionFailClosed {
				// ...
			}
		} else if llmResult != nil {
			// ...
		}
	}
	
	// ... 其余代码 ...
}
```

### 2.3 在 main.go 中初始化完整系统

在 `cmd/gateway/main.go` 中添加初始化代码：

```go
package main

import (
	// ... 现有 imports ...
	"github.com/kaixuan/llm-gateway-go/domains/promptinjection"
)

func main() {
	// ... 现有代码 ...
	
	// ========================================
	// PI 错误分析系统初始化
	// ========================================
	
	// 1. 创建错误收集器
	piErrorCollector := promptinjection.NewErrorCollector(
		db,
		cfg.PIErrorCollectorEnabled, // 从配置读取
	)
	
	// 2. 创建 LLM 客户端
	var piLLMClient promptinjection.LLMClient
	if cfg.PIAnalyzerEnabled {
		piLLMClient = promptinjection.NewHTTPLLMClient(
			cfg.PIAnalyzerBaseURL,  // 例如: "http://localhost:8080"
			cfg.PIAnalyzerAPIKey,   // 网关的 API key
			cfg.PIAnalyzerModel,    // 例如: "claude-3-5-sonnet"
		)
	}
	
	// 3. 创建错误分析器
	piErrorAnalyzer := promptinjection.NewErrorAnalyzer(
		db,
		piErrorCollector,
		piLLMClient,
		cfg.PIAnalyzerModel,
		cfg.PIAnalyzerEnabled,
	)
	
	// 4. 将收集器注入到 Detector
	if piDetector != nil { // piDetector 是已有的检测器实例
		piDetector.SetErrorCollector(piErrorCollector)
	}
	
	// 5. 启动后台分析 Worker（如果启用）
	if cfg.PIAnalyzerEnabled {
		go runPIAnalyzerWorker(ctx, piErrorAnalyzer, cfg.PIAnalyzerIntervalMinutes)
	}
	
	// 6. 注册管理 API
	piAnalysisHandler := admin.NewPIAnalysisHandler(
		piErrorCollector,
		piErrorAnalyzer,
		db,
	)
	adminRouter.Mount("/pi-analysis", piAnalysisHandler.Routes())
	
	// ... 其余代码 ...
}

// runPIAnalyzerWorker 后台分析 Worker
func runPIAnalyzerWorker(ctx context.Context, analyzer *promptinjection.ErrorAnalyzer, intervalMinutes int) {
	if intervalMinutes <= 0 {
		intervalMinutes = 5 // 默认 5 分钟
	}
	
	ticker := time.NewTicker(time.Duration(intervalMinutes) * time.Minute)
	defer ticker.Stop()
	
	slog.Info("pi_analyzer_worker: started", "interval_minutes", intervalMinutes)
	
	for {
		select {
		case <-ticker.C:
			processed, err := analyzer.ProcessBatch(ctx)
			if err != nil {
				slog.Error("pi_analyzer_worker: batch processing failed", "error", err)
			} else if processed > 0 {
				slog.Info("pi_analyzer_worker: batch completed", "processed", processed)
			}
			
		case <-ctx.Done():
			slog.Info("pi_analyzer_worker: shutting down")
			return
		}
	}
}
```

---

## 3. 配置项

在 `config/config.go` 中添加配置项：

```go
type Config struct {
	// ... 现有配置 ...
	
	// PI 错误分析系统
	PIErrorCollectorEnabled   bool   `env:"LLM_GATEWAY_PI_ERROR_COLLECTOR_ENABLED" envDefault:"true"`
	PIAnalyzerEnabled         bool   `env:"LLM_GATEWAY_PI_ANALYZER_ENABLED" envDefault:"true"`
	PIAnalyzerBaseURL         string `env:"LLM_GATEWAY_PI_ANALYZER_BASE_URL" envDefault:"http://localhost:8080"`
	PIAnalyzerAPIKey          string `env:"LLM_GATEWAY_PI_ANALYZER_API_KEY"`
	PIAnalyzerModel           string `env:"LLM_GATEWAY_PI_ANALYZER_MODEL" envDefault:"claude-3-5-sonnet"`
	PIAnalyzerIntervalMinutes int    `env:"LLM_GATEWAY_PI_ANALYZER_INTERVAL_MINUTES" envDefault:"5"`
}
```

### 环境变量示例

```bash
# 启用错误收集
LLM_GATEWAY_PI_ERROR_COLLECTOR_ENABLED=true

# 启用自动分析
LLM_GATEWAY_PI_ANALYZER_ENABLED=true

# 网关地址（回环调用）
LLM_GATEWAY_PI_ANALYZER_BASE_URL=http://localhost:8080

# 分析使用的 API Key（需要在网关中创建专用 key）
LLM_GATEWAY_PI_ANALYZER_API_KEY=sk-pi-analyzer-internal-xxx

# 分析使用的模型
LLM_GATEWAY_PI_ANALYZER_MODEL=claude-3-5-sonnet

# 分析间隔（分钟）
LLM_GATEWAY_PI_ANALYZER_INTERVAL_MINUTES=5
```

---

## 4. 管理 API 实现

创建 `admin/pi_analysis_handler.go`：

```go
package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/kaixuan/llm-gateway-go/domains/promptinjection"
)

// PIAnalysisHandler PI 分析管理接口
type PIAnalysisHandler struct {
	collector *promptinjection.ErrorCollector
	analyzer  *promptinjection.ErrorAnalyzer
	db        *sql.DB
}

// NewPIAnalysisHandler 创建处理器
func NewPIAnalysisHandler(
	collector *promptinjection.ErrorCollector,
	analyzer *promptinjection.ErrorAnalyzer,
	db *sql.DB,
) *PIAnalysisHandler {
	return &PIAnalysisHandler{
		collector: collector,
		analyzer:  analyzer,
		db:        db,
	}
}

// Routes 返回路由
func (h *PIAnalysisHandler) Routes() chi.Router {
	r := chi.NewRouter()
	
	r.Get("/queue", h.GetQueue)
	r.Get("/results", h.GetResults)
	r.Get("/results/{queue_id}", h.GetResult)
	r.Post("/trigger", h.TriggerAnalysis)
	r.Get("/stats", h.GetStats)
	
	return r
}

// GetQueue 获取错误队列
func (h *PIAnalysisHandler) GetQueue(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limitStr := r.URL.Query().Get("limit")
	
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 200 {
			limit = l
		}
	}
	
	// 如果指定了 status，使用收集器的方法
	if status == "pending" || status == "" {
		errors, err := h.collector.GetPendingErrors(r.Context(), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"errors": errors,
			"count":  len(errors),
		})
		return
	}
	
	// 通用查询
	query := `
		SELECT 
			id, error_type, error_hash, priority,
			tenant_id, session_id, request_id,
			error_message, status, retry_count, created_at
		FROM error_analysis_queue
		WHERE status = $1
		ORDER BY priority DESC, created_at DESC
		LIMIT $2
	`
	
	rows, err := h.db.QueryContext(r.Context(), query, status, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	
	var errors []map[string]interface{}
	for rows.Next() {
		var e struct {
			ID          int64
			ErrorType   string
			ErrorHash   string
			Priority    int
			TenantID    string
			SessionID   sql.NullString
			RequestID   sql.NullString
			ErrorMsg    string
			Status      string
			RetryCount  int
			CreatedAt   string
		}
		
		if err := rows.Scan(
			&e.ID, &e.ErrorType, &e.ErrorHash, &e.Priority,
			&e.TenantID, &e.SessionID, &e.RequestID,
			&e.ErrorMsg, &e.Status, &e.RetryCount, &e.CreatedAt,
		); err != nil {
			continue
		}
		
		errors = append(errors, map[string]interface{}{
			"id":           e.ID,
			"error_type":   e.ErrorType,
			"error_hash":   e.ErrorHash,
			"priority":     e.Priority,
			"tenant_id":    e.TenantID,
			"session_id":   e.SessionID.String,
			"request_id":   e.RequestID.String,
			"error_message": e.ErrorMsg,
			"status":       e.Status,
			"retry_count":  e.RetryCount,
			"created_at":   e.CreatedAt,
		})
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"errors": errors,
		"count":  len(errors),
	})
}

// GetResults 获取分析结果列表
func (h *PIAnalysisHandler) GetResults(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 200 {
			limit = l
		}
	}
	
	query := `
		SELECT 
			r.id, r.analysis_id, r.queue_id,
			r.root_cause, r.error_category, r.is_false_positive,
			r.confidence_score, r.severity_level,
			q.error_type, q.tenant_id, r.created_at
		FROM error_analysis_results r
		JOIN error_analysis_queue q ON r.queue_id = q.id
		ORDER BY r.created_at DESC
		LIMIT $1
	`
	
	rows, err := h.db.QueryContext(r.Context(), query, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	
	var results []map[string]interface{}
	for rows.Next() {
		var r struct {
			ID              int64
			AnalysisID      string
			QueueID         int64
			RootCause       string
			ErrorCategory   string
			IsFalsePositive bool
			ConfidenceScore float64
			SeverityLevel   string
			ErrorType       string
			TenantID        string
			CreatedAt       string
		}
		
		if err := rows.Scan(
			&r.ID, &r.AnalysisID, &r.QueueID,
			&r.RootCause, &r.ErrorCategory, &r.IsFalsePositive,
			&r.ConfidenceScore, &r.SeverityLevel,
			&r.ErrorType, &r.TenantID, &r.CreatedAt,
		); err != nil {
			continue
		}
		
		results = append(results, map[string]interface{}{
			"id":                r.ID,
			"analysis_id":       r.AnalysisID,
			"queue_id":          r.QueueID,
			"root_cause":        r.RootCause,
			"error_category":    r.ErrorCategory,
			"is_false_positive": r.IsFalsePositive,
			"confidence_score":  r.ConfidenceScore,
			"severity_level":    r.SeverityLevel,
			"error_type":        r.ErrorType,
			"tenant_id":         r.TenantID,
			"created_at":        r.CreatedAt,
		})
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"results": results,
		"count":   len(results),
	})
}

// GetResult 获取单个分析结果详情
func (h *PIAnalysisHandler) GetResult(w http.ResponseWriter, r *http.Request) {
	queueIDStr := chi.URLParam(r, "queue_id")
	queueID, err := strconv.ParseInt(queueIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid queue_id", http.StatusBadRequest)
		return
	}
	
	query := `
		SELECT 
			r.id, r.analysis_id, r.queue_id,
			r.root_cause, r.error_category, r.is_false_positive,
			r.confidence_score, r.suggested_fix,
			r.rule_adjustment, r.code_changes,
			r.severity_level, r.impact_scope, r.affected_users_estimate,
			r.similar_cases, r.pattern_frequency,
			r.analysis_duration_ms, r.llm_tokens_used,
			r.created_at,
			q.error_type, q.error_message, q.tenant_id, q.input_text
		FROM error_analysis_results r
		JOIN error_analysis_queue q ON r.queue_id = q.id
		WHERE r.queue_id = $1
	`
	
	var result struct {
		ID                    int64
		AnalysisID            string
		QueueID               int64
		RootCause             string
		ErrorCategory         string
		IsFalsePositive       bool
		ConfidenceScore       float64
		SuggestedFix          string
		RuleAdjustment        json.RawMessage
		CodeChanges           json.RawMessage
		SeverityLevel         string
		ImpactScope           string
		AffectedUsersEstimate int
		SimilarCases          json.RawMessage
		PatternFrequency      int
		AnalysisDurationMs    int
		LLMTokensUsed         int
		CreatedAt             string
		ErrorType             string
		ErrorMessage          string
		TenantID              string
		InputText             sql.NullString
	}
	
	err = h.db.QueryRowContext(r.Context(), query, queueID).Scan(
		&result.ID, &result.AnalysisID, &result.QueueID,
		&result.RootCause, &result.ErrorCategory, &result.IsFalsePositive,
		&result.ConfidenceScore, &result.SuggestedFix,
		&result.RuleAdjustment, &result.CodeChanges,
		&result.SeverityLevel, &result.ImpactScope, &result.AffectedUsersEstimate,
		&result.SimilarCases, &result.PatternFrequency,
		&result.AnalysisDurationMs, &result.LLMTokensUsed,
		&result.CreatedAt,
		&result.ErrorType, &result.ErrorMessage, &result.TenantID, &result.InputText,
	)
	
	if err == sql.ErrNoRows {
		http.Error(w, "result not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// TriggerAnalysis 手动触发分析
func (h *PIAnalysisHandler) TriggerAnalysis(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BatchSize int `json:"batch_size"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// 使用默认值
		req.BatchSize = 10
	}
	
	if req.BatchSize <= 0 || req.BatchSize > 100 {
		req.BatchSize = 10
	}
	
	// 在后台执行
	go func() {
		ctx := context.Background()
		processed, err := h.analyzer.ProcessBatch(ctx)
		if err != nil {
			fmt.Printf("Manual analysis failed: %v\n", err)
		} else {
			fmt.Printf("Manual analysis completed: processed=%d\n", processed)
		}
	}()
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":    "Analysis triggered",
		"batch_size": req.BatchSize,
	})
}

// GetStats 获取统计信息
func (h *PIAnalysisHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.collector.GetStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}
```

---

## 5. 部署清单

### 5.1 数据库迁移

```bash
# 应用迁移
psql -h <host> -U <user> -d llm_gateway \
  -f sql/migrations/startup/971_pi_error_analysis_system.sql

# 验证表创建
psql -h <host> -U <user> -d llm_gateway -c "\d error_analysis_queue"
psql -h <host> -U <user> -d llm_gateway -c "\d error_analysis_results"
```

### 5.2 创建专用 API Key

```bash
# 在网关中创建专用的 API key 用于 PI 分析
# 方法 1: 通过管理界面创建
# 方法 2: 通过数据库直接插入（示例）

INSERT INTO api_keys (key, name, tenant_id, enabled, created_at)
VALUES (
    'sk-pi-analyzer-internal-' || gen_random_uuid()::text,
    'PI Analyzer Internal',
    'system',
    true,
    NOW()
);
```

### 5.3 环境变量配置

在部署配置中添加：

```bash
# .env 或 systemd service 文件
LLM_GATEWAY_PI_ERROR_COLLECTOR_ENABLED=true
LLM_GATEWAY_PI_ANALYZER_ENABLED=true
LLM_GATEWAY_PI_ANALYZER_BASE_URL=http://localhost:8080
LLM_GATEWAY_PI_ANALYZER_API_KEY=sk-pi-analyzer-internal-xxx
LLM_GATEWAY_PI_ANALYZER_MODEL=claude-3-5-sonnet
LLM_GATEWAY_PI_ANALYZER_INTERVAL_MINUTES=5
```

### 5.4 编译和部署

```bash
# 编译
go build -o llm-gateway-go ./cmd/gateway

# 运行测试
go test ./domains/promptinjection/...

# 部署到服务器
scp llm-gateway-go user@server:/path/to/deploy/
ssh user@server "systemctl restart llm-gateway"
```

---

## 6. 使用示例

### 6.1 通过 API 查询

```bash
# 查看待处理的错误
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  "http://localhost:8080/api/admin/pi-analysis/queue?status=pending&limit=10"

# 查看分析结果
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  "http://localhost:8080/api/admin/pi-analysis/results?limit=10"

# 查看特定错误的详细分析
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  "http://localhost:8080/api/admin/pi-analysis/results/123"

# 手动触发分析
curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"batch_size": 10}' \
  "http://localhost:8080/api/admin/pi-analysis/trigger"

# 查看统计
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  "http://localhost:8080/api/admin/pi-analysis/stats"
```

### 6.2 通过数据库查询

```sql
-- 查看最近的错误
SELECT 
    id, error_type, priority, tenant_id,
    error_message, status, created_at
FROM error_analysis_queue
WHERE status = 'pending'
ORDER BY priority DESC, created_at DESC
LIMIT 10;

-- 查看分析结果
SELECT 
    r.analysis_id,
    r.root_cause,
    r.error_category,
    r.is_false_positive,
    r.confidence_score,
    r.suggested_fix,
    r.severity_level,
    q.error_type,
    q.tenant_id
FROM error_analysis_results r
JOIN error_analysis_queue q ON r.queue_id = q.id
ORDER BY r.created_at DESC
LIMIT 10;
```

---

## 7. 验证清单

部署后验证：

- [ ] 数据库表已创建
- [ ] 环境变量已配置
- [ ] API Key 已创建
- [ ] 服务启动无错误
- [ ] 后台 Worker 正在运行（检查日志）
- [ ] 手动触发分析成功
- [ ] API 端点可访问
- [ ] 错误能够被自动收集
- [ ] 分析结果能够正常保存

---

## 8. 监控和告警

建议监控指标：

1. **队列长度**: `SELECT COUNT(*) FROM error_analysis_queue WHERE status = 'pending'`
2. **分析成功率**: 成功分析数 / 总错误数
3. **平均分析时间**: `SELECT AVG(analysis_duration_ms) FROM error_analysis_results`
4. **误报率**: `SELECT COUNT(*) WHERE is_false_positive = true`
5. **Worker 健康**: 检查日志中的 "pi_analyzer_worker" 消息

---

## 9. 故障排查

常见问题：

1. **分析不执行**
   - 检查 `PI_ANALYZER_ENABLED` 是否为 true
   - 检查后台 Worker 是否启动
   - 查看日志中的错误信息

2. **LLM 调用失败**
   - 检查 API Key 是否有效
   - 检查网关地址是否正确
   - 检查模型是否可用

3. **数据库错误**
   - 检查迁移是否成功应用
   - 检查数据库连接是否正常

---

## 总结

完整的 PI 智能体系统现已就绪，包括：

✅ **数据库基础设施** - 错误队列和分析结果存储  
✅ **错误收集器** - 自动收集和去重  
✅ **LLM 分析器** - 智能分析和建议生成  
✅ **HTTP 客户端** - 通过网关 API 调用 LLM  
✅ **管理 API** - 查询和管理接口  
✅ **后台 Worker** - 自动批处理  
✅ **完整文档** - 部署和使用指南  

系统已准备好打包部署！
