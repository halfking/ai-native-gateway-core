package streaming

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestClassifyRoutingError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantCode    string
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "nil error",
			err:         nil,
			wantCode:    "routing_unknown_error",
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "Internal routing service error",
		},
		{
			name:        "not configured",
			err:         errors.New("provider client not configured"),
			wantCode:    "routing_not_configured",
			wantStatus:  http.StatusServiceUnavailable,
			wantMessage: "Routing service temporarily unavailable",
		},
		{
			name:        "routing DB not configured",
			err:         errors.New("routing DB not configured"),
			wantCode:    "routing_not_configured",
			wantStatus:  http.StatusServiceUnavailable,
			wantMessage: "Routing service temporarily unavailable",
		},
		{
			name:        "connection refused",
			err:         errors.New("connection refused to database"),
			wantCode:    "routing_connection_error",
			wantStatus:  http.StatusServiceUnavailable,
			wantMessage: "Routing service temporarily unavailable",
		},
		{
			name:        "timeout",
			err:         errors.New("context deadline exceeded: query timeout"),
			wantCode:    "routing_connection_error",
			wantStatus:  http.StatusServiceUnavailable,
			wantMessage: "Routing service temporarily unavailable",
		},
		{
			name:        "relation does not exist",
			err:         errors.New(`relation "_request_logs" does not exist`),
			wantCode:    "routing_schema_error",
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "Internal routing configuration error",
		},
		{
			name:        "partition not found",
			err:         errors.New(`no partition of relation "request_wal" found for row`),
			wantCode:    "routing_schema_error",
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "Internal routing configuration error",
		},
		{
			name:        "function does not exist",
			err:         errors.New(`function recent_success_rate(integer) does not exist`),
			wantCode:    "routing_schema_error",
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "Internal routing configuration error",
		},
		{
			name:        "generic database error",
			err:         errors.New("syntax error at or near"),
			wantCode:    "routing_database_error",
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "Internal routing service error",
		},
		{
			name:        "permission denied",
			err:         errors.New("permission denied for table credentials"),
			wantCode:    "routing_database_error",
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "Internal routing service error",
		},
		{
			name:        "file does not exist (not a schema error)",
			err:         errors.New("file /tmp/config.json does not exist"),
			wantCode:    "routing_database_error",
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "Internal routing service error",
		},
		// ── 2026-11-15 211 incident shapes (verbatim from production logs) ──
		{
			// Client hung up while its request was parked behind an open
			// circuit (300s ZCode timeout during the outage). Must not land in
			// the routing_database_error bucket that ops dashboards group by.
			name:        "client canceled (raw context.Canceled)",
			err:         context.Canceled,
			wantCode:    "routing_client_canceled",
			wantStatus:  499,
			wantMessage: "Client closed the request before routing completed",
		},
		{
			name:        "client canceled (wrapped string, production shape)",
			err:         errors.New("context canceled"),
			wantCode:    "routing_client_canceled",
			wantStatus:  499,
			wantMessage: "Client closed the request before routing completed",
		},
		{
			// Protective circuit after repeated candidate-query failures.
			// Retryable condition: 503, its own dashboard code — not a 500
			// "database" error.
			name:        "candidates query circuit open (production shape)",
			err:         errors.New("candidates query circuit open (retry after 2026-11-14T15:43:01Z)"),
			wantCode:    "routing_circuit_open",
			wantStatus:  http.StatusServiceUnavailable,
			wantMessage: "Routing service temporarily unavailable",
		},
		{
			// The d5eb218a2 regression: candidates SQL referenced mc.released_at
			// outside the CTE scope. Gateway-side SQL defect = schema/config
			// error, NOT database infrastructure.
			name:        "missing FROM-clause entry (production shape, 42P01)",
			err:         errors.New(`query candidates failed: ERROR: missing FROM-clause entry for table "mc" (SQLSTATE 42P01) (context: model=claude-opus-5-5, tenant_id=default)`),
			wantCode:    "routing_schema_error",
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "Internal routing configuration error",
		},
		{
			// Bare deadline (no "timeout" substring) previously fell through to
			// the database fallback too.
			name:        "bare context deadline exceeded",
			err:         errors.New("context deadline exceeded"),
			wantCode:    "routing_connection_error",
			wantStatus:  http.StatusServiceUnavailable,
			wantMessage: "Routing service temporarily unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := classifyRoutingError(tt.err)
			if rc.code != tt.wantCode {
				t.Errorf("code = %q, want %q", rc.code, tt.wantCode)
			}
			if rc.httpStatus != tt.wantStatus {
				t.Errorf("httpStatus = %d, want %d", rc.httpStatus, tt.wantStatus)
			}
			if rc.message != tt.wantMessage {
				t.Errorf("message = %q, want %q", rc.message, tt.wantMessage)
			}
		})
	}
}
