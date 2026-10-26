package main

// output_compliance_control.go — wires the output-compliance/脱敏 interceptor
// into the streaming ChatHandler's ResponseInterceptor chain.
//
// This mirrors goal_control.go's pattern: construct the checker + interceptor
// with the right *sql.DB and owner-lookup function, and add it to the chain.
// The interceptor implementation lives in domains/hooks/outputcompliance/interceptor.go;
// this file is just the "last mile" wiring.
//
// Owner rule (see docs/2026-07-09-session-tagging-redaction-architecture.md §2.3):
//   - callerOwner = api_keys.owner_user of the key that made the request
//     （会话族 details 族里**最近一轮**的 api_key_owner_user）
//   - dataOwner   = session_dim.owner_user (the session's primary owner)
//   - text match → allow plaintext; mismatch or empty → redact
//
// Post a990bfd61 the interceptor takes ctx + dataOwner only
// (callerOwner rides on InterceptRequest.CallerOwner); the write-time
// redact-body path still consumes the (callerOwner, dataOwner) pair.
//
// ★ 2026-10-20（v1 族退役，迁移 890）：callerOwner 的**来源换了**。
// 旧实现用 LATERAL 去 v1 宽族取「该会话最近一条请求行」的 api_key_owner_user；
// 那一族已整体退役 ⇒ 该腿 42P01 ⇒ 整条查询失败 ⇒ dataOwner 也会一起丢成空
// （空 ⇒ 保守脱敏，方向是安全的，但 owner 规则从此只剩「一律脱敏」一种结局）。
// ⇒ 现在 callerOwner 取**会话族 details 族里最近一轮**的 api_key_owner_user，
// dataOwner 仍取 session_dim.owner_user。
//
// ⚠⚠ 为什么**不**用 session_dim.api_key_owner_user（近在眼前、零额外往返）：
// 迁移 358 的触发器用**同一个** NEW.api_key_owner_user 写 session_dim 的
// owner_user 与 api_key_owner_user 两列 ⇒ 两列**恒等** ⇒
// owner_mismatch 模式**恒真** ⇒ 该规则**彻底失去判别力**
// ⇒ 那不是"换个来源"，是**把安全规则拆了**。
// 原 v1 腿的语义是「**最近一条**请求的 key owner」——会话中途换 key 时它与
// dataOwner 不相等 ⇒ 规则照常判不匹配 ⇒ 照常脱敏。
// ⇒ 要保住判别力，就必须保留「最近一轮」这个**时间语义**。
// 真库实测：details 族 hot ∪ parent 在 (session_id, turn_no) 上**零重叠**
// ⇒ UNION ALL 不会把同一轮数两次；1133 个会话里 166 个有 owner，
// 取不到 ⇒ 空 ⇒ 规则判「不属自有」⇒ **保守脱敏**（fail-closed，绝不漏）。
// 代价：一次带索引的额外查询（~0.15ms），值得。
//
// 详见 lookupOwners 的函数注释。

import (
	"context"
	"database/sql"
	"log/slog"
	"sync/atomic"

	outputcompliancehook "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
)

// buildOutputComplianceInterceptor constructs the output-compliance checker and
// wraps it in a ResponseInterceptor. Returns nil (feature inert) when the DB is
// unavailable or the checker cannot be built — the chain then simply omits it.
func buildOutputComplianceInterceptor(db *sql.DB) *outputcompliancehook.OutputComplianceInterceptor {
	if db == nil {
		slog.Info("output_compliance_control: disabled (no DB)")
		return nil
	}
	checker, err := outputcompliance.NewChecker(db)
	if err != nil {
		slog.Warn("output_compliance_control: NewChecker failed, feature inert", "error", err)
		return nil
	}
	checker.SetManagedRuleSource(managedOutputRuleSource)
	ownerFn := makeDataOwnerLookup(db)
	return outputcompliancehook.NewOutputComplianceInterceptor(checker, ownerFn)
}

