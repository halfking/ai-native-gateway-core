// 门：迁移 890（v1 族退役）之后，这个包里的运行时 SQL 与夹具形态。
//
// 背景（2026-10-21 / R65 轮实测）：迁移 890 删掉了 `_request_logs` v1 族，
// 把这个包的 SQL 常量与**夹具**一起改了形态，但**没有**把两边的判据一起改。
// 后果有两类，都不是「一条红」那么简单：
//
//  1. ★★ 实现侧留了一颗定时炸弹：890 把 UPDATE 里的
//     `JOIN public._request_logs l` 删掉了，**却没改 SET 子句**——SET 仍在写
//     `l.request_status`，而别名 `l` 已随 v1 族一起消失 ⇒ 这条语句**根本无法被
//     解析**：`ERROR: missing FROM-clause entry for table "l"` (42P01)。
//     注意这是**解析期**错误：PG 在 bind 阶段就拒绝它，与 `AND FALSE` 会不会
//     命中任何行无关。当时是「候选集恒空 ⇒ UPDATE 永不可达」把它藏住了。
//
//  2. ★★★★ 夹具钉死了 SQL **文本** ⇒ 实现一改，夹具失配；而失配在 pgxmock
//     里不是「这条断言红」，而是**期望队列头被一个永不满足的 ExpectedQuery
//     卡住** ⇒ 后续每次调用都 "was not expected" ⇒ 产品的「探针失败 ⇒ 退避
//     重试」路径把它放大成**永不退出的循环** ⇒ 唯一的出口是 go test 的默认
//     600s 超时。实测：整包 606.375s，唯一未结束的测试是
//     TestSessionRequestStatusBackfill_GaugeSampledBeforeFirstDrain。
//
// ⇒ 本文件把这两类都变成「几秒内、会说话的红」。
package v2

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// statusBackfillGateFile is this file's own name — the self-scan below must
// skip it, because it necessarily contains the very patterns it forbids.
const statusBackfillGateFile = "session_request_status_backfill_v1_retirement_test.go"

// retiredV1Relation is the relation family migration 890 dropped.
const retiredV1Relation = "public._request_logs"

// TestStatusBackfillRuntimeSQLNeverReferencesTheRetiredV1Family 钉住
// 「运行时 SQL 不得引用已删的 v1 族」。
//
// 引用即 42P01，而且**在解析期**就炸——不是「查出来是空」，是「语句根本不
// 能送」。这一族里已经有一个实例（890 留下的 `l` 别名），所以这条不是
// 假想。
//
// 例外只有一处：**探针** sessionRequestStatusSourceProbeSQL 故意用
// `to_regclass('public._request_logs')` 去问「它还在不在」——那是**缺席检测器**，
// 不是引用（to_regclass 对缺席返回 NULL，不抛错）。它不在本门范围内。
func TestStatusBackfillRuntimeSQLNeverReferencesTheRetiredV1Family(t *testing.T) {
	batchSQL, _ := buildRequestStatusUpdateSQL([]requestStatusBackfillRow{
		{id: 42, partitionDate: statusFixedDate(), tenantID: "tenant_a", requestID: "req_42"},
	})
	for _, stmt := range []struct {
		name string
		sql  string
	}{
		{"remaining count", sessionRequestStatusRemainingSQL},
		{"candidate query", sessionRequestStatusCandidateSQL},
		{"batch update", batchSQL},
	} {
		if strings.Contains(strings.ToLower(executableSQL(stmt.sql)), retiredV1Relation) {
			t.Errorf("%s references the retired v1 family (%s) — migration 890 DROPPED "+
				"it, so any reference is a 42P01 at parse time, not an empty result.\n"+
				"--- statement ---\n%s", stmt.name, retiredV1Relation, stmt.sql)
		}
	}
	// The probe is the ONE legitimate mention: it asks whether the relation is
	// still there. If that ever changes, the retirement detector changes with
	// it, so it is pinned here rather than left implicit.
	if !strings.Contains(executableSQL(sessionRequestStatusSourceProbeSQL), "to_regclass('"+retiredV1Relation+"')") {
		t.Error("the source probe no longer probes the retired family by to_regclass — " +
			"retirement detection depends on it")
	}
}

// TestStatusBackfillBatchUpdateNeverNamesTheVanishedAlias 钉住 890 留下的那颗
// 定时炸弹的**类别**：语句里不得出现一个「没有定义的别名」。
//
// 这一条今天唯一的具体形态是 `l.`：别名 l 原本由 `JOIN public._request_logs l`
// 引入，890 删掉 JOIN 时没删 SET 子句里的引用 ⇒ 42P01。
func TestStatusBackfillBatchUpdateNeverNamesTheVanishedAlias(t *testing.T) {
	batchSQL, _ := buildRequestStatusUpdateSQL([]requestStatusBackfillRow{
		{id: 42, partitionDate: statusFixedDate(), tenantID: "tenant_a", requestID: "req_42"},
	})
	if strings.Contains(executableSQL(batchSQL), "l.") {
		t.Errorf("batch UPDATE names alias `l`, which no longer exists (it came from the "+
			"JOIN to the retired v1 family) ⇒ 42P01 at parse time.\n--- statement ---\n%s", batchSQL)
	}
	// The alias that IS defined must be the one used.
	if !strings.Contains(executableSQL(batchSQL), "t.request_status") {
		t.Errorf("batch UPDATE must assign from the table's own alias t (identity "+
			"assignment); got:\n%s", batchSQL)
	}
}

