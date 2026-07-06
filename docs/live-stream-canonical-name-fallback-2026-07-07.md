# 实时请求流字段 Fallback 与模型标准名称聚合修复

**日期**: 2026-07-07  
**分支**: `fix/live-stream-canonical-name-fallback`  
**关联**: 基于前一轮修复（`fix/live-stream-dimension-queues`）的二次审计发现

## 一、问题背景

用户反馈实时请求流分维列表仍存在问题：

1. **字段取值缺少 fallback 链**：当 Model/ModelCategory 为空时，直接跳过或归入"其它"，未尝试从相近字段取值
2. **模型分维按凭据级名称聚合**：同一模型在不同凭据下可能有不同上游名称（如 `gpt-4-0613` vs `gpt-4-turbo`），当前按 `outboundModel` 聚合，导致同一模型分散到多条泳道；应按 `canonical_name`（标准名称）聚合

## 二、深度审计发现

### 发现 1：字段取值缺少关键 fallback 环节

| 字段 | 当前逻辑 | 缺失的 fallback | 影响 |
|------|---------|----------------|------|
| **Model** | `outboundModel → clientModel` | **未使用 `CanonicalID`** | 有 canonical_id 的请求仍可能显示为空模型 |
| **ModelCategory** | 从 `Model` 推导 | **未从 `Provider` 推导** | OpenAI 凭据请求模型为空时无法归入 "openai" vendor |
| **ProviderCode** | `credential → provider` ✅ | 无问题 | - |

**关键问题**：
- `RequestLogEntry` 包含 `CanonicalID` 字段（`telemetry/client.go:102`），但 `adminLiveRequestFromEntry` 和 `LiveRequestFromTelemetry` **完全未使用**
- 当 Model 为空但 Provider 已知时（如某些流式请求只有 provider_id），无法推导 ModelCategory

### 发现 2：模型分维使用错误的聚合键

**当前行为**：
- `LiveRequest.Model` 存储 `outboundModel`（上游名称）
- `liveStreamDimensionKey("model", req)` 返回 `req.Model`
- 聚合时按 `outboundModel` 分组

**问题示例**：
- 凭据 A：`gpt-4` → 上游 `gpt-4-0613`
- 凭据 B：`gpt-4` → 上游 `gpt-4-turbo`
- 当前：显示为两条独立泳道
- 期望：聚合到同一泳道 `gpt-4`（canonical_name）

**根因**：`LiveRequest` 结构体缺少 `CanonicalName` 字段，分维逻辑未使用标准名称。

## 三、修复方案

### 修复 1：添加 canonical_id → canonical_name 反查机制

**`admin/live_stream_sse.go`**：
- 新增 `canonicalCache sync.Map`：缓存 `canonical_id → canonical_name` 映射
- 新增 `CanonicalNameFor(ctx, canonicalID)` 方法：从 `models_canonical` 表查询并缓存
- 查询逻辑：
  ```sql
  SELECT canonical_name
  FROM models_canonical
  WHERE id = $1 AND COALESCE(status, 'active') = 'active'
  ```

### 修复 2：实现完整的字段 fallback 链

**Model fallback 链**（`LiveRequestFromTelemetry`）：
```
outboundModel → clientModel → canonical_name (从 CanonicalID 反查) → (keep empty)
```

**ModelCategory fallback 链**：
```
从 Model 推导（ModelVendorFor） → 从 Provider 推导（VendorFromProvider）
```

**新增 `VendorFromProvider` 函数**：
- 静态映射 provider code → vendor（如 `openai` → `openai`）
- 支持复合 provider code 部分匹配（如 `openai-azure` → `openai`）
- 覆盖主流 provider：OpenAI、Anthropic、Google、阿里、智谱、DeepSeek、字节、百度、月之暗面等

### 修复 3：添加 CanonicalName 字段并用于聚合

**`LiveRequest` 结构体**：
```go
Model         string `json:"model"`           // Display name (backward compat)
CanonicalName string `json:"canonical_name"`  // Standard name for aggregation
```

**`liveStreamDimensionKey` 函数**（model 维度）：
```go
if req.CanonicalName != "" {
    return req.CanonicalName  // 优先使用标准名称
}
return req.Model  // Fallback 保证向后兼容
```

### 修复 4：更新数据流各环节

