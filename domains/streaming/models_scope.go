package streaming

// models_scope.go —— 模型列表端点的「口径」（scope）解析与精选过滤。
//
// 2026-10-29：/v1/models 与 /v1beta/models 此前**无条件输出全量**可路由模型
// （615+ 个）。而本仓自己早就有「特色模型」这个一等概念——
// routing_policy.featured_models（运营维护的精选清单）
//   ├─ 路由侧：autoroute 的 FeaturedBonus 加权
//   ├─ 路由侧：bg/shared_pick.go 的「featured 优先」选凭据
//   ├─ 候选侧：domains/streaming 的 alternativesSQL 的 tier-2
//   └─ 管理侧：admin/routing.go 的「精选」过滤
//
// ⇒ 精选清单才是「这个网关推荐什么」的真正答案，而全量清单只是「技术上能路由
// 什么」。默认输出后者，客户端拿到的就是一堆没有优先级、没有上下文长度、
// 名字还带供应商后缀的模型名。⇒ 默认改成精选，全量改为**显式参数**才输出。
//
// # 口径的来源与优先级
// 请求参数（?scope=… / ?all=…） > 运维默认（models.list_scope，热加载）
//
// 请求参数优先于运维默认：运维设的是「默认」，不是「上限」——需要全量的客户端
// 应当能在不发版、不改配置的前提下拿到全量。
//
// # 为什么不把「精选」做成第二个 SQL 常量
//
// 活度门（cmb.available / node_probe_state / 凭据与供应商存活门…）有十几条，
// 两份拷贝必然漂移——r1027 的契约测试（TestModelsRoutableSQLGatePredicates、
// TestModelsRoutableAgreesWithRoutableView）就是为「门漏了一条」而写的。
// ⇒ 精选过滤做成**同一个查询里的一个布尔参数**（$1），门只有一份。
//
// # 精选清单读哪一行
//
// tenant_id='default'——全仓读这份清单的既有约定，三处同款：
// autoroute.loadFeaturedModels / admin/routing.go 的精选过滤 /
// domains/streaming 的 alternativesSQL。

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// modelsListScopeKey is the settings_kv key (hot-reloaded) for the operator's
// default discovery scope.
//
// Setting it to "all" restores the pre-2026-10-29 behaviour (the full
// routable list) without a deploy — the same escape-hatch shape as
// models.list_only_routable, which exists for the same kind of reason: the
// discovery list is a public contract, and a default change on a public
// contract must be revertible without a release train.
const modelsListScopeKey = "models.list_scope"

// The two scopes the discovery endpoints understand.
const (
	// modelsScopeFeatured serves only the operator-curated featured models.
	modelsScopeFeatured = "featured"
	// modelsScopeAll serves every currently-routable model.
	modelsScopeAll = "all"
)

// modelsFeaturedCTE resolves the curated featured set for the discovery
// queries.
//
// ★ The two-CTE shape (policy → featured) is load-bearing: a set-returning
// function in the target list is expanded BEFORE LIMIT is applied, so
// `SELECT LOWER(unnest(featured_models)) ... ORDER BY id LIMIT 1` would
// return exactly ONE name — the first element of the array — instead of the
// whole list. Pinning the row first (policy) and expanding afterwards
// (featured) is the only order that yields the full list.
//
// ★ The names are lower-cased because the list is hand-maintained while the
// model columns come from the catalog, and the two disagree on case often
// enough that an exact-match filter would silently drop entries. Same
// reasoning as alternativesSQL's LOWER(...) = ANY (LOWER(...)).
const modelsFeaturedCTE = `
		WITH policy AS (
			SELECT COALESCE(featured_models, ARRAY[]::TEXT[]) AS names
			FROM routing_policy
			WHERE tenant_id = 'default'
			ORDER BY id
			LIMIT 1
		),
		featured AS (
			SELECT LOWER(unnest(names)) AS name
			FROM policy
		)
`

