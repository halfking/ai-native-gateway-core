//go:build !integration

package main

import (
	"testing"
)

// ── 门 ────────────────────────────────────────────────────────────────

// TestV1WriteLivenessSQLReadsBothFaces 把「读哪两个面」钉住。
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：这道门**反转**了。
//
// 原门要求读数必须读 **v1 族的两个面**（`_request_logs_hot` ∪ `_request_logs`），
// 理由是本地真库父表落后 hot 约 491 分钟，只读一个面会把健康的写腿报成 dead。
//
// 现在那一族被整体 DROP ⇒ 任何提到它的语句都是 **42P01**（整条语句失败，
// 不是那条腿失败）⇒ 读数已改读**会话族的两个面**
// （`session_turns_hot` ∪ `session_turns`，见 v1_write_liveness.go）。
// ⇒ 门从「必须读到 v1 两面」反转成「**不许再提 v1**；会话族两面都要读」。
//
// 为什么「会话族两面都要读」这条不能丢：会话族的 hot 与 parent 同样是两个
// **不同的关系**，只读 parent 依然会把「最近 N 分钟没有流量」误报成 dead
// （父面晋级延迟 ⇒ parent 落后 hot）。v1 族退役换掉了被读的那一族，
// 没有换掉「两个面」这件事本身。
func TestV1WriteLivenessSQLReadsBothFaces(t *testing.T) {
	// 阳性：会话族的两个面都要读（只读父面会把健康的写腿报成 dead）。
	for _, face := range []string{
		"session_turns_hot", "session_turns",
	} {
		if !containsIdentifier(v1WriteLivenessSQL, face) {
			t.Errorf("存活读数没有读 %s —— 只读一个面会把「最近 N 分钟没有流量」"+
				"误报成死亡信号（本地真库父面落后 hot 约 491 分钟那个实测）", face)
		}
	}
	// 阴性（退役）：不许再提 v1 族。
	//
	// 为什么是**族**语义（前缀命中即算命中）而不是标识符边界匹配：
	// containsIdentifier 要求 needle 后面不是标识符字节，而 '_' **就是**
	// 标识符字节 ⇒ containsIdentifier(sql, "_request_logs") 对
	// "_request_logs_hot" 返回 **false**，整族都漏掉。这里要的就是族语义。
	//
	// 反过来，族名前缀带下划线 ⇒ 规范视图 public.request_logs（**另一个**关系）
	// 不会被误伤（下面的负控制钉住这一点）。
	if containsSub(v1WriteLivenessSQL, "_request_logs") {
		t.Errorf("存活读数仍然提到 v1 族（_request_logs*）—— 那一族已被迁移 890 整体 DROP，" +
			"任何提到它的语句都是 42P01（整条语句失败，不是那条腿失败）")
	}

	// ★ 正控制：判据必须看得见 v1 族，否则上面那条否定断言恒绿。
	if !containsSub("SELECT count(*) FROM _request_logs_hot", "_request_logs") {
		t.Error("判据没有牙：一个确实提到 _request_logs_hot 的片段被判成「没提到」")
	}
	// 负控制：不能误伤会话族与规范视图。
	for _, other := range []string{
		"SELECT count(*) FROM session_turns_hot",
		"SELECT count(*) FROM public.request_logs",
	} {
		if containsSub(other, "_request_logs") {
			t.Errorf("判据误伤：%q 不含 v1 族，却被判成提到了", other)
		}
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
