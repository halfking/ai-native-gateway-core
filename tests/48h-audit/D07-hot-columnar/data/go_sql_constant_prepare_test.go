// Package data - D07 数据测试（第六批）：**仓内 Go SQL 常量对真库做 PREPARE 普查**。
//
// # 这道门补的是哪一层
//
// R79 续四抓到的 P1 说明了一件容易被当成「当然没问题」的事：**一段 SQL 出现在代码里、
// 甚至有测试断言它的文本包含某个 JOIN，并不代表它能执行。**
// `domains/sessionsummary/message_source_v2.go` 里唯一相关的测试是
// `TestV2SessionBodiesBaseQuery_UsesCurrentMonthView`，它断言
// `strings.Contains(v2SessionBodiesBaseQuery, "LEFT JOIN public.session_turns_with_current_month t")`
// ——绿的。而那条查询在真库上直接 `ERROR: column t.origin_actor does not exist`。
//
// 「形状核对」这一层门禁在结构上就抓不到这件事：它比较的是**文本**，
// 而缺陷在**数据库的目录**里。缺的那一层是「用真库把 SQL 规划一遍」。
//
// # 为什么用 PREPARE 而不是真跑
//
// `PREPARE` 只做解析+规划，**不执行**，所以：
//   - 不会改动数据（本域主库全程只读，这条是硬约束）；
//   - 不需要真实参数值；
//   - 但缺列、缺对象、语法错、类型错全部在规划期就报出来——
//     `column ... does not exist` 正是规划期错误。
//
// # 错误分类：为什么「列不存在」判红而「关系不存在」不判红
//
// 普查第一版把所有规划错误一律判红，跑出来 100+ 条红里混着两种完全不同的东西：
//
//	42703 undefined_column   —— **列真的不存在**。仓内所有迁移 SQL 都翻过，
//	                          没有任何一条把 origin_actor 投进
//	                          session_turns_with_current_month；基表 session_turns
//	                          上它存在（attnum 99），所以这不是「列还没建」，
//	                          而是**视图的定值列清单漏了它**。环境无关 → 判红。
//	42P01 undefined_table / 42704 undefined_object
//	                       —— **本机没这个对象**。本机 schema_migrations 只到 V359，
//	                          大量迁移不在 startup 通道，于是库比代码旧。
//	                          这类错误与代码对错无关 → 只记录，不判红。
//	其他                    —— 没预料到的形状 → 判红（fail-closed）。
//
// 这条分类本身就是本轮的一条教训：**一个门刚写出来时的红，未必是缺陷，
// 往往是门的判据太粗。** 但粗判据不能靠「都放行」来修——要按错误码分层。
//
// 跑测（无库自动 skip；只需能读目录的只读角色，PREPARE 不改动数据）：
//
//	D07_S01_PG_URL=postgres://reader@127.0.0.1:5432/llm_gateway?sslmode=disable \
//	  go test -timeout 300s ./tests/48h-audit/D07-hot-columnar/data/...
package data

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// goBackquotedSQLConst matches a Go constant/var whose value is a single
// backquoted SQL statement. The `(?s)` is required: most of these bodies span
// many lines.
//
// fmt placeholders are excluded rather than expanded. A constant carrying %s/%d
// cannot be PREPAREd as written, and inventing values for it would mean the
// gate is testing a query nobody runs. They stay uncovered on purpose — see the
// coverage log below so the gap is visible instead of silent.
var goBackquotedSQLConst = regexp.MustCompile("(?s)(?:const|var)\\s+(\\w+)(?:\\s+\\w+)*\\s*=\\s*`([^`]*)`")

// hasFmtPlaceholder is deliberately narrow: %s %d %v %q and ${...} are the
// shapes that make a body untemplated-unprepareable. A bare % in a LIKE
// pattern is legal SQL and must not exclude the constant.
var hasFmtPlaceholder = regexp.MustCompile(`%[sdvq]|%\{|\$\{`)

// statementStart matches a body that *begins* with a DML keyword, i.e. a
// complete statement PREPARE can take.
//
// Why "begins" and not merely "contains SELECT": the first version used
// `contains`, and 48 constants came back as syntax_error — which looked like 48
// broken queries. They were mostly SQL *fragments* (`admin/logs.go::requestLogsJoins`
// is a LEFT JOIN block, `sessionSummarySelectCols` is a column list,
// `governorAutoRouteTriggerDDL` is CREATE TRIGGER). PREPARE cannot take a
// fragment, so it reports syntax_error, and a fragment is not a defect.
//
// That is the third time this round that a gate flagged the wrong object: the
// other two were checking a query per string literal, and reading only bare
// identifiers. The pattern is consistent enough to state as a rule — see
// "判据的对象要和被判定的东西同类".
var statementStart = regexp.MustCompile(`(?is)^\s*(--[^\n]*\n|\s)*(SELECT|INSERT|UPDATE|DELETE|WITH)\b`)

