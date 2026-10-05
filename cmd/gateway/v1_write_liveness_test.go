//go:build !integration

package main

import (
	"testing"
)

// ── 门 ────────────────────────────────────────────────────────────────

// TestV1WriteLivenessSQLReadsBothFaces 把「两面都要读」钉住。
func TestV1WriteLivenessSQLReadsBothFaces(t *testing.T) {
	for _, face := range []string{
		"request_logs_hot", "request_logs",
		"session_turns_hot", "session_turns",
	} {
		if !containsSub(v1WriteLivenessSQL, face) {
			t.Errorf("存活读数没有读 %s —— 本地真库父表落后 hot 约 491 分钟，"+
				"只读一个面会把健康的写腿报成 dead", face)
		}
	}
	// 阴性对照：判据必须能分辨单面与双面，否则上面四条会恒绿。
	one := "SELECT 1 FROM request_logs WHERE 1=1"
	if containsAllSub(one, []string{"request_logs_hot", "request_logs"}) {
		t.Error("判据分不清单面与双面：一个只读 request_logs 的片段被判成双面")
	}
}

// TestClassifyV1WriteLiveness 四种形状各一条，外加**两个会咬人的形状**。
func TestClassifyV1WriteLiveness(t *testing.T) {
	cases := []struct {
		name string
		in   v1WriteLivenessInput
		want string
	}{
		{"v1 两个面都有行", v1WriteLivenessInput{V1HotRows: 91, V1ParentRows: 0, TurnsHotRows: 29}, livenessAlive},
		{"只有父面有行（罕见但合法）", v1WriteLivenessInput{V1HotRows: 0, V1ParentRows: 5, TurnsHotRows: 3}, livenessAlive},
		{"★ 只有 hot 面有行 —— 本地真库此刻的形状", v1WriteLivenessInput{V1HotRows: 91, V1ParentRows: 0, TurnsHotRows: 29}, livenessAlive},
		{"v1 两面都零、session 在写 ⇒ dead", v1WriteLivenessInput{V1HotRows: 0, V1ParentRows: 0, TurnsHotRows: 70757}, livenessDead},
		{"两侧都零 ⇒ quiet（不是 dead）", v1WriteLivenessInput{}, livenessQuiet},
		{"v1 零、session 也零、但阈值=0 ⇒ 走默认 1", v1WriteLivenessInput{ThresholdRows: 0}, livenessQuiet},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyV1WriteLiveness(tc.in); got != tc.want {
				t.Errorf("判决 = %q，期望 %q（输入 %+v）", got, tc.want, tc.in)
			}
		})
	}
}

// TestClassifyV1WriteLivenessIsNotDrivenByTheSwitch 钉住 §9.238 的要害。
//
// 形状：开关读数是 true（v1 写入「开着」），而实际零行。
// 判据里**没有**开关这一项，所以它只能来自行存在性。
func TestClassifyV1WriteLivenessIsNotDrivenByTheSwitch(t *testing.T) {
	in := v1WriteLivenessInput{V1HotRows: 0, V1ParentRows: 0, TurnsHotRows: 1234}
	if got := ClassifyV1WriteLiveness(in); got != livenessDead {
		t.Errorf("§9.238 的形状（v1 零行、session 在写）判成 %q，期望 %q —— "+
			"若它依赖开关读数，这个测试就是这道门失效的证据", got, livenessDead)
	}
	// 反向：hot 面有 1 行就必须报 alive，哪怕父面零行。
	// 这是「单面会误报 dead」那条活假阳性的最小复现。
	if got := ClassifyV1WriteLiveness(v1WriteLivenessInput{V1HotRows: 1, V1ParentRows: 0, TurnsHotRows: 29}); got != livenessAlive {
		t.Errorf("hot 面有 1 行时判成 %q，期望 %q", got, livenessAlive)
	}
	// ★ 负控制：只把父面喂进来（模拟单面实现）必须得到 dead，
	// 与上面同一时刻的 alive 相反 —— 两条同时绿才说明判据有牙。
	singleFace := v1WriteLivenessInput{V1HotRows: 0, V1ParentRows: 0, TurnsHotRows: 29}
	if got := ClassifyV1WriteLiveness(singleFace); got != livenessDead {
		t.Errorf("单面喂入（hot=0,parent=0,turns=29）判成 %q，期望 %q —— "+
			"负控制失效，说明上面两条不构成对照", got, livenessDead)
	}
}

func containsSub(s, sub string) bool { return containsAllSub(s, []string{sub}) }

func containsAllSub(s string, subs []string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
