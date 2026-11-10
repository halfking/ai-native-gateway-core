// bg/model_tier.go — "常用模型" (commonly-used model) tier判定, 用于自检分级.
//
// 需求: 针对常用模型强化自检 (更高频/更深/更高优先级/更快恢复), 其它模型降低频度.
//
// 常用模型 = 静态精选 routing_policy.featured_models ∪ 用量 Top-N
// (_request_logs_hot 近窗口成功调用最多的 N 个模型). 与 daily-selfcheck 的
// featured→most_used 分级一致. 用量集合 + 静态集合由后台 ticker 每 10min 刷新一次,
// 通过 atomic.Value 提供无锁读. nil-db / 未启动时 IsFeaturedModel 回退到 false
// (不影响故障路径 — node-probe 仍按错误触发, 只是Priority/backoff 不分级).
//
// 详见 docs/自检优化/02-常用模型分级自检.md.
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/internal/dbrows"
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
	db   *pgxpool.Pool
	cfg  ModelTierConfig
	cur  atomic.Pointer[featuredSet]
	done chan struct{}
	once atomic.Bool
	stop sync.Once
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
// The prime refresh runs synchronously under a 10s timeout so a DB hiccup at
// startup cannot block main() indefinitely (audit issue #14).
func (m *ModelTier) Start(ctx context.Context) {
	if m == nil || m.db == nil || !m.once.CompareAndSwap(false, true) {
		return
	}
	primeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	m.refresh(primeCtx)
	cancel()
	SpawnLoop(ctx, "model_tier.loop", m.loop)
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
			refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
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
		m.stop.Do(func() { close(m.done) })
	}
}

