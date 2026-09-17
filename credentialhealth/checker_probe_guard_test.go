package credentialhealth

// R37 (2026-09-17) 钉桩（合并版）：RecoverExpired 的可用性恢复守卫与
// bg/credential_recovery.go 同款——"全模型 broken 才阻止"计数必须经
// internal/probemode.GuardStateTable 按探测模式选权威源，不得硬编码冻结的
// model_probe_state。

import (
	"os"
	"strings"
	"testing"
)

func TestRecoverExpiredGuardUsesModeAwareGuardTable(t *testing.T) {
	src, err := os.ReadFile("checker.go")
	if err != nil {
		t.Fatalf("read checker.go: %v", err)
	}
	source := string(src)
	if !strings.Contains(source, "probemode.GuardStateTable()") {
		t.Fatal("RecoverExpired guard must select its state source via probemode.GuardStateTable")
	}
	if strings.Contains(source, "FROM model_probe_state mps") {
		t.Fatal("RecoverExpired guard regressed to hardcoded frozen model_probe_state")
	}
}
