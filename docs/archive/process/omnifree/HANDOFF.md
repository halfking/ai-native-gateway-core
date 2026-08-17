# OmniFree 工作交接文档

**交接日期**: 2026-08-07  
**当前状态**: 数据层完成，应用层待集成

---

## 已完成工作

### ✅ 深度审计
- 3 个并行 agent 审计
- 发现 11 项阻断问题
- 详细报告: `docs/omnifree/AUDIT-ROUND2-FIXES.md`

### ✅ 数据层修复 (Commit e5528809)
- SQL 迁移可执行（事务、TEXT tenant、RLS、幂等索引）
- seed 导入可执行（pq.Array、tenant-scoped upsert）
- 部署脚本安全（移除凭据、psql 参数）
- Go 代码 tenant 类型对齐
- **验证**: go vet/build/test 全部通过

### ✅ 集成方案设计 (Commit 529fa128)
- `INTEGRATION-PLAN.md`: 详细 4 Phase 设计
- `FINAL-REPORT-ROUND2.md`: 完整审计报告

---

## 当前可用功能

### 数据层 (8.5/10)

```bash
# 1. 执行迁移
export OMNIFREE_DATABASE_URL='postgres://user:pass@host:port/db'
psql "$OMNIFREE_DATABASE_URL" -v ON_ERROR_STOP=1 -f sql/migrations/075-omnifree-schema.sql

# 2. 导入 seed
go run cmd/seed-free-resources/main.go \
    --db-url="$OMNIFREE_DATABASE_URL" \
    --catalog docs/omnifree/seed/free_resource_catalog.json \
    --templates docs/omnifree/seed/auto_combo_templates.json \
    --keyless docs/omnifree/seed/keyless_providers.json

# 3. 验证
psql "$OMNIFREE_DATABASE_URL" -c "SELECT COUNT(*) FROM free_resource_catalog;"
```

### 已就绪的模块

- ✅ `domains/freeresource`: QuotaTracker (Record/Preflight/CorrectFromHeaders)
- ✅ `domains/autocombo`: Resolver (Resolve 数据库模板/内置模板)
- ✅ `domains/autocombo`: Engine (评分/分层/选择)
- ✅ `bg/freequotareset`: 配额重置 worker
- ✅ `bg/freequotacleanup`: 配额清理 worker

---

## 待完成：应用层集成

### Phase 1: 接口适配 (1 天)

**目标**: 不改变现有行为，只添加接口

#### 1.1 重构 VirtualFactory

**当前问题**: `VirtualFactory.Build()` 查询不存在的 credentials 列

**解决方案**: 改为接收 `[]provider.Candidate`

```go
// 新签名
func (vf *VirtualFactory) BuildFromCandidates(
    ctx context.Context,
    spec *AutoComboSpec,
    baseCandidates []provider.Candidate,  // 从 provider.Client 获取
    tenantID string,
) ([]provider.Candidate, error) {
    // 1. 加载 free_resource_catalog
    freeResources := vf.loadFreeResourceCatalog(ctx, tenantID, spec)
    
    // 2. 过滤：只保留 (CatalogCode, StandardizedName) 在目录中的候选
    filtered := vf.filterByFreeResources(baseCandidates, freeResources)
    
    // 3. 配额预检
    withQuota := vf.filterByQuota(ctx, filtered, tenantID)
    
    // 4. 评分排序（复用现有 Engine）
    scored := vf.engine.ScoreAndSort(withQuota, spec.ScoringWeightsJSON)
    
    return scored, nil
}
```

**关键**: 
- 输入/输出都是 `[]provider.Candidate`
- 不再自己查 credentials
- 移除 `loadCredentialCandidates` 和 `loadKeylessCandidates`

#### 1.2 添加 ChatHandler setter

```go
// domains/streaming/handler.go

type ChatHandler struct {
    // ... 现有字段
    
    // OmniFree (optional)
    autoComboResolver *autocombo.Resolver
    autoComboFactory  *autocombo.VirtualFactory
    quotaTracker      *freeresource.QuotaTracker
}

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

#### 1.3 验证

```bash
go build ./domains/autocombo ./domains/streaming
go test ./domains/autocombo ./domains/streaming
# 确保现有测试不受影响
```

---

### Phase 2: 核心集成 (1 天)

#### 2.1 auto/* 路由检测

在 `domains/streaming/handler.go` 的 `ServeHTTP` 中：

```go
clientModel := extractModel(bodyBytes)