// ddlOrTransactionControl rejects bodies that are statements PREPARE is the
// wrong tool for, even though they are complete SQL.
// selectsOrWith / fromClause back the "no FROM means it is a fragment" rule
// above. Matching FROM loosely (FROM\s, FROM(", FROM\n) is enough here: the
// goal is only to notice that SOME relation is named.
var selectsOrWith = regexp.MustCompile(`(?is)^\s*(--[^\n]*\n|\s)*(SELECT|WITH)\b`)
var fromClause = regexp.MustCompile(`(?i)\bFROM\b`)

// concatAfter matches the ` +` that follows a closing backtick when a Go const
// is built by concatenating several raw strings.
var concatAfter = regexp.MustCompile(`^\s*\+`)

var ddlOrTransactionControl = regexp.MustCompile(`(?is)^\s*(--[^\n]*\n|\s)*(CREATE|ALTER|DROP|GRANT|REVOKE|SET|BEGIN|COMMIT|VACUUM|ANALYZE|COPY|REINDEX|CLUSTER|REFRESH|DO|CALL)\b`)

type sqlCandidate struct {
	file   string // repo-relative
	name   string // Go identifier
	line   int    // 1-based line of the declaration, for human triage
	sql    string
	params int // count of $N placeholders
}

// key uniquely identifies a constant.
//
// `file::name` is NOT unique: domains/routeincident/action_infra.go declares six
// different `sql` constants (one per function scope). Keying the justification
// registry by file::name meant the self-shrinking check fired on whichever of
// the six planned cleanly and then DELETED the entry, stripping the
// justification from the other five — which then reported red for a reason that
// had nothing to do with their own SQL. A content hash keeps the key stable
// across line moves while staying unique per statement.
func (c sqlCandidate) key() string {
	sum := sha256.Sum256([]byte(c.sql))
	return fmt.Sprintf("%s:%d::%s::%s", c.file, c.line, c.name, hex.EncodeToString(sum[:4]))
}

// prepareFailure classifies one PREPARE error.
type prepareFailure struct {
	cand     sqlCandidate
	code     string // pgerrcode, e.g. 42703
	msg      string
	verdict  string // "red" | "unverifiable"
	reasonIs string
}

func collectSQLCandidates(t *testing.T) []sqlCandidate {
	t.Helper()
	root := repoRoot(t)
	var out []sqlCandidate
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".build-local", "dist", "web", "installer":
				return fs.SkipDir
			}
			// The gate must not read itself: its own backquoted examples would
			// otherwise enter the corpus. Same self-reference trap documented in
			// paginated_view_limit_pushdown_test.go.
			if d.Name() == "48h-audit" && filepath.Base(filepath.Dir(path)) == "tests" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// SQLite stores are not PostgreSQL. storage/sqlite/request_log_store.go
		// declares plain `SELECT a, b, FROM t`-shaped constants (placeholder
		// syntax, `;`-terminated, SQLite-only column names) that PREPARE against
		// PostgreSQL reports as syntax_error / missing column. Those are not
		// defects — they are a different dialect pointed at the wrong server,
		// which is the same "gate judged the wrong object" shape as the three
		// earlier revisions of this file. Scope the sweep to the PostgreSQL
		// trees.
		if strings.Contains(filepath.ToSlash(path), "/storage/sqlite/") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		src := string(b)
		for _, m := range goBackquotedSQLConst.FindAllStringSubmatchIndex(src, -1) {
			name, body := src[m[2]:m[3]], src[m[4]:m[5]]
			line := 1 + strings.Count(src[:m[0]], "\n")
			// Go concatenates raw strings with ` + var + `. The regex can only
			// capture the first backquoted piece, so such a body is a truncated
			// fragment: `domains/reportrollup/rollup.go::internalPersonDaySQL`
			// embeds `' + personUnknown + '` and PREPAREd as-is reports
			// "unterminated quoted string". Detect the trailing ` +` after the
			// closing backtick and skip rather than reporting a syntax error for
			// something that is not a statement.
			if concatAfter.MatchString(src[m[5]:]) {
				continue
			}
			// A complete DML statement is the only thing PREPARE can take.
			// Fragments (JOIN blocks, column lists) and DDL are excluded —
			// see statementStart's doc comment for why "contains" was wrong.
			if !statementStart.MatchString(body) {
				continue
			}
			if ddlOrTransactionControl.MatchString(body) {
				continue
			}
			// A SELECT/WITH body with no FROM references no relation, so nothing
			// in it can be checked against the catalog: those are column-list
			// fragments (`toolexecution/postgres_store.go::selectExecutionCols`
			// is `SELECT execution_id, ..., created_at` with the FROM appended
			// by the caller) and `__mo_modality__`-style placeholders. Including
			// them reports a missing column for a statement nobody runs.
			if (selectsOrWith.MatchString(body)) && !fromClause.MatchString(body) {
				continue
			}
			if hasFmtPlaceholder.MatchString(body) {
				continue
			}
			out = append(out, sqlCandidate{
				file:   rel,
				name:   name,
				line:   line,
				sql:    strings.TrimSpace(body),
				params: strings.Count(body, "$"),
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		return out[i].name < out[j].name
	})
	return out
}