// TestStatusBackfillCountMatcherIsNotAnAlwaysYes 钉住夹具的**匹配器**。
//
// 语义匹配器（statusBackfillQueryMatcher）是本轮为了根治「夹具钉文本」而引入
// 的；但它自己也有一个失效模式：如果它变成「什么都匹配」，夹具就静默地失去
// 全部约束力——而门绿、测试也绿。
//
// ⇒ 所以必须有人证明它**会拒绝**。这是「对量具本身下门」：量具也需要被量。
func TestStatusBackfillCountMatcherIsNotAnAlwaysYes(t *testing.T) {
	if err := statusBackfillQueryMatcher(statusRemainingCountTag, "SELECT 0::bigint"); err != nil {
		t.Errorf("matcher rejected a genuine single-column bigint count query: %v", err)
	}
	for _, fake := range []string{
		"SELECT 1",
		"SELECT count(*) FROM public.session_turns",
		"UPDATE public.session_turns t SET request_status = 'x'",
		"SELECT now()",
	} {
		if err := statusBackfillQueryMatcher(statusRemainingCountTag, fake); err == nil {
			t.Errorf("matcher accepted %q as the remaining-count query — a matcher that "+
				"always passes makes the whole fixture toothless", fake)
		}
	}
	// The default path must still be the regexp matcher (tag-prefixed
	// expectations are the only special case).
	if err := statusBackfillQueryMatcher(`WHERE t\.id > \$1`, "WHERE t.id > $1"); err != nil {
		t.Errorf("default regexp path broke: %v", err)
	}
	if err := statusBackfillQueryMatcher(`WHERE t\.id > \$1`, "SELECT 1"); err == nil {
		t.Error("default regexp path accepted a non-matching query")
	}
}

// TestStatusBackfillCommentStripperActuallyStrips 钉住剥注释器本身。
//
// 量具也要被量：如果 stripSQLComments 悄悄不剥（或者剥过头），上面所有
// 「文本断言」就都变成了巧合检测器。
//
// 三条正交用例：
//
//	① 注释里写了关键词、可执行文本里没有 ⇒ 剥完后必须**没有**；
//	② 可执行文本里有 ⇒ 剥完后必须**有**；
//	③ 字符串字面量里的 '--' 不是注释 ⇒ 不能被吃掉。
func TestStatusBackfillCommentStripperActuallyStrips(t *testing.T) {
	// ① the keyword lives ONLY in a comment.
	onlyInComment := "SELECT 1\n-- AND FALSE is discussed here\nFROM public.session_turns"
	if strings.Contains(executableSQL(onlyInComment), "AND FALSE") {
		t.Error("stripSQLComments left a comment-only keyword in the text — every " +
			"text assertion in this package would be satisfiable by a comment")
	}
	// ② the keyword is executable.
	executable := "SELECT 1\nFROM public.session_turns\nWHERE x AND FALSE"
	if !strings.Contains(executableSQL(executable), "AND FALSE") {
		t.Error("stripSQLComments removed an EXECUTABLE keyword — the assertions " +
			"would miss the very thing they are supposed to pin")
	}
	// ③ a '--' inside a string literal is not a comment.
	literal := "SELECT 'a -- b' AS x"
	if !strings.Contains(executableSQL(literal), "'a -- b'") {
		t.Error("stripSQLComments ate a string literal containing '--' — it would " +
			"silently rewrite executable SQL")
	}
	// ④ a block comment is stripped too, including nested ones.
	block := "SELECT 1 /* outer /* inner */ still outer */ FROM public.session_turns"
	if strings.Contains(executableSQL(block), "inner") {
		t.Error("stripSQLComments left a block comment (or its nested part) in the text")
	}
}

// statusUnboundedRunCall matches a run()/Start() call fed an unbounded context.
//
// Why these two methods only: drain()/processBatch() return on the first error,
// so a stale fixture makes them FAIL fast; run()/Start() are the ones with a
// retry-backoff loop, so a stale fixture makes them HANG — and the only exit is
// go test's default 600s timeout.
var statusUnboundedRunCall = regexp.MustCompile(`\.(run|Start)\(\s*context\.Background\(\)`)

// TestStatusBackfillTestsNeverHandAnUnboundedContextToTheRetryLoop 钉住
// 「会重试的入口必须自带时限」。
//
// 这是本轮 606 秒挂起的**根因放大器**：夹具失配本身只是让探针失败，而
// run() 的「探针失败 ⇒ 退避重试」把它变成永不退出；测试若传
// context.Background()，唯一的出口就是框架的 600s 默认超时 ⇒ 一次 1 秒的夹具
// 失配被放大成 10 分钟的 CI 挂起。
//
// ⇒ 规则：**凡是调 run()/Start()，必须传有界 ctx。**
func TestStatusBackfillTestsNeverHandAnUnboundedContextToTheRetryLoop(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == statusBackfillGateFile {
			// This file necessarily contains the forbidden pattern.
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		checked++
		for _, m := range statusUnboundedRunCall.FindAllString(string(b), -1) {
			t.Errorf("%s calls %s with an unbounded context — the job's retry-backoff "+
				"loop would turn a fixture regression into a hang bounded only by "+
				"go test's default 600s timeout. Pass context.WithTimeout instead.", name, m)
		}
	}
	if checked == 0 {
		t.Fatal("no test files were scanned — the scan found nothing and proves nothing")
	}
}
