package bg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func TestEmitSyncAuditReturnsInsertFailure(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectExec("INSERT INTO node_probe_runs").
		WithArgs(
			7, "raw-model",
			true, 200, "", 15, "",
			true, 200, "", 16, "",
			true, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			"raw-model", "outbound-model", 7,
			"https://provider.example/v1/chat/completions", pgxmock.AnyArg(), "request", "response",
			15, false,
			"parent-request",
		).
		WillReturnError(errors.New("audit schema unavailable"))

	worker := &NodeProbeWorker{auditDB: mock}
	err = worker.emitSyncAudit(context.Background(), 7, "raw-model",
		nodeProbeRoundResult{
			ok: true, providerID: 7, outboundModel: "outbound-model", httpStatus: 200,
			latencyMs: 15, requestURL: "https://provider.example/v1/chat/completions",
			requestHeaders: map[string]string{"Authorization": "secret", "X-Test": "allowed"},
			requestBody:    "request", responseBody: "response",
		},
		nodeProbeRoundResult{ok: true, httpStatus: 200, latencyMs: 16},
		time.Now().Add(-time.Second), "parent-request")
	if err == nil || !strings.Contains(err.Error(), "audit schema unavailable") {
		t.Fatalf("emitSyncAudit error = %v, want wrapped insert failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestProbeHeadersJSONReturnsJSONText(t *testing.T) {
	if got := probeHeadersJSON(nil); got != "{}" {
		t.Fatalf("probeHeadersJSON(nil) = %q, want {}", got)
	}
	got := probeHeadersJSON(map[string]string{"X-Test": "allowed"})
	if got != `{"X-Test":"allowed"}` {
		t.Fatalf("probeHeadersJSON() = %q", got)
	}
}
