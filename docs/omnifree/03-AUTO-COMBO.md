# 虚拟自动路由设计 (Auto Combo)

> 零配置免费资源聚合 — 参考 OmniRoute virtualFactory 实现动态候选池

---

## 🎯 设计目标

用户请求 `auto/free` 或 `auto/best-free` 时：
1. **无需预配置**: 不需要手工创建 combo，运行时动态构建
2. **自动聚合**: 从所有可用免费凭据 + keyless 提供商构建候选池
3. **智能路由**: 基于健康度、延迟、配额剩余评分并轮换
4. **透明降级**: 失败自动 fallback 到下一候选

---

## 📐 架构设计

### 请求流程

```
用户请求: POST /v1/chat/completions
{
  "model": "auto/best-free",
  "messages": [...]
}
  ↓
AutoComboResolver.Resolve("auto/best-free")
  ├─ 解析路由变体: "best-free" → variant="cheap"
  ├─ 查找模板: auto_combo_templates WHERE combo_name='auto/best-free'
  └─ 返回 AutoComboSpec
  ↓
VirtualFactory.Build(spec)
  ├─ 1. 查询已连接的免费凭据
  │   SELECT c.* FROM credentials c
  │   JOIN free_resource_catalog frc ON c.provider_code = frc.provider_code
  │   WHERE c.is_free_tier = TRUE AND c.enabled = TRUE
  │     AND frc.tos_verdict IN ('ok', 'caution')
  │
  ├─ 2. 配额预检过滤
  │   FOR EACH credential:
  │     IF NOT QuotaPreflight(credential) THEN skip
  │
  ├─ 3. 添加 keyless 提供商
  │   SELECT * FROM keyless_providers
  │   WHERE enabled = TRUE AND allowlist_in_auto_combo = TRUE
  │
  └─ 4. 构建候选池 CandidatePool[]
      [{provider, model, credential_id, cost=0, health, latency, quota_remaining}, ...]
  ↓
Engine.SelectCandidate(candidatePool)
  ├─ 计算综合评分 (scoring.go)
  │   score = w1*health + w2*(1-norm_latency) + w3*quota_remaining + w4*(1-cost)
  │
  ├─ 分层 (ScoreTierRotator)
  │   - top tier: score >= 0.8
  │   - mid tier: 0.5 <= score < 0.8
  │   - rest tier: score < 0.5
  │
  ├─ 选择策略
  │   IF top tier has clear winner (score diff >= 0.1):
  │     RETURN winner
  │   ELSE:
  │     weighted_tier_selection() → round_robin_within_tier()
  │
  └─ 返回 SelectedCandidate
  ↓
StreamExecutor.Execute(candidate)
  ↓
失败? → fallback 到下一候选 (最多 3 次)
```

---

## 🛠️ Go 实现

### 1. Auto Combo Resolver

