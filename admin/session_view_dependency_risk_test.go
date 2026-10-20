// Package admin — session_view_dependency_risk_test.go
//
// 会话存储解耦 v3：把「哪些会话域读路径不能迁到 session 族原生源」钉成
// 可执行的守卫，而不是留在审计报告里等人重推。
//
// 背景（2026-09-30 实测，见 docs/audit/2026-09-30-session-request-data-re-audit.md
// §5.5.3 / §5.5.5）：`db.SessionFamilyTurnsSourceSQL()` **不是**
// _request_logs 视图的等价替代。镜像欠账清零之后仍有 641,452 个 request_id
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

// sessionKeyedReaders lists the session-domain readers that look rows up by
// **request key** rather than by a session predicate
// (request_id / client_request_id / parent_request_id).
//
// # ★ 2026-10-20（迁移 885，v1 族真删）：判据从「必须留在视图上」换成
// 「读的源必须覆盖会话族的两条腿」
//
// 原判据（forbiddenViewReaders）是「这些读方**必须**继续用
// request_logs_with_current_month，不得用原生源」，理由是：
// 「原生源会漏 27.6% 的行 —— 那些行只存在于 v1（探针/自检、无会话头流量、
// in_progress 占位行、标题/摘要回环）」。
//
// 那条理由的**前提是「v1 还在」**。885 之后 canonical 视图**本身就是**
// 会话族两臂体（hot ∪ 母表 + 734 details 层）⇒ 视图与原生源返回**同样的行**
// ⇒ 「视图 vs 原生源」这个区分**已经没有对象**。
//
// ⇒ 不变式换成等价且仍成立的那一条：**读的源必须覆盖会话族的两条腿**
// （hot ∪ 母表）。只读 hot 会静默漏掉已 promote 的行 —— 那才是真正的
// 「漏读」形态（promote 会把 hot 搬进母表并排空 hot）。
//
// ⚠ 谓词形状的标记（marker）仍然要钉：那道门真正防的是「这个读方的查询
// 形状变了」——形状变了就得重新分类，而不是让分类器跟着漂。
type sessionKeyedReaders = []struct {
	// fileWide bans the source check at file level. Use it only when
	// EVERY view usage in that file is ineligible; a function-scoped check
	// silently passes when the file has several occurrences of the marker.
	fileWide bool
	file     string
	marker   string // a distinctive string that must still be present
	shape    string // why this reader is in the class
}

// coversBothSessionLegs 报告一段源码里读的源是否覆盖会话族两条腿。
//
// 允许的三种形态（都是 hot ∪ 母表）：
//   - canonical 视图 request_logs_with_current_month（885 之后 = 会话族两臂体）
//   - db.SessionFamilyTurnsSourceSQL()（原生源，hot ∪ 母表）
//   - db.SessionFamilyTurnsForSessionSQL()（原生源，会话谓词内推版）
func coversBothSessionLegs(span string) bool {
	return strings.Contains(span, "request_logs_with_current_month") ||
		strings.Contains(span, "SessionFamilyTurnsSourceSQL") ||
		strings.Contains(span, "SessionFamilyTurnsForSessionSQL")
}

var sessionKeyedReaderTable = sessionKeyedReaders{
	{
		file:   "unified_detail.go",
		marker: "WHERE rl.request_id = $1",
		shape:  "unified detail is a per-request_id lookup — no session predicate to scope on",
	},
	{
		file:   "unified_detail.go",
		marker: "WHERE rl.client_request_id = $1",
		shape:  "client-request lookup, same hazard as request_id",
	},
	{
		file:   "session_turns_tree.go",
		marker: "WHERE parent_request_id = ANY($1)",
		shape:  "children are fetched by parent_request_id, not by session",
	},
	{
		file:   "session_turns_unified.go",
		marker: "parent_request_id = ANY($2)",
		shape:  "same as session_turns_tree: request_id-keyed fan-out",
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
		//
		// fileWide: BOTH view queries in this file are ineligible, so the ban is
		// file-level. A function-scoped check was tried first and proved
		// ineffective: the file contains two occurrences of the marker, so the
		// guard inspected whichever came first and the second stayed unchecked —
		// mutating only the second call site left the test green.
		fileWide: true,
		file:     "no_topic_session.go",
		marker:   "FROM request_logs_with_current_month rl",
		shape:    "no-topic listing is keyed on gw_task_id IS NULL — the header-less traffic that never mirrors",
	},
	{
		file:   "session_title.go",
		marker: "FROM request_logs_with_current_month rl",
		shape:  "loadTaskLogsForTitle keys on sessionLogsWhere → gw_task_id = $1, not on a session predicate",
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
// ★ 2026-10-20（885）：判据从「必须在视图上、不得用原生源」换成
// 「读的源必须覆盖会话族两条腿（hot ∪ 母表）」—— 理由见
// sessionKeyedReaderTable 的头注（canonical 视图在 885 之后就是会话族两臂体，
// 「视图 vs 原生源」已无对象）。
//
// Discriminating power was verified by mutation: pointing any one of these
// readers at a **single-leg** source (hot only) makes this test red.
func TestSessionRequestKeyedReadersStayOnView(t *testing.T) {
	for _, tc := range sessionKeyedReaderTable {
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
			// ★ 反转后的正向判据：读的源必须覆盖会话族两条腿。
			//
			// ⚠ **源这一项按文件级判，不按函数级**：「这个文件读的源覆盖两条腿吗」
			// 是文件的属性；而「这个谓词形状还在吗」才是每条查询的属性。
			// 函数级判源会踩一个具体的坑：`funcSpan` 从 marker 往后取，
			// 而 marker（谓词）常常就写在 FROM 源**下面一行** ⇒ 源那一行
			// 落在 span 之外 ⇒ 判据恒红（反过来：源在 marker 之上时恒绿）。
			// ⇒ 源判文件级，谓词判函数级。
			if !coversBothSessionLegs(body) {
				t.Errorf("%s 的源既不是 canonical 视图、也不是会话族原生源 —— "+
					"它读不到会话族的一半行（promote 会把 hot 搬进母表并排空 hot ⇒ 只读 hot = 静默漏读）。\n"+
					"该读方是 %s。", tc.file, tc.shape)
			}
			// ★ 反转后的负向判据：**单腿**源（hot only）不得出现。
			if strings.Contains(body, "SessionFamilyTurnsHotSourceSQL") {
				t.Errorf("%s 用了 SessionFamilyTurnsHotSourceSQL（只有 hot 一条腿）—— "+
					"promote 之后 hot 被排空，读它会静默漏掉已归档的轮次。该读方是 %s。",
					tc.file, tc.shape)
			}
		})
	}
}

