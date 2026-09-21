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
// in one query, keeping the interceptor interface (sessionID, tenantID) simple.

import (
	"context"
	"database/sql"
	"log/slog"

	outputcompliancehook "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
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
	ownerFn := makeOwnerLookup(db)
	return outputcompliancehook.NewOutputComplianceInterceptor(checker, ownerFn)
}

// buildRedactBodyFn 构造 write-time 客户端可见脱敏函数（2026-07-09，增强 1）。
// 与 buildOutputComplianceInterceptor 使用同一个 checker，但 ownerFn 需转换类型。
func buildRedactBodyFn(db *sql.DB) func([]byte, string, string) []byte {
	if db == nil {
		return nil
	}
	checker, err := outputcompliance.NewChecker(db)
	if err != nil {
		slog.Warn("output_compliance_control: buildRedactBodyFn NewChecker failed", "error", err)
		return nil
	}
	// 复用同一个 owner lookup，但签名需匹配 streaming.RedactOwnerContextFunc
	ownerFn := makeOwnerLookup(db)
	return streaming.BuildRedactBodyFn(checker, streaming.RedactOwnerContextFunc(ownerFn))
}

// makeOwnerLookup returns an OwnerContextFunc that resolves (callerOwner, dataOwner)
// for a session in one DB round-trip:
//   - dataOwner   = session_dim.owner_user
//   - callerOwner = the api_key_owner_user of the most recent request in the session
//                   (i.e. the owner of the key currently driving this session)
//
// Any error degrades to ("","") which the owner rule treats conservatively
// (redact). This is acceptable: a failed lookup should never leak sensitive data.
func makeOwnerLookup(db *sql.DB) outputcompliancehook.OwnerContextFunc {
	return func(sessionID, tenantID string) (callerOwner, dataOwner string) {
		if sessionID == "" {
			return "", ""
		}
		ctx := context.Background()
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
}
