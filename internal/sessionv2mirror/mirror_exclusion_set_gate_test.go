package sessionv2mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMirrorExclusionSetIsRegistered is the guard for the thing §9.93 already
// cost us once: a request that stops being mirrored and leaves no trace.
//
// # WHAT THIS PINS
//
// PersistHook's returned closure is a series of early returns, and every return
// before the write is dispatched is a request that will not exist in the
// session family. Today there are SIX of them, in two different kinds:
//
//	policy exclusions — by design, permanent for their whole category:
//	  A  !entry.Success && !isTerminalFailure(entry)
//	     in_progress placeholders. §9.93's rationale: V2 turns are idempotent
//	     on request_id and cannot be updated, so mirroring a placeholder would
//	     permanently record success=false and drop the later success
//	     enrichment.
//	  B  IsProbeSyntheticSession(entry)
//	     probe / self-check traffic; the facts stay on the v1 face by design.
//	  C  !synthetic && telemetry.IsInternalAutoEntry(entry)
//	     gateway-internal title/summary loopbacks are not user turns. Note
//	     the !synthetic half: loopbacks with NO session head ARE mirrored, as
//	     system sessions — so this exclusion is much narrower than the label
//	     suggests, and narrowing it is a real behaviour change.
//
//	operational guards — state-dependent, not design:
//	  D  entry == nil
//	  E  !shadowWriteEnabled()
//	  F  req == nil  (session id computed empty; already logs slog.Warn)
//
// A's accepted set is pinned by terminal_failure_gate_test.go. B and C have
// their predicates pinned by synthetic_session_test.go and
// internal_traffic_gate_test.go. What NOBODY pinned is the ENUMERATION — that
// the set is exactly these six and nothing else.
//
// # WHY THE ENUMERATION, AND NOT JUST THE POLICY ONES
//
// A first draft of this file registered only the three policy exclusions and
// asserted a count of 3. It failed immediately, reporting 7. Two of those
// were artifacts (the closure signature line, and returns that are nil-safety
// or flag checks), and three were real returns nobody had classified: the nil
// entry, the shadow flag, and req == nil. Reading the closure had produced a
// confident, wrong number — the same shape as §9.119's "3 files".
//
// The point is not that operational guards are interesting. It is that
// "policy exclusion" and "early return" are different sets, and a gate that
// pins only the former silently permits the latter to grow. Deleting guard E
// changes behaviour just as much as adding a fourth policy exclusion: the
// mirror would start writing when the flag says off.
//
// THE SHAPE OF THE CHECK
//
//  1. every registered predicate is present in the pre-dispatch window — this
//     catches a swapped or renamed guard (a coverage NARROWING);
//  2. the number of top-level returns in that window equals the number of
//     registered entries — this catches an ADDED guard with an unfamiliar
//     predicate, which is the case this gate exists for;
//  3. anti-vacuity: the window must be substantial and must contain the
//     positive-control predicate, so a broken slice fails instead of passing.
//
// # WHY THE DISPATCH POINT IS THE ANCHOR
//
// Because one return in this function is not an exclusion at all:
// `if !shadowWriteDispatchAsync { run(); return }` performs the write
// synchronously and then returns. Counting returns without reference to the
// dispatch point would classify a dispatch branch as a coverage hole.
func TestMirrorExclusionSetIsRegistered(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("hook.go"))
	if err != nil {
		t.Fatalf("读不到 hook.go：%v", err)
	}
	text := string(src)

	hookSig := "return func(entry *telemetry.RequestLogEntry) {"
	hookStart := strings.Index(text, hookSig)
	if hookStart < 0 {
		t.Fatal("hook.go 里找不到 PersistHook 的返回闭包签名；" +
			"解析失败会让本门退化成「什么都没查」的绿跑")
	}
	// Start AFTER the signature line. Slicing from hookStart itself counts the
	// `return func(...)` token as a guard — which the first draft of this test
	// did, and which is why its baseline was 7 instead of 6.
	bodyStart := hookStart + len(hookSig)

	dispatchRel := strings.Index(text[bodyStart:], "runShadowWrite(writer")
	if dispatchRel < 0 {
		t.Fatal("hook.go 里找不到 runShadowWrite 派发点；" +
			"本门靠它区分「排除」与「写完就返回」，锚点失效等于判据失效")
	}
	preWrite := text[bodyStart : bodyStart+dispatchRel]

	// Anti-vacuity, condition 3. The last registered predicate is used as the
	// positive control: its absence means the window is wrong, not that the
	// guard was removed.
	type registered struct{ predicate, kind, why string }
	want := []registered{
		{"entry == nil", "运行期",
			"D 空指针防护：不是覆盖面决策"},
		{"isTerminalFailure(entry)", "策略",
			"A 非终态占位：in_progress 行在会话族永远没有孪生（§9.93：V2 按 request_id 幂等且不可更新）"},
		{"IsProbeSyntheticSession(entry)", "策略",
			"B 探针合成会话：探针/自检流量按设计不镜像，事实留在 v1 面"},
		{"IsInternalAutoEntry(entry)", "策略",
			"C 内部回环：网关内部标题/摘要生成回环不是用户 turn。注意 !synthetic 条件——" +
				"无会话头的回环仍会镜像成 system 会话，所以这条比标签看起来窄得多"},
		{"shadowWriteEnabled()", "运行期",
			"E 影子写开关：关掉时本就不写。删掉它会让镜像在开关说「关」时照写"},
		{"req == nil", "运行期",
			"F 合成会话 id 算空：已有 slog.Warn（§9.93 的修复）。这是排查信号，不是覆盖面决策"},
	}
	if len(preWrite) < 500 {
		t.Fatalf("写入派发前的区间只有 %d 字节，几乎必然是解析错误而不是真的变短了", len(preWrite))
	}
	// Hoisted rather than indexed inline at each use. Purely for readability;
	// there was never a parser problem with the inline form (see the note in
	// the commit message for how that false lead arose).
	positiveControl := want[len(want)-1].predicate
	if !strings.Contains(preWrite, positiveControl) {
		t.Fatalf("写入派发前的区间里找不到正向对照判据 %q；区间切错了，"+
			"此时下面的「早退数 = %d」是空转", positiveControl, len(want))
	}

	// Condition 1 — every registered predicate present.
	for _, r := range want {
		if !strings.Contains(preWrite, r.predicate) {
			t.Errorf("写入派发前的区间里找不到已登记的排除判据 %q（%s，%s）——"+
				"要么它被删了/改了（覆盖面收窄，行为改变且无人发现），要么本门区间切错了",
				r.predicate, r.kind, r.why)
		}
	}

	// Condition 2 — exactly one top-level return per registered entry.
	//
	// Counted over non-blank, non-comment lines whose first token is `return`.
	// The body uses one-return-per-block style, so a guard's return always
	// starts its own line. If someone rewrites a guard as `if c { return }`
	// on one line this count changes and the gate asks for re-registration —
	// intended friction, not a bug.
	exclReturns := 0
	for _, line := range strings.Split(preWrite, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if trimmed == "return" || strings.HasPrefix(trimmed, "return ") {
			exclReturns++
		}
	}
	if exclReturns != len(want) {
		policy := 0
		for _, r := range want {
			if r.kind == "策略" {
				policy++
			}
		}
		t.Errorf("写入派发前有 %d 个早退，但只登记了 %d 条（其中策略排除 %d 条）。\n"+
			"多出来的早退是**静默的覆盖面收缩**：没有编译错误、没有测试转红、没有日志、"+
			"没有指标、没有 outbox 行——那一类请求就此在会话族里不存在，"+
			"而几个月后只表现为「数不出行、归因不了」（§9.93 / §9.54.3 的形状）。\n"+
			"少掉的早退同样要处理：那是覆盖面的收窄，同样不会有人发现。\n"+
			"确认是有为之的后，把新条目的判据、**种类（策略/运行期）**与原因补进本测试的 want。",
			exclReturns, len(want), policy)
	}
}
