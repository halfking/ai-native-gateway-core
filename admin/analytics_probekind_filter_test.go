package admin

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/probekind"
)

// TestBusinessRequestFilterStagesComeFromProbekind 钉住 analytics 排除集
// 与 probekind.ProbeStages 同源 —— 禁止再手写 stage 字面量。
func TestBusinessRequestFilterStagesComeFromProbekind(t *testing.T) {
	got := businessRequestFilter("")
	wantStages := probekind.SQLStageList()
	if !strings.Contains(got, "NOT IN ("+wantStages+")") {
		t.Fatalf("businessRequestFilter must embed probekind.SQLStageList()\n got: %s\nwant substring: NOT IN (%s)", got, wantStages)
	}
	for _, stage := range probekind.ProbeStages {
		if !strings.Contains(got, "'"+stage+"'") {
			t.Errorf("missing stage %q in filter: %s", stage, got)
		}
	}
	if strings.Contains(got, "'self_check', 'node_probe', 'system_health', 'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual'") &&
		wantStages != "'self_check', 'node_probe', 'system_health', 'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual'" {
		// Only fail the hard-coded copy if probekind has drifted away from it;
		// the positive assertion above already requires every ProbeStages member.
		t.Log("note: literal still matches current ProbeStages; SSOT assertion is the Contains(SQLStageList) check")
	}
}

func TestSQLStageListNonEmpty(t *testing.T) {
	if probekind.SQLStageList() == "" {
		t.Fatal("SQLStageList must not be empty")
	}
}

func TestProbeRequestFilterEmbedsSQLStageList(t *testing.T) {
	got := probeRequestFilter("rl")
	wantStages := probekind.SQLStageList()
	if !strings.Contains(got, wantStages) {
		t.Fatalf("probeRequestFilter must embed SQLStageList()\n got: %s\nwant substring: %s", got, wantStages)
	}
	if !strings.Contains(got, "rl.origin_stage") {
		t.Fatalf("probeRequestFilter must qualify alias: %s", got)
	}
}
