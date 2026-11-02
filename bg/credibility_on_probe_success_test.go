package bg

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
)

func TestCredibilityTriggerKey(t *testing.T) {
	a := credibilityTriggerKey(7, " GLM-4 ")
	b := credibilityTriggerKey(7, "glm-4")
	if a != b {
		t.Fatalf("key must be case/trim insensitive: %q vs %q", a, b)
	}
	if a != "7|glm-4" {
		t.Fatalf("got %q", a)
	}
}

func TestProbeSuccessInflightDedupe(t *testing.T) {
	w := &ModelCredibilityWorker{}
	key := "1|m"
	if !w.tryBeginProbeSuccessTrigger(key) {
		t.Fatal("first begin must succeed")
	}
	if w.tryBeginProbeSuccessTrigger(key) {
		t.Fatal("second begin must fail while inflight")
	}
	w.endProbeSuccessTrigger(key)
	if !w.tryBeginProbeSuccessTrigger(key) {
		t.Fatal("after end, begin must succeed again")
	}
	w.endProbeSuccessTrigger(key)
}

func TestProbeSuccessShouldSkip_VerifiedCooldown(t *testing.T) {
	// Unit-level: exercise the switch with a synthetic Previous via a
	// thin helper — we can't hit LoadPrevious without a DB, so we pin
	// the cooldown duration helpers and the wiring gates instead.
	w := &ModelCredibilityWorker{}
	if d := w.probeSuccessCooldown(); d != 6*time.Hour {
		t.Fatalf("default cooldown = %v, want 6h", d)
	}
	if d := w.probeSuccessRecheck(); d != 1*time.Hour {
		t.Fatalf("default recheck = %v, want 1h", d)
	}
	// Recheck must never exceed cooldown even if misconfigured later.
	if w.probeSuccessRecheck() > w.probeSuccessCooldown() {
		t.Fatal("recheck must be ≤ cooldown")
	}
}

func TestNotifyProbeSuccess_NilSafe(t *testing.T) {
	var w *ModelCredibilityWorker
	w.NotifyProbeSuccess(1, "m", "test") // must not panic
	(&ModelCredibilityWorker{}).NotifyProbeSuccess(1, "m", "test")
	(&ModelCredibilityWorker{}).NotifyProbeSuccess(0, "m", "test")
	(&ModelCredibilityWorker{db: nil}).NotifyProbeSuccess(1, "", "test")
}

func TestNodeProbeSuccessWiresCredibilityTrigger(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "node_probe.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "w.modelCredibilityTrigger(credID, model)") {
		t.Error("runOne success branch must call modelCredibilityTrigger")
	}
	// MarkNodeProbeHealthy must NOT fire credibility (business success is hot).
	idx := strings.Index(body, "func MarkNodeProbeHealthy(")
	if idx < 0 {
		t.Fatal("MarkNodeProbeHealthy missing")
	}
	rest := body[idx:]
	end := strings.Index(rest[1:], "\nfunc ")
	if end > 0 {
		rest = rest[:end+1]
	}
	if strings.Contains(rest, "modelCredibilityTrigger") {
		t.Error("MarkNodeProbeHealthy must NOT call modelCredibilityTrigger — " +
			"business success would amplify credibility probe cost")
	}
}

func TestMainWiresCredibilityOnProbeSuccess(t *testing.T) {
	root := filepath.Join(filepath.Dir(mustCallerFile(t)), "..")
	src, err := os.ReadFile(filepath.Join(root, "cmd/gateway/main.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "SetModelCredibilityTrigger") {
		t.Error("main.go must wire SetModelCredibilityTrigger")
	}
	if !strings.Contains(body, `NotifyProbeSuccess(int64(credID), rawModel, "node_probe_success")`) {
		t.Error("main.go must call NotifyProbeSuccess with node_probe_success source")
	}
	if strings.Count(body, "SetModelCredibilityTrigger") < 2 {
		t.Error("both nodeProbeWorker construction branches must wire the credibility trigger")
	}
}

func TestCredibilityOnProbeSuccessSettingsRegistered(t *testing.T) {
	root := filepath.Join(filepath.Dir(mustCallerFile(t)), "..")
	src, err := os.ReadFile(filepath.Join(root, "settings/spec_model_credibility.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, key := range []string{
		"model_credibility.trigger_on_probe_success",
		"model_credibility.probe_success_cooldown_hours",
		"model_credibility.probe_success_recheck_hours",
		"model_credibility.trigger_on_integrity_anomaly",
		"model_credibility.integrity_anomaly_cooldown_hours",
		"model_credibility.trusted_skip",
	} {
		if !strings.Contains(body, key) {
			t.Errorf("settings missing %s", key)
		}
	}
}

func TestProbeSuccessShouldSkip_UsesVerdictConstants(t *testing.T) {
	// Pin that the skip switch references the same verdict SSOT as the package.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "credibility_on_probe_success.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, v := range []string{
		"modelcredibility.VerdictVerified",
		"modelcredibility.VerdictSuspected",
		"modelcredibility.VerdictDiluted",
		"modelcredibility.VerdictUnknown",
	} {
		if !strings.Contains(body, v) {
			t.Errorf("cooldown switch must use %s", v)
		}
	}
	_ = context.Background()
	_ = modelcredibility.VerdictVerified
}

func mustCallerFile(t *testing.T) string {
	t.Helper()
	_, f, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return f
}
