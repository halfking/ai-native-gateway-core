package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// MetaKeyModel 模型名 metadata 键名。
//
// 调用方负责在进入 Pipeline 前把 model 写入 env.Metadata[MetaKeyModel]；
// CacheLookupHook 与 CacheSaveHook 都从此键读取 model，避免重复约定。
const MetaKeyModel = "model"

// MetaKeyCacheHit 缓存命中标记 metadata 键名。
//
// 命中时设 true；CacheSaveHook 借此跳过写入。
const MetaKeyCacheHit = "cache_hit"

// CacheLookupHook 在 PreRouting 阶段检查缓存命中。
//
// 行为：
//   - Enabled: env.TransformedRequest 非 nil 且缓存命中未标记
//   - Execute: 用 (TenantID, Model, hash(TransformedRequest)) 取缓存；
//     命中 → 设置 env.UpstreamResponse + env.StatusCode=200 + cache_hit=true
//     未命中 → 设置 cache_hit=false，继续走 Upstream
//   - OnError: 缓存错误**透传**（不静默吞），由调用方决定是否降级
//
// 短路语义：
//
//	命中后 Pipeline 仍可继续（不直接 return error）；但调用方可以
//	在执行 Upstream 阶段前检查 env.UpstreamResponse != nil 跳过真正调用。
//	本 Hook 不修改 env.Error，避免破坏 Pipeline 整体错误流。
type CacheLookupHook struct {
	store Store
}

// NewCacheLookupHook 构造 CacheLookupHook。
func NewCacheLookupHook(store Store) *CacheLookupHook {
	return &CacheLookupHook{store: store}
}

// Name 返回 Hook 名称。
func (h *CacheLookupHook) Name() string { return "cache.lookup" }

// Priority 返回 Hook 优先级（PreRouting 阶段）。
func (h *CacheLookupHook) Priority() int { return 50 }

// Enabled 报告 Hook 是否启用。
func (h *CacheLookupHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	if env == nil || env.TransformedRequest == nil {
		return false
	}
	// 已命中过（或已显式标记 miss）则跳过
	if env.Metadata == nil {
		return true
	}
	if _, exists := env.Metadata[MetaKeyCacheHit]; exists {
		return false
	}
	return true
}

// Execute 执行缓存查找。
func (h *CacheLookupHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	model, _ := env.Metadata[MetaKeyModel].(string)
	key := CacheKey{
		TenantID: env.TenantID,
		Model:    model,
		Hash:     hashBytes(env.TransformedRequest),
	}
	entry, hit, err := h.store.Get(key)
	if err != nil {
		// 不静默吞：在 metadata 上记录错误供上游决策
		env.Metadata["cache_lookup_error"] = err.Error()
		env.Metadata[MetaKeyCacheHit] = false
		return err
	}
	if hit && entry != nil {
		env.Metadata[MetaKeyCacheHit] = true
		env.UpstreamResponse = entry.Value
		env.StatusCode = 200
	} else {
		env.Metadata[MetaKeyCacheHit] = false
	}
	return nil
}

// OnError 缓存 lookup 失败时的处理：透传 error（不吞）。
func (h *CacheLookupHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	// 缓存查找失败：标记 metadata 后让 Pipeline 继续走 Upstream（降级）
	// 因此这里返回 nil 表示"已处理"
	if env != nil && env.Metadata != nil {
		env.Metadata[MetaKeyCacheHit] = false
		env.Metadata["cache_lookup_error"] = err.Error()
	}
	return nil
}

// CacheSaveHook 在 PostResponse 阶段保存响应到缓存。
//
// 行为：
//   - Enabled: 缓存未命中 且 UpstreamResponse 非空
//   - Execute: 写入 Store
//   - OnError: 吞掉 error（保存失败不应影响响应返回）
type CacheSaveHook struct {
	store Store
	ttl   time.Duration
}

// NewCacheSaveHook 构造 CacheSaveHook。
// ttl<=0 表示永不过期。
func NewCacheSaveHook(store Store, ttl time.Duration) *CacheSaveHook {
	return &CacheSaveHook{store: store, ttl: ttl}
}

// Name 返回 Hook 名称。
func (h *CacheSaveHook) Name() string { return "cache.save" }

// Priority 返回 Hook 优先级（PostResponse 阶段）。
func (h *CacheSaveHook) Priority() int { return 50 }

// Enabled 报告 Hook 是否启用。
func (h *CacheSaveHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	if env == nil || env.UpstreamResponse == nil {
		return false
	}
	if env.Metadata == nil {
		return true
	}
	hit, _ := env.Metadata[MetaKeyCacheHit].(bool)
	// 已命中（lookup 阶段已取过）则不重复保存
	if hit {
		return false
	}
	return true
}

// Execute 执行缓存保存。
func (h *CacheSaveHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	model, _ := env.Metadata[MetaKeyModel].(string)
	key := CacheKey{
		TenantID: env.TenantID,
		Model:    model,
		Hash:     hashBytes(env.TransformedRequest),
	}
	return h.store.Set(&CacheEntry{
		Key:       key,
		Value:     env.UpstreamResponse,
		CreatedAt: time.Now(),
		TTL:       h.ttl,
	})
}

// OnError 缓存保存失败：吞掉 error（不影响响应）。
func (h *CacheSaveHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	if env != nil && env.Metadata != nil {
		env.Metadata["cache_save_error"] = err.Error()
	}
	return nil
}

// hashBytes 计算字节切片的 SHA-256 十六进制哈希。
func hashBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// 编译期接口断言。
var (
	_ pipeline.Hook = (*CacheLookupHook)(nil)
	_ pipeline.Hook = (*CacheSaveHook)(nil)
)
