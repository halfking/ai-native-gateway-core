package telemetry

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ============================================================================
// 819 落点（审计 §9.66）的判据。
//
// **为什么这组判据里有一道是"读源码文本"的**：要钉的性质是
// "markRequestAbandonedPending / clearRequestAbandonedPending 的**调用**
// 落在 insertRequestLog / updateRequestLog 的 `if logsWrite {}` **之外**"。
// 这一条无法用行为测试表达——真要行为验证就得把 S4 开关拉起来再跑一次全链路
// 集成测试，成本与耦合都不成比例。
//
// **而"读文本的判据"正是本项目栽过两次的地方**（§9.59.7 的自指护栏、
// §9.65 提到的探针位置问题），所以这里对三个自由度逐个设防：
//
//   1. **存在性**：函数名在文件里出现过 —— 最弱，定义处就能满足；
//   2. **归属**：先把目标函数的**函数体**切出来，再在体内找 —— 防止
//      「同一文件另一段代码恰好有同样的符号」顶缸；
//   3. **顺序**：体内 `if logsWrite {` 的偏移必须**大于**调用点的偏移 ——
//      这一条才是"在门控之外"的真正含义，且与 1/2 正交。
//
// 三条一起上，删调用、把调用搬进门控、或者只剩一个同名定义，都必须变红。
// 变异见 TestAbandonedMarkerGateIsNotDecorative 的配套说明（开发期实跑）。
// ============================================================================

// abandonedGateSpec is one (function, marker call) pair under test.
type abandonedGateSpec struct {
	funcName string
	call     string
	// Why: what breaks if this marker ends up inside the gate.
	why string
}

var abandonedGateSpecs = []abandonedGateSpec{
	{
		funcName: "insertRequestLog",
		call:     "markRequestAbandonedPending(ctx, tx, entry)",
		why:      "S4 停写后 request_logs 不再产生 in_progress 占位 INSERT，门控内则「开始了」永不落任何表",
	},
	{
		funcName: "updateRequestLog",
		call:     "clearRequestAbandonedPending(ctx, tx, entry.RequestID)",
		why:      "DELETE 半边是承重的那半边：搬进门控后 request_abandoned 停止表示 abandoned 集合，改为全流量增长",
	},
}