// justifiedPrepareFailures registers preparation failures that are understood
// and accepted, with the reason inline. Self-shrinking: an entry whose constant
// now plans cleanly turns the gate red.
//
// An entry with an EMPTY reason is not "accepted" — it is a placeholder that
// keeps the item visible as a failure. Use one deliberately when the item is
// confirmed real but not yet triaged.
var justifiedPrepareFailures = map[string]string{
	// ── P1：origin_actor（同型两处，开关开与关都撞）──────────────────────────
	// 视图 session_turns_with_current_month 的定值列清单（bootstrap / 640 同款体，
	// 65 列）没有 origin_actor，而基表 session_turns 上它存在（attnum 99）。
	// db/db.go:2990 只保证 request_logs_hot / request_logs 上有该列，
	// 全仓没有任何 SQL 把它投进 turns 视图。源码常量原文实跑：
	//   ERROR: column t.origin_actor does not exist
	//
	// **两条设置路径都中招**，这是定 P1 的理由：
	//   main_pipeline.go:1416 在 sessions_v2_compression_read（默认 true）下
	//   SetMessageSource(NewPerTurnDigestSource(pool))，而
	//   NewPerTurnDigestSource = gatedPerTurnDigestSource{digest: perTurnDigestSource,
	//   fallback: v2SessionBodiesSource}
	//     · 开关 sessions_summary_per_turn_digest 开 → sessionTurnDigestQuery（digest）
	//     · 开关关（默认）→ 逐字节委托 v2SessionBodiesBaseQuery（fallback）
	//   **两条都引用 t.origin_actor，都失败。** 唯一可用配置是把
	//   sessions_v2_compression_read 置 false 退回 V1 request_logs 源。
	// 消费面：会话摘要（GenerateSummary / GenerateRollingSummary）的输入读取。
	// digest 测试也写明「source errors must reach the caller regardless of gate state」。
	"domains/sessionsummary/message_source_v2.go:75::v2SessionBodiesBaseQuery::b19fb1b9":   "P1：turns 视图漏投影 origin_actor；per-turn-digest 开关的 fallback 路径，会话摘要输入读取必失败",
	"domains/sessionsummary/message_source_digest.go:89::sessionTurnDigestQuery::f6b0acc8": "P1：同上，per-turn-digest 开关的 digest 路径；开关开/关两条路都撞这一个列",

	// ── P1-2｜diagnostic_runs.route_key / routing_audit_log.reason 缺列 ──────
	// 证据闭合（三方一致）：基线 01-schema.sql:7955 的 diagnostic_runs 列清单无 route_key；
	// 真库 pg_attribute 实测同样没有；全仓仅有的两处 ALTER TABLE diagnostic_runs
	// （445 与 391）都只 ADD COLUMN created_at/updated_at，**没有任何迁移添加 route_key**。
	// route_key 仅存在于 390_routing_persistence_hardening.sql 的 routing_audit_log
	// ——**另一张表**，疑串表。
	//
	// 可达性（定 P1 而非 P1 候选的依据）：
	//   cmd/gateway/main.go:3539 routeincident.NewStore + :3545 NewObserver
	//   → telemetryClient.AddOnRequestLogPersisted(observer.AsHook())   **每条落库的请求日志**
	//   → Observer.Transition → writeAudit → persistRunInTx  （查 diagnostic_runs.route_key）
	//   → 失败后重试 maxRetries=4（共 5 次）后 o.failed++ 并只记 warning
	// admin 侧 NewRouteIncidentsHandler 无条件接线，DiagnosticRunsList /
	// AuditLogListByRun 同样失败。
	//
	// **加重的一条**：重试循环的注释写「Transient: lock conflict, transient deadlock…
	// the next persisted row will catch up」——但 `42703 undefined_column` 是**永久性**错误，
	// 每次重试与每条后续请求都必然同样失败。这套重试对它零收益，只是把热路径上的
	// 失败查询放大 5 倍。
	"domains/routeincident/action_infra.go:390::sql::98af2ee0": "P1：diagnostic_runs 无 route_key（基线/真库/迁移三方一致）；observer 热路径每条请求日志命中",
	"domains/routeincident/action_infra.go:479::sql::d83eead3": "P1：同上 persistRunInTx，observer 热路径",
	"domains/routeincident/action_infra.go:507::sql::93e0ec6f": "P1：同上 loadRunByIDInTx",
	"domains/routeincident/action_infra.go:529::sql::93e0ec6f": "P1：同上 loadRunByID",
	"domains/routeincident/action_infra.go:608::sql::6f536007": "P1：routing_audit_log 无 reason 列；admin 审计列表失败",
	"domains/routeincident/action_infra.go:666::sql::66e1c23e": "P1：同上；admin NewRouteIncidentsHandler 无条件接线",
	"domains/routeincident/evidence.go:149::sql::27a821dc":     "P1：同上 diagnostic_runs.route_key（RecordEvidenceExportAudit）",

	// ── P1-3｜tool_usage_stats 列名漂移：Go 用 tool_name/date，schema 是 tool_id/usage_date ──
	// 基线 01-schema.sql 与真库 pg_attribute **两个独立 SSOT 逐列一致**（id, tool_id,
	// tenant_id, usage_date, call_count, …，既无 tool_name 也无 date），全仓无任何改名迁移。
	// 而 domains/toolexecution/postgres_store.go 直接
	// INSERT INTO tool_usage_stats_hot (… tool_name, date, …) ON CONFLICT (tool_name, date)。
	// 活接线：cmd/gateway/tool_execution_integration.go:40 `te.NewPostgresStore(db, logger)`。
	"domains/toolexecution/postgres_store.go:307::q::37f54877":    "P1：tool_usage_stats* 无 tool_name/date（schema 为 tool_id/usage_date，两个 SSOT 一致）；cmd/gateway 有活接线",
	"domains/toolexecution/postgres_store.go:354::qHot::e9a8513a": "P1：同上 tool_usage_stats_hot 列名漂移",
	"domains/toolexecution/postgres_store.go:382::q::8ad940b4":    "P1：同上 tool_usage_stats* 列名漂移",

	// ── P1-4｜model_aliases.alias 列名错误：应为 raw_name ──
	// 基线 model_aliases 列：id, canonical_id, **raw_name**, quantization, surface,
	// status, notes, created_at, updated_at, client_profiles —— 没有 alias。
	// admin/logs.go:1249 另有注释佐证「model_aliases.raw_name is persisted lowercase」。
	"internal/reasoncap/pgsource.go:59::q::6d71f06d": "P1：model_aliases 无 alias 列（应为 raw_name），reasoncap 覆盖查询不可执行",

	// ── 设计内的模板占位符（假阳性）────────────────────────────────────────
	// `__mo_modality__` 不是列名，是等待替换的模板标记：该常量在启动时先探测
	// information_schema.columns 里有没有 model_offers.provider_modality，再据探测结果
	// 把占位符换成 moModalityWithColumn（真列）或兼容常量。
	// 见 admin/credential_models_dto.go 的 :76、:120、:138。
	// 按原样 PREPARE 当然失败——**它本来就不是一个会被原样执行的语句**。
	"admin/credential_models_dto.go:32::offerListSQLColumns::361f5ffa": "假阳性：`__mo_modality__` 是启动探测后替换的模板标记，不是列名（见 :76/:120/:138）",
}

