# PI 智能体完整方案设计

## 执行摘要

当前 **Phase 1 已完成基础设施**（错误收集、LLM 分析），但缺少：
1. ❌ 自动化智能体（需要手动触发）
2. ❌ LLM 客户端实现（接口已定义但未实现）
3. ❌ 自动应用修复（分析结果需要手动处理）
4. ❌ 源码修改能力（无法自动提交代码）

本文档提供**完整的智能体方案**，包括三个层次的自动化。

---

## 方案对比

### 方案 A: 完全自动化智能体（推荐）

**架构**：
```
错误发生 → 自动收集 → 定时分析 → 自动应用 → Git PR → 人工审批
```

**优点**：
- ✅ 全自动，无需人工干预
- ✅ 可以自动修改代码并提交 PR
- ✅ 持续学习和优化
- ✅ 快速响应（分钟级）

**缺点**：
- ⚠️ 需要 Git 仓库写权限
- ⚠️ 需要代码审查流程
- ⚠️ 初期可能产生噪音

**实现成本**: 3-5 天

### 方案 B: 半自动智能体（推荐用于初期）

**架构**：
```
错误发生 → 自动收集 → 定时分析 → 生成建议 → 人工审查 → 人工应用
```

**优点**：
- ✅ 风险可控
- ✅ 人工最终决策
- ✅ 学习曲线平缓
- ✅ 可以先观察质量

**缺点**：
- ⚠️ 需要人工介入
- ⚠️ 响应较慢

**实现成本**: 1-2 天

### 方案 C: 查询式工具（最简单）

**架构**：
```
错误发生 → 自动收集 → 按需查询 → 查看建议 → 人工处理
```

**优点**：
- ✅ 实现简单
- ✅ 无自动化风险
- ✅ 即时可用

**缺点**：
- ⚠️ 完全依赖人工
- ⚠️ 可能漏掉问题

**实现成本**: 0.5 天（基本已完成）

---

## 推荐实施路径

### 阶段 1: 立即可用（今天完成）

**目标**: 让系统能够查询和查看分析结果

#### 1.1 实现 LLM 客户端

选择**回环调用方式**（最简单）：

```go
// 配置
LLM_GATEWAY_PI_ANALYZER_ENABLED=true
LLM_GATEWAY_PI_ANALYZER_MODEL=claude-3-5-sonnet
LLM_GATEWAY_PI_ANALYZER_API_KEY=sk-xxx  // 使用网关自己的 API key
```

#### 1.2 创建命令行工具

```bash
# 手动触发分析
./llm-gateway-go analyze-errors --batch-size=10

# 查看待分析错误
./llm-gateway-go list-errors --status=pending

# 查看分析结果
./llm-gateway-go show-analysis --error-id=123
```

#### 1.3 创建管理 API

```
GET  /api/admin/pi-analysis/queue          # 查看队列
GET  /api/admin/pi-analysis/results        # 查看结果
POST /api/admin/pi-analysis/trigger        # 手动触发分析
GET  /api/admin/pi-analysis/stats          # 统计信息
```

### 阶段 2: 自动化（1-2 天）

**目标**: 定时自动分析，生成可视化建议

#### 2.1 后台 Worker

```go
// 在 cmd/gateway/main.go 中启动
if cfg.PIAnalyzerEnabled {
    go runPIAnalyzerWorker(ctx, db, collector, analyzer)
}

func runPIAnalyzerWorker(ctx context.Context, ...) {
    ticker := time.NewTicker(5 * time.Minute)
    for {
        select {
        case <-ticker.C:
            processed, err := analyzer.ProcessBatch(ctx)
            if err != nil {
                slog.Error("pi_analyzer: batch failed", "error", err)
            } else if processed > 0 {
                slog.Info("pi_analyzer: batch completed", "processed", processed)
            }
        case <-ctx.Done():
            return
        }
    }
}
```

#### 2.2 通知机制

```go
// 分析完成后发送通知
func (ea *ErrorAnalyzer) notifyAnalysisComplete(result *AnalysisResult) {
    if result.SeverityLevel == "critical" || result.SeverityLevel == "high" {
        // 发送告警
        // 选项：Email, Slack, 企业微信, 钉钉
    }
}
```

#### 2.3 Web UI

创建管理界面展示：
- 错误列表（按优先级、类型、租户）
- 分析结果详情
- 修复建议
- 趋势图表