// sanitizeRestoredValues 持有「本会话已还原值」的来源函数，由
// installSmartSaniGuard 在装配还原拦截器时写入，供 write-time 脱敏器
// （buildRedactBodyFn）读取（2026-10-10）。
//
// 为什么是包级持有而不是构造参数：两处装配点相隔数百行且不在同一个函数里
// （main.go 的 routing 装配 vs installSmartSaniGuard），穿参数要动启动主流程的
// 签名。它持有的是**无状态函数值**，不是租户数据——真正的会话映射表每次调用
// 都从 ctx / Redis 现取，进程里不缓存任何一条敏感值。
//
// 为 nil（未装配 sanitize、或还原器初始化失败）时两条路径都退回旧行为。
var sanitizeRestoredValues atomic.Value

// sanitizeSessionValuesFunc 是上面那个持有槽的类型别名，读写两侧共用。
type sanitizeSessionValuesFunc func(ctx context.Context, sessionID, tenantID string) ([]string, error)

// storeSanitizeRestoredValues 写入来源（installSmartSaniGuard 调用）。
func storeSanitizeRestoredValues(fn sanitizeSessionValuesFunc) {
	if fn == nil {
		return
	}
	sanitizeRestoredValues.Store(fn)
}

// loadSanitizeRestoredValues 读取来源；未装配时返回 nil。
func loadSanitizeRestoredValues() sanitizeSessionValuesFunc {
	if sanitizeRestoredValues.Load() == nil {
		return nil
	}
	fn, _ := sanitizeRestoredValues.Load().(sanitizeSessionValuesFunc)
	return fn
}

// buildRedactBodyFn 构造 write-time 客户端可见脱敏函数（2026-07-09，增强 1）。
// Legacy adapter for callers without a request; gateway routing uses the context-aware form.
func buildRedactBodyFn(db *sql.DB) func([]byte, string, string) []byte {
	fn := buildRedactBodyWithContextFn(db)
	if fn == nil {
		return nil
	}
	return func(body []byte, session, tenant string) []byte {
		return fn(context.Background(), body, session, tenant)
	}
}

func buildRedactBodyWithContextFn(db *sql.DB) func(context.Context, []byte, string, string) []byte {
	if db == nil {
		return nil
	}
	checker, err := outputcompliance.NewChecker(db)
	if err != nil {
		slog.Warn("output_compliance_control: buildRedactBodyFn NewChecker failed", "error", err)
		return nil
	}
	checker.SetManagedRuleSource(managedOutputRuleSource)
	return streaming.BuildRedactBodyWithContextFn(checker, makeRedactOwnerLookup(db), func(ctx context.Context, session, tenant string) ([]string, error) {
		// Routing is constructed before the sanitizer registers its source.
		if source := loadSanitizeRestoredValues(); source != nil {
			return source(ctx, session, tenant)
		}
		return nil, nil
	})
}