// ── 未分诊 backlog（R79 续五 遗留；补了 FROM/拼接过滤后由 9 条收缩为 6 条）──────────────────────────────────
//
// 这 9 条**确认能采集到、确认不是「判错对象」的误报**（SQLite 目录与 SQL 片段/DDL
// 已在抽取阶段排除），但本轮没有逐条定性：缺列可能是「仓内漏了迁移」，也可能是
// 「真库比代码旧」，需要逐个查基线才能下结论。
//
// **登记它们而不是让门永久红**，理由是一个已经被验证过的失效形态：
// 一道长期红的门会让人习惯性忽略它，此后它连自己抓到了什么都不再有人看。
// 但登记不等于放过——下面加了**棘轮**：
//
//   - 新增一条无法规划的 SQL（未登记）→ 立刻红；
//   - 任一登记项开始能正常 PREPARE → 红（问题已修，白名单该收缩）；
//   - backlog 条数不等于 expectedUntriagedBacklog → 红（有人动了 backlog 而没同步改常量）。
//
// 换句话说：这道门今天绿，是因为已知的 9 条被记账了；它不会因为记账而变瞎。
const expectedUntriagedBacklog = 0

// untriagedPrepareBacklog 已清空：R79 续五的 9 条已逐条定性完毕。
//
//   - 3 条是抽取器判错对象（已修抽取器，随之从语料消失）：selectExecutionCols /
//     pgSourceColumns 是无 FROM 的列清单片段；internalPersonDaySQL 是 Go 字符串
//     拼接体，闭引号后紧跟 ` + var +`，正则只抓到第一段反引号内容。
//   - 1 条是设计内的模板占位符：admin/credential_models_dto.go::offerListSQLColumns
//     里的 `__mo_modality__` 由启动探测 model_offers.provider_modality 后替换成
//     moModalityWithColumn / 兼容常量（该文件 :76、:120、:138）。
//   - 1 条是 PREPARE 的推断限制：modelcatalog 的 $8 只在 CTE 内被引用，
//     无参数类型时 PG 无法推断（42P08），运行时驱动会传类型，非语句缺陷。
//   - 4 条是**真缺陷**，已升级进上方的定性登记表。
//
// 机制保留：将来再有无法定性的新条目，用这个表记账，并由
// expectedUntriagedBacklog 棘轮守住。
var untriagedPrepareBacklog = map[string]string{}

