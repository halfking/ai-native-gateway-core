package streaming

import (
	"errors"
	"net/http"
	"testing"
)

func TestClassifyRoutingError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   string
		wantStatus int
	}{
		{
			name:       "not configured",
			err:        errors.New("provider client not configured"),
			wantCode:   "routing_not_configured",
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "routing DB not configured",
			err:        errors.New("routing DB not configured"),
			wantCode:   "routing_not_configured",
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "connection refused",
			err:        errors.New("connection refused to database"),
			wantCode:   "routing_connection_error",
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "timeout",
			err:        errors.New("context deadline exceeded: query timeout"),
			wantCode:   "routing_connection_error",
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "relation does not exist",
			err:        errors.New(`relation "request_logs" does not exist`),
			wantCode:   "routing_schema_error",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "partition not found",
			err:        errors.New(`no partition of relation "request_wal" found for row`),
			wantCode:   "routing_schema_error",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "function does not exist",
			err:        errors.New(`function recent_success_rate(integer) does not exist`),
			wantCode:   "routing_schema_error",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "generic database error",
			err:        errors.New("syntax error at or near"),
			wantCode:   "routing_database_error",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "permission denied",
			err:        errors.New("permission denied for table credentials"),
			wantCode:   "routing_database_error",
			wantStatus: http.StatusInternalServerError,
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
			if rc.message == "" {
				t.Error("message should not be empty")
			}
		})
	}
}