// lookupOwners resolves (callerOwner, dataOwner) for a session in one DB
// round-trip:
//   - dataOwner   = session_dim.owner_user（会话主属主 = 首个请求的 owner）
//   - callerOwner = **该会话最近一轮**的 api_key_owner_user（会话族 details 族）
//
// ★ 2026-10-20（迁移 890，v1 族退役）：callerOwner 的来源从
//   「`_request_logs` 里最近一条请求行的 api_key_owner_user」换成
//   「会话族 details 族里最近一轮的 api_key_owner_user」。
//
// ⚠⚠ 为什么**不能**用 `session_dim.api_key_owner_user`（近在眼前、零额外往返）：
//
//	domains/outputcompliance/owner.go 的规则是
//		OwnerAllowsSensitive(caller, data) = caller != "" && data != "" && caller == data
//	而 session_dim 的两列**来自同一处**：迁移 358 的触发器用
//	`COALESCE(NULLIF(TRIM(session_dim.owner_user),''), NEW.api_key_owner_user)`
//	与 `COALESCE(NULLIF(TRIM(session_dim.api_key_owner_user),''), NEW.api_key_owner_user)`
//	**同时**写 ⇒ 两列恒等 ⇒ caller == data 恒成立
//	⇒ owner_mismatch 模式**变成恒真** ⇒ 该规则**彻底失去判别力**。
//
//	⇒ 换句话说：用 session_dim 那一列不是"换个来源"，是**把安全规则拆了**。
//	原 v1 腿的语义是「**最近一条**请求的 key owner」——**会话中途换了 key 时，
//	它与 dataOwner 不相等 ⇒ 规则照常判不匹配 ⇒ 照常脱敏**。
//	要保住这个判别力，就必须保留「最近一轮」这个时间语义。
//
//	⇒ 来源 = session_turn_details(_hot) 按 session_id 取 turn_no 最大的一轮。
//	真库实测：hot ∪ parent 在 (session_id, turn_no) 上**零重叠**（0 命中）
//	⇒ UNION ALL 不会把同一轮数两次；1133 个会话里 166 个有 owner
//	（与 session_dim 的 165 同量级），取不到 ⇒ 空 ⇒ 规则判「不属自有」⇒
//	**保守脱敏**（fail-closed）。
//
//	代价：一次额外查询（带索引 session_turn_details_*_session_turn_key
//	(session_id, turn_no, partition_date)），实测 ~0.15ms。
//	这个代价换来的是「owner 规则还判得出不匹配」，值得。
//
// ★ 取不到 ⇒ 两个 owner 都空 ⇒ owner 规则判「不属自有」⇒ 保守脱敏。
//
//	任何错误都降级为 ("","")，这也是 owner 规则对待空值的处理方式：
//	一次失败的查表不该变成一次明文放行。
func lookupOwners(ctx context.Context, db *sql.DB, sessionID, tenantID string) (callerOwner, dataOwner string) {
	if sessionID == "" {
		return "", ""
	}
	// 一行两列：dataOwner 直接取 session_dim；callerOwner 取会话族 details 族
	// 里最近一轮（turn_no 最大）的 api_key_owner_user。
	row := db.QueryRowContext(ctx, `
		SELECT sd.owner_user,
		       (SELECT x.api_key_owner_user
		          FROM (
		              SELECT api_key_owner_user, turn_no
		                FROM public.session_turn_details_hot
		               WHERE session_id = $1
		              UNION ALL
		              SELECT api_key_owner_user, turn_no
		                FROM public.session_turn_details
		               WHERE session_id = $1
		          ) x
		         ORDER BY x.turn_no DESC
		         LIMIT 1)
		FROM session_dim sd
		WHERE sd.gw_session_id = $1
		  AND ($2 = '' OR sd.tenant_id = $2)
		LIMIT 1
	`, sessionID, tenantID)
	var data, caller sql.NullString
	if err := row.Scan(&data, &caller); err != nil {
		// session_dim 行不存在或查询出错 → 保守空值（owner 规则：空 ⇒ 脱敏）
		return "", ""
	}
	return caller.String, data.String
}

// makeOwnerLookup returns the write-time (callerOwner, dataOwner) lookup
// matching streaming.RedactOwnerContextFunc (BuildRedactBodyFn path).
func makeRedactOwnerLookup(db *sql.DB) streaming.RedactOwnerContextFunc {
	return func(sessionID, tenantID string) (string, string) {
		return lookupOwners(context.Background(), db, sessionID, tenantID)
	}
}

// makeDataOwnerLookup adapts the same lookup to the interceptor OwnerContextFunc
// shape (2026-09-29 a990bfd61: ctx-aware, dataOwner only — callerOwner now
// travels on InterceptRequest.CallerOwner, supplied by the streaming layer).
func makeDataOwnerLookup(db *sql.DB) outputcompliancehook.OwnerContextFunc {
	return func(ctx context.Context, sessionID, tenantID string) string {
		_, dataOwner := lookupOwners(ctx, db, sessionID, tenantID)
		return dataOwner
	}
}