```go
// domains/autocombo/resolver.go

package autocombo

import (
    "context"
    "database/sql"
    "strings"
)

type Resolver struct {
    db *sql.DB
}

// Resolve 解析 auto/* 模型 ID 到 AutoComboSpec
func (r *Resolver) Resolve(ctx context.Context, modelID string, tenantID int64) (*AutoComboSpec, error) {
    // 1. 检查是否为 auto/* 模式
    if !strings.HasPrefix(modelID, "auto/") {
        return nil, nil  // 不是 auto combo
    }
    
    // 2. 查找模板
    var spec AutoComboSpec
    err := r.db.QueryRowContext(ctx, `
        SELECT 
            id, combo_name, variant, tier_filter, free_type_filter,
            tos_filter, provider_allowlist, provider_denylist,
            scoring_weights_json, max_candidates, exploration_rate
        FROM auto_combo_templates
        WHERE combo_name = $1 AND enabled = TRUE AND tenant_id = $2
    `, modelID, tenantID).Scan(
        &spec.ID, &spec.ComboName, &spec.Variant, &spec.TierFilter,
        &spec.FreeTypeFilter, &spec.ToSFilter, &spec.ProviderAllowlist,
        &spec.ProviderDenylist, &spec.ScoringWeightsJSON,
        &spec.MaxCandidates, &spec.ExplorationRate,
    )
    
    if err == sql.ErrNoRows {
        // 3. 回退到内置模板
        return r.getBuiltinTemplate(modelID)
    }
    if err != nil {
        return nil, err
    }
    
    return &spec, nil
}

// getBuiltinTemplate 内置模板回退
func (r *Resolver) getBuiltinTemplate(modelID string) (*AutoComboSpec, error) {
    // 映射常见模式
    builtinMap := map[string]string{
        "auto/free":         "cheap",
        "auto/best-free":    "cheap",
        "auto/coding:free":  "coding",
        "auto/reasoning:free": "smart",
        "auto/fast:free":    "fast",
    }
    
    variant, ok := builtinMap[modelID]
    if !ok {
        return nil, fmt.Errorf("unknown auto combo: %s", modelID)
    }
    
    return &AutoComboSpec{
        ComboName:   modelID,
        Variant:     variant,
        TierFilter:  []string{"free"},
        ToSFilter:   []string{"ok", "caution"},
        ScoringWeightsJSON: json.RawMessage(`{
            "health_score": 0.3,
            "latency_p95": 0.2,
            "quota_remaining": 0.25,
            "cost": 0.0,
            "task_fit": 0.15,
            "tier_affinity": 0.1
        }`),
        MaxCandidates:   50,
        ExplorationRate: 0.05,
    }, nil
}

type AutoComboSpec struct {
    ID                 int64
    ComboName          string
    Variant            string
    TierFilter         []string
    FreeTypeFilter     []string
    ToSFilter          []string
    ProviderAllowlist  []string
    ProviderDenylist   []string
    ScoringWeightsJSON json.RawMessage
    MaxCandidates      int
    ExplorationRate    float64
}
```

### 2. Virtual Factory