// clientSource reads the file under test. Failures are fatal rather than
// skipped: "could not read the source" must not read as "gate passed".
func clientSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join("client.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// extractFuncBody returns the source of the named top-level func, including
// its braces. Scoping to the body is what makes the order assertion bite: a
// marker call sitting in some *other* function cannot satisfy it.
func extractFuncBody(t *testing.T, src, funcName string) string {
	t.Helper()
	start := strings.Index(src, "func (c *Client) "+funcName+"(")
	if start < 0 {
		t.Fatalf("func (c *Client) %s not found in client.go", funcName)
	}
	open := strings.Index(src[start:], "{")
	if open < 0 {
		t.Fatalf("%s: no opening brace", funcName)
	}
	depth, i := 0, start+open
	for ; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("%s: unbalanced braces; body extraction failed", funcName)
	return ""
}

var logsWriteGateRe = regexp.MustCompile(`(?m)^\tif logsWrite \{$`)

// TestAbandonedMarkerCallsAreOutsideTheS4Gate pins freedom #3 (order) within
// freedom #2 (attribution): the marker call must appear in the function body
// BEFORE the `if logsWrite {` gate line.
//
// Why "before" and not merely "somewhere in the body": the gate is an
// if-block, so anything textually after it is inside it. Comparing offsets is
// the only way to tell the two apart without parsing Go.
func TestAbandonedMarkerCallsAreOutsideTheS4Gate(t *testing.T) {
	src := clientSource(t)
	for _, spec := range abandonedGateSpecs {
		t.Run(spec.funcName, func(t *testing.T) {
			body := extractFuncBody(t, src, spec.funcName)

			gate := logsWriteGateRe.FindStringIndex(body)
			if gate == nil {
				t.Fatalf("no `if logsWrite {` line in %s — the S4 gate this test pins against is gone; "+
					"either the gate moved or it was deleted, and neither can be silently accepted. (%s)",
					spec.funcName, spec.why)
			}

			call := strings.Index(body, spec.call)
			if call < 0 {
				t.Errorf("%s: call `%s` not found in the function body — "+
					"the abandoned-marker wiring is missing. (%s)", spec.funcName, spec.call, spec.why)
				return
			}

			if call > gate[0] {
				t.Errorf("%s: call `%s` sits at offset %d, AFTER the `if logsWrite {` gate at offset %d "+
					"⇒ it is INSIDE the S4 stop-write gate and will die with v1. (%s)",
					spec.funcName, spec.call, call, gate[0], spec.why)
				return
			}
			t.Logf("ok: %s — call at %d, gate at %d (call is %d bytes ahead of the gate)",
				spec.funcName, call, gate[0], gate[0]-call)
		})
	}
}

// TestAbandonedMarkerBothHalvesAreWired guards against the cheaper regression:
// someone deletes ONE of the two calls. Each half alone is nearly harmless
// (an unwritten marker loses 0.047% of rows; an undeleted marker grows the
// table at full traffic rate) and neither existing test above would notice if
// only one were removed... except this one, which asserts both exist in their
// own function bodies.
func TestAbandonedMarkerBothHalvesAreWired(t *testing.T) {
	src := clientSource(t)
	for _, spec := range abandonedGateSpecs {
		body := extractFuncBody(t, src, spec.funcName)
		if strings.Count(body, spec.call) != 1 {
			t.Errorf("%s: expected exactly 1 occurrence of `%s` in the body, got %d "+
				"(0 = half removed, >1 = called from the wrong place too)",
				spec.funcName, spec.call, strings.Count(body, spec.call))
		}
	}
}

// ============================================================================
// 行为判据：谓词必须窄。
//
// 上一轮吃过这个亏（占位符检测用 `「[^」]+」` 命中正常中文行文，于是门永远红，
// 而永远红的门等于没有门）。这里的对称风险是反的：谓词写成 `!entry.Success`
// 就**太宽**——updateRequestLog 的 RowsAffected==0 回落 INSERT 带着**终态**
// entry 也会走 insertRequestLog，于是会给一个**已经结束**的请求盖上
// abandoned 标记，表语义当场从「遗弃集合」变成「一堆已完成的请求」。
//
// 这个门钉住**真函数**，不是它的复刻。
// ============================================================================

func TestRequestIsStartedNotFinished_Predicate(t *testing.T) {
	sp := func(s string) *string { return &s }

	cases := []struct {
		name  string
		entry *RequestLogEntry
		want  bool
	}{
		{"in_progress 是唯一的真信号", &RequestLogEntry{RequestStatus: sp(RequestStatusInProgress)}, true},
		{"终态 success 不算", &RequestLogEntry{RequestStatus: sp("success"), Success: true}, false},
		{"终态 failure 不算（哪怕 Success=false）", &RequestLogEntry{RequestStatus: sp("failure")}, false},
		{"终态 rate_limited 不算", &RequestLogEntry{RequestStatus: sp("rate_limited")}, false},
		// 这条是「谓词太宽」会漏掉的那一格：Success=false 但状态是终态。
		// 回落 INSERT 走的就是这一格，用 !Success 判就会误标。
		{"Success=false 但 request_status=failure ⇒ 仍不算", &RequestLogEntry{Success: false, RequestStatus: sp("failure")}, false},
		{"status 缺失不算（宁可不记也不误记）", &RequestLogEntry{Success: true}, false},
		{"空 status 不算", &RequestLogEntry{RequestStatus: sp("")}, false},
		{"nil entry 不算", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestIsStartedNotFinished(tc.entry); got != tc.want {
				t.Errorf("requestIsStartedNotFinished() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRequestIsStartedNotFinishedRejectsTheWideForm is the anti-regression for
// "someone widens the predicate to !Success". It asserts the *distinguishing*
// case directly so the table above cannot be quietly edited to agree.
func TestRequestIsStartedNotFinishedRejectsTheWideForm(t *testing.T) {
	// 回落 INSERT 的形态：终态 failure、Success=false。
	// A `!entry.Success` predicate would return true here — and that is wrong.
	entry := &RequestLogEntry{Success: false, RequestStatus: strPtr("failure")}
	if requestIsStartedNotFinished(entry) {
		t.Fatal("a terminal-failure entry was classified as started-not-finished; " +
			"this is exactly the RowsAffected==0 fallback INSERT that would get a bogus " +
			"abandoned marker, turning request_abandoned from the abandoned set into a dump of completed requests")
	}
}
