package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
	"github.com/kaixuan/llm-gateway-go/internal/taskbus"
)

// cacheSavePayload 是 cache.save.v1 的入队载荷。
type cacheSavePayload struct {
	TenantID string `json:"tenant_id"`
	Model    string `json:"model"`
	Hash     string `json:"hash"`
	Value    []byte `json:"value"`
	TTLNanos int64  `json:"ttl_nanos"`
}

// CacheSaveTaskHook 是 cache_save 的 TaskBus 示范迁：Execute 只入队。
type CacheSaveTaskHook struct {
	eq  taskbus.Enqueuer
	ttl time.Duration
}

// NewCacheSaveTaskHook 构造 TaskBus 版 cache_save（生产挂载用）。
func NewCacheSaveTaskHook(eq taskbus.Enqueuer, ttl time.Duration) *CacheSaveTaskHook {
	return &CacheSaveTaskHook{eq: eq, ttl: ttl}
}

// Name 返回 Hook 名称（与 Inline 版区分，便于门禁定位）。
func (h *CacheSaveTaskHook) Name() string { return "cache.save.taskbus" }

// Priority PostResponse。
func (h *CacheSaveTaskHook) Priority() int { return 50 }

// Enabled 与 Inline 版同口径。
func (h *CacheSaveTaskHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	_ = ctx
	if env == nil || env.UpstreamResponse == nil || h == nil || h.eq == nil {
		return false
	}
	if env.Metadata == nil {
		return true
	}
	hit, _ := env.Metadata[MetaKeyCacheHit].(bool)
	return !hit
}

// DispatchKind 声明 TaskBus。
func (h *CacheSaveTaskHook) DispatchKind() pipeline.DispatchKind {
	return pipeline.DispatchTaskBus
}

// TaskSpec 合同三件套。
func (h *CacheSaveTaskHook) TaskSpec() pipeline.TaskSpec {
	return pipeline.TaskSpec{
		TaskType:      taskbus.TaskTypeCacheSaveV1,
		SchemaVersion: 1,
		IdempotencyOf: cacheSaveIdempotencyKey,
	}
}

func cacheSaveIdempotencyKey(env *domain.PipelineRequest) string {
	if env == nil {
		return "cache.save:nil"
	}
	reqID := ""
	if env.Envelope != nil {
		reqID = env.Envelope.RequestID
	}
	model := ""
	if env.Metadata != nil {
		model, _ = env.Metadata[MetaKeyModel].(string)
	}
	hash := hashBytes(env.TransformedRequest)
	return fmt.Sprintf("%s:%s:%s:%s", reqID, env.TenantID, model, hash)
}

// Execute 只构造 payload 并入队（业务在 Handler）。
func (h *CacheSaveTaskHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if h == nil || h.eq == nil {
		return fmt.Errorf("cache.save.taskbus: nil enqueuer")
	}
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	model, _ := env.Metadata[MetaKeyModel].(string)
	payload, err := json.Marshal(cacheSavePayload{
		TenantID: env.TenantID,
		Model:    model,
		Hash:     hashBytes(env.TransformedRequest),
		Value:    append([]byte(nil), env.UpstreamResponse...),
		TTLNanos: int64(h.ttl),
	})
	if err != nil {
		return err
	}
	key := cacheSaveIdempotencyKey(env)
	return h.eq.Enqueue(ctx, taskbus.TaskTypeCacheSaveV1, key, 1, payload)
}

// OnError 入队失败不影响响应。
func (h *CacheSaveTaskHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	_ = ctx
	if env != nil {
		if env.Metadata == nil {
			env.Metadata = make(map[string]any)
		}
		env.Metadata["cache_save_error"] = err.Error()
	}
	return nil
}

// RegisterCacheSaveHandler 登记 cache.save.v1 消费者（写 Store）。
func RegisterCacheSaveHandler(r *taskbus.Registry, store Store) {
	if r == nil || store == nil {
		return
	}
	r.MustRegister(taskbus.TaskTypeCacheSaveV1, taskbus.HandlerFunc(func(ctx context.Context, task taskbus.Task) error {
		var p cacheSavePayload
		if err := json.Unmarshal(task.Payload, &p); err != nil {
			return err
		}
		return store.Set(&CacheEntry{
			Key:       CacheKey{TenantID: p.TenantID, Model: p.Model, Hash: p.Hash},
			Value:     p.Value,
			CreatedAt: time.Now(),
			TTL:       time.Duration(p.TTLNanos),
		})
	}))
}

var (
	_ pipeline.Hook        = (*CacheSaveTaskHook)(nil)
	_ pipeline.Kinded      = (*CacheSaveTaskHook)(nil)
	_ pipeline.TaskSpecced = (*CacheSaveTaskHook)(nil)
)
