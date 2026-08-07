# OmniFree 应用层集成设计方案

**设计时间**: 2026-08-07  
**目标**: 将 OmniFree 数据层接入 gateway 请求链，实现 `auto/free` 等虚拟路由的完整生命周期

**前置条件**: 数据层已修复完成（commit e5528809），迁移和 seed 可正常部署

---

## 1. 集成架构

### 1.1 当前请求链（简化）

```
HTTP Request
  → ChatHandler.ServeHTTP
  → resolveCandidatesForRequest(provider.Client, model, tenant)
  → provider.Client.GetCandidates() 
      ↓ 返回 []provider.Candidate
  → executor.Execute(candidates)
  → OnStreamCompleted 回调
```

### 1.2 集成后架构

```
HTTP Request (model="auto/free")
  → ChatHandler.ServeHTTP
  → 检测 auto/* 前缀
  → autoCombo.Resolver.Resolve(model, tenant)
      ↓ 返回 AutoComboSpec
  → autoCombo.VirtualFactory.Build(spec, tenant)
      ↓ 内部调用 provider.Client.GetCandidates 获取完整候选
      ↓ 按 free_resource_catalog 过滤
      ↓ 配额预检
      ↓ 返回 []provider.Candidate (复用现有类型)
  → executor.Execute(candidates)
  → OnStreamCompleted 回调
      ↓ quota.Record(success, tokens, tenant)
  → 429 处理路径
      ↓ quota.CorrectFromHeaders(headers)
```

---

## 2. 核心接口适配

### 2.1 VirtualFactory 改造

**当前问题**: `VirtualFactory.Build()` 返回自定义 `[]autocombo.Candidate`，与 executor 需要的 `[]provider.Candidate` 不兼容。

**解决方案**: 重构 VirtualFactory 为候选过滤器而非独立构建器

```go
// 新接口设计
type VirtualFactory struct {
    db           *sql.DB
    quotaTracker *freeresource.QuotaTracker
    provider     providerResolver  // 注入 provider.Client
}

// Build 改为接收已有候选，按免费资源目录过滤并评分
func (vf *VirtualFactory) Build(
    ctx context.Context,
    spec *AutoComboSpec,
    baseCandidates []provider.Candidate,  // 从 provider.Client 获取
    tenantID string,
) ([]provider.Candidate, error) {
    // 1. 加载 free_resource_catalog 中该租户启用的免费资源
    freeResources := vf.loadFreeResources(ctx, tenantID, spec)
    
    // 2. 过滤 baseCandidates，只保留匹配免费资源的候选
    //    匹配规则: (provider_code, model_id) 存在于 free_resource_catalog
    filtered := vf.filterByFreeResources(baseCandidates, freeResources)
    
    // 3. 配额预检，过滤耗尽的候选
    withQuota := vf.filterByQuota(ctx, filtered, tenantID)
    
    // 4. 按 AutoComboSpec 的 ScoringWeights 评分并排序
    scored := vf.scoreAndSort(withQuota, spec)
    
    // 5. 返回的仍是 []provider.Candidate，executor 可直接使用
    return scored, nil
}
```

**关键点**:
- 不再自己查询 credentials，改为接收 `provider.Client.GetCandidates()` 的结果
- 只负责"过滤 + 评分"，不负责"构建候选"
- 返回类型保持 `[]provider.Candidate`，无需适配层

### 2.2 候选匹配逻辑

```go
type freeResourceEntry struct {
    ProviderCode string
    ModelID      string
    // ... 其他元数据
}

func (vf *VirtualFactory) filterByFreeResources(
    candidates []provider.Candidate,
    freeResources []freeResourceEntry,
) []provider.Candidate {
    // 构建 (provider_code, model_id) 索引
    freeIndex := make(map[string]bool)
    for _, fr := range freeResources {
        key := fr.ProviderCode + ":" + fr.ModelID
        freeIndex[key] = true
    }
    
    var result []provider.Candidate
    for _, cand := range candidates {
        // provider.Candidate 有 CatalogCode (provider) 和 RawModel/StandardizedName
        key := cand.CatalogCode + ":" + cand.StandardizedName
        if freeIndex[key] {
            result = append(result, cand)
        }
    }
    return result
}
```

---

## 3. ChatHandler 集成点

### 3.1 注入 AutoCombo 组件

