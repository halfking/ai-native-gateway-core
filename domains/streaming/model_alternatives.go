package streaming

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // matches handler.go's existing import of the same package
	"github.com/kaixuan/llm-gateway-go/provider"
)

// ModelAlternative is one entry in the "pick a different model" list returned
// when the requested model has no routable node.
type ModelAlternative struct {
	// Model is the canonical name the client should send back in `model`.
	Model string `json:"model"`
	// DisplayName is human-facing (e.g. "Claude Sonnet 4.6"); falls back to
	// Model when the catalog has no display name.
	DisplayName string `json:"display_name,omitempty"`
	// Family groups entries in a picker ("claude", "gpt", "gemini").
	Family string `json:"family,omitempty"`
	// ContextWindow is in tokens; omitted when unknown, so a client cannot
	// mistake "unknown" for zero.
	ContextWindow *int `json:"context_window,omitempty"`
	// Featured marks membership in routing_policy.featured_models — the
	// operator-curated set, as opposed to something merely reachable.
	Featured bool `json:"featured"`
	// Reason explains why this entry is being offered. Values:
	// "task_match" (matches the session's task type), "featured",
	// "popular" (recent successful usage).
	Reason string `json:"reason,omitempty"`
}

// ModelAlternativesResult is what the handler embeds in the 503 body.
type ModelAlternativesResult struct {
	// RequestedModel is the model that had no routable node.
	RequestedModel string `json:"requested_model"`
	// TaskType is the session's inferred task type when one was available,
	// so the client can explain WHY these models were suggested.
	TaskType string `json:"task_type,omitempty"`
	// RequestedModality is the media modality the request carried, when it
	// carried one. It is set only on the modality-mismatch path, where the
	// client needs to know that the suggestions were filtered to models that
	// can actually read that media.
	//
	// ★ Without it, the client cannot tell "these are models that can take
	// your images" from "these are models that happen to be up" — and the
	// second reading is how a user ends up switching to another text-only
	// model and hitting the same wall.
	RequestedModality string `json:"requested_modality,omitempty"`
	// Alternatives is ordered best-first and may be empty, which is a
	// meaningful answer: nothing at all is routable right now, so the
	// client should wait rather than retry a different name.
	Alternatives []ModelAlternative `json:"alternatives"`
}

// maxModelAlternatives bounds the list. Long lists are unusable in a picker and
// make the error body large enough to matter on mobile links; ~8 is the most a
// person will read before giving up.
const maxModelAlternatives = 8

// alternativesQueryTimeout bounds the SYNCHRONOUS lookup on a cache miss. This
// runs on an already-failing request, so it must not add meaningful latency:
// better to return the plain 503 than to make a stalled client wait longer for
// a nicety.
const alternativesQueryTimeout = 800 * time.Millisecond

// alternativesRefreshBudget bounds the BACKGROUND recompute that fills the
// cache. It is decoupled from any request, so it can afford the query's
// intrinsic cost: R173（2026-10-28，252 PG 日志审计）EXPLAIN ANALYZE 实测本查询
// 干净负载下就要 1.27s（v_routable_credential_models 枚举 3.1 万凭据×模型对），
// 而 pgss 全量 mean 411ms / max 1.73s——横跨 800ms 同步预算两侧，预算命中即取消
// （252 生产 24h 窗实测 2,230 次「canceling statement due to user request」，
// 建议列表实际上从未送达）。后台预算放开到 5s，让重算真正算完进缓存。
const alternativesRefreshBudget = 5 * time.Second

// alternativesCacheTTL：缓存新鲜窗。数据源（可路由集=探针状态、featured=运营
// 编辑、热度=7 天流量）都是分钟级慢变量，2 分钟陈旧度对「换一个模型试试」的
// 建议场景无感；过长会让模型下线后建议仍指向它。
const alternativesCacheTTL = 2 * time.Minute

