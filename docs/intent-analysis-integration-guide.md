# 意图分析系统集成指南 - Gateway V2

本文档说明如何将意图分析系统集成到 `cmd/gateway-v2/main.go`。

---

## 📋 集成概述

**集成方式**: Pipeline Hook  
**阶段**: PreRouting（在路由决策前执行）  
**优先级**: 50（在安全检测后，路由决策前）  
**影响**: 无破坏性，可选启用

---

## 🔧 集成步骤

### 步骤1: 在 newDeps() 中初始化意图分析组件

在 `cmd/gateway-v2/main.go` 的 `newDeps()` 函数中添加：

```go
import (
    // ... 现有导入
    "github.com/kaixuan/llm-gateway-go/domains/intentconfig"           //nolint:depguard
    "github.com/kaixuan/llm-gateway-go/domains/hooks/intentanalysis"  //nolint:depguard
)

func newDeps(cfg *v2Config) *v2Deps {
    // ... 现有代码 ...
    
    // === 意图分析组件初始化 ===
    // 注意：需要真实的数据库连接才能工作
    var intentAnalysisHook *intentanalysis.IntentAnalysisHook
    
    if cfg.EnableIntentAnalysis && cfg.DatabaseURL != "" {
        // 1. 连接数据库
        pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
        if err != nil {
            logger.Warn("intent_analysis: failed to connect to database, disabled", "error", err)
        } else {
            // 2. 创建配置管理器
            intentCfgMgr := intentconfig.NewManager(pool, logger)
            if err := intentCfgMgr.Start(context.Background()); err != nil {
                logger.Warn("intent_analysis: failed to start config manager, disabled", "error", err)
            } else {
                // 3. 创建存储层
                evolutionStore := intentconfig.NewPGEvolutionStore(pool, logger)
                
                // 4. 创建分析器
                analyzer := intentconfig.NewAnalyzer(intentCfgMgr, evolutionStore, logger)
                
                // 5. 创建Hook
                intentAnalysisHook = intentanalysis.NewIntentAnalysisHook(analyzer, logger)
                
                logger.Info("intent_analysis: initialized successfully")
            }
        }
    }
    
    return &v2Deps{
        // ... 现有字段 ...
        IntentAnalysisHook: intentAnalysisHook,  // 新增字段
    }
}
```

### 步骤2: 在 v2Deps 结构体中添加字段

在 `v2Deps` 结构体中添加：

```go
type v2Deps struct {
    // ... 现有字段 ...
    
    // IntentAnalysisHook 意图分析 Hook（可选）
    IntentAnalysisHook *intentanalysis.IntentAnalysisHook
}
```

### 步骤3: 在 v2Config 中添加配置项

在 `v2Config` 结构体中添加：

```go
type v2Config struct {
    // ... 现有字段 ...
    
    // EnableIntentAnalysis 是否启用意图分析
    EnableIntentAnalysis bool
    
    // DatabaseURL 数据库连接URL（意图分析需要）
    DatabaseURL string
}
```

在 `loadConfig()` 函数中添加：

```go
func loadConfig() *v2Config {
    cfg := &v2Config{
        // ... 现有配置 ...
        
        EnableIntentAnalysis: getEnv("LLM_GATEWAY_V2_INTENT_ANALYSIS", "false") == "true",
        DatabaseURL:          getEnv("DATABASE_URL", ""),
    }
    return cfg
}
```

### 步骤4: 在 buildPipeline() 中注册Hook

在 `buildPipeline()` 函数中添加意图分析阶段：

```go
func buildPipeline(deps *v2Deps) *pipeline.RequestPipeline {
    p := pipeline.NewRequestPipeline()
    
    // ... 现有阶段 ...
    
    // === Phase: Intent Analysis (PreRouting, priority 50) ===
    // 在安全检测后、路由决策前分析用户意图
    // 分析结果写入 req.Metadata["intent_analysis"]，可供后续阶段使用
    if deps.Config.EnableIntentAnalysis && deps.IntentAnalysisHook != nil {
        p.AddStage(&pipeline.PipelineStage{
            Name:  "intent_analysis",
            Phase: pipeline.PhasePreRouting,
            Mode:  pipeline.ModeSequential,
            Hooks: []pipeline.Hook{deps.IntentAnalysisHook},
        })
    }
    
    // ... 其他阶段 ...
    
    return p
}
```

---

## 🚀 运行配置

### 环境变量

```bash
# 启用意图分析
export LLM_GATEWAY_V2_INTENT_ANALYSIS=true

# 数据库连接（必需）
export DATABASE_URL="postgres://user:password@localhost:5432/llm_gateway?sslmode=disable"

# 其他V2配置
export LLM_GATEWAY_V2_CACHE=true
export LLM_GATEWAY_V2_SECURITY=true
export LLM_GATEWAY_V2_AUDIT=true
```

### 启动命令

```bash
# 开发环境
LLM_GATEWAY_V2_INTENT_ANALYSIS=true \
DATABASE_URL="postgres://xutaohuang@localhost:5432/llm_gateway?sslmode=disable" \
go run ./cmd/gateway-v2

# 生产环境（使用.env文件）
export $(cat .env | xargs)
go run ./cmd/gateway-v2
```

---

## 📊 使用示例

### 请求示例

```bash
# 代码意图请求
curl -X POST http://localhost:8782/v1/chat \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: test_session_001" \
  -H "X-Tenant-ID: test_tenant" \
  -d '{
    "messages": [
      {"role": "user", "content": "请帮我实现一个快速排序算法"}
    ],
    "model": "gpt-4"
  }'
```

