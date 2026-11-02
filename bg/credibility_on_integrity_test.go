package bg

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNotifyIntegrityAnomaly_NilSafe(t *testing.T) {
	var w *ModelCredibilityWorker
	w.NotifyIntegrityAnomaly(1, "m", "p", "model_mismatch", "test")
	(&ModelCredibilityWorker{}).NotifyIntegrityAnomaly(1, "m", "p", "model_mismatch", "test")
	(&ModelCredibilityWorker{}).NotifyIntegrityAnomaly(0, "m", "", "model_mismatch", "test")
	(&ModelCredibilityWorker{db: nil}).NotifyIntegrityAnomaly(1, "", "", "model_mismatch", "test")
}

func TestIntegrityAnomalyCooldownDefault(t *testing.T) {
	w := &ModelCredibilityWorker{}
	if d := w.integrityAnomalyCooldown(); d != time.Hour {
		t.Fatalf("default = %v, want 1h", d)
	}
}

func TestMainWiresIntegrityCredibilityHooks(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..")
	src, err := os.ReadFile(filepath.Join(root, "cmd/gateway/main.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, needle := range []string{
		"integrity.NotifyRecorder",
		"NotifyIntegrityAnomaly",
		"SetCredibilityOnDrift",
		`"integrity_live"`,
		`"integrity_fp_drift"`,
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("main.go missing %s", needle)
		}
	}
}

func TestFingerprintDriftWiresCredibilityHook(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "integrity_fingerprint_drift.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "onDriftRecorded") {
		t.Error("IntegrityFingerprintDrift must carry onDriftRecorded hook")
	}
	if !strings.Contains(body, "SetCredibilityOnDrift") {
		t.Error("must export SetCredibilityOnDrift")
	}
	if !strings.Contains(body, "w.onDriftRecorded(p.credID, p.rawModel)") {
		t.Error("commit success path must fire onDriftRecorded")
	}
}

func TestCredibilityOnIntegritySettingsRegistered(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..")
	src, err := os.ReadFile(filepath.Join(root, "settings/spec_model_credibility.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, key := range []string{
		"model_credibility.trigger_on_integrity_anomaly",
		"model_credibility.integrity_anomaly_cooldown_hours",
		"model_credibility.trusted_skip",
	} {
		if !strings.Contains(body, key) {
			t.Errorf("settings missing %s", key)
		}
	}
}

func TestScanAllFiltersTrusted_ScanNodeDoesNot(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "model_credibility_worker.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "filterTrustedSkipTargets") {
		t.Error("ScanAll must filter trusted_skip")
	}
	// ScanNode comment must pin the manual exemption.
	idx := strings.Index(body, "func (w *ModelCredibilityWorker) ScanNode(")
	if idx < 0 {
		t.Fatal("ScanNode missing")
	}
	head := body[idx-200 : idx]
	if !strings.Contains(head, "不受") && !strings.Contains(head, "不受限") &&
		!strings.Contains(head, "Manual") && !strings.Contains(head, "trusted_skip") {
		// The doc comment immediately above ScanNode must mention the exemption.
		t.Error("ScanNode doc must state trusted_skip / featured do not apply to manual trigger")
	}
	if strings.Contains(body[idx:idx+400], "filterTrustedSkipTargets") {
		t.Error("ScanNode must NOT call filterTrustedSkipTargets")
	}
}

func TestIntegrityAnomalySharesInflightWithProbeSuccess(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "credibility_on_integrity.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "tryBeginProbeSuccessTrigger") {
		t.Error("integrity trigger must share inflight map with probe-success")
	}
	if !strings.Contains(body, "not_featured") {
		t.Error("integrity trigger must pin featured gate")
	}
	if !strings.Contains(body, "trusted_skip") {
		t.Error("integrity trigger must pin trusted_skip gate")
	}
}