// alternativesCacheMaxKeys bounds the cache: key = (tenant, taskType)。taskType
// 来自 isKnownTaskType 的小闭集加 ""，tenant 在本部署面个位数 ⇒ 正常远小于上限；
// 上限只防异常面（如租户爆量）把缓存变成无界结构，超限逐出最旧。
const alternativesCacheMaxKeys = 64

// alternativesQueryer is the narrow slice of *pgxpool.Pool the finder needs,
// so tests can hand in a mock that would otherwise answer the lookup and
// thereby prove the dead-context guard below actually fires.
type alternativesQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// ModelAlternativesFinder resolves "what CAN this caller use right now".
//
// It is deliberately separate from the admin available-models catalog
// (admin/routing.go): that endpoint is a near-static inventory gated on
// mo.available / c.status / p.enabled, and it never consults
// v_routable_credential_models. A model whose only credentials are
// quota-exhausted, cooling, or in probe backoff still appears "available"
// there — precisely the state we are trying to route around here. Offering
// such a model as an alternative would send the client straight into a
// second failure.
type ModelAlternativesFinder struct {
	db alternativesQueryer
	// intentCache is autoroute's per-session task-type cache. Optional: nil
	// simply means no session-level task type, and the result falls back to
	// featured + popularity ordering.
	intentCache *autoroute.SessionIntentCache
	// cacheOnce/cache：结果缓存（按 tenant+taskType），见 alternativesCache。
	// ensureCache 惰性初始化，零值字面量构造（测试路径）同样可用。
	cacheOnce sync.Once
	cache     *alternativesCache
	// queryFn 是查询缝：默认走 alternativesSQL + db.Query；测试注入以观察
	// 「缓存命中不再发查询」「后台重算只发一次」「冷未命中同步预算失败后转后台」。
	// nil ⇒ 默认实现。
	// requestModality（★ 2026-10-28 模态不匹配轮）：过滤建议列表，只留真能
	// 服务该媒体模态的模型；'' = 不过滤。★ 合并教训：上游的缓存重构与本轮的
	// 模态过滤在同一条缝上相撞——缝必须带 modality，缓存键按模态分开，
	// 否则 vision 请求会命中「含 text-only 模型」的缓存列表，过滤形同虚设。
	queryFn func(ctx context.Context, excludeModel, tenantID, taskType, requestModality string) ([]ModelAlternative, error)
}

// alternativesCacheEntry 是一个 (tenant, taskType) 键的缓存槽。alts 为空且
// computedAt 有效是**有意义的答案**（当前什么路由都不可达），不是「没算过」。
type alternativesCacheEntry struct {
	alts       []ModelAlternative
	computedAt time.Time
	computing  bool
}

// alternativesCache 是 Find 的结果缓存：TTL 新鲜期内直接命中；过期条目继续
// 服务（stale-while-revalidate）并由单个后台 goroutine 重算（beginCompute
// 即 singleflight）；真正的冷未命中才走同步查询路径。
//
// ★ R173 修的病不是「查询慢」本身——慢是内在的（pgss mean 411ms，EXPLAIN
// 干净负载 1.27s），而是「每次失败请求都从头算一遍、算不完就被 800ms 预算
// 杀掉、建议列表从未送达」。分钟级慢变量的结果集按 (tenant, taskType) 缓存
// 后，DB 每个键每 TTL 至多算一次，失败请求零新增延迟。
type alternativesCache struct {
	mu      sync.Mutex
	now     func() time.Time // 可注入时钟；nil ⇒ time.Now
	ttl     time.Duration
	entries map[string]*alternativesCacheEntry
}

func newAlternativesCache(ttl time.Duration) *alternativesCache {
	return &alternativesCache{ttl: ttl, entries: make(map[string]*alternativesCacheEntry)}
}

func (c *alternativesCache) clockNow() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func cacheKey(tenantID, taskType, requestModality string) string {
	return tenantID + "\x00" + taskType + "\x00" + requestModality
}

