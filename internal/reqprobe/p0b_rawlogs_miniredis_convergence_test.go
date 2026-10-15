package reqprobe

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// rawLogFixture is a captured upstream_response body from local raw-logs
// (base64-decoded). Provenance fields keep the audit trail honest.
type rawLogFixture struct {
	SourceRawLog string          `json:"source_raw_log"`
	RequestID    string          `json:"request_id"`
	Direction    string          `json:"direction"`
	Protocol     string          `json:"protocol"`
	Body         json.RawMessage `json:"body"`
}

func loadRawLogFixture(t *testing.T, name string) rawLogFixture {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "testdata", "rawlogs", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fx rawLogFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	if len(fx.Body) == 0 || fx.RequestID == "" {
		t.Fatalf("fixture %s missing body/request_id", name)
	}
	return fx
}

// TestP0BRawLogsMiniredisCrossInstanceConvergence drives Diagnose + learn +
// strip from **disk-captured** raw-logs bodies (not hand-typed shapes).
func TestP0BRawLogsMiniredisCrossInstanceConvergence(t *testing.T) {
	cases := []struct {
		fixture string
		status  int
		proto   string
		want    string
		stripIn string // wire for StripParamsForWire
	}{
		{
			fixture: "pydantic_service_tier_422.json",
			status:  422,
			proto:   "openai-completions",
			want:    "service_tier",
			stripIn: "openai-completions",
		},
		{
			fixture: "unknown_param_reasoning_effort.json",
			status:  400,
			proto:   "openai-completions",
			want:    "reasoning_effort",
			stripIn: "openai-completions",
		},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			fx := loadRawLogFixture(t, tc.fixture)
			storeA, mr := newTestRedisStore(t)
			rdbB := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = rdbB.Close() })
			// 必须与 newTestRedisStore 同前缀，否则跨实例读不到（假红根因）。
			storeB := NewRedisStore(rdbB, "llmgw:reqanom:test")

			coordA := NewCoordinator(storeA)
			defer coordA.Close()
			in := Input{HTTPStatus: tc.status, ErrorBody: []byte(fx.Body), Protocol: tc.proto}
			diag, ok := Diagnose(in)
			if !ok || diag.Param != tc.want {
				t.Fatalf("Diagnose(%s rid=%s) = %+v ok=%v, want %s (raw-log前提在场)",
					fx.SourceRawLog, fx.RequestID, diag, ok, tc.want)
			}
			meta := TerminalMeta{ProviderCode: "rawlog-mock", OutboundModel: "fixture-m", ProviderID: 77}
			coordA.RecordTerminal(in, diag, meta, false)

			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				recs, _, err := storeA.List(context.Background(), Filter{ProviderCode: meta.ProviderCode, Model: meta.OutboundModel})
				if err == nil && len(recs) > 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}

			coordB := NewCoordinator(storeB)
			defer coordB.Close()
			var learned []string
			found := false
			for time.Now().Before(deadline.Add(2 * time.Second)) {
				coordB.InvalidateLearned()
				learned = coordB.LearnedParams(context.Background(), meta.ProviderCode, meta.OutboundModel)
				for _, p := range learned {
					if p == tc.want {
						found = true
						break
					}
				}
				if found {
					break
				}
				time.Sleep(15 * time.Millisecond)
			}
			if !found {
				t.Fatalf("instance B LearnedParams=%v, want %s (cross-instance from %s)",
					learned, tc.want, fx.RequestID)
			}
			body := []byte(`{"model":"fixture-m","messages":[],"` + tc.want + `":"x","stream_options":{"include_usage":true}}`)
			out, stripped := StripParamsForWire(body, tc.want, tc.stripIn)
			if len(stripped) != 1 || stripped[0] != tc.want {
				t.Fatalf("strip = %v, want [%s]", stripped, tc.want)
			}
			if strings.Contains(string(out), `"`+tc.want+`"`) {
				t.Fatalf("body still has %s after strip: %s", tc.want, out)
			}
		})
	}
}