// OmniFree: auto/* 虚拟路由
if h.autoComboResolver != nil && strings.HasPrefix(clientModel, "auto/") {
    spec, err := h.autoComboResolver.Resolve(r.Context(), clientModel, tenantID)
    if err != nil || spec == nil {
        writeErrorJSONCtx(r.Context(), w, http.StatusNotFound, requestID, 
            "model_not_found", "Unknown auto combo: "+clientModel, nil)
        return
    }
    
    // 获取宽泛的候选池（所有模型或指定 base model）
    allCandidates, policy, modality, err := resolveCandidatesForRequest(
        r.Context(), h.provider, "*", clientProfile, tenantID, bodyBytes,
    )
    
    // 使用 VirtualFactory 过滤免费候选
    candidates, err := h.autoComboFactory.BuildFromCandidates(
        r.Context(), spec, allCandidates, tenantID,
    )
    
    if len(candidates) == 0 {
        writeErrorJSONCtx(r.Context(), w, http.StatusServiceUnavailable, requestID,
            "no_free_candidates", "No available free resources", nil)
        return
    }
    
    // 继续正常执行流程
    // ... executor.Execute(candidates)
}
```

#### 2.2 注意事项

- `model="*"` 或实现 `spec.BaseModel()` 返回宽泛模型
- 保留精确 `model="auto"` 走原有 autoroute
- 添加 `auto/*` 请求计数指标

---

### Phase 3: 配额生命周期 (0.5 天)

#### 3.1 Record 调用

```go
// handler.go 的 OnStreamCompleted 回调
OnStreamCompleted: func(outcome executors.StreamOutcome) {
    // ... 现有逻辑
    
    if h.quotaTracker != nil && strings.HasPrefix(clientModel, "auto/") && result != nil {
        _ = h.quotaTracker.Record(r.Context(), freeresource.RecordRequest{
            CredentialID: result.SelectedCredentialID,
            ProviderCode: result.SelectedProviderCode,
            ModelID:      result.SelectedModelID,
            WindowTypes:  []freeresource.WindowType{
                freeresource.WindowTypeDay1,
                freeresource.WindowTypeMonth1,
            },
            Success:    !outcome.Interrupted,
            TokenCount: int64(result.TotalTokens),
            TenantID:   tenantID,
            Timestamp:  time.Now().UTC(),
        })
    }
}
```

#### 3.2 CorrectFromHeaders 调用

```go
// executor 429 处理
if resp.StatusCode == 429 && h.quotaTracker != nil {
    headers := make(map[string]string)
    for k, v := range resp.Header {
        if len(v) > 0 {
            headers[k] = v[0]
        }
    }
    _ = h.quotaTracker.CorrectFromHeaders(ctx, freeresource.CorrectionRequest{
        CredentialID: credentialID,
        ProviderCode: providerCode,
        ModelID:      modelID,
        Headers:      headers,
        TenantID:     tenantID,
    })
}
```

---

### Phase 4: Worker 启动 (0.5 天)

#### 4.1 main.go 启动

```go
// cmd/gateway/main.go

if dbConn.Enabled() && os.Getenv("OMNIFREE_ENABLED") == "true" {
    stdlibDB := dbConn.Stdlib()
    
    // 配额重置 worker
    quotaResetWorker := freequotareset.NewWorker(stdlibDB, 5*time.Minute)
    go quotaResetWorker.Run(context.Background())
    
    // 配额清理 worker
    quotaCleanupWorker := freequotacleanup.NewWorker(stdlibDB, 24*time.Hour, 90)
    go quotaCleanupWorker.Run(context.Background())
    
    log.Println("OmniFree workers started")
}
```

#### 4.2 注入 AutoCombo

```go
if os.Getenv("OMNIFREE_ENABLED") == "true" {
    stdlibDB := dbConn.Stdlib()
    
    resolver := autocombo.NewResolver(stdlibDB)
    quotaTracker := freeresource.NewQuotaTracker(stdlibDB)
    factory := autocombo.NewVirtualFactory(stdlibDB, quotaTracker)
    
    chatHandler.SetAutoCombo(resolver, factory, quotaTracker)
}
```

---

## 测试计划

### 单元测试

```go
// domains/autocombo/virtual_factory_test.go
func TestVirtualFactory_BuildFromCandidates(t *testing.T) {
    // 传入 mock provider.Candidate
    // 验证过滤和评分逻辑
}
```

### 集成测试

```go
// domains/streaming/handler_autocombo_test.go
func TestChatHandler_AutoFree_E2E(t *testing.T) {
    // 1. 设置测试数据库
    // 2. 注入 AutoCombo
    // 3. POST /v1/chat/completions, model="auto/free"
    // 4. 验证返回免费候选
    // 5. 验证配额记录
}
```

### 回归测试

- ✅ 普通模型请求不受影响
- ✅ 精确 `model=auto` 继续走 autoroute
- ✅ `auto/unknown` 返回 404

---

## 关键文件清单

### 需要修改

- `domains/autocombo/virtual_factory.go` - 重构 Build 方法
- `domains/streaming/handler.go` - 添加 auto/* 分支
- `cmd/gateway/main.go` - 注入 AutoCombo + 启动 worker

### 需要新建

- `domains/autocombo/virtual_factory_integration.go` - 新的过滤逻辑
- `domains/streaming/handler_autocombo_test.go` - 集成测试

---

## 预估工作量

| Phase | 工作量 | 关键任务 |
|-------|--------|---------|
| Phase 1 | 1 天 | 重构 VirtualFactory + setter |
| Phase 2 | 1 天 | auto/* 路由 + 候选解析 |
| Phase 3 | 0.5 天 | Record/Correct 调用 |
| Phase 4 | 0.5 天 | Worker 启动 + E2E 测试 |
| **总计** | **2-3 天** | |

---

## 风险与注意事项

### 高风险

1. **VirtualFactory 过滤逻辑错误** → 详细单元测试
2. **配额记录失败** → 错误不阻塞请求，只记录日志
3. **与现有 auto 冲突** → 精确前缀匹配

### 中风险

1. **Worker 性能影响** → 分批执行 + 监控
2. **RLS 权限问题** → 集成测试验证

---

## 紧急提醒

### 🔴 凭据轮换

**立即执行**：

```sql
-- 主机: 172.16.2.210:5432
-- 用户: kxuser
-- 旧密码: kxuser123 (已泄露)

ALTER USER kxuser WITH PASSWORD '<新强密码>';
```

---

## 相关文档

- **审计报告**: `docs/omnifree/AUDIT-ROUND2-FIXES.md`
- **集成方案**: `docs/omnifree/INTEGRATION-PLAN.md`
- **最终报告**: `docs/omnifree/FINAL-REPORT-ROUND2.md`
- **本交接文档**: `docs/omnifree/HANDOFF.md`

---

## 联系与支持

- **代码仓库**: https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go
- **当前分支**: main
- **最新提交**: 8e072528
- **数据层修复**: e5528809

---

**交接完成时间**: 2026-08-07  
**下一步**: 按 Phase 1-4 实施应用层集成
