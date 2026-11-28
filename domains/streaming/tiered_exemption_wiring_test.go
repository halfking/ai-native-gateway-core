package streaming

import (
	"os"
	"strings"
	"testing"
)

// TestTieredExemptionSwitchesWiredToFourSurfaces 判据：豁免开关必须接入四面三级配额预检。
//
// 防止静默失效（L138 教训：team_budget_gate 写好后零调用点至今）——本判据读源码钉住
// settings.TieredExemptDefaultTenant/TieredExemptInternalKeys 在四面的字面调用。
func TestTieredExemptionSwitchesWiredToFourSurfaces(t *testing.T) {
	files := []string{
		"handler.go",
		"messages.go",
		"responses.go",
		"audio_service.go",
	}
	
	for _, fname := range files {
		src, err := os.ReadFile(fname)
		if err != nil {
			t.Fatalf("read %s: %v", fname, err)
		}
		content := string(src)
		
		// 验证两个开关调用都出现
		if !strings.Contains(content, "settings.TieredExemptDefaultTenant()") {
			t.Errorf("%s: missing settings.TieredExemptDefaultTenant() call", fname)
		}
		if !strings.Contains(content, "settings.TieredExemptInternalKeys()") {
			t.Errorf("%s: missing settings.TieredExemptInternalKeys() call", fname)
		}
		
		// 验证 PreCheckOrgAndUserForOwner 调用也在（三级预检本体）
		if !strings.Contains(content, "PreCheckOrgAndUserForOwner") {
			t.Errorf("%s: missing PreCheckOrgAndUserForOwner call (tiered precheck not wired)", fname)
		}
	}
}

// TestTeamBudgetGateWiredToFourSurfaces 判据：团队月度预算闸必须接入四面。
//
// 防止 L138 同型缺陷：写了判定体没有调用点 ⇒ 门静默失效。
func TestTeamBudgetGateWiredToFourSurfaces(t *testing.T) {
	files := []string{
		"handler.go",
		"messages.go",
		"responses.go",
		"audio_service.go",
	}
	
	for _, fname := range files {
		src, err := os.ReadFile(fname)
		if err != nil {
			t.Fatalf("read %s: %v", fname, err)
		}
		content := string(src)
		
		// 验证 CheckTeamMonthlyBudget 调用存在
		if !strings.Contains(content, "CheckTeamMonthlyBudget") {
			t.Errorf("%s: missing CheckTeamMonthlyBudget call (team_budget_gate not wired)", fname)
		}
		
		// 验证 KeyInfo.TeamID 读取存在
		if !strings.Contains(content, "keyInfo.TeamID") {
			t.Errorf("%s: missing keyInfo.TeamID access (team gate cannot function)", fname)
		}
	}
}