### 阶段 3: 智能修复（3-5 天）

**目标**: 自动应用简单的修复建议

#### 3.1 规则调整自动应用

```go
// 自动应用规则调整
func (ea *ErrorAnalyzer) applyRuleAdjustment(ctx context.Context, result *AnalysisResult) error {
    if !result.RuleAdjustment != nil {
        return nil
    }
    
    // 只自动应用低风险的调整
    if result.SeverityLevel != "low" && result.SeverityLevel != "medium" {
        return nil // 高风险的需要人工审批
    }
    
    // 应用调整
    if timeout, ok := result.RuleAdjustment["timeout_sec"].(float64); ok {
        // 更新数据库配置
        _, err := ea.db.ExecContext(ctx, 
            "UPDATE prompt_injection_policies SET llm_detection_timeout_sec = $1 WHERE tenant_id = $2",
            int(timeout), result.TenantID)
        return err
    }
    
    return nil
}
```

#### 3.2 代码修改 PR

```go
// 生成代码修改 PR
func (ea *ErrorAnalyzer) createCodeFixPR(ctx context.Context, result *AnalysisResult) error {
    if result.CodeChanges == nil {
        return nil
    }
    
    // 1. Clone 仓库到临时目录
    // 2. 创建新分支: fix/pi-error-<analysis-id>
    // 3. 应用代码修改
    // 4. Commit with详细说明
    // 5. Push 并创建 PR
    // 6. PR 描述包含：
    //    - 错误描述
    //    - 根因分析
    //    - 修复说明
    //    - 分析 ID（可追溯）
    
    // 使用 go-git 或调用 git 命令
    return nil
}
```

---

## 实现细节

### 1. 在网关中暴露分析结果

#### 新增管理 API 端点

