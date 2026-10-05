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
//     (stored on request_logs.api_key_owner_user per row)
//   - dataOwner   = session_dim.owner_user (the session's primary owner)
//   - text match → allow plaintext; mismatch or empty → redact
//
// The lookup reads the latest request_log row for the session to get BOTH
// owners (api_key_owner_user = caller, and session_dim.owner_user = data owner)
// in one query. Post a990bfd61 the interceptor takes ctx + dataOwner only
// (callerOwner rides on InterceptRequest.CallerOwner); the write-time
// redact-body path still consumes the (callerOwner, dataOwner) pair.

import (
	"context"
	"database/sql"
	"log/slog"

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
	ownerFn := makeDataOwnerLookup(db)
	return outputcompliancehook.NewOutputComplianceInterceptor(checker, ownerFn)
}

// buildRedactBodyFn 构造 write-time 客户端可见脱敏函数（2026-07-09，增强 1）。
// 与 buildOutputComplianceInterceptor 使用同一个 checker；write-time 路径没有
// 请求上下文，caller/data owner 均来自 DB 查询（RedactOwnerContextFunc 形态）。
func buildRedactBodyFn(db *sql.DB) func([]byte, string, string) []byte {
	if db == nil {
		return nil
	}
	checker, err := outputcompliance.NewChecker(db)
	if err != nil {
		slog.Warn("output_compliance_control: buildRedactBodyFn NewChecker failed", "error", err)
		return nil
	}
	return streaming.BuildRedactBodyFn(checker, makeRedactOwnerLookup(db))
}

// lookupOwners resolves (callerOwner, dataOwner) for a session in one DB
// round-trip:
//   - dataOwner   = session_dim.owner_user
//   - callerOwner = the api_key_owner_user of the most recent request in the session
//     (i.e. the owner of the key currently driving this session)
//
// Any error degrades to ("","") which the owner rule treats conservatively
// (redact). This is acceptable: a failed lookup should never leak sensitive data.
func lookupOwners(ctx context.Context, db *sql.DB, sessionID, tenantID string) (callerOwner, dataOwner string) {
	if sessionID == "" {
		return "", ""
	}
	// Prefer session_dim (has both columns); fall back is implicit via empty strings.
	row := db.QueryRowContext(ctx, `
		SELECT sd.owner_user, rl.api_key_owner_user
		FROM session_dim sd
		LEFT JOIN LATERAL (
			SELECT api_key_owner_user
			FROM request_logs
			WHERE gw_session_id = sd.gw_session_id
			  AND ($2 = '' OR tenant_id = $2)
			ORDER BY ts DESC
			LIMIT 1
		) rl ON true
		WHERE sd.gw_session_id = $1
		  AND ($2 = '' OR sd.tenant_id = $2)
		LIMIT 1
	`, sessionID, tenantID)
	var data, caller sql.NullString
	if err := row.Scan(&data, &caller); err != nil {
		// session_dim row missing or query error → conservative empty
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
// on InterceptRequest.CallerOwner from the streaming layer, so the
// request_logs LATERAL join is unnecessary on this lane. Guards: missing
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
