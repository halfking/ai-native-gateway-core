package probelane

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/probekind"
)

func TestPhysicalEnabledDefaultsOff(t *testing.T) {
	ResetPhysicalOverrideForTest()
	t.Setenv(EnvPhysical, "")
	if PhysicalEnabled() {
		t.Fatal("empty env must keep physical lane off")
	}
}

func TestPhysicalEnabledEnvOn(t *testing.T) {
	ResetPhysicalOverrideForTest()
	t.Setenv(EnvPhysical, "true")
	if !PhysicalEnabled() {
		t.Fatal("true must enable physical lane")
	}
}

func TestShouldIsolateRequiresFlagAndProbe(t *testing.T) {
	t.Cleanup(ResetPhysicalOverrideForTest)

	SetPhysicalEnabledForTest(false)
	if ShouldIsolate("node_probe", "", nil, "node-probe-worker") {
		t.Fatal("flag off ⇒ must not isolate")
	}

	SetPhysicalEnabledForTest(true)
	if !ShouldIsolate("node_probe", "", nil, "node-probe-worker") {
		t.Fatal("flag on + probe stage ⇒ must isolate")
	}
	if ShouldIsolate(probekind.BusinessStage, "", nil, "") {
		t.Fatal("business traffic must not isolate")
	}
	if !ShouldIsolate("self_check", "", nil, "self-check-worker") {
		t.Fatal("self_check must isolate when flag on")
	}
}

func TestRedisSuffixProbe(t *testing.T) {
	if got := RedisSuffixProbe("main"); got != "probe:main" {
		t.Fatalf("main → %q", got)
	}
	if got := RedisSuffixProbe("probe:main"); got != "probe:main" {
		t.Fatalf("idempotent → %q", got)
	}
	if got := RedisSuffixProbe("status:success"); got != "probe:status:success" {
		t.Fatalf("status → %q", got)
	}
}

// TestShouldIsolateUsesStatsArmPredicate 钉住「物理隔离写侧与 stats 两臂同口径」。
//
// 2026-11-02 审计发现：写侧曾用 probekind.IsProbe（完整判定，含 origin_stage），
// 而 stats 两臂（Go 臂 domains/stats.IsProbeTraffic、SQL 臂
// bg.ProbeTrafficExclusionPredicateView）都用 markers-only ⇒ 隔离开启时
// stage-only 探测行会「写进探测族表、却被 Go 臂当业务流量计入
// request_stats_minute」，且 SQL 臂读不到它 ⇒ 两臂对同一 ON CONFLICT 键
// 给出不同答案。
//
// 本门钉住：ShouldIsolate 的判定集合 == probekind.IsProbeMarkers 的集合
// （= stats 两臂的判定集合）。三处同源于 probekind，无第二份名单。
func TestShouldIsolateUsesStatsArmPredicate(t *testing.T) {
	t.Cleanup(ResetPhysicalOverrideForTest)
	SetPhysicalEnabledForTest(true)

	cases := []struct {
		name        string
		stage       string
		taskType    string
		qualityFlag []string
		actor       string
	}{
		{"actor only", "", "", nil, "node-probe-worker"},
		{"task_type only", "", probekind.ProbeTaskType, nil, ""},
		{"quality_flags only", "", "", []string{probekind.ProbeQualityFlag}, ""},
		{"stage + actor", "self_check", "", nil, "self-check-worker"},
		{"stage only (no marker)", "self_check", "", nil, ""},
		{"business stage", probekind.BusinessStage, "", nil, ""},
		{"nothing", "", "", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := probekind.IsProbeMarkers(tc.taskType, tc.qualityFlag, tc.actor)
			got := ShouldIsolate(tc.stage, tc.taskType, tc.qualityFlag, tc.actor)
			if got != want {
				t.Fatalf("ShouldIsolate = %v，stats 两臂口径（IsProbeMarkers）= %v；\n"+
					"两者不一致 ⇒ 隔离开启时该行会被 Go 臂与 SQL 臂判成不同结果，"+
					"同一 ON CONFLICT 键两臂给出不同答案",
					got, want)
			}
		})
	}
}

// TestEveryRealProbeWriterCarriesMarker 钉住「markers-only 判定覆盖全部真实写入者」。
//
// 这是 ShouldIsolate 改用 markers-only 的**前提**：若某个真实写入者的
// actor 不在白名单里，markers-only 就会漏掉它的流量（探测流量漏进业务表）。
// ProbeActors 由 probekind.Writers 派生 ⇒ 构造上即覆盖；
// 本门显式钉住这个不变量，避免后人把 actor 从 Writers 里删掉却忘了后果。
func TestEveryRealProbeWriterCarriesMarker(t *testing.T) {
	for actor := range probekind.Writers {
		if !probekind.IsProbeMarkers("", nil, actor) {
			t.Errorf("真实写入者 actor=%q 不被 markers-only 认作探测 ⇒ "+
				"ShouldIsolate 会漏掉它的流量（探测流量漏进业务表）", actor)
		}
	}
	if len(probekind.Writers) == 0 {
		t.Fatal("Writers 表为空——量具坏了（门会恒绿）")
	}
}
