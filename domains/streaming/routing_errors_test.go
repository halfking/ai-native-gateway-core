package streaming

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
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
		// ── 2026-11-16 巡检补正：哨兵形态与文本形态必须落同一个桶 ──
		//
		// ★ 为什么加这几条：2026-11-15 的 P2 审计把字串判据换成 errors.Is 时
		//   **删掉了字串兜底**，而生产形态恰恰是裸文本形态
		//   ⇒ 两条既有门禁转红（code=routing_database_error）。
		// ★ 这几条把「同一个故障，哨兵形态与文本形态归因相同」钉死：
		//   分类器最怕的是同一个故障因构造方式不同被拆进两个看板桶。
		{
			// 被 %w 包过的 context.Canceled：errors.Is 分支。
			name:        "client canceled (wrapped sentinel)",
			err:         fmt.Errorf("candidates query aborted: %w", context.Canceled),
			wantCode:    "routing_client_canceled",
			wantStatus:  499,
			wantMessage: "Client closed the request before routing completed",
		},
		{
			// 哨兵形态的熔断：provider 生产路径就是 %w 包出来的。
			name:        "candidates query circuit open (wrapped sentinel)",
			err:         fmt.Errorf("%w (retry after 2026-11-14T15:43:01Z)", provider.ErrCandidatesCircuitOpen),
			wantCode:    "routing_circuit_open",
			wantStatus:  http.StatusServiceUnavailable,
			wantMessage: "Routing service temporarily unavailable",
		},
		{
			// ★ 哨兵形态的超时**不是**客户端断开：与文本形态 "timeout"
			//   同归 routing_connection_error（503）。
			//   若这里落成 routing_client_canceled(499)，
			//   服务端超时会伪装成「客户端跑了」⇒ 看板归因错、告警错。
			name:        "context deadline exceeded (sentinel)",
			err:         context.DeadlineExceeded,
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
