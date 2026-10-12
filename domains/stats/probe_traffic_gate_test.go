package stats

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

func intPtr(v int) *int { return &v }

// probeCase is one row under test: a request_logs entry and whether the board
// must refuse to count it.
type probeCase struct {
	name  string
	entry *telemetry.RequestLogEntry
	want  bool
}

// TestIsProbeTraffic covers the live (Go) half of the board probe exclusion.
// Each case is one arm of bg.ProbeTrafficExclusionPredicateView; the SQL twin
// is the source of truth, this guards against the two drifting apart silently.
func TestIsProbeTraffic(t *testing.T) {
	cases := []probeCase{
		{
			name:  "nil entry is not probe",
			entry: nil,
			want:  false,
		},
		{
			name:  "plain business row passes",
			entry: &telemetry.RequestLogEntry{RequestStatus: strptr("success")},
			want:  false,
		},
		{
			name:  "quality_flags probe arm",
			entry: &telemetry.RequestLogEntry{QualityFlags: []string{"probe"}},
			want:  true,
		},
		{
			name:  "quality_flags other issue is not probe",
			entry: &telemetry.RequestLogEntry{QualityFlags: []string{"empty_tool_name"}},
			want:  false,
		},
		{
			name:  "task_type probe_triggered arm",
			entry: &telemetry.RequestLogEntry{TaskType: strptr("probe_triggered")},
			want:  true,
		},
		{
			name:  "task_type normal is not probe",
			entry: &telemetry.RequestLogEntry{TaskType: strptr("chat")},
			want:  false,
		},
	}

	for _, actor := range probeGatewayActors {
		cases = append(cases, probeCase{
			name:  "origin_actor arm: " + actor,
			entry: &telemetry.RequestLogEntry{OriginActor: strptr(actor)},
			want:  true,
		})
	}

	cases = append(cases,
		probeCase{
			name:  "business actor is not probe",
			entry: &telemetry.RequestLogEntry{OriginActor: strptr("auto-title-generator")},
			want:  false,
		},
		probeCase{
			name:  "whitespace-padded actor still matches",
			entry: &telemetry.RequestLogEntry{OriginActor: strptr("  probe-service  ")},
			want:  true,
		},
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsProbeTraffic(tc.entry); got != tc.want {
				t.Errorf("IsProbeTraffic() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFromTelemetryEntryExcludesProbeTraffic is the behavioural gate: a probe row
// that reaches the live accumulator must contribute nothing to request_stats_minute.
// Before the fix, probe rows landed in the board's 总请求数 and dragged 成功率 down.
func TestFromTelemetryEntryExcludesProbeTraffic(t *testing.T) {
	base := func() *telemetry.RequestLogEntry {
		return &telemetry.RequestLogEntry{
			Op:            telemetry.RequestLogUpdate,
			TenantID:      "default",
			RequestStatus: strptr(telemetry.RequestStatusFailure),
			PromptTokens:  intPtr(10),
		}
	}

	business := base()
	if _, _, _, ok := FromTelemetryEntry(business, testNow()); !ok {
		t.Fatal("business row must still be counted")
	}

	for _, actor := range probeGatewayActors {
		probe := base()
		probe.OriginActor = strptr(actor)
		if main, dims, drills, ok := FromTelemetryEntry(probe, testNow()); ok {
			t.Errorf("probe actor %q produced a rollup row (requests=%d dims=%d drills=%d); "+
				"probe traffic must not reach the board counters", actor, main.Requests, len(dims), len(drills))
		}
	}

	qualityProbe := base()
	qualityProbe.QualityFlags = []string{"probe"}
	if _, _, _, ok := FromTelemetryEntry(qualityProbe, testNow()); ok {
		t.Error("quality_flags=['probe'] row must not be counted")
	}

	taskProbe := base()
	taskProbe.TaskType = strptr("probe_triggered")
	if _, _, _, ok := FromTelemetryEntry(taskProbe, testNow()); ok {
		t.Error("task_type='probe_triggered' row must not be counted")
	}
}

// TestProbeActorSetMatchesSQLPredicate pins the Go actor list to the SQL one.
// bg is not importable here (bg imports this package), so the SQL constant is
// read from its source file. If someone adds an actor to the SQL predicate and
// forgets the Go list, live and rolled-up numbers disagree silently.
func TestProbeActorSetMatchesSQLPredicate(t *testing.T) {
	path := filepath.Join("..", "..", "bg", "probe_policy.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bg/probe_policy.go: %v", err)
	}

	const marker = "ProbeTrafficExclusionPredicateView = "
	idx := strings.Index(string(src), marker)
	if idx < 0 {
		t.Fatal("ProbeTrafficExclusionPredicateView not found in bg/probe_policy.go")
	}
	body := string(src)[idx:]

	for _, actor := range probeGatewayActors {
		if !strings.Contains(body, "'"+actor+"'") {
			t.Errorf("actor %q is in the Go list but missing from the SQL predicate; "+
				"the live writer and the rollup writer must classify the same rows", actor)
		}
	}

	// Reverse direction: every actor quoted inside the predicate's NOT IN list
	// must exist on the Go side, or a probe class leaks into the live path.
	// Bound the scan to the const's own final literal ("…'))\"") — a bare
	// LastIndex("')") would run past the declaration into the next const and
	// parse that one's actors too.
	open := strings.Index(body, "origin_actor, '') NOT IN ('")
	if open < 0 {
		t.Fatal("origin_actor NOT IN arm not found in the SQL predicate")
	}
	rest := body[open+len("origin_actor, '') NOT IN ('"):]
	end := strings.Index(rest, `'))"`)
	if end < 0 {
		t.Fatal("could not delimit the origin_actor NOT IN list")
	}
	list := rest[:end]
	for _, part := range strings.Split(list, ",") {
		actor := strings.Trim(strings.TrimSpace(part), "'")
		if actor == "" {
			continue
		}
		found := false
		for _, known := range probeGatewayActors {
			if known == actor {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("actor %q appears in the SQL predicate but not in the Go probeGatewayActors list", actor)
		}
	}
}