```go
// domains/autocombo/virtual_factory.go

package autocombo

import (
    "context"
    "database/sql"
    "llm-gateway-go/domains/freeresource"
)

type VirtualFactory struct {
    db            *sql.DB
    quotaTracker  *freeresource.QuotaTracker
}

// Build 动态构建虚拟 combo 的候选池
func (vf *VirtualFactory) Build(ctx context.Context, spec *AutoComboSpec, tenantID int64) (*VirtualCombo, error) {
    var candidates []Candidate
    
    // 1. 加载已连接的免费凭据
    credCandidates, err := vf.loadCredentialCandidates(ctx, spec, tenantID)
    if err != nil {
        return nil, fmt.Errorf("load credential candidates: %w", err)
    }
    candidates = append(candidates, credCandidates...)
    
    // 2. 加载 keyless 提供商
    keylessCandidates, err := vf.loadKeylessCandidates(ctx, spec, tenantID)
    if err != nil {
        return nil, fmt.Errorf("load keyless candidates: %w", err)
    }
    candidates = append(candidates, keylessCandidates...)
    
    // 3. 配额预检过滤
    filtered := vf.filterByQuota(ctx, candidates, tenantID)
    
    // 4. 限制候选数量
    if len(filtered) > spec.MaxCandidates {
        filtered = filtered[:spec.MaxCandidates]
    }
    
    return &VirtualCombo{
        Name:            spec.ComboName,
        Variant:         spec.Variant,
        CandidatePool:   filtered,
        ExplorationRate: spec.ExplorationRate,
    }, nil
}

func (vf *VirtualFactory) loadCredentialCandidates(ctx context.Context, spec *AutoComboSpec, tenantID int64) ([]Candidate, error) {
    query := `
        SELECT 
            c.id AS credential_id,
            c.provider_code,
            frc.model_id,
            frc.display_name,
            c.health_score,
            c.p95_latency_ms,
            0 AS cost_per_1m
        FROM credentials c
        JOIN free_resource_catalog frc ON c.provider_code = frc.provider_code
        WHERE c.tenant_id = $1
          AND c.enabled = TRUE
          AND c.is_free_tier = TRUE
          AND frc.enabled = TRUE
          AND frc.tos_verdict = ANY($2)
          AND ($3::text[] IS NULL OR c.provider_code = ANY($3))
          AND (c.provider_code != ALL($4) OR $4 = '{}')
    `
    
    rows, err := vf.db.QueryContext(ctx, query, tenantID, 
        pq.Array(spec.ToSFilter),
        pq.Array(spec.ProviderAllowlist),
        pq.Array(spec.ProviderDenylist))
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    
    var candidates []Candidate
    for rows.Next() {
        var c Candidate
        if err := rows.Scan(&c.CredentialID, &c.ProviderCode, &c.ModelID,
            &c.DisplayName, &c.HealthScore, &c.LatencyP95, &c.CostPer1M); err != nil {
            return nil, err
        }
        candidates = append(candidates, c)
    }
    
    return candidates, rows.Err()
}

func (vf *VirtualFactory) loadKeylessCandidates(ctx context.Context, spec *AutoComboSpec, tenantID int64) ([]Candidate, error) {
    query := `
        SELECT 
            kp.provider_code,
            pc.display_name,
            kp.reliability_score,
            100 AS est_latency_ms  -- 估计延迟
        FROM keyless_providers kp
        JOIN provider_catalog pc ON kp.provider_code = pc.code
        WHERE kp.tenant_id = $1
          AND kp.enabled = TRUE
          AND kp.allowlist_in_auto_combo = TRUE
          AND ($2::text[] IS NULL OR kp.provider_code = ANY($2))
    `
    
    rows, err := vf.db.QueryContext(ctx, query, tenantID, pq.Array(spec.ProviderAllowlist))
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    
    var candidates []Candidate
    for rows.Next() {
        var c Candidate
        c.CredentialID = SYNTHETIC_KEYLESS_CREDENTIAL_ID
        c.IsKeyless = true
        c.CostPer1M = 0
        
        if err := rows.Scan(&c.ProviderCode, &c.DisplayName, &c.HealthScore, &c.LatencyP95); err != nil {
            return nil, err
        }
        candidates = append(candidates, c)
    }
    
    return candidates, rows.Err()
}

func (vf *VirtualFactory) filterByQuota(ctx context.Context, candidates []Candidate, tenantID int64) []Candidate {
    filtered := make([]Candidate, 0, len(candidates))
    
    for _, c := range candidates {
        if c.IsKeyless {
            // Keyless 无配额限制
            filtered = append(filtered, c)
            continue
        }
        
        // 配额预检
        ok, err := vf.quotaTracker.Preflight(ctx, freeresource.PreflightRequest{
            CredentialID:    c.CredentialID,
            ProviderCode:    c.ProviderCode,
            ModelID:         c.ModelID,
            WindowType:      freeresource.WindowTypeDay1,
            DefaultLimit:    1000,
            MinRemainingPct: 0.1,
            TenantID:        tenantID,
        })
        
        if err != nil {
            log.Warnf("quota preflight error for %s/%s: %v", c.ProviderCode, c.ModelID, err)
            continue
        }
        
        if ok {
            filtered = append(filtered, c)
        } else {
            log.Debugf("skipping quota-exhausted candidate: %s/%s", c.ProviderCode, c.ModelID)
        }
    }
    
    return filtered
}

const SYNTHETIC_KEYLESS_CREDENTIAL_ID = -1

type VirtualCombo struct {
    Name            string
    Variant         string
    CandidatePool   []Candidate
    ExplorationRate float64
}

type Candidate struct {
    CredentialID int64
    ProviderCode string
    ModelID      string
    DisplayName  string
    HealthScore  float64
    LatencyP95   int
    CostPer1M    float64
    IsKeyless    bool
}
```

### 3. Score Tier Rotator

