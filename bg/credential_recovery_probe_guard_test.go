package bg

// credential_recovery_probe_guard_test.go — R37 (2026-09-17) 钉桩。
//
// useNewProbeMode（默认开）下 model_probe_state 停更：凭据恢复的
// "全模型 broken 才阻止"守卫若继续读冻结旧表，新系统（node_probe_state）
// 已证实坏死的模型将以冻结的 healthy 形态漏过守卫，坏死模型随凭据翻回
// ready 入池。守卫必须读 v_node_probe_state_compat（旧词汇投影）。
// 源码形状断言，无 PG 的 CI 也能挡住回归（同 routing_health_checks_sql_test 手法）。

import (
	"os"
	"strings"
	"testing"
)

func TestCredentialRecoveryGuardReadsCompatView(t *testing.T) {
	src, err := os.ReadFile("credential_recovery.go")
	if err != nil {
		t.Fatalf("read credential_recovery.go: %v", err)
	}
	source := string(src)
	if !strings.Contains(source, "FROM v_node_probe_state_compat mps") {
		t.Fatal("credential recovery 'all models broken' guard must read v_node_probe_state_compat (frozen model_probe_state cannot see new-system-confirmed dead models)")
	}
	if !strings.Contains(source, "AND mps.state = 'broken_confirmed'") {
		t.Fatal("guard lost the broken_confirmed predicate")
	}
}

func TestCredentialRecoveryGuardDoesNotReadFrozenTableDirectly(t *testing.T) {
	src, err := os.ReadFile("credential_recovery.go")
	if err != nil {
		t.Fatalf("read credential_recovery.go: %v", err)
	}
	if strings.Contains(string(src), "FROM model_probe_state mps") {
		t.Fatal("recovery guard regressed to reading frozen model_probe_state directly")
	}
}
