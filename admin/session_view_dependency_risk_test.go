// Package admin — session_view_dependency_risk_test.go
//
// 会话存储解耦 v3：把「哪些会话域读路径不能迁到 session 族原生源」钉成
// 可执行的守卫，而不是留在审计报告里等人重推。
//
// 背景（2026-09-30 实测，见 docs/audit/2026-09-30-session-request-data-re-audit.md
// §5.5.3 / §5.5.5）：`db.SessionFamilyTurnsSourceSQL()` **不是**
// request_logs 视图的等价替代。镜像欠账清零之后仍有 641,452 个 request_id
// （2,321,464 的 27.6%）能在视图里查到、在原生源查不到 —— 它们是
// 无会话头流量（探针/自检，IsProbeSyntheticSession 按设计排除）、in_progress
// 占位行（hook.go:71 不镜像）、以及标题/摘要生成器内部回环
// （hook.go:102 IsInternalAutoEntry 排除）。
//
// 判据不是「镜像是否补齐」，而是**查询的谓词形态**：
//
//	A. 带 gw_session_id / session_id 谓词（会话内读）→ 原生源可用，
//	   差异只是按设计排除的内部回环与非终态行（会话口径 2.30%）。
//	B. 不带会话谓词（按 request_id / client_request_id / parent_request_id
//	   反查，或全量时间窗聚合）→ 原生源会漏 27.6% 的行，**禁止迁移**。
package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// forbiddenViewReaders lists the session-domain readers that MUST keep using
// request_logs_with_current_moth. Each entry records the file, the predicate
// shape that makes it ineligible, and the measured cost of migrating it.
//
// Keep this list tight: it is a safety rail, not a wishlist. Each line here
// was classified by reading the actual WHERE clause AND confirmed against
// the live database — see the audit report.
var forbiddenViewReaders = []struct {
	// fileWide bans the native helper anywhere in the file. Use it only when
	// EVERY view usage in that file is ineligible; a function-scoped check
	// silently passes when the file has several occurrences of the marker.
	fileWide   bool
	file       string
	marker     string // a distinctive string that must still be present
	shape      string // why the native source cannot serve it
	mustAbsent string // what a naive migration would introduce instead
}{
	{
		file:       "unified_detail.go",
		marker:     "WHERE request_id = $1",
		shape:      "unified detail is a per-request_id lookup — no session predicate to scope on",
		mustAbsent: "SessionFamilyTurnsSourceSQL",
	},
	{
		file:       "unified_detail.go",
		marker:     "WHERE client_request_id = $1",
		shape:      "client-request lookup, same hazard as request_id",
		mustAbsent: "SessionFamilyTurnsSourceSQL",
	},
	{
		file:       "session_turns_tree.go",
		marker:     "WHERE parent_request_id = ANY($1)",
		shape:      "children are fetched by parent_request_id, not by session",
		mustAbsent: "SessionFamilyTurnsSourceSQL",
	},
	{
		file:       "session_turns_unified.go",
		marker:     "parent_request_id = ANY($2)",
		shape:      "same as session_turns_tree: request_id-keyed fan-out",
		mustAbsent: "SessionFamilyTurnsSourceSQL",
	},
	{
		// Correction to the first pass of this table (2026-09-30): these three
		// were initially filed under "session-scoped, only bodies constrains
		// them". Reading the actual WHERE clauses showed otherwise —
		// noTopicLogsWhere is `gw_task_id IS NULL AND api_key_prefix = $1`
		// ("rows with no task"), and sessionLogsWhere keys on
		// `gw_task_id = $1`. Both are task-keyed, not session-keyed, and both
		// select exactly the traffic that never enters the session family.
		//
		// Measured: for gw_task_id-keyed lookups the view has 132,955 rows of
		// which 37,064 (27.9%) are unreachable natively — the same 27.6%
		// class as the request_id probes.
		//
		// NOTE session_title.go also has a genuinely session-scoped query at
		// :321 (`WHERE gw_session_id = $1 AND tenant_id = $2`) which IS
		// eligible. The two are told apart by shape — the ineligible one
		// aliases the source (`... rl`, because it joins bodies), the
		// eligible one does not — and the guard is function-scoped, since
		// forbidding the whole file would be wrong.
		// fileWide: BOTH view queries in this file are ineligible, so the ban is
		// file-level. A function-scoped check was tried first and proved
		// ineffective: the file contains two occurrences of the marker, so the
		// guard inspected whichever came first and the second stayed unchecked —
		// mutating only the second call site left the test green.
		fileWide:   true,
		file:       "no_topic_session.go",
		marker:     "FROM request_logs_with_current_month rl",
		shape:      "no-topic listing is keyed on gw_task_id IS NULL — the header-less traffic that never mirrors",
		mustAbsent: "SessionFamilyTurnsSourceSQL",
	},
	{
		file:       "session_title.go",
		marker:     "FROM request_logs_with_current_month rl",
		shape:      "loadTaskLogsForTitle keys on sessionLogsWhere → gw_task_id = $1, not on a session predicate",
		mustAbsent: "SessionFamilyTurns",
	},
}

// funcSpan returns the source of the function enclosing marker. Guards that
// mix an ineligible query and an eligible one in the same file (session_title
// .go has both) must not degrade into a file-level check.
// It searches FORWARD from the marker, never backward. An earlier version
// walked back to the previous `\nfunc `; when the marker was a function name
// sitting on the declaration line, that landed in the *previous* function and
// the guard silently checked the wrong scope — a false negative that only
// mutation testing exposed.
func funcSpan(src, marker string) (string, bool) {
	idx := strings.Index(stripGoComments(src), marker)
	if idx < 0 {
		return "", false
	}
	after := idx + len(marker)
	nl := strings.IndexByte(src[after:], '\n')
	if nl < 0 {
		return src[idx:], true
	}
	after += nl + 1
	rest := src[after:]
	next := strings.Index(rest, "\nfunc ")
	if next < 0 {
		return src[idx:], true
	}
	return src[idx : after+next+1], true
}

