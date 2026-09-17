package bg

// R37 (2026-09-17) 钉桩（合并版）：凭据恢复的"全模型 broken 才阻止"守卫
// 必须经 probeGuardStateTable()（→ internal/probemode.GuardStateTable）按
// 探测模式选权威源表，禁止硬编码冻结的 model_probe_state——新模式下冻结行
// 会让新系统证实坏死的模型以 healthy 形态漏过守卫随凭据翻回 ready 入池。
// （与 R36 补位轮实现对账后采其 probemode 单一事实源机制。）

import (
	"os"
	"strings"
	"testing"
)

func TestCredentialRecoveryGuardUsesModeAwareGuardTable(t *testing.T) {
	src, err := os.ReadFile("credential_recovery.go")
	if err != nil {
		t.Fatalf("read credential_recovery.go: %v", err)
	}
	source := string(src)
	if !strings.Contains(source, "probeGuardStateTable()") {
		t.Fatal("recovery guard must select its state source via probeGuardStateTable (probemode.GuardStateTable)")
	}
	if !strings.Contains(source, "AND mps.state = 'broken_confirmed'") {
		t.Fatal("guard lost the broken_confirmed predicate")
	}
	if strings.Contains(source, "FROM model_probe_state mps") {
		t.Fatal("recovery guard regressed to hardcoded frozen model_probe_state")
	}
}