```go
// admin/pi_analysis.go
package admin

type PIAnalysisHandler struct {
    collector *promptinjection.ErrorCollector
    analyzer  *promptinjection.ErrorAnalyzer
    db        *sql.DB
}

// GET /api/admin/pi-analysis/queue
func (h *PIAnalysisHandler) GetQueue(w http.ResponseWriter, r *http.Request) {
    // 查询参数
    status := r.URL.Query().Get("status") // pending, processing, analyzed, failed
    errorType := r.URL.Query().Get("error_type")
    tenantID := r.URL.Query().Get("tenant_id")
    limit := parseIntDefault(r.URL.Query().Get("limit"), 50)
    offset := parseIntDefault(r.URL.Query().Get("offset"), 0)
    
    // 构建查询
    query := `
        SELECT 
            id, error_type, error_hash, priority,
            tenant_id, session_id, request_id,
            input_text, error_message,
            status, retry_count, created_at
        FROM error_analysis_queue
        WHERE 1=1
    `
    args := []interface{}{}
    
    if status != "" {
        query += " AND status = $" + fmt.Sprint(len(args)+1)
        args = append(args, status)
    }
    
    if errorType != "" {
        query += " AND error_type = $" + fmt.Sprint(len(args)+1)
        args = append(args, errorType)
    }
    
    if tenantID != "" {
        query += " AND tenant_id = $" + fmt.Sprint(len(args)+1)
        args = append(args, tenantID)
    }
    
    query += " ORDER BY priority DESC, created_at DESC LIMIT $" + fmt.Sprint(len(args)+1) + " OFFSET $" + fmt.Sprint(len(args)+2)
    args = append(args, limit, offset)
    
    rows, err := h.db.QueryContext(r.Context(), query, args...)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    defer rows.Close()
    
    // 解析并返回 JSON
    // ...
}

// GET /api/admin/pi-analysis/results/{queue_id}
func (h *PIAnalysisHandler) GetResult(w http.ResponseWriter, r *http.Request) {
    queueID := chi.URLParam(r, "queue_id")
    
    query := `
        SELECT 
            r.id, r.analysis_id, r.queue_id,
            r.root_cause, r.error_category, r.is_false_positive,
            r.confidence_score, r.suggested_fix,
            r.rule_adjustment, r.code_changes,
            r.severity_level, r.impact_scope, r.affected_users_estimate,
            r.similar_cases, r.pattern_frequency,
            r.auto_applied, r.applied_at, r.applied_by,
            r.human_verified, r.verification_feedback,
            r.analysis_duration_ms, r.llm_tokens_used,
            r.created_at,
            q.error_type, q.error_message, q.tenant_id
        FROM error_analysis_results r
        JOIN error_analysis_queue q ON r.queue_id = q.id
        WHERE r.queue_id = $1
    `
    
    // 查询并返回详细结果
    // ...
}

// POST /api/admin/pi-analysis/trigger
func (h *PIAnalysisHandler) TriggerAnalysis(w http.ResponseWriter, r *http.Request) {
    var req struct {
        BatchSize int `json:"batch_size"`
    }
    
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    
    if req.BatchSize <= 0 || req.BatchSize > 100 {
        req.BatchSize = 10
    }
    
    // 在后台goroutine中执行
    go func() {
        ctx := context.Background()
        processed, err := h.analyzer.ProcessBatch(ctx)
        if err != nil {
            slog.Error("pi_analysis: manual trigger failed", "error", err)
        } else {
            slog.Info("pi_analysis: manual trigger completed", "processed", processed)
        }
    }()
    
    w.WriteHeader(http.StatusAccepted)
    json.NewEncoder(w).Encode(map[string]interface{}{
        "message": "Analysis triggered",
        "batch_size": req.BatchSize,
    })
}

// GET /api/admin/pi-analysis/stats
func (h *PIAnalysisHandler) GetStats(w http.ResponseWriter, r *http.Request) {
    stats, err := h.collector.GetStats(r.Context())
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    
    json.NewEncoder(w).Encode(stats)
}

// POST /api/admin/pi-analysis/results/{queue_id}/apply
func (h *PIAnalysisHandler) ApplyFix(w http.ResponseWriter, r *http.Request) {
    queueID := chi.URLParam(r, "queue_id")
    
    // 1. 查询分析结果
    // 2. 验证是否可以应用
    // 3. 应用修复（规则调整、配置更新等）
    // 4. 记录应用状态
    
    // ...
}

// POST /api/admin/pi-analysis/results/{queue_id}/verify
func (h *PIAnalysisHandler) VerifyResult(w http.ResponseWriter, r *http.Request) {
    queueID := chi.URLParam(r, "queue_id")
    
    var req struct {
        Verified bool   `json:"verified"`
        Feedback string `json:"feedback"`
    }
    
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    
    // 更新验证状态
    _, err := h.db.ExecContext(r.Context(), `
        UPDATE error_analysis_results
        SET human_verified = $1,
            verification_feedback = $2,
            verified_at = NOW(),
            verified_by = $3
        WHERE queue_id = $4
    `, req.Verified, req.Feedback, getUserID(r), queueID)
    
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    
    w.WriteHeader(http.StatusOK)
}
```

### 2. 配置和启动

#### 环境变量

```bash
# 启用 PI 错误收集
LLM_GATEWAY_PI_ERROR_COLLECTOR_ENABLED=true

# 启用 PI 分析器
LLM_GATEWAY_PI_ANALYZER_ENABLED=true
LLM_GATEWAY_PI_ANALYZER_MODEL=claude-3-5-sonnet
LLM_GATEWAY_PI_ANALYZER_BATCH_SIZE=10
LLM_GATEWAY_PI_ANALYZER_INTERVAL=5m

# LLM 客户端配置（回环方式）
LLM_GATEWAY_PI_ANALYZER_ENDPOINT=http://localhost:8080/v1/chat/completions
LLM_GATEWAY_PI_ANALYZER_API_KEY=sk-internal-analyzer

# 自动应用（可选）
LLM_GATEWAY_PI_AUTO_APPLY_ENABLED=false  # 初期建议关闭
LLM_GATEWAY_PI_AUTO_APPLY_MAX_SEVERITY=medium  # 只自动应用 low/medium

# 通知（可选）
LLM_GATEWAY_PI_NOTIFICATION_ENABLED=false
LLM_GATEWAY_PI_NOTIFICATION_WEBHOOK=https://...
```

#### 在 main.go 中启动

