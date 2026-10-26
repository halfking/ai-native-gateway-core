// data_horizon_gate_test.go — per-instance session-turn canary gate.
//
// The initial r1033 gate compared local activity with GLOBAL MAX(ts). That
// looked correct on one gateway but failed under shared PostgreSQL: another
// gateway's fresh turn could mask a complete mirror outage on this instance.
// These tests pin the corrected identity proof: each local, mirror-eligible
// terminal request_id must appear in session_turns_hot OR session_turns after
// the async mirror tolerance.
package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func readAdminSourceFile(name string) (string, error) {
	for _, dir := range []string{".", ".."} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			return string(b), nil
		}
	}
	return "", os.ErrNotExist
}

var dhNow = time.Date(2026, 10, 16, 3, 50, 0, 0, time.UTC)

func dhCanary(id string, age time.Duration) requestMirrorCanary {
	return requestMirrorCanary{RequestID: id, SeenAt: dhNow.Add(-age)}
}

func TestDataHorizonCanaryVerdictMatrix(t *testing.T) {
	cases := []struct {
		name      string
		canaries  []requestMirrorCanary
		present   map[string]bool
		wantStale bool
	}{
		{
			name:      "idle gateway has no local canary",
			canaries:  nil,
			present:   map[string]bool{},
			wantStale: false,
		},
		{
			name:      "immature absent canary stays inside async mirror budget",
			canaries:  []requestMirrorCanary{dhCanary("req-new", dataHorizonLagTolerance-time.Second)},
			present:   map[string]bool{},
			wantStale: false,
		},
		{
			name:      "mature canary in hot leg is healthy",
			canaries:  []requestMirrorCanary{dhCanary("req-hot", dataHorizonLagTolerance+time.Second)},
			present:   map[string]bool{"req-hot": true},
			wantStale: false,
		},
		{
			name:      "mature local canary absent is stale",
			canaries:  []requestMirrorCanary{dhCanary("req-missing", dataHorizonLagTolerance+time.Second)},
			present:   map[string]bool{},
			wantStale: true,
		},
		{
			name:     "another gateway fresh request cannot mask local missing id",
			canaries: []requestMirrorCanary{dhCanary("req-local-missing", dataHorizonLagTolerance+time.Second)},
			// "req-other-gateway-fresh" deliberately is not the local id. The old
			// MAX(ts) gate would call this healthy; identity matching must not.
			present:   map[string]bool{"req-other-gateway-fresh": true},
			wantStale: true,
		},
		{
			name: "continuous traffic cannot overwrite an older mature missing canary",
			canaries: []requestMirrorCanary{
				dhCanary("req-old-missing", dataHorizonLagTolerance+time.Second),
				dhCanary("req-new-present", time.Second),
			},
			present:   map[string]bool{"req-new-present": true},
			wantStale: true,
		},
		{
			name:      "expired canary is idle history not a current alarm",
			canaries:  []requestMirrorCanary{dhCanary("req-old", dataHorizonRecentTrafficWindow+time.Second)},
			present:   map[string]bool{},
			wantStale: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notice, got := dataHorizonNoticeFor(tc.canaries, tc.present, dhNow)
			if got != tc.wantStale {
				t.Fatalf("stale=%v, want %v, notice=%+v", got, tc.wantStale, notice)
			}
			if got && (notice == nil || !notice.Stale || notice.Unknown) {
				t.Fatalf("stale verdict shape=%+v, want stale=true unknown=false", notice)
			}
			if !got && notice != nil {
				t.Fatalf("healthy/idle path returned notice=%+v", notice)
			}
		})
	}
}

func TestDataHorizonUnknownNeverClaimsFreshness(t *testing.T) {
	notice, ok := dataHorizonNoticeForUnknown()
	if !ok || notice == nil || !notice.Unknown || notice.Stale {
		t.Fatalf("unknown verdict=%+v ok=%v", notice, ok)
	}
	for _, banned := range []string{"数据是新的", "仍在记录", "未停更"} {
		if strings.Contains(notice.Effect+notice.Silence, banned) {
			t.Errorf("unknown wording claims freshness with %q", banned)
		}
	}
}