```go
// domains/autocombo/engine.go

package autocombo

import (
    "math"
    "math/rand"
)

type Engine struct {
    weights ScoringWeights
}

type ScoringWeights struct {
    HealthScore     float64 `json:"health_score"`
    LatencyP95      float64 `json:"latency_p95"`
    QuotaRemaining  float64 `json:"quota_remaining"`
    Cost            float64 `json:"cost"`
    TaskFit         float64 `json:"task_fit"`
    TierAffinity    float64 `json:"tier_affinity"`
}

// SelectCandidate 从候选池选择一个候选
func (e *Engine) SelectCandidate(pool []Candidate) (*Candidate, error) {
    if len(pool) == 0 {
        return nil, fmt.Errorf("empty candidate pool")
    }
    
    // 1. 计算评分
    scored := e.scoreAll(pool)
    
    // 2. 分层
    tiers := e.splitTiers(scored)
    
    // 3. 选择策略
    if len(tiers.Top) > 0 {
        winner := tiers.Top[0]
        // 如果有明显优胜者 (领先 >= 0.1)
        if len(tiers.Top) > 1 && winner.Score-tiers.Top[1].Score >= 0.1 {
            return &winner.Candidate, nil
        }
        // 否则在 top tier 内轮换
        return e.roundRobin(tiers.Top), nil
    }
    
    if len(tiers.Mid) > 0 {
        return e.roundRobin(tiers.Mid), nil
    }
    
    if len(tiers.Rest) > 0 {
        return e.roundRobin(tiers.Rest), nil
    }
    
    return nil, fmt.Errorf("no viable candidates")
}

func (e *Engine) scoreAll(pool []Candidate) []ScoredCandidate {
    scored := make([]ScoredCandidate, len(pool))
    
    // 归一化因子
    maxLatency := 0
    for _, c := range pool {
        if c.LatencyP95 > maxLatency {
            maxLatency = c.LatencyP95
        }
    }
    if maxLatency == 0 {
        maxLatency = 1000  // 默认
    }
    
    for i, c := range pool {
        normLatency := float64(c.LatencyP95) / float64(maxLatency)
        quotaRemaining := 1.0  // TODO: 从 quota_tracker 获取实际剩余
        
        score := e.weights.HealthScore*c.HealthScore +
            e.weights.LatencyP95*(1-normLatency) +
            e.weights.QuotaRemaining*quotaRemaining +
            e.weights.Cost*(1-c.CostPer1M/100.0)  // 免费 = 0, 归一化
        
        scored[i] = ScoredCandidate{
            Candidate: c,
            Score:     score,
        }
    }
    
    // 按评分降序排序
    sort.Slice(scored, func(i, j int) bool {
        return scored[i].Score > scored[j].Score
    })
    
    return scored
}

func (e *Engine) splitTiers(scored []ScoredCandidate) Tiers {
    var tiers Tiers
    
    for _, sc := range scored {
        if sc.Score >= 0.8 {
            tiers.Top = append(tiers.Top, sc)
        } else if sc.Score >= 0.5 {
            tiers.Mid = append(tiers.Mid, sc)
        } else {
            tiers.Rest = append(tiers.Rest, sc)
        }
    }
    
    return tiers
}

func (e *Engine) roundRobin(tier []ScoredCandidate) *Candidate {
    // 简单轮换 (实际应使用持久化的 round-robin state)
    idx := rand.Intn(len(tier))
    return &tier[idx].Candidate
}

type ScoredCandidate struct {
    Candidate Candidate
    Score     float64
}

type Tiers struct {
    Top  []ScoredCandidate
    Mid  []ScoredCandidate
    Rest []ScoredCandidate
}
```

---

## 🔌 集成到 Streaming Pipeline

### 在请求入口处理 auto/*