// TestSessionKeyedReaderSourceGuardHasTeeth 是上面那道门的**阳性对照**：
// 同一把判据必须能抓住「只读 hot 一条腿」这个退化。
//
// ⚠ 没有这一条，上面那道门就只是「没坏」的同义反复 —— 而判据纪律明确
// 指出：只断言「没坏」的判据，在「干脆没做」面前永远是绿的。
func TestSessionKeyedReaderSourceGuardHasTeeth(t *testing.T) {
	// ① 单腿源必须被识别为「不覆盖两条腿」。
	if coversBothSessionLegs("FROM ` + dbpkg.SessionFamilyTurnsHotSourceSQL() + ` rl") {
		t.Error("coversBothSessionLegs 接受了只有 hot 一条腿的源 —— 判据没有区分力")
	}
	// ② 两腿源（原生源 / 视图）必须被接受。
	for _, ok := range []string{
		"FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` rl",
		"FROM ` + dbpkg.SessionFamilyTurnsForSessionSQL() + ` rl",
		"FROM request_logs_with_current_month rl",
	} {
		if !coversBothSessionLegs(ok) {
			t.Errorf("coversBothSessionLegs 拒绝了合法形态 %q —— 判据过严", ok)
		}
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
// nativeSessionReaderExemptions lists the class-A readers that deliberately
// keep a v1-store read, each with the reason it is not a §5.5.5 violation.
//
// 删掉一条 nativeSessionReaders 条目很容易，但「这条为什么例外」比「这条被删了」
// 重要得多 —— 没有记录，半年后没人敢动它，也没人知道它是不是还能删。
//
//	session_summary_v2.go — buildRequestLogsFallbackQuery 的 turns 腿
//	  读 public.request_logs_with_current_month 而不是原生源。
//	  原因：这条 fallback 只在**主路径查不到轮次**时才被调用。把它换成
//	  session 族原生源（02c93d04e 做过）等于让它去读主路径刚判定为空的同一批
//	  表，必然返回 0 行 → `no turns found` → HTTP 500。审计缺陷 7。
//	  代价（实测，同一会话 gw_63798b79，169 轮）：
//	    v1 视图 + 排除谓词   21.6 ms / 10,013 buffers
//	    原生源               0.55 ms /    280 buffers   （≈40×）
//	  接受这个代价：该路径今天服务 0 个会话（有业务轮次却缺失于原生源的会话
//	  实测为 0），而加上 v1 源是它能返回任何东西的唯一办法。
var nativeSessionReaderExemptions = map[string]string{
	"session_summary_v2.go": "buildRequestLogsFallbackQuery 的 turns 腿必须读 v1 视图；" +
		"换成原生源即使 fallback 恒返回 0 轮（审计缺陷 7）。代价 21.6ms vs 0.55ms，" +
		"落在今天服务 0 个会话的路径上。",
}

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
	// 例外必须仍然被**钉住**：被豁免的文件不得悄悄改回 v1 读，也不得
	// 悄悄开始读一个第三种源。豁免不是空白，是一条独立的断言。
	for file, reason := range nativeSessionReaderExemptions {
		t.Run("exemption/"+file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(".", file))
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			code := stripGoComments(string(raw))
			if strings.Contains(code, "SessionFamilyTurnsForSessionSQL") {
				t.Errorf("%s 的豁免理由已失效或已过期：它又开始读原生源了。%s",
					file, reason)
			}
			if !strings.Contains(code, "request_logs_with_current_month") {
				t.Errorf("%s 的豁免理由要求它读 v1 视图，实际没有。%s", file, reason)
			}
		})
	}
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