// TestData_GoSQLConstants_PrepareAgainstRealDB plans every untemplated SQL
// constant found in the repository against the real database.
//
// PREPARE plans without executing, so this stays read-only — which matters
// because this domain's primary database is under a read-only contract.
func TestData_GoSQLConstants_PrepareAgainstRealDB(t *testing.T) {
	conn, ctx := connectAuditDB(t)

	if len(untriagedPrepareBacklog) != expectedUntriagedBacklog {
		t.Fatalf("未分诊 backlog 有 %d 条，但 expectedUntriagedBacklog = %d。"+
			"有人增删了 backlog 条目却没同步这个常量——那说明 backlog 被动了，"+
			"请同步更新常量并在提交信息里说明这条是被分诊掉了还是新进来的",
			len(untriagedPrepareBacklog), expectedUntriagedBacklog)
	}
	for k, v := range untriagedPrepareBacklog {
		if _, dup := justifiedPrepareFailures[k]; dup {
			t.Fatalf("backlog 与已定性登记表键冲突：%s", k)
		}
		justifiedPrepareFailures[k] = v
	}

	cands := collectSQLCandidates(t)
	if len(cands) < 50 {
		t.Fatalf("only %d untemplated SQL constants collected from the repo; the corpus "+
			"shrank or the extractor broke (a sweep that quietly finds almost nothing is "+
			"not evidence of health)", len(cands))
	}
	t.Logf("仓内可原样 PREPARE 的 SQL 常量：%d 条", len(cands))

	var (
		red            []prepareFailure
		unverifiable   []prepareFailure
		planned        int
		prepareCounter int
	)

	// Every registry key must still correspond to a collected candidate.
	//
	// The self-shrinking check below only fires when a registered constant
	// PREPAREs cleanly — which covers "the bug got fixed", but NOT "the
	// extractor stopped collecting it". After the FROM/concat filters were
	// added, four backlog entries became unreachable while the gate stayed
	// green, silently carrying justifications for statements the corpus no
	// longer contains. A registry that can hold unreachable keys is a registry
	// that hides its own backlog.
	collected := make(map[string]bool, len(cands))
	for _, c := range cands {
		collected[c.key()] = true
	}
	for key := range justifiedPrepareFailures {
		if !collected[key] {
			t.Errorf("登记项 %q 已不在本轮语料里——要么它已修好/不再被抽取（请删除），"+
				"要么抽取器漏了它（那是缺陷，请修抽取器）", key)
		}
	}

	for _, c := range cands {
		key := c.key()
		prepareCounter++
		stmtName := fmt.Sprintf("d07_prepare_%d", prepareCounter)

		// Each PREPARE runs in its own implicit transaction: a failing statement
		// would otherwise abort a shared one and hide every later result.
		if _, err := conn.Exec(ctx, "PREPARE "+stmtName+" AS "+c.sql); err != nil {
			f := classifyPrepareError(c, err)
			if f.verdict == "red" {
				if reason, ok := justifiedPrepareFailures[key]; ok && reason != "" {
					t.Logf("已登记：%s — %s", key, reason)
				} else {
					red = append(red, f)
				}
			} else {
				unverifiable = append(unverifiable, f)
			}
			continue
		}
		planned++
		// Reported, never mutated: deleting the entry here would strip the
		// justification from every OTHER constant that shares this key, and the
		// mutation would happen mid-iteration.
		if _, ok := justifiedPrepareFailures[key]; ok {
			t.Errorf("白名单条目已失效：%s 现在能正常 PREPARE（说明问题已修，或登记写错了对象）——请删除", key)
		}
		if _, err := conn.Exec(ctx, "DEALLOCATE "+stmtName); err != nil {
			t.Logf("DEALLOCATE %s: %v", stmtName, err)
		}
	}

	t.Logf("PREPARE 普查结果：%d 条规划成功；%d 条判定为缺陷（判红）；%d 条因本机缺对象无法验证（不判红）",
		planned, len(red), len(unverifiable))
	if len(unverifiable) > 0 {
		sample := unverifiable
		if len(sample) > 5 {
			sample = sample[:5]
		}
		for _, u := range sample {
			t.Logf("  本机无法验证（多半是迁移未应用）：%s — %s %s",
				u.cand.key(), u.code, firstLine(u.msg))
		}
		if len(unverifiable) > len(sample) {
			t.Logf("  …另有 %d 条同类", len(unverifiable)-len(sample))
		}
	}

	for _, r := range red {
		// Print the full key: it is what justifiedPrepareFailures is keyed by, so
		// triaging this item should be a copy-paste, not a re-derivation.
		t.Errorf("SQL 常量在真库上无法规划（%s）——这条 SQL 在代码里存在，但执行即报错：\n\t键：%s\n\t%s",
			r.code, r.cand.key(), firstLine(r.msg))
	}
}