```go
// ChatHandler 新增字段
type ChatHandler struct {
    // ... 现有字段
    
    // autoCombo (OmniFree Phase 3) resolves auto/* virtual routes.
    // When non-nil and model starts with "auto/", the handler uses
    // AutoCombo resolver + factory to filter free candidates before
    // passing them to the executor. nil disables (auto/* treated as
    // unknown model).
    autoComboResolver *autocombo.Resolver
    autoComboFactory  *autocombo.VirtualFactory
    quotaTracker      *freeresource.QuotaTracker
}

// 新增 setter (在 main.go 调用)
func (h *ChatHandler) SetAutoCombo(
    resolver *autocombo.Resolver,
    factory *autocombo.VirtualFactory,
    tracker *freeresource.QuotaTracker,
) {
    h.autoComboResolver = resolver
    h.autoComboFactory = factory
    h.quotaTracker = tracker
}
```

### 3.2 请求路由逻辑

在 `resolveCandidatesForRequest` 之前插入 AutoCombo 分支：

```go
// 在 handler.go ServeHTTP 的候选解析前
clientModel := extractModel(bodyBytes)

// OmniFree: auto/* 虚拟路由
if h.autoComboResolver != nil && strings.HasPrefix(clientModel, "auto/") {
    spec, err := h.autoComboResolver.Resolve(r.Context(), clientModel, tenantID)
    if err != nil || spec == nil {
        // auto/unknown 或数据库错误，返回 404
        writeErrorJSONCtx(r.Context(), w, http.StatusNotFound, requestID, "model_not_found", "Unknown auto combo: "+clientModel, nil)
        return
    }
    
    // 获取基础候选池（所有可用的免费模型）
    // 这里可以用一个宽泛的模型列表，或者从 free_resource_catalog 动态构建
    baseCandidates, policy, modality, err := resolveCandidatesForRequest(
        r.Context(), h.provider, spec.BaseModel(), clientID.Fingerprint.ClientProfile, tenantID, bodyBytes,
    )
    if err != nil {
        // ... 错误处理
    }
    
    // 使用 VirtualFactory 过滤和评分
    candidates, err := h.autoComboFactory.Build(r.Context(), spec, baseCandidates, tenantID)
    if err != nil || len(candidates) == 0 {
        writeErrorJSONCtx(r.Context(), w, http.StatusServiceUnavailable, requestID, "no_free_candidates", "No available free resources", nil)
        return
    }
    
    // 继续正常执行流程，candidates 已是过滤后的免费候选
    // ... executor.Execute(candidates)
} else {
    // 非 auto/* 或 autoCombo 未启用，走原有逻辑
    candidates, policy, modality, err := resolveCandidatesForRequest(...)
    // ...
}
```

**注意**: `spec.BaseModel()` 需要实现，返回一个能覆盖免费资源的宽泛模型名，或者 VirtualFactory 内部遍历 `free_resource_catalog` 为每个 (provider, model) 调用 `GetCandidates`。

---

## 4. 配额生命周期集成

### 4.1 Record 调用点

在 `OnStreamCompleted` 回调中记录配额消耗：

```go
// handler.go ServeHTTP
result, execErr = h.executor.Execute(&executors.ExecParams{
    // ... 其他参数
    OnStreamCompleted: func(outcome executors.StreamOutcome) {
        h.emitTrace(r.Context(), requestID, gwtrace.StreamEnd(outcome.ChunkCount, outcome.Interrupted))
        
        // OmniFree: 记录免费资源配额
        if h.quotaTracker != nil && strings.HasPrefix(clientModel, "auto/") && result != nil {
            // 从 ExecuteResult 提取实际使用的 credential/provider/model
            credID := result.SelectedCredentialID
            provider := result.SelectedProviderCode
            model := result.SelectedModelID
            tokens := result.TotalTokens  // prompt + completion
            
            _ = h.quotaTracker.Record(r.Context(), freeresource.RecordRequest{
                CredentialID: credID,
                ProviderCode: provider,
                ModelID:      model,
                WindowTypes:  []freeresource.WindowType{
                    freeresource.WindowTypeDay1,
                    freeresource.WindowTypeMonth1,
                },
                Success:    outcome.Kind == "" || outcome.Kind == errorsx.KindNone,
                TokenCount: int64(tokens),
                TenantID:   tenantID,
                Timestamp:  time.Now().UTC(),
            })
        }
    },
})
```

### 4.2 CorrectFromHeaders 调用点

在 429 错误处理路径调用：

