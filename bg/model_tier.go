// bg/model_tier.go — "常用模型" (commonly-used model) tier判定, 用于自检分级.
//
// 需求: 针对常用模型强化自检 (更高频/更深/更高优先级/更快恢复), 其它模型降低频度.
//
// 常用模型 = 静态精选 routing_policy.featured_models ∪ 用量 Top-N
// (request_logs_hot 近窗口成功调用最多的 N 个模型). 与 daily-selfcheck 的
// featured→most_used 分级一致. 用量集合 + 静态集合由后台 ticker 每 10min 刷新一次,
// 通过 atomic.Value 提供无锁读. nil-db / 未启动时 IsFeaturedModel 回退到 false
// (不影响故障路径 — node-probe 仍按错误触发, 只是Priority/backoff 不分级).
//
// 详见 docs/自检优化/02-常用模型分级自检.md.
package bg

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// featuredSet is an immutable snapshot of the "常用模型" set, published via
// atomic.Value for lock-free reads. Either field matching ⇒ featured.
type featuredSet struct {
	static map[string]struct{} // routing_policy.featured_models (raw + standardized)
	usage  map[string]struct{} // top-N by request volume
}

func (s *featuredSet) has(model string) bool {
	if s == nil || model == "" {
		return false
	}
	_, ok := s.static[model]
	if !ok {
		_, ok = s.usage[model]
	}
	return ok
}

// ModelTierConfig tunes the usage-tier refresh.
type ModelTierConfig struct {
	RefreshInterval time.Duration // default 10m
}

// ModelTier caches the featured set and answers IsFeaturedModel.
type ModelTier struct {
	db    *pgxpool.Pool
	cfg   ModelTierConfig
	cur   atomic.Pointer[featuredSet]
	done  chan struct{}
	once  atomic.Bool
}

// NewModelTier constructs the tier cache. nil db ⇒ a no-op tier (IsFeaturedModel
// always false), so callers need not branch.
func NewModelTier(db *pgxpool.Pool, cfg ModelTierConfig) *ModelTier {
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 10 * time.Minute
	}
	return &ModelTier{db: db, cfg: cfg, done: make(chan struct{})}
}

// Start launches the background refresh. Idempotent; safe to call once.
func (m *ModelTier) Start(ctx context.Context) {
	if m == nil || m.db == nil || !m.once.CompareAndSwap(false, true) {
		return
	}
	// Prime synchronously so the first probe tick already sees a fresh set.
	m.refresh(ctx)
	go m.loop(ctx)
}

func (m *ModelTier) loop(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.done:
			return
		case <-ticker.C:
			refreshCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			m.refresh(refreshCtx)
			cancel()
		}
	}
}

// Stop halts the refresh loop.
func (m *ModelTier) Stop() {
	if m == nil {
		return
	}
	if m.once.Load() {
		select {
		case <-m.done:
		default:
			close(m.done)
		}
	}
}

func (m *ModelTier) refresh(ctx context.Context) {
	fs := &featuredSet{static: map[string]struct{}{}, usage: map[string]struct{}{}}

	// Static featured list (operator-curated, all tenants unioned).
	if rows, err := m.db.Query(ctx, `SELECT COALESCE(featured_models, ARRAY[]::TEXT[]) FROM routing_policy`); err == nil {
		for rows.Next() {
			var arr []string
			if err := rows.Scan(&arr); err == nil {
				for _, mm := range arr {
					if mm = normalizeModelKey(mm); mm != "" {
						fs.static[mm] = struct{}{}
					}
				}
			}
		}
		rows.Close()
	} else {
		slog.Warn("model_tier: load static featured failed", "error", err)
	}

	// Usage Top-N.
	if topN := settings.GetPlatformInt("probe.featured_usage_top_n", 20); topN > 0 {
		windowHours := settings.GetPlatformInt("probe.featured_usage_window_hours", 168)
		if windowHours <= 0 {
			windowHours = 168
		}
		if rows, err := m.db.Query(ctx, `
			SELECT model
			FROM request_logs_hot
			WHERE success AND ts > now() - make_interval(hours => $1)
			  AND model IS NOT NULL AND model <> ''
			GROUP BY model
			ORDER BY count(*) DESC
			LIMIT $2`, windowHours, topN); err == nil {
			for rows.Next() {
				var model string
				if err := rows.Scan(&model); err == nil {
					if model = normalizeModelKey(model); model != "" {
						fs.usage[model] = struct{}{}
					}
				}
			}
			rows.Close()
		} else {
			slog.Warn("model_tier: load usage top-N failed", "error", err)
		}
	}

	// Merge usage into static so callers can check one map if they prefer; keep
	// them separate here for observability (StaticCount/UsageCount).
	m.cur.Store(fs)
	slog.Info("model_tier: featured set refreshed",
		"static", len(fs.static), "usage", len(fs.usage))
}

// IsFeaturedModel reports whether the model is "常用" (static featured ∪ usage).
// Accepts both the raw model name and an optional canonical/standardized name
// (both are checked against the cached set). Safe for concurrent use.
func (m *ModelTier) IsFeaturedModel(rawModel, canonical string) bool {
	if m == nil {
		return false
	}
	fs := m.cur.Load()
	if fs == nil {
		return false
	}
	if fs.has(normalizeModelKey(rawModel)) {
		return true
	}
	if canonical != "" {
		return fs.has(normalizeModelKey(canonical))
	}
	return false
}

// StaticCount / UsageCount are small diagnostic helpers.
func (m *ModelTier) StaticCount() int {
	if fs := m.cur.Load(); fs != nil {
		return len(fs.static)
	}
	return 0
}
func (m *ModelTier) UsageCount() int {
	if fs := m.cur.Load(); fs != nil {
		return len(fs.usage)
	}
	return 0
}

// normalizeModelKey lowercases + trims so raw vs canonical casing differences
// don't split one model into two keys. (provider_models stores lowercase
// raw_model_name; client-supplied model may differ in case.)
func normalizeModelKey(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			out = append(out, byte(r-'A'+'a'))
		case r == ' ' || r == '\t' || r == '\n':
			// drop whitespace
		default:
			// append the rune as-is (UTF-8); lowercase only ASCII for simplicity,
			// vendor model names are ASCII.
			if r < 128 {
				out = append(out, byte(r))
			} else {
				out = append(out, []byte(string(r))...)
			}
		}
	}
	return string(out)
}

// ── Global accessor (lowest-friction wiring) ────────────────────────────────
//
// A process-wide ModelTier avoids threading it through every probe struct.
// main.go calls SetGlobalModelTier once after construction; probe sites read via
// globalIsFeaturedModel. nil ⇒ false (no tier data ⇒ no differentiation).

var globalModelTier atomic.Pointer[ModelTier]

// SetGlobalModelTier installs the process-wide tier cache (call once at startup).
func SetGlobalModelTier(m *ModelTier) { globalModelTier.Store(m) }

// globalIsFeaturedModel is the nil-safe global lookup used by probe producers.
func globalIsFeaturedModel(rawModel, canonical string) bool {
	if m := globalModelTier.Load(); m != nil {
		return m.IsFeaturedModel(rawModel, canonical)
	}
	return false
}

// FeaturedQueuePriority returns the Priority to use when enqueuing a probe for a
// model: the featured priority setting for 常用 models, else the passed fallback.
func FeaturedQueuePriority(rawModel string, fallback int16) int16 {
	if globalIsFeaturedModel(rawModel, "") {
		if p := settings.GetPlatformInt("probe.featured_queue_priority", 80); p > 0 {
			return int16(p)
		}
	}
	return fallback
}
