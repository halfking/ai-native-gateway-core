package credentialhealth

// R37 (2026-09-17) 钉桩：RecoverExpired 的可用性恢复守卫与
// bg/credential_recovery.go 同款——"全模型 broken 才阻止"计数必须读
// v_node_probe_state_compat（新模式下权威源），不得回读冻结的
// model_probe_state。源码形状断言（同 bg 包先例）。

import (
	"os"
	"strings"
	"testing"
)

func TestRecoverExpiredGuardReadsCompatView(t *testing.T) {
	src, err := os.ReadFile("checker.go")
	if err != nil {
		t.Fatalf("read checker.go: %v", err)
	}
	source := string(src)
	if !strings.Contains(source, "FROM v_node_probe_state_compat mps") {
		t.Fatal("RecoverExpired availability guard must read v_node_probe_state_compat (frozen model_probe_state hides new-system-confirmed dead models)")
	}
	if strings.Contains(source, "FROM model_probe_state mps") {
		t.Fatal("RecoverExpired guard regressed to reading frozen model_probe_state directly")
	}
}