```go
// executor 内部或 handler 的重试逻辑
if resp.StatusCode == 429 {
    headers := make(map[string]string)
    for k, v := range resp.Header {
        if len(v) > 0 {
            headers[k] = v[0]
        }
    }
    
    if h.quotaTracker != nil {
        _ = h.quotaTracker.CorrectFromHeaders(ctx, freeresource.CorrectionRequest{
            CredentialID: credentialID,
            ProviderCode: providerCode,
            ModelID:      modelID,
            Headers:      headers,
            TenantID:     tenantID,
        })
    }
}
```

---

## 5. Worker 启动

### 5.1 main.go 集成

```go
// cmd/gateway/main.go, 在其他 worker 启动后

// OmniFree workers (启动需要条件：数据库可用 且 OmniFree 已启用)
if dbConn.Enabled() && os.Getenv("OMNIFREE_ENABLED") == "true" {
    stdlibDB := dbConn.Stdlib()
    
    // 配额重置 worker (5 分钟周期)
    quotaResetWorker := freequotareset.NewWorker(stdlibDB, 5*time.Minute)
    go quotaResetWorker.Run(context.Background())
    defer func() {
        // 优雅停止 (通过 context cancel)
    }()
    
    // 配额清理 worker (24 小时周期)
    quotaCleanupWorker := freequotacleanup.NewWorker(stdlibDB, 24*time.Hour, 90)
    go quotaCleanupWorker.Run(context.Background())
    
    log.Println("OmniFree workers started")
}
```

### 5.2 Worker 租户边界修正

**当前问题**: worker 全表更新/删除，无租户条件

**修复**:

```go
// bg/freequotareset/worker.go
func (w *Worker) resetExpiredWindows(ctx context.Context) error {
    // 添加 tenant_id 条件或依赖 RLS
    // 如果应用启动时设置了 app.current_tenant，RLS 会自动过滤
    // 否则需要显式 WHERE tenant_id = $1
    result, err := w.db.ExecContext(ctx, `
        UPDATE free_quota_tracker
        SET is_exhausted = FALSE,
            exhausted_at = NULL,
            auto_reset_at = NULL
        WHERE is_exhausted = TRUE
          AND auto_reset_at IS NOT NULL
          AND auto_reset_at <= now()
    `)
    // RLS 会限制更新范围到当前租户
    // 如果 worker 需要跨租户，设置 app.bypass_rls = 'true'
}
```

---

## 6. 配额窗口语义修正

### 6.1 当前问题

1. **rolling 窗口** - `hour-5/day-7` 使用 `End=ts`，每次请求生成新 `window_start`，无法复用行
2. **零时间戳** - `Record` 接受零值，导致 year-1 窗口
3. **首次 429** - `CorrectFromHeaders` 只 UPDATE，首次无行时静默失败

### 6.2 修复方案

```go
// quota_tracker.go

func (qt *QuotaTracker) Record(ctx context.Context, req RecordRequest) error {
    // 1. 默认零时间戳为当前 UTC 时间
    if req.Timestamp.IsZero() {
        req.Timestamp = time.Now().UTC()
    }
    
    // 2. 计算窗口时，rolling 窗口使用分桶策略
    windows := qt.computeWindows(req.Timestamp, req.WindowTypes)
    
    // 3. 对 hour-5/day-7，使用小时/日边界作为 window_start
    //    例如 hour-5: window_start = ts.Truncate(time.Hour).Add(-4*time.Hour)
    //           end   = window_start.Add(5*time.Hour)
    //    这样同一 5 小时窗口内的所有请求共享同一行
    
    // ... 其余逻辑不变
}

func (qt *QuotaTracker) CorrectFromHeaders(ctx context.Context, req CorrectionRequest) error {
    // 改为 UPSERT，首次也能写入
    _, err := qt.db.ExecContext(ctx, `
        INSERT INTO free_quota_tracker (
            credential_id, provider_code, model_id, window_type,
            window_start, window_end, request_count, token_count,
            corrected_limit, is_exhausted, exhausted_at, tenant_id, updated_at
        )
        VALUES ($1, $2, $3, 'day-1', $4, $5, 0, 0, $6, TRUE, now(), $7, now())
        ON CONFLICT (credential_id, provider_code, model_id, window_type, window_start, tenant_id)
        DO UPDATE SET
            corrected_limit = EXCLUDED.corrected_limit,
            is_exhausted = TRUE,
            exhausted_at = now(),
            auto_reset_at = EXCLUDED.window_end,
            updated_at = now()
    `, ...)
    
    return err
}
```