// lookup 返回该键的**快照**（列表切片头 + 新鲜度 + 是否存在）。必须在锁内取值：
// 后台 store 会并发写条目字段，调用方绝不能拿着活条目指针在锁外读。
// 切片内容视为不可变（store 只换新切片，从不原位改写），快照因此安全。
func (c *alternativesCache) lookup(key string) (alts []ModelAlternative, fresh, found bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false, false
	}
	return e.alts, c.clockNow().Sub(e.computedAt) < c.ttl, true
}

// beginCompute 抢后台重算权：返回 false 表示已有 goroutine 在算（singleflight）。
func (c *alternativesCache) beginCompute(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		e = &alternativesCacheEntry{}
		c.entries[key] = e
		c.evictLocked()
	}
	if e.computing {
		return false
	}
	e.computing = true
	return true
}

func (c *alternativesCache) endCompute(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		e.computing = false
	}
}

func (c *alternativesCache) store(key string, alts []ModelAlternative) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		e = &alternativesCacheEntry{}
		c.entries[key] = e
		c.evictLocked()
	}
	e.alts = alts
	e.computedAt = c.clockNow()
}

// evictLocked 在超限时逐出 computedAt 最旧的槽。调用方必须已持锁。
func (c *alternativesCache) evictLocked() {
	if len(c.entries) <= alternativesCacheMaxKeys {
		return
	}
	var oldestKey string
	var oldest time.Time
	for k, e := range c.entries {
		if oldest.IsZero() || e.computedAt.Before(oldest) {
			oldestKey, oldest = k, e.computedAt
		}
	}
	delete(c.entries, oldestKey)
}

func (f *ModelAlternativesFinder) ensureCache() *alternativesCache {
	f.cacheOnce.Do(func() {
		// 已注入的缓存（测试缝）不覆盖——Once 只补默认值，不做替换。
		if f.cache == nil {
			f.cache = newAlternativesCache(alternativesCacheTTL)
		}
	})
	return f.cache
}

// filterAlternatives 是缓存服务路径的按请求收口：共享缓存里存的是**不排除任何
// 模型**的全量列表（后台重算传 ""），请求级排除（别把自己建议给自己）与条数
// 上限在这里补做。必须返回新切片——调用方还会改 Result 的其余字段，缓存槽里
// 的底层数组不能被外头共享。
func filterAlternatives(alts []ModelAlternative, requestedModel string) []ModelAlternative {
	out := make([]ModelAlternative, 0, len(alts))
	for _, a := range alts {
		if requestedModel != "" && strings.EqualFold(a.Model, requestedModel) {
			continue
		}
		out = append(out, a)
	}
	if len(out) > maxModelAlternatives {
		out = out[:maxModelAlternatives]
	}
	return out
}

func NewModelAlternativesFinder(db *pgxpool.Pool) *ModelAlternativesFinder {
	return &ModelAlternativesFinder{db: db}
}

// SetIntentCache wires autoroute's session intent cache so task-type-aware
// suggestions work on ordinary (non-`model=auto`) requests.
func (f *ModelAlternativesFinder) SetIntentCache(c *autoroute.SessionIntentCache) {
	if f != nil {
		f.intentCache = c
	}
}