func (m *ModelTier) refresh(ctx context.Context) {
	fs := &featuredSet{static: map[string]struct{}{}, usage: map[string]struct{}{}}

	// Static featured list (operator-curated, scoped to the 'default' tenant
	// since routing_policy is per-tenant and probe-side categorization is global;
	// cross-tenant unioning would mis-classify tenant-specific models. Override
	// tenant via probe.featured_tenant setting if needed for multi-tenant
	// deployments).
	//
	// ★ 2026-11-10 恢复检测及时性轮：★ **两个数据源共用一套展开逻辑**。
	//
	// 取证事实（211 / acc-pg17）：featured 的两个来源都存**裸 canonical 名** ——
	//   ① routing_policy.featured_models（36 条，运维策展的主力清单）
	//   ② models_canonical.featured（2026-11-09 迁移 953 加的列机制）
	// 而探针侧三个门（probe_model_scope.go 的 scope 门、probe_service.go 的
	// backoff 乘数、model_tier.go 的队列优先级）都用
	// globalIsFeaturedModel(raw, "") —— **只比 raw 名**；但
	// provider_models.raw_model_name 多数带**供应商命名空间前缀**
	// （z-ai/glm-5.3-flash、codex/gpt-6-sol、minimaxai/minimax-m3）。
	// ⇒ 只装载裸名 ⇒ 命名空间形态**两个来源都**拿不到 featured 待遇
	//   ⇒ 恢复检测仍迟到（免费面实测 p90 511 分钟 / max 1791 分钟）。
	//
	// ⇒ 故此处对**两个来源**一律展开：每个 featured canonical 的**全部**
	//   provider_models.raw_model_name 变体一并进 static 集合。
	//   裸名 + 命名空间形态都进 ⇒ 三门同时生效（它们都读这一份集合）。
	//
	// ★ 爆炸半径（211 真库实测，2026-11-10）：
	//   仅展开 ②（canonical 侧）：static 45 → 89（+44 条 raw 变体）
	//   再展开 ①（routing_policy 侧）：static 89 → **210**（+121 条）
	//   新增获得 featured 待遇的绑定 = free 46 对（可用 20）+ per_token 51 对
	//   （可用 37）+ token_plan 10 对（可用 10）= 107 对；
	//   ★ 其中 58 对**从未进入探测调度**（无错误证据 ⇒ 泵不挑），
	//     仅 9 对处于失败态（410/429/502）会因 backoff 减半多几次重试
	//   ⇒ **日探测量增量 < 0.3%**（基线 24h 9233 次探测）。
	//
	// ★ 口径一致性：本展开只影响**探针分层**，不进入路由与发现口径
	//   ⇒ 不改路由行为。
	staticTenant := settings.GetPlatformString("probe.featured_tenant", "default")
	// ★ 2026-11-11（断链⑦）：feat_names CTE 抽到 featured_sources.go，
	// 与 selfcheck/model_probe watchdog 共用同一出处（本查询的 SQL 体
	// 逐字未变——只是单一事实源化）。
	if rows, err := m.db.Query(ctx, `
		WITH feat_names AS (`+FeaturedNamesCTESQL()+`)
		SELECT fn.name, COALESCE(pm.raw_model_name, '')
		FROM feat_names fn
		LEFT JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.name
		LEFT JOIN provider_models pm ON pm.canonical_id = mc.id`, staticTenant); err == nil {
		defer rows.Close()
		for rows.Next() {
			var name, raw string
			if err := rows.Scan(&name, &raw); err != nil {
				dbrows.SkipOrFail("bg.ModelTier.refresh/staticFeatured", err)
				continue
			}
			if name = normalizeModelKey(name); name != "" {
				fs.static[name] = struct{}{}
			}
			// ★ raw 变体：裸名之外的命名空间形态也进集合。
			//   LEFT JOIN ⇒ 没有 provider_models 行的名字只得到裸名（正常）。
			if raw = normalizeModelKey(raw); raw != "" {
				fs.static[raw] = struct{}{}
			}
		}
		// R67-B 修正：这里原先只 warn 就继续，随后把**整个**截断集合写进
		// `m.cur.Store(fs)`，永久替换掉之前那份完整的 featured 集合。本函数
		// 与 hotconfig.reload 的形状完全相同（整份配置的全量替换），而 R66
		// 正因如此把 hotconfig 升级成硬失败——两处口径不一致。
		// 截断的集合会让高频模型静默退出深探范围，且直到下一个 tick 才会自愈。
		if err := rows.Err(); err != nil {
			slog.Warn("model_tier: static featured row iteration aborted; keeping previous featured set",
				"tenant", staticTenant, "error", err)
			return
		}
	} else {
		// 12h 审计修正：Query 本身失败（连接抖动/超时——DB 故障最常见形态）
		// 原先同样只 warn 后落到 m.cur.Store(fs)，把空 static 集合全量替换
		// 掉上一份完整集合。与上方 rows.Err 分支同因同果：全量替换形态下
		// 任一段失败都必须保旧集，等下一个 tick 重试。
		slog.Warn("model_tier: load static featured failed; keeping previous featured set",
			"tenant", staticTenant, "error", err)
		return
	}

	// Usage Top-N. Audit fix: _request_logs_hot has no `model` column; the real
	// model-name is COALESCE(outbound_model, client_model). GROUP BY raw_model
	// dedupes across alias variants (audit issue #11).
	if topN := settings.GetPlatformInt("probe.featured_usage_top_n", 20); topN > 0 {
		// 2026-11-05: default window 24h — aligned with INV-3 automatic probe
		// scope. Also exclude probe traffic: without the flag probes themselves
		// fed the Top-N, keeping probed-but-unused models "featured" forever.
		windowHours := settings.GetPlatformInt("probe.featured_usage_window_hours", 24)
		if windowHours <= 0 {
			windowHours = 24
		}
		if rows, err := m.db.Query(ctx, `
			SELECT raw_model FROM (
				-- ★ 890（v1 族退役）：源换成会话族热腿（v1 热表已 DROP）。
				-- 窗口 72h ≫ hot 保留期 8h，v1 侧本来就只看得到最近 8h，
				-- 会话族热腿保留期同款 ⇒ 口径不变。
				SELECT rl.raw_model_name AS raw_model,
				       count(*) AS calls
				FROM `+db.SessionFamilyTurnsHotSourceSQL()+` rl
				WHERE rl.success
				  AND rl.ts > now() - make_interval(hours => $1)
				  -- 会话族没有 quality_flags ⇒ 用会话族那条探针排除谓词。
				  AND `+fmt.Sprintf(probeTrafficExclusionPredicateSession, "rl", "rl", "rl")+`
				  -- 模型名口径用 rl.raw_model_name（会话族里它就是
				  -- COALESCE(outbound_model, client_model) 的同义列，见
				  -- internal/sessionv2mirror/s1a_fields.go 的取值口径登记）。
				  AND rl.raw_model_name IS NOT NULL
				  AND rl.raw_model_name <> ''
				GROUP BY rl.raw_model_name
			) t
			ORDER BY calls DESC
			LIMIT $2`, windowHours, topN); err == nil {
			defer rows.Close()
			for rows.Next() {
				var model string
				if err := rows.Scan(&model); err != nil {
					if dbrows.SkipOrFail("bg.ModelTier.refresh/usageTopN", err) {
						continue
					}
				} else if model = normalizeModelKey(model); model != "" {
					fs.usage[model] = struct{}{}
				}
			}
			// R67-B 修正：同上。usage 段截断会让高频模型退出深探范围。
			if err := rows.Err(); err != nil {
				slog.Warn("model_tier: usage top-N row iteration aborted; keeping previous featured set",
					"window_hours", windowHours, "top_n", topN, "error", err)
				return
			}
		} else {
			// 12h 审计修正：同上 static 段——Query 失败同样保旧集，
			// 不落全量替换。
			slog.Warn("model_tier: load usage top-N failed; keeping previous featured set",
				"window_hours", windowHours, "top_n", topN, "error", err)
			return
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