// makeOwnerLookup (2026-09-30 ctx-aware drift): the interceptor owner lookup
// reads ONLY the session owner (session_dim.owner_user) — callerOwner rides
// on InterceptRequest.CallerOwner from the streaming layer, so the second
// column of the lookup is unnecessary on this lane. Guards: missing
// session/tenant or a cancelled context return "" without a DB round-trip.
func makeOwnerLookup(db *sql.DB) outputcompliancehook.OwnerContextFunc {
	return func(ctx context.Context, sessionID, tenantID string) string {
		if sessionID == "" || tenantID == "" {
			return ""
		}
		if err := ctx.Err(); err != nil {
			return ""
		}
		var owner sql.NullString
		if err := db.QueryRowContext(ctx,
			`SELECT owner_user FROM session_dim
			 WHERE gw_session_id = $1 AND tenant_id = $2
			 LIMIT 1`,
			sessionID, tenantID).Scan(&owner); err != nil {
			return ""
		}
		return owner.String
	}
}

// hasOutputComplianceInterceptor reports whether any interceptor in the slice
// is an *outputcompliancehook.OutputComplianceInterceptor.
func hasOutputComplianceInterceptor(interceptors []response.ResponseInterceptor) bool {
	for _, ic := range interceptors {
		if _, ok := ic.(*outputcompliancehook.OutputComplianceInterceptor); ok {
			return true
		}
	}
	return false
}

// insertRestoreBeforeCompliance rebuilds the interceptor slice with the
// sanitize-restore chain inserted immediately BEFORE the first output
// compliance interceptor (restore rewrites placeholders that compliance
// redaction produced earlier in the same process, so it must run after
// compliance's own pass over the ORIGINAL bytes — appending it at the end
// would let a placeholder land in front of the client before restore gets a
// chance to map it back). With no compliance interceptor present
// (data-plane mode, no DB) restore is appended at the end so the response
// chain still carries it.
func insertRestoreBeforeCompliance(interceptors []response.ResponseInterceptor, restore response.ResponseInterceptor) []response.ResponseInterceptor {
	if restore == nil {
		return interceptors
	}
	out := make([]response.ResponseInterceptor, 0, len(interceptors)+1)
	inserted := false
	for _, ic := range interceptors {
		if !inserted {
			if _, ok := ic.(*outputcompliancehook.OutputComplianceInterceptor); ok {
				out = append(out, restore, ic)
				inserted = true
				continue
			}
		}
		out = append(out, ic)
	}
	if !inserted {
		out = append(out, restore)
	}
	return out
}

// ensureOutputComplianceFallback installs a built-in output-compliance
// interceptor on the handler when its chain has none and none can be built
// from the DB (lite / data-plane mode without a database). Idempotent: a
// second call with a compliance interceptor already present is a no-op.
func ensureOutputComplianceFallback(handler *streaming.ChatHandler) {
	if handler == nil {
		return
	}
	chain := handler.ResponseInterceptorForWire()
	if chain != nil && hasOutputComplianceInterceptor(chain.ListInterceptors()) {
		return
	}
	var existing []response.ResponseInterceptor
	if chain != nil {
		existing = chain.ListInterceptors()
	}
	// Built-in fallback: nil checker DB is acceptable — the interceptor's
	// default-enabled mode redacts on owner mismatch with an empty owner
	// context (conservative redaction). buildOutputComplianceInterceptor(nil)
	// returns nil, so construct the hook directly.
	hook := outputcompliancehook.NewOutputComplianceInterceptor(nil, nil)
	handler.SetResponseInterceptor(response.NewInterceptorChain(append(existing, hook)...))
}