### 分析结果（在 req.Metadata 中）

```json
{
  "intent_analysis": {
    "primary_intent": "code",
    "primary_confidence": 0.85,
    "confidence_level": "high",
    "intent_drift_score": 0.0,
    "is_intent_changed": false,
    "intent_shift_type": "no_history",
    "intent_stability": 1.0,
    "turn_number": 1,
    "recommendation": "意图明确(code, 置信度0.85)，建议使用专用模型以获得最佳效果; 建议使用代码专用模型（如Claude-3.5-Sonnet、GPT-4等）",
    "classifier_version": "pattern_layered",
    "analysis_latency_ms": 15
  },
  "intent_candidates": [
    {"kind": "code", "confidence": 0.85},
    {"kind": "reasoning", "confidence": 0.10},
    {"kind": "chat", "confidence": 0.05}
  ]
}
```

---

## 🔍 后续阶段使用分析结果

### 在路由阶段使用

```go
// 在路由Hook中读取意图分析结果
func (h *RouterHook) Execute(ctx context.Context, req *domain.PipelineRequest) error {
    if analysis, ok := req.Metadata["intent_analysis"].(map[string]any); ok {
        intent := analysis["primary_intent"].(string)
        confidence := analysis["primary_confidence"].(float64)
        driftScore := analysis["intent_drift_score"].(float64)
        
        // 根据意图调整路由策略
        if intent == "code" && confidence > 0.8 {
            // 优先选择代码专用模型
            req.Metadata["preferred_models"] = []string{"claude-3.5-sonnet", "gpt-4"}
        }
        
        // 意图漂移严重时重新评估
        if driftScore > 0.5 {
            h.logger.Warn("significant intent drift, reconsidering routing")
            // 触发重新推荐逻辑
        }
    }
    
    // ... 继续路由逻辑
}
```

### 在审计阶段使用

```go
// 在审计Hook中记录意图信息
func (h *AuditHook) Execute(ctx context.Context, req *domain.PipelineRequest) error {
    auditRecord := &AuditRecord{
        SessionID: req.SessionID,
        RequestID: req.Envelope.RequestID,
    }
    
    // 附加意图分析结果
    if analysis, ok := req.Metadata["intent_analysis"].(map[string]any); ok {
        auditRecord.Intent = analysis["primary_intent"].(string)
        auditRecord.IntentConfidence = analysis["primary_confidence"].(float64)
        auditRecord.IntentDrift = analysis["intent_drift_score"].(float64)
    }
    
    // 保存审计记录
    h.store.Save(ctx, auditRecord)
    return nil
}
```

---

## 🎯 验证集成

### 1. 检查日志

启动后应看到：

```
level=INFO msg="intent_analysis: initialized successfully"
level=INFO msg="intentconfig: manager started" poll_interval=30s
level=INFO msg="gateway-v2 starting" listen=:8782 stages=...
```

请求处理时应看到：

```
level=INFO msg="intent_analysis: completed" session_id=test_session_001 turn=1 intent=code confidence=0.85 drift=0 changed=false latency_ms=15
```

### 2. 检查数据库

```sql
-- 查看意图演化记录
SELECT 
    session_id, 
    turn_number, 
    primary_intent, 
    primary_confidence, 
    intent_drift_score,
    classified_at
FROM session_intent_evolution
ORDER BY classified_at DESC
LIMIT 10;

-- 查看当前配置
SELECT 
    tenant_id, 
    strategy, 
    drift_threshold, 
    multi_turn_memory
FROM intent_classifier_config;
```

### 3. 功能测试

```bash
# 多轮对话测试意图演化
SESSION_ID="test_$(date +%s)"

# 第1轮：代码
curl -X POST http://localhost:8782/v1/chat \
  -H "X-Session-ID: $SESSION_ID" \
  -d '{"messages":[{"role":"user","content":"写个排序"}]}'

# 第2轮：继续代码
curl -X POST http://localhost:8782/v1/chat \
  -H "X-Session-ID: $SESSION_ID" \
  -d '{"messages":[{"role":"user","content":"优化一下"}]}'

# 第3轮：切换到推理
curl -X POST http://localhost:8782/v1/chat \
  -H "X-Session-ID: $SESSION_ID" \
  -d '{"messages":[{"role":"user","content":"证明其时间复杂度"}]}'

# 查询演化记录
psql $DATABASE_URL -c "SELECT turn_number, primary_intent, primary_confidence, intent_drift_score FROM session_intent_evolution WHERE session_id='$SESSION_ID' ORDER BY turn_number;"
```

---

## ⚠️ 注意事项

### 性能影响

- **延迟**: 每次请求增加约 15-20ms（包含数据库查询）
- **数据库负载**: 每次请求1次SELECT + 1次INSERT
- **建议**: 生产环境使用连接池和索引优化

### 可选性

- 意图分析是**可选功能**，默认关闭
- 如果未启用或初始化失败，不影响主流程
- 建议在压测后逐步启用

### 数据库要求

- 必须先执行迁移文件（359-363）
- 需要配置平台级默认配置
- 建议定期归档历史数据

---

## 📚 相关文档

- **设计文档**: `docs/intent-analysis-implementation-summary.md`
- **最终报告**: `docs/final-delivery-report.md`
- **数据库状态**: `docs/database-status-report.md`
- **集成测试**: `domains/intentconfig/integration_test.go`

---

**集成状态**: ⏳ 待实施  
**预计时间**: 30-60分钟  
**风险等级**: 低（可选功能，无破坏性变更）