---

## 7. 测试计划

### 7.1 单元测试补充

```go
// domains/autocombo/virtual_factory_test.go
func TestVirtualFactory_FilterByFreeResources(t *testing.T) {
    // 测试候选过滤逻辑
}

func TestVirtualFactory_BuildWithProviderCandidates(t *testing.T) {
    // 集成测试：传入 provider.Candidate，验证输出
}

// domains/freeresource/quota_tracker_integration_test.go
func TestQuotaTracker_RecordAndPreflight_RealDB(t *testing.T) {
    if testing.Short() {
        t.Skip()
    }
    // 使用 testcontainers 或 CI PostgreSQL 执行真实迁移和 CRUD
}
```

### 7.2 集成测试

```go
// domains/streaming/handler_autocombo_test.go
func TestChatHandler_AutoFree_E2E(t *testing.T) {
    // 1. 设置测试数据库和 seed
    // 2. 注入 AutoCombo 到 ChatHandler
    // 3. 发送 POST /v1/chat/completions, model="auto/free"
    // 4. 验证候选来自免费资源
    // 5. 验证配额记录到数据库
}
```

### 7.3 回归测试

- 验证普通模型请求（非 `auto/*`）不受影响
- 验证精确 `model=auto` 的现有 autoroute 功能继续工作
- 验证 `auto/unknown` 返回 404

---

## 8. 实施步骤

### Phase 1: 接口适配（不改变行为）

1. ✅ 重构 `VirtualFactory.Build()` 接收 `[]provider.Candidate`
2. ✅ 添加 `ChatHandler.SetAutoCombo()` setter
3. ✅ 添加 `auto/*` 检测但暂时返回 404（占位）
4. ✅ 编译通过，现有测试不受影响

### Phase 2: 核心集成

1. ✅ 实现 `Resolver.Resolve()` 数据库查询
2. ✅ 实现 `VirtualFactory` 过滤和评分逻辑
3. ✅ 在 `ChatHandler` 中接入 `auto/*` 路由
4. ✅ 添加单元测试和 mock 测试

### Phase 3: 配额生命周期

1. ✅ 在 `OnStreamCompleted` 调用 `Record`
2. ✅ 在 429 路径调用 `CorrectFromHeaders`
3. ✅ 修正配额窗口语义
4. ✅ 添加配额集成测试

### Phase 4: Worker 和完整验证

1. ✅ 在 `main.go` 启动 worker
2. ✅ 执行完整 E2E 测试
3. ✅ 回归测试
4. ✅ 提交并推送

---

## 9. 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| VirtualFactory 过滤逻辑错误 | `auto/free` 返回空候选 | 详细单元测试 + 日志 |
| 配额记录失败 | 配额追踪不准确 | 错误不阻塞请求，只记录日志 |
| Worker 全表更新影响性能 | 数据库负载 | 添加 LIMIT/分批 + 监控 |
| `auto/*` 与现有 `auto` 冲突 | 路由混淆 | 精确前缀匹配，`auto` 走原有 autoroute |
| 租户隔离失效 | 跨租户数据泄露 | RLS policy + 集成测试验证 |

---

## 10. 性能考虑

- **候选过滤**: O(n) 遍历，n 为 provider.Candidate 数量（通常 < 100）
- **配额查询**: 索引覆盖 `(credential_id, window_type)`，单次 < 5ms
- **Worker**: 每 5 分钟/24 小时执行，影响可忽略
- **RLS**: 已建立 `tenant_id` 索引，查询不受影响

---

## 11. 监控指标

建议添加以下 Prometheus 指标：

```go
omnifree_auto_requests_total{combo="auto/free",status="success|no_candidates|error"}
omnifree_quota_records_total{provider,model,window_type}
omnifree_quota_corrections_total{provider}
omnifree_worker_runs_total{worker="reset|cleanup"}
```

---

## 总结

本方案通过"过滤器"而非"构建器"模式接入 OmniFree，最小化对现有架构的侵入：

- ✅ 复用 `provider.Candidate` 类型，无需适配层
- ✅ 在 `resolveCandidatesForRequest` 前插入 `auto/*` 分支
- ✅ 配额生命周期通过回调钩子接入，不改变主流程
- ✅ Worker 独立启动，可通过环境变量开关

**预计工作量**: 2-3 天（含测试和验证）

**下一步**: 开始 Phase 1 实施，或根据评审意见调整方案