1. **`adminLiveRequestFromEntry`**（`cmd/gateway/main.go`）：
   - 从 `entry.CanonicalID` 提取 `canonicalID` 并传递给 `LiveRequestFromTelemetry`

2. **`LiveRequestFromTelemetry`**（`admin/live_stream_sse.go`）：
   - 接收 `canonicalID` 参数
   - Model fallback 链中调用 `h.CanonicalNameFor(ctx, canonicalID)`
   - 填充 `out.CanonicalName` 用于聚合
   - ModelCategory fallback 链中调用 `VendorFromProvider(providerCode)`

3. **Redis 序列化**（`admin/live_stream_redis_store.go`）：
   - `liveRequestRedisPayload` 添加 `CanonicalName` 字段
   - `marshalLiveRequestRedisPayload`/`unmarshalLiveRequestRedisPayload` 同步更新

4. **DB replay 路径**（`admin/live_stream_sse.go` `replay` 函数）：
   - SQL JOIN `models_canonical` 表获取 `canonical_name`
   - Scan 时填充 `r.CanonicalName`
   - 应用 ModelCategory fallback（从 Model → 从 Provider）

5. **Idle marker**（`admin/live_stream_redis_store.go`）：
   - model 维度的 idle marker 同时设置 `Model` 和 `CanonicalName`，保证聚合逻辑一致

## 四、fallback 链完整路径图

```
┌─ Model ────────────────────────────────────────────────┐
│ outboundModel → clientModel → canonical_name (from ID) │
└────────────────────────────────────────────────────────┘
                         ↓
┌─ ModelCategory ──────────────────────────────────────────┐
│ ModelVendorFor(Model) → VendorFromProvider(ProviderCode) │
└──────────────────────────────────────────────────────────┘
                         ↓
┌─ ProviderCode (已有) ─────────────────┐
│ credential → provider (已在调用者实现) │
└───────────────────────────────────────┘
```

## 五、修改文件清单

| 文件 | 改动 |
|------|------|
| `admin/live_stream_sse.go` | 新增 `canonicalCache`、`CanonicalNameFor`、`VendorFromProvider`；更新 `LiveRequest` 结构体；重写 `LiveRequestFromTelemetry` 实现完整 fallback；更新 `replay` SQL |
| `admin/live_stream_redis_store.go` | 更新 `liveRequestRedisPayload` 和序列化函数；更新 `liveStreamDimensionKey` 使用 `CanonicalName`；更新 idle marker 创建逻辑 |
| `cmd/gateway/main.go` | 更新 `adminLiveRequestFromEntry` 提取并传递 `canonicalID` |

## 六、测试

- `go build ./admin` ✅
- `go build ./cmd/gateway` ✅
- `go test ./admin` ✅

## 七、部署后验证要点

1. **模型聚合**：同一模型（如 gpt-4）在不同凭据下聚合到同一泳道，显示标准名称
2. **字段 fallback**：
   - 当 `outboundModel`/`clientModel` 都为空但 `canonical_id` 存在时，Model 显示 canonical_name
   - 当 Model 为空但 Provider 已知时，ModelCategory 从 Provider 推导（如 openai provider → openai vendor）
3. **向后兼容**：无 canonical_id 的旧请求仍正常显示（fallback 到 outboundModel）
4. **缓存命中率**：观察日志中 canonical_id 查询频率，确认缓存生效

## 八、潜在影响与风险

| 项目 | 风险评估 | 缓解措施 |
|------|---------|---------|
| 向后兼容 | 低 | `CanonicalName` 为空时 fallback 到 `Model`；前端消费新增的 `canonical_name` 字段为可选 |
| 性能影响 | 低 | canonical_id 查询有缓存；DB replay 新增的 JOIN 是 LEFT JOIN 且走索引 |
| 数据一致性 | 低 | 已有请求的 Redis payload 不含 `canonical_name` 字段，反序列化时为空字符串，fallback 逻辑自动处理 |
| Provider→Vendor 映射准确性 | 中 | `VendorFromProvider` 覆盖主流 provider；新 provider 需手动添加映射 |

## 九、后续优化建议

1. **Provider→Vendor 映射维护**：将静态映射表改为从 DB 查询（providers 表新增 `vendor` 字段）
2. **前端展示优化**：在 model tile 的 tooltip 中同时显示 `canonical_name` 和 `outbound_model`，便于用户区分
3. **监控指标**：添加 `canonical_cache_hit_rate` 指标，观察缓存效果