func TestSessionTurnCanaryPresenceQueriesBothLegs(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT ARRAY(
			SELECT DISTINCT request_id
			FROM (
				SELECT request_id FROM public.session_turns_hot WHERE request_id = ANY($1::text[])
				UNION ALL
				SELECT request_id FROM public.session_turns WHERE request_id = ANY($1::text[])
			) turns
		)
	`)).
		WithArgs([]string{"req-hot", "req-parent"}).
		WillReturnRows(pgxmock.NewRows([]string{"array"}).AddRow([]string{"req-hot", "req-parent"}))

	present, err := sessionTurnCanaryPresenceFrom(context.Background(), mock, []requestMirrorCanary{
		{RequestID: "req-hot"}, {RequestID: "req-parent"}, {RequestID: "req-hot"},
	})
	if err != nil {
		t.Fatalf("presence query: %v", err)
	}
	if !present["req-hot"] || !present["req-parent"] || len(present) != 2 {
		t.Fatalf("presence=%v, want exact hot+parent ids", present)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDataHorizonEndpointShape(t *testing.T) {
	h := &Handler{} // nil DB -> unknown
	rec := httptest.NewRecorder()
	h.handleDataHorizon(rec, httptest.NewRequest("GET", "/api/admin/data-horizon", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	v, ok := body["data_horizon"]
	if !ok {
		t.Fatalf("missing data_horizon: %s", rec.Body.String())
	}
	if v != nil {
		obj, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("unexpected data_horizon=%#v", v)
		}
		stale, _ := obj["stale"].(bool)
		unknown, _ := obj["unknown"].(bool)
		if stale == unknown {
			t.Fatalf("invalid state stale=%v unknown=%v", stale, unknown)
		}
	}
	if got := rec.Header().Get(DataHorizonHeader); got != "unknown" {
		t.Fatalf("header=%q", got)
	}
}

func TestDataHorizonCanaryRingIsLocalAndBounded(t *testing.T) {
	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	base := dhNow.Add(-9 * time.Minute)
	hub.AddDataHorizonCanary("req-first", base)
	hub.AddDataHorizonCanary("req-same-minute", base.Add(30*time.Second))
	hub.AddDataHorizonCanary("req-next-minute", base.Add(time.Minute))

	got := hub.DataHorizonCanaries()
	if len(got) != 2 || got[0].RequestID != "req-first" || got[1].RequestID != "req-next-minute" {
		t.Fatalf("canary ring=%+v; first request per minute must remain", got)
	}
	got[0].RequestID = "mutated"
	if hub.DataHorizonCanaries()[0].RequestID != "req-first" {
		t.Fatal("DataHorizonCanaries leaked mutable backing state")
	}
}

func TestBoardFallbackSourceIsSessionFamily(t *testing.T) {
	from, alias := boardRequestLogsFromClause()
	if alias != "r" {
		t.Fatalf("alias=%q", alias)
	}
	for _, needle := range []string{"session_turns_hot", "session_turn_details_hot", "session_turns ", "request_status"} {
		if !strings.Contains(from, needle) {
			t.Errorf("fallback source missing %q", needle)
		}
	}
	if strings.Contains(from, "without_customer_id") {
		t.Fatal("fallback still uses frozen v1 wrapper")
	}
}

func TestUsageShortWindowSourceIsSessionHot(t *testing.T) {
	from, alias := requestLogsFromClause(3)
	if alias != "r" || !strings.Contains(from, "session_turns_hot") || strings.Contains(from, "_request_logs_hot") {
		t.Fatalf("short source=%q alias=%q", from, alias)
	}
	fromLong, _ := requestLogsFromClause(30)
	if !strings.Contains(fromLong, "request_logs_with_current_month") {
		t.Fatalf("long source=%q", fromLong)
	}
}