// modelsFeaturedPredicate restricts a discovery query to the featured set
// when $1 is true; $1 = false serves the full list.
//
// ★ Three name forms are matched on purpose:
//
//   - canonical_name    — what the discovery list advertises;
//   - raw_model_name    — how the admin UI's own featured filter matches
//     (admin/routing.go: fm == rawName || fm == canonicalName);
//   - standardized_name — how the ROUTER resolves a featured model
//     (bg/shared_pick.go: pm.standardized_name = ANY(featured_models) OR
//     pm.raw_model_name = ANY(featured_models)).
//
// Matching only the canonical form would hide a model the router treats as
// featured — the discovery list would then contradict the routing order
// (a client cannot pick what it cannot see).
//
// ★ The provider-side names are matched through an EXISTS over
// provider_models keyed by canonical_id rather than through an alias of the
// main query: the two discovery queries (the routable one and the legacy
// model_offers one) use different aliases for that relation, and an
// alias-independent predicate keeps ONE copy of the rule for both.
//
// ★ $1 is a parameter rather than two SQL constants so the liveness gate
// list exists in exactly one place — a second copy is precisely the drift
// the r1027 contract tests exist to catch.
const modelsFeaturedPredicate = `
		  AND (
		        NOT $1::boolean
		     OR LOWER(mc.canonical_name) = ANY (SELECT name FROM featured)
		     OR EXISTS (
		          SELECT 1
		          FROM provider_models pmf
		          WHERE pmf.canonical_id = mc.id
		            AND (LOWER(pmf.raw_model_name) = ANY (SELECT name FROM featured)
		              OR LOWER(pmf.standardized_name) = ANY (SELECT name FROM featured))
		     )
		  )
`

// resolveModelsScope decides the scope of one discovery request.
//
// Precedence: explicit query parameter > operator default (models.list_scope).
//
// Accepted query forms:
// ?scope=featured | ?scope=all   — explicit
// ?all=true | ?all=false         — shorthand for the same two scopes
//
// ★ An unrecognised value is reported as 400 rather than silently falling
// back to the default. A client that asked for the full list and received
// the featured list (or the reverse) would draw a wrong conclusion about
// what the gateway can serve — and it would draw it SILENTLY, which is the
// failure mode this repo's "同一句话不许两份" discipline exists to prevent.
//
// ?scope= (empty) is treated as absent, not as an error.
func resolveModelsScope(r *http.Request) (string, error) {
	fallback := defaultModelsScope()
	if r == nil || r.URL == nil {
		return fallback, nil
	}
	q := r.URL.Query()

	if raw := strings.TrimSpace(q.Get("scope")); raw != "" {
		switch strings.ToLower(raw) {
		case modelsScopeFeatured:
			return modelsScopeFeatured, nil
		case modelsScopeAll:
			return modelsScopeAll, nil
		default:
			return "", fmt.Errorf("invalid scope %q (want %q or %q)", raw, modelsScopeFeatured, modelsScopeAll)
		}
	}

	if raw := strings.TrimSpace(q.Get("all")); raw != "" {
		all, ok := parseModelsScopeBool(raw)
		if !ok {
			return "", fmt.Errorf("invalid all=%q (want true or false)", raw)
		}
		if all {
			return modelsScopeAll, nil
		}
		return modelsScopeFeatured, nil
	}

	return fallback, nil
}

// defaultModelsScope reads the operator's configured default scope.
//
// ★ An unreadable or unrecognised stored value falls back to "featured"
// (the documented default) rather than to "all": the curated list is the
// safer of the two — it advertises models the gateway actually recommends.
func defaultModelsScope() string {
	raw := strings.TrimSpace(settings.GetPlatformString(modelsListScopeKey, modelsScopeFeatured))
	switch strings.ToLower(raw) {
	case modelsScopeAll:
		return modelsScopeAll
	case modelsScopeFeatured:
		return modelsScopeFeatured
	default:
		return modelsScopeFeatured
	}
}

// parseModelsScopeBool parses the boolean spellings a client may use for
// ?all=. The second return value reports whether the input was recognised.
func parseModelsScopeBool(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	}
	return false, false
}

// modelsScopeFeaturedOnly reports whether a scope means "featured only".
func modelsScopeFeaturedOnly(scope string) bool {
	return scope == modelsScopeFeatured
}