// Find returns routable alternatives to requestedModel, ordered best-first.
//
// Ordering: entries matching the session's task type come first, then other
// featured models, then models with recent successful traffic. Within a group,
// higher recent usage wins.
//
// taskType may be "" when nothing is known about the session; the query then
// degrades to featured-then-popular.
//
// requestModality filters the suggestions to models that can actually serve
// that media modality ("" = no filter, the historical behaviour). Errors are
// logged and reported as an empty list, never propagated: the caller is
// already writing an error response and must not be derailed by a failure in
// the suggestion path.
//
// ★ Why the filter exists: the modality-mismatch path tells the client
// "this model cannot read your images — use one that can". A list that still
// contains text-only models makes that advice unusable: the user follows it
// and hits the identical wall one model over. The filter uses the SAME
// acceptance rule as the router's modality gate (provider.modalityAccepts),
// so a suggested model is guaranteed to be routable for that modality.
func (f *ModelAlternativesFinder) Find(
	ctx context.Context,
	requestedModel, tenantID, taskType, requestModality string,
) ModelAlternativesResult {
	result := ModelAlternativesResult{
		RequestedModel:    requestedModel,
		TaskType:          taskType,
		RequestedModality: strings.TrimSpace(strings.ToLower(requestModality)),
		Alternatives:      []ModelAlternative{},
	}
	if f == nil || f.db == nil {
		return result
	}

	// ★ 2026-10-26（252 PG 日志审计）：零候选 503 路径的调用方经常在进入本函数
	// 前就已经断开——客户端对 503 不等待建议列表。请求 ctx 已死时，这份列表必然
	// 随响应一起被丢弃，再往下的查询注定以「canceling statement due to user
	// request」收场：252 生产日志 10-24~10-26 三天实测 1,399 次取消（本查询独占
	// 同期 PG ERROR 噪音约 46%）。ctx 已死即返回空表，不再向连接池要连接。
	// （R173 复测：该守卫之后仍有 2,230 次/天取消——守卫只挡「进来前已死」，
	// 挡不住「跑超时被杀」，见下方缓存路径的由来。）
	if err := ctx.Err(); err != nil {
		return result
	}

	qfn := f.queryFn
	if qfn == nil {
		qfn = f.queryAlternativesSQL
	}
	c := f.ensureCache()
	// ★ 缓存键必须含 modality：同一 (tenant, taskType) 下，vision 请求与
	// 文本请求的合法建议集合不同——共享键会让 vision 请求命中一份
	// 「含 text-only 模型」的列表（2026-10-28 合并抓到）。
	key := cacheKey(tenantID, taskType, result.RequestedModality)

	// 命中（新鲜或过期）：条目存在就直接服务——过期条目后台重算（singleflight
	// 由 beginCompute 保证），请求侧零新增延迟。空列表且新鲜是**有意义的答案**
	//（当前无路由可达），照答，不重算。
	if cachedAlts, fresh, found := c.lookup(key); found {
		if !fresh && c.beginCompute(key) {
			f.refreshInBackground(key, tenantID, taskType, result.RequestedModality, qfn)
		}
		if cachedAlts != nil {
			result.Alternatives = filterAlternatives(cachedAlts, requestedModel)
			return result
		}
		if fresh {
			return result
		}
		// 过期且从未成功存过列表（历史重算一直失败）⇒ 落到冷路径再试一次。
	}

	// 冷未命中：同步尝试一次（800ms 预算，维持既有失败路径延迟契约）。成功即
	// 进缓存；失败（多半是被预算取消）不阻塞请求——转后台用宽预算重算，给
	// 下一个失败请求用。
	syncCtx, cancel := context.WithTimeout(ctx, alternativesQueryTimeout)
	alts, err := qfn(syncCtx, requestedModel, tenantID, taskType, result.RequestedModality)
	cancel()
	if err == nil {
		c.store(key, alts)
		result.Alternatives = filterAlternatives(alts, requestedModel)
		return result
	}
	slog.WarnContext(ctx, "model alternatives lookup failed; returning plain no_candidate",
		"error", err, "requested_model", requestedModel, "tenant_id", tenantID)
	f.refreshInBackground(key, tenantID, taskType, result.RequestedModality, qfn)
	return result
}