```go
// domains/streaming/handler.go

func (h *Handler) HandleChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    // 1. 解析是否为 auto combo
    autoSpec, err := h.autoResolver.Resolve(ctx, req.Model, req.TenantID)
    if err != nil {
        return nil, fmt.Errorf("resolve auto combo: %w", err)
    }
    
    if autoSpec != nil {
        // 2. 构建虚拟 combo
        virtualCombo, err := h.virtualFactory.Build(ctx, autoSpec, req.TenantID)
        if err != nil {
            return nil, fmt.Errorf("build virtual combo: %w", err)
        }
        
        // 3. 选择候选
        candidate, err := h.autoEngine.SelectCandidate(virtualCombo.CandidatePool)
        if err != nil {
            return nil, fmt.Errorf("select candidate: %w", err)
        }
        
        // 4. 重写请求
        req.ProviderCode = candidate.ProviderCode
        req.ModelID = candidate.ModelID
        req.CredentialID = candidate.CredentialID
        
        log.Infof("auto-routed %s → %s/%s (score=%.2f)", 
            autoSpec.ComboName, candidate.ProviderCode, candidate.ModelID, candidate.HealthScore)
    }
    
    // 5. 执行常规流程
    return h.executor.Execute(ctx, req)
}
```

---

## 📊 Admin API

### 1. 列出 Auto Combo 模板

```http
GET /admin/auto-combos

Response:
{
  "combos": [
    {
      "id": 1,
      "combo_name": "auto/free",
      "variant": "cheap",
      "tier_filter": ["free"],
      "tos_filter": ["ok", "caution"],
      "enabled": true,
      "created_at": "2026-08-01T00:00:00Z"
    },
    ...
  ]
}
```

### 2. 预览 Auto Combo 候选池

```http
GET /admin/auto-combos/auto/best-free/preview

Response:
{
  "combo_name": "auto/best-free",
  "candidate_count": 15,
  "candidates": [
    {
      "provider_code": "openrouter",
      "model_id": "openai/gpt-3.5-turbo:free",
      "credential_id": 42,
      "health_score": 0.95,
      "latency_p95": 850,
      "quota_remaining": 0.75,
      "is_keyless": false
    },
    {
      "provider_code": "opencode",
      "model_id": "gpt-4o-mini",
      "credential_id": -1,
      "health_score": 0.80,
      "latency_p95": 1200,
      "quota_remaining": 1.0,
      "is_keyless": true
    },
    ...
  ]
}
```

### 3. 创建自定义 Auto Combo

```http
POST /admin/auto-combos
{
  "combo_name": "auto/my-free-pool",
  "variant": "smart",
  "tier_filter": ["free"],
  "tos_filter": ["ok"],
  "provider_allowlist": ["openrouter", "groq", "mistral"],
  "scoring_weights": {
    "health_score": 0.4,
    "latency_p95": 0.3,
    "quota_remaining": 0.2,
    "task_fit": 0.1
  }
}

Response:
{
  "id": 5,
  "combo_name": "auto/my-free-pool",
  "enabled": true
}
```

---

## 🧪 测试方案

### 单元测试

```go
func TestResolver_Resolve(t *testing.T) {
    // 测试内置模板回退
    // 测试数据库模板覆盖
    // 测试非 auto/* 模型
}

func TestVirtualFactory_Build(t *testing.T) {
    // 测试凭据候选加载
    // 测试 keyless 候选加载
    // 测试配额过滤
    // 测试候选数量限制
}

func TestEngine_SelectCandidate(t *testing.T) {
    // 测试评分计算
    // 测试分层逻辑
    // 测试明显优胜者选择
    // 测试轮换逻辑
}
```

### 集成测试

```bash
# 测试 auto/free 路由
curl -X POST http://localhost:8781/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "model": "auto/free",
    "messages": [{"role": "user", "content": "Hello"}]
  }'

# 验证响应头中的实际提供商
# X-LLM-Gateway-Provider: openrouter
# X-LLM-Gateway-Model: openai/gpt-3.5-turbo:free
```

---

## 📈 监控指标

```go
// Auto combo 使用次数
counter("auto_combo.requests", tags: [combo_name, selected_provider])

// 候选池大小分布
histogram("auto_combo.candidate_pool_size", tags: [combo_name])

// 选择延迟
histogram("auto_combo.selection_latency_ms", tags: [combo_name])

// Fallback 次数
counter("auto_combo.fallbacks", tags: [combo_name, failed_provider])
```

---

**下一步**: 阅读 `10-AUDIT-CHECKLIST.md` 进行方案审计。