// classifyPrepareError splits "the code is wrong" from "this database is older
// than the code". See the file header for why the two are not the same thing.
func classifyPrepareError(c sqlCandidate, err error) prepareFailure {
	code, msg := pgErrCode(err)
	f := prepareFailure{cand: c, code: code, msg: msg}
	switch code {
	case "42P01", // undefined_table
		"42704", // undefined_object
		"3F000", // invalid_schema_name
		"42501": // insufficient_privilege — the audit role is deliberately read-only
		f.verdict = "unverifiable"
		f.reasonIs = "本机库比代码旧或权限受限，与代码对错无关"
	case "42P08":
		// indeterminate_datatype: PREPARE carries no parameter types, so a
		// parameter used only inside a CTE cannot be inferred. The driver sends
		// typed values at runtime, so this is a limitation of the probe, not a
		// defect in the statement (modelcatalog's upsert: `could not determine
		// data type of parameter $8`).
		f.verdict = "unverifiable"
		f.reasonIs = "PREPARE 无参数类型的推断限制（42P08），运行时驱动会传类型，非语句缺陷"
	case "42703": // undefined_column — genuinely missing, environment-independent
		f.verdict = "red"
		f.reasonIs = "列不存在：仓内无任何迁移创建/投影它，判红"
	case "42P02", // ambiguous_column
		"42803", // grouping error
		"42P10", // invalid_column_reference
		"42725": // ambiguous_function
		f.verdict = "red"
		f.reasonIs = "SQL 语义错误，与库版本无关"
	default:
		// fail-closed: an unanticipated error shape is not evidence of health.
		f.verdict = "red"
		f.reasonIs = "未预料的错误形状，按 fail-closed 判红"
	}
	return f
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " …"
	}
	return s
}

// pgErrCode pulls the SQLSTATE out of a pgx error so classification keys on
// the server's own verdict rather than on substring-matching English messages.
func pgErrCode(err error) (code, msg string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, pgErr.Message
	}
	return "", err.Error()
}