```go
// cmd/gateway/main.go

// 创建 PI 错误收集器
piCollector := promptinjection.NewErrorCollector(
    db,
    cfg.PIErrorCollectorEnabled,
)

// 创建 LLM 客户端
piLLMClient := promptinjection.NewGatewayLLMClient(
    cfg.PIAnalyzerModel,
    cfg.PIAnalyzerEndpoint,
    cfg.PIAnalyzerAPIKey,
    "http://localhost:8080", // 回环
)

// 创建分析器
piAnalyzer := promptinjection.NewErrorAnalyzer(
    db,
    piCollector,
    piLLMClient,
    cfg.PIAnalyzerModel,
    cfg.PIAnalyzerEnabled,
)

// 将收集器注入到 Detector
piDetector, err := promptinjection.NewDetector(db)
if err != nil {
    log.Fatal(err)
}
piDetector.SetErrorCollector(piCollector)  // 需要添加这个方法

// 启动后台分析 worker
if cfg.PIAnalyzerEnabled {
    go runPIAnalyzerWorker(ctx, piAnalyzer, cfg.PIAnalyzerInterval)
}

// 注册管理 API
piAnalysisHandler := admin.NewPIAnalysisHandler(piCollector, piAnalyzer, db)
adminRouter.Mount("/pi-analysis", piAnalysisHandler.Routes())
```

---

## 查询和使用指南

### 1. 通过 API 查询

```bash
# 查看待处理的错误
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  "http://localhost:8080/api/admin/pi-analysis/queue?status=pending&limit=10"

# 查看已分析的结果
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  "http://localhost:8080/api/admin/pi-analysis/queue?status=analyzed&limit=10"

# 查看特定错误的分析结果
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

### 2. 通过数据库直接查询

```sql
-- 查看待分析的错误（按优先级）
SELECT 
    id, error_type, priority, tenant_id,
    error_message, status, retry_count, created_at
FROM error_analysis_queue
WHERE status = 'pending'
ORDER BY priority DESC, created_at ASC
LIMIT 10;

-- 查看最近的分析结果
SELECT 
    r.id, r.analysis_id, 
    r.root_cause, r.error_category, r.is_false_positive,
    r.confidence_score, r.suggested_fix, r.severity_level,
    q.error_type, q.error_message, q.tenant_id,
    r.created_at
FROM error_analysis_results r
JOIN error_analysis_queue q ON r.queue_id = q.id
ORDER BY r.created_at DESC
LIMIT 10;

-- 查看误报统计
SELECT 
    q.error_type,
    COUNT(*) as total,
    SUM(CASE WHEN r.is_false_positive THEN 1 ELSE 0 END) as false_positives,
    AVG(r.confidence_score) as avg_confidence
FROM error_analysis_queue q
JOIN error_analysis_results r ON q.id = r.queue_id
WHERE q.created_at > NOW() - INTERVAL '7 days'
GROUP BY q.error_type;

-- 查看修复建议
SELECT 
    r.analysis_id,
    r.root_cause,
    r.suggested_fix,
    r.rule_adjustment,
    r.code_changes,
    r.severity_level
FROM error_analysis_results r
WHERE r.auto_applied = false
  AND r.human_verified IS NULL
ORDER BY 
    CASE r.severity_level
        WHEN 'critical' THEN 1
        WHEN 'high' THEN 2
        WHEN 'medium' THEN 3
        ELSE 4
    END,
    r.created_at DESC;
```

### 3. 通过命令行工具（需实现）

```bash
# 列出待处理错误
./llm-gateway-go pi-analysis list --status=pending

# 手动触发分析
./llm-gateway-go pi-analysis run --batch-size=10

# 查看分析结果
./llm-gateway-go pi-analysis show --id=123

# 应用修复
./llm-gateway-go pi-analysis apply --id=123

# 查看统计
./llm-gateway-go pi-analysis stats --days=7
```

---

## 总结

### 当前状态
✅ Phase 1 基础设施已完成  
❌ 缺少自动化智能体  
❌ 缺少 LLM 客户端实现  
❌ 缺少 API 暴露  

### 立即可做（今天）
1. 实现 LLM 客户端（回环方式）
2. 创建管理 API 端点
3. 启动后台 Worker
4. 通过 API 或数据库查询结果

### 推荐路径
**Week 1**: 方案 B（半自动）- 自动分析，人工审查  
**Week 2-3**: 方案 A（全自动）- 加入自动应用和 PR 生成  
**持续**: 监控质量，调整提示词，优化分析准确度  

### 下一步
您希望我继续实现哪个部分？
1. 实现 LLM 客户端（回环调用）
2. 创建管理 API 端点
3. 实现后台 Worker
4. 创建 Web UI

请告诉我优先级，我会立即开始实现。