// queryAlternativesSQL 是生产查询实现：alternativesSQL + db.Query，行扫描错误
// 视为整体失败（不留部分行进缓存）。超时预算由调用方决定：同步路径
// alternativesQueryTimeout、后台路径 alternativesRefreshBudget。
func (f *ModelAlternativesFinder) queryAlternativesSQL(
	ctx context.Context, excludeModel, tenantID, taskType, requestModality string,
) ([]ModelAlternative, error) {
	rows, err := f.db.Query(ctx, alternativesSQL,
		strings.ToLower(strings.TrimSpace(excludeModel)),
		tenantID,
		taskType,
		maxModelAlternatives,
		strings.TrimSpace(strings.ToLower(requestModality)),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	alts := []ModelAlternative{}
	for rows.Next() {
		var (
			alt           ModelAlternative
			displayName   *string
			family        *string
			contextWindow *int
		)
		if err := rows.Scan(&alt.Model, &displayName, &family, &contextWindow,
			&alt.Featured, &alt.Reason); err != nil {
			slog.WarnContext(ctx, "model alternatives row scan failed", "error", err)
			continue
		}
		if displayName != nil {
			alt.DisplayName = *displayName
		}
		if family != nil {
			alt.Family = *family
		}
		alt.ContextWindow = contextWindow
		alts = append(alts, alt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return alts, nil
}

// refreshInBackground 用与请求解耦的 ctx（Background + 5s 预算）重算一个键。
// 调用方必须已通过 beginCompute 拿到重算权；成功进缓存，失败保留旧值并清
// computing 标志，让下一次失败请求能再试。
func (f *ModelAlternativesFinder) refreshInBackground(
	key, tenantID, taskType, requestModality string,
	qfn func(ctx context.Context, excludeModel, tenantID, taskType, requestModality string) ([]ModelAlternative, error),
) {
	go func() {
		defer f.cache.endCompute(key)
		ctx, cancel := context.WithTimeout(context.Background(), alternativesRefreshBudget)
		defer cancel()
		alts, err := qfn(ctx, "", tenantID, taskType, requestModality)
		if err != nil {
			slog.WarnContext(ctx, "model alternatives background refresh failed",
				"error", err, "tenant_id", tenantID, "task_type", taskType)
			return
		}
		f.cache.store(key, alts)
	}()
}

// alternativesSQL selects canonical models that have at least one routable
// (credential, model) binding right now.
//
// Availability comes from v_routable_credential_models.is_routable, the same
// gate the router itself uses, so anything offered here is genuinely reachable
// at this instant. The admin catalog's shallower filter would suggest models
// whose only credentials are quota-exhausted or cooling.
//
// Parameters:
//
//	$1 requested model (lowercased) — excluded from its own suggestion list
//	$2 tenant id                    — own-tenant plus shared 'default' providers
//	$3 task type ('' when unknown)  — drives the task_match tier
//	$4 row limit
//	$5 requested modality ('' = no filter) — restricts suggestions to models
//	   that can serve that media modality; see the routable CTE for the rule
//
// Ordering tiers (tier 1 first):
//
//	1 task_match — in task_default_routing for $3, primary tier before fallback
//	2 featured   — routing_policy.featured_models, operator-curated
//	3 popular    — recent successful traffic, as a last resort
//
// The 7-day usage count is a LEFT JOIN so a freshly-added featured model with
// no traffic still appears (at count 0) rather than being dropped.
//
// 2026-10-13 S3 波 2：usage_7d 热面由字面量常量改为拼接表达式（会话族原生
// 源），const 随之降为 var——见 usage_7d CTE 内注释。
//
// ★★ 2026-10-29：模态接受臂（/*MODALITY_ACCEPT_ARMS*/）由
// provider/modality_accept.go 的**唯一片段**渲染（$MODALITY → "$5"）⇒ 本文件
// 不再是判据的第二份抄本 ⇒ 「改一份忘另一份」从根上消失（此前只有锁步门
// 让它「当场红」）。渲染用 strings.Replace 而不是把片段拼进 raw string 中间：
// 拼接会把 raw string 拆成半个 ⇒ 扫描器在拆分点失明（实测见
// provider/sql_comment_syntax_test.go 的 TestSQLCommentScanner_SeesPastConcatenation）。
var alternativesSQL = strings.Replace(
	alternativesSQLRaw,
	"/*MODALITY_ACCEPT_ARMS*/",
	provider.ModalityAcceptArmsSQL("$5"),
	1,
)

// alternativesSQLRaw 是建议列表查询的正文（模态接受臂以占位符留在里面）。
var alternativesSQLRaw = `
WITH policy AS (
    SELECT COALESCE(featured_models, ARRAY[]::TEXT[]) AS featured
    FROM routing_policy
    WHERE tenant_id = 'default'
    ORDER BY id
    LIMIT 1
),
routable AS (
    -- Distinct canonical models with >= 1 routable binding. GROUP BY because a
    -- model routable via several credentials must still be offered once, and
    -- 522 lets each (credential×model) binding carry its own context-window
    -- override; the advertised window is the most generous routable binding so
    -- the picker does not under-sell a model that one credential widened.
    SELECT mc.id AS canonical_id,
           mc.canonical_name,
           mc.display_name,
           mc.family,
           MAX(COALESCE(cmb.context_window_override, mc.context_window_override, mc.context_window)) AS context_window
    FROM v_routable_credential_models v
    JOIN provider_models pm ON pm.id = v.provider_model_id
    JOIN credential_model_bindings cmb ON cmb.credential_id = v.credential_id AND cmb.provider_model_id = v.provider_model_id
    JOIN models_canonical mc ON mc.id = pm.canonical_id
    WHERE v.is_routable = TRUE
      AND (v.tenant_id = $2 OR v.tenant_id = 'default')
      AND COALESCE(mc.status, 'active') = 'active'
      AND mc.canonical_name <> $1
      -- ★ 2026-10-28: 模态过滤（$5）。当本次请求带图/音频而请求的模型是
      -- 文本模型时，建议列表必须只列**真能读该模态**的模型。
      -- $5 为空（文本请求 / 历史路径）⇒ 不过滤，保持既有行为。
      -- ★★ 2026-10-29：判据**不再在本文件里抄写**——模态接受臂由
      -- provider/modality_accept.go 的唯一片段渲染（$MODALITY → "$5"）。
      -- 本块此前是判据的**第二份 SQL 抄本**（闭世界：无兜底精确臂，未知请求
      -- 值 ⇒ 建议列表为空；而第一份 candidateQuerySQLBase 是开世界兜底）⇒
      -- 两份抄本在域外语义相反 ⇒ 现在同源渲染 ⇒ 分歧从根上消失。
      -- 「只有一份」由 domains/streaming/modality_single_source_test.go 钉住。
      AND (
          /*MODALITY_ACCEPT_ARMS*/
      )
    GROUP BY mc.id, mc.canonical_name, mc.display_name, mc.family
),
task_models AS (
    -- Empty when $3 is '' (unknown task type), which collapses tier 1.
    SELECT canonical_model, MIN(
               CASE tier WHEN 'primary' THEN 1 WHEN 'secondary' THEN 2 ELSE 3 END
           ) AS tier_rank
    FROM task_default_routing
    WHERE $3 <> ''
      AND task_type = $3
      AND (tenant_id = $2 OR tenant_id IS NULL)
      -- 2026-09-08 audit: the platform-level default row is tenant_id IS NULL
      -- (477/480 seeds INSERT without tenant_id; admin/auto_route_defaults.go
      -- NULLs it). The literal 'default' belongs to routing_policy's own
      -- convention — copied here it matched nothing, so tier-1 task_match
      -- came up empty whenever only the platform default routing existed.
    GROUP BY canonical_model
),
usage_7d AS (
    -- _request_logs stores the standardized name in canonical_model
    -- (migration 458), not canonical_name.
    --
    -- 2026-09-25 audit (252 PG log): the canonical view
    -- request_logs_with_current_month is a UNION of session_turns and
    -- _request_logs arms with nested anti-join dedup probes. Aggregating a
    -- 7-day window over it cost 151s on 252 (EXPLAIN ANALYZE; partition
    -- bitmap heap + ~12.5k per-row probe fetches against wide rows), and
    -- this query runs on the chat failure path (zero-candidate
    -- suggestions) — every caller cancelled at the client timeout (59
    -- cancels / 15min) and the feature never returned. Popularity is a
    -- tier-3 ordering signal only, so we now count over _request_logs_hot
    -- (the recent-traffic surface, same precedent as the probe queries'
    -- traffic EXISTS) which rides idx_request_logs_hot_canonical_model_ts
    -- and returns in single-digit ms. Trade-off: the "popular" tier
    -- reflects the hot retention window (~8h of traffic) rather than a
    -- full 7 days.
    -- 2026-10-13 S3 波 2（停写切换）：热面从 _request_logs_hot 换到会话族
    -- 原生源（db.SessionFamilyTurnsSourceSQL：hot ∪ parent 两腿、无反连接
    -- probe）——S4 停写后新流量的 canonical_model 只落会话族，v1 hot 只会
    -- 排空。无反连接，2026-09-25 的视图成本裁决不适用；顺带修复原实现的
    -- 一个隐含缺陷（7 天窗只读 hot 表，promote 排空的行本来就数不到——
    -- parent 腿补齐了 8h 之外的部分）。探针/内部回环轮次本就不进会话族，
    -- 热度口径反而更干净。
    SELECT canonical_model, COUNT(*) AS cnt
    FROM ` + db.SessionFamilyTurnsSourceSQL() + `
    WHERE ts > now() - interval '7 days'
      AND success = TRUE
      AND canonical_model IS NOT NULL
    GROUP BY canonical_model
)
SELECT r.canonical_name,
       r.display_name,
       r.family,
       r.context_window,
       (LOWER(r.canonical_name) = ANY (SELECT LOWER(unnest(featured)) FROM policy)) AS featured,
       CASE
           WHEN t.canonical_model IS NOT NULL THEN 'task_match'
           WHEN LOWER(r.canonical_name) = ANY (SELECT LOWER(unnest(featured)) FROM policy) THEN 'featured'
           ELSE 'popular'
       END AS reason
FROM routable r
LEFT JOIN task_models t ON t.canonical_model = r.canonical_name
LEFT JOIN usage_7d u ON u.canonical_model = r.canonical_name
WHERE t.canonical_model IS NOT NULL
   OR LOWER(r.canonical_name) = ANY (SELECT LOWER(unnest(featured)) FROM policy)
   OR COALESCE(u.cnt, 0) > 0
ORDER BY
    CASE
        WHEN t.canonical_model IS NOT NULL THEN 1
        WHEN LOWER(r.canonical_name) = ANY (SELECT LOWER(unnest(featured)) FROM policy) THEN 2
        ELSE 3
    END,
    COALESCE(t.tier_rank, 9),
    COALESCE(u.cnt, 0) DESC,
    r.canonical_name
LIMIT $4
`

// resolveTaskTypeForAlternatives determines the session's task type using three
// increasingly expensive sources, stopping at the first hit:
//
//  1. X-Gw-Task-Hint header — the client told us outright
//  2. autoroute session intent cache — a previous `model=auto` request on this
//     session already classified it (Redis-backed, 10min TTL)
//  3. inline heuristic classification of this very request
//
// Step 3 matters because autoroute only classifies when model == "auto", so an
// ordinary request naming an explicit model has no task type on file. The
// heuristic classifier is pure keyword/threshold work with no LLM call and no
// database access, which is why it is acceptable on a request that is already
// failing.
//
// Returns "" when nothing can be determined; callers treat that as "order by
// featured then popularity".
func resolveTaskTypeForAlternatives(
	ctx context.Context,
	r httpHeaderGetter,
	cache *autoroute.SessionIntentCache,
	sigs autoroute.ClassificationSignals,
) string {
	if hint := strings.TrimSpace(r.Header(autoTaskHintHeader)); hint != "" {
		if isKnownTaskType(hint) {
			return hint
		}
	}

	if cache != nil {
		sessionID := strings.TrimSpace(r.Header("X-Gw-Session-Id"))
		if sessionID == "" {
			sessionID = strings.TrimSpace(r.Header("X-Session-Id"))
		}
		if sessionID != "" {
			if cached, ok := cache.Get(sessionID); ok && cached.TaskType != "" {
				return string(cached.TaskType)
			}
		}
	}

	// Inline heuristic. Defaults are used rather than the tuned thresholds
	// because this path has no TuningStore handy and a suggestion list does
	// not warrant threading one through; misclassification here costs an
	// imperfectly-ordered list, not a wrong route.
	classifier := autoroute.NewHeuristicClassifier(
		autoroute.DefaultHeuristicThresholds(),
		autoroute.DefaultKeywords(),
	)
	classification, err := classifier.Classify(ctx, sigs)
	if err != nil || classification == nil {
		return ""
	}
	return string(classification.Primary)
}

// SetModelAlternativesFinder wires the zero-candidate suggestion path. Leaving
// it unset keeps the historical bare 503.
func (h *ChatHandler) SetModelAlternativesFinder(f *ModelAlternativesFinder) {
	h.altFinder = f
}

// findModelAlternatives resolves the session's task type and returns routable
// alternatives for a request whose model has no candidate.
//
// Never returns an error: this runs while the handler is already committed to
// writing a 503, so every failure degrades to an empty list and the client gets
// the plain error it would have received before.
//
// requestModality filters the suggestions to models that can serve that media
// modality; "" keeps the historical unfiltered behaviour.
func (h *ChatHandler) findModelAlternatives(
	r *http.Request,
	reqBody *chatRequestBody,
	bodyBytes []byte,
	clientModel string,
	keyInfo *authentication.KeyInfo,
	requestModality string,
) ModelAlternativesResult {
	if h == nil || h.altFinder == nil {
		return ModelAlternativesResult{
			RequestedModel: clientModel,
			Alternatives:   []ModelAlternative{},
		}
	}

	tenantID := "default"
	if keyInfo != nil && keyInfo.TenantID != "" {
		tenantID = keyInfo.TenantID
	}

	// Reuse autoroute's signal extraction so the inline heuristic sees exactly
	// what a model="auto" request would have produced.
	sigs := extractSignalsForAuto(reqBody, bodyBytes)
	taskType := resolveTaskTypeForAlternatives(
		r.Context(),
		requestHeaderGetter{h: r.Header},
		h.altFinder.intentCache,
		sigs,
	)

	return h.altFinder.Find(r.Context(), clientModel, tenantID, taskType, requestModality)
}

// isKnownTaskType validates a client-supplied X-Gw-Task-Hint against
// autoroute.AllTaskTypes. An unrecognized hint is ignored rather than trusted,
// so a typo cannot silently collapse tier 1 of the suggestion query (which
// matches on exact task_type equality and would return nothing).
func isKnownTaskType(s string) bool {
	for _, t := range autoroute.AllTaskTypes {
		if string(t) == s {
			return true
		}
	}
	return false
}

// httpHeaderGetter is the narrow slice of *http.Request that
// resolveTaskTypeForAlternatives needs, so it can be tested without
// constructing a full request.
type httpHeaderGetter interface {
	Header(key string) string
}

// requestHeaderGetter adapts *http.Request to httpHeaderGetter.
type requestHeaderGetter struct {
	h interface{ Get(string) string }
}

func (g requestHeaderGetter) Header(key string) string {
	if g.h == nil {
		return ""
	}
	return g.h.Get(key)
}
