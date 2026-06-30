package streaming

import (
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
			err:         errors.New(`relation "request_logs" does not exist`),
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