// TestSessionRequestKeyedReadersStayOnView is the executable form of the
// §5.5.5 classification.
//
// Discriminating power was verified by mutation: swapping any one of these
// readers to the native source makes this test red, because the check is on
// the marker being absent from the native-source form and present in the
// view form — not on a `[\s\S]*` pattern that would tolerate both.
func TestSessionRequestKeyedReadersStayOnView(t *testing.T) {
	for _, tc := range forbiddenViewReaders {
		t.Run(tc.file+"/"+tc.marker, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(".", tc.file))
			if err != nil {
				t.Fatalf("read %s: %v", tc.file, err)
			}
			body := stripGoComments(string(raw))
			span := body
			if !tc.fileWide {
				// Mixed eligibility inside one file — scope to the function.
				s, ok := funcSpan(body, tc.marker)
				if !ok {
					t.Fatalf("%s: could not locate the function containing %q; update the guard's marker", tc.file, tc.marker)
				}
				span = s
			}
			if !strings.Contains(span, tc.marker) {
				t.Fatalf("%s no longer contains %q — the query shape changed; re-classify it against §5.5.3 before assuming the marker is still safe to guard",
					tc.file, tc.marker)
			}
			if strings.Contains(span, tc.mustAbsent) {
				t.Errorf("%s now uses %s, but this reader is %s.\n"+
					"Native mode would silently drop the rows that only exist in v1 — probes, in_progress placeholders and title/summary loopbacks (27.6%% of the view by request_id, 27.9%% by gw_task_id). Keep it on the view until S6 is preceded by a decision on those rows.",
					tc.file, tc.mustAbsent, tc.shape)
			}
		})
	}
}

// TestSessionScopedReadersUseNativeSource is the positive half: the readers
// that legitimately moved must stay moved, so a future refactor cannot
// quietly undo the migration (or, more importantly, cannot re-point them at
// a *different* incomplete source).
//
// Each entry is a session-scoped read whose predicate shape makes the native
// source eligible (see §5.5.5 class A).
var nativeSessionReaders = []struct {
	file string
	want string
}{
	{"session_online.go", "SessionFamilyTurnsForSessionSQL"},
	{"session_summary_v2.go", "SessionFamilyTurnsForSessionSQL"},
	{"session_compare.go", "SessionFamilyTurnsForSessionSQL"},
	{"turns_sessions.go", "SessionFamilyTurnsSourceSQL"},
	// 2026-09-30 second wave, all verified against the live DB (same session,
	// same tenant): session_list summary 1607/1607 with byte-identical
	// MIN/MAX ts, session_list detail 500/500 with an identical md5
	// fingerprint over the ordered request_ids, session_turns_tree main
	// query 1607/1607, session_title gw_task_id both resolve to 'auto'.
	{"session_list.go", "SessionFamilyTurnsForSessionSQL"},
	{"session_turns_tree.go", "SessionFamilyTurnsForSessionSQL"},
	{"session_export.go", "SessionFamilyTurnsForSessionSQL"},
	// session_title.go holds BOTH an ineligible query (loadTaskLogsForTitle,
	// keyed on gw_task_id) and an eligible one (the gw_task_id resolver at
	// :321, keyed on gw_session_id). It is listed here for the eligible half;
	// the other half is pinned by forbiddenViewReaders above.
	{"session_title.go", "SessionFamilyTurnsForSessionSQL"},
}

// stripGoComments removes line and block comments so a guard matches CODE,
// not prose. Without this, the positive check below was satisfied by the
// explanatory comment above querySessionTimeline (which names the helper),
// and swapping the call for a different source kept the test green —
// a guard that cannot fail. Mutation-verified.
func stripGoComments(src string) string {
	src = blockCommentRE.ReplaceAllString(src, " ")
	src = lineCommentRE.ReplaceAllString(src, " ")
	return src
}

var (
	blockCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineCommentRE  = regexp.MustCompile(`(?m)//[^\n]*`)
)

func TestSessionScopedReadersUseNativeSource(t *testing.T) {
	for _, tc := range nativeSessionReaders {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(".", tc.file))
			if err != nil {
				t.Fatalf("read %s: %v", tc.file, err)
			}
			code := stripGoComments(string(raw))
			if !strings.Contains(code, tc.want) {
				t.Errorf("%s must read the session family via %s (session-scoped predicate makes it eligible — audit §5.5.5 class A)",
					tc.file, tc.want)
			}
		})
	}
}

// TestNoSprintfOnNativeSourceSQL keeps the Sprintf hazard from coming back in
// a new call site: a SQL blob must never be the format-string argument of
// fmt.Sprintf, because the projection contains `LIKE 'sys:%'` and the '%'
// would be consumed as a verb (see the §5.5.4 incident and
// TestTurnsSessionSQLHasNoFormatArtifacts, which covers the generated output).
func TestNoSprintfOnNativeSourceSQL(t *testing.T) {
	// Sprintf(`...` + db.SessionFamilyXxxSQL() + `... %d ...`, args)
	pat := regexp.MustCompile("(?s)fmt\\.Sprintf\\(`[^`]*`\\s*\\+\\s*(db|dbpkg)\\.SessionFamily")
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range matches {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if pat.Match(raw) {
			t.Errorf("%s passes a session-family SQL blob into fmt.Sprintf's format string; "+
				"use string concatenation (and strconv.Itoa for bind positions) instead — "+
				"the projection contains %% verbs and will corrupt the query at runtime", f)
		}
	}
}
