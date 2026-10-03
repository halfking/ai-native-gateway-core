// mirror_drift_class_parity_test.go — 把「SQL 分类器与 Go 分类器必须一致」这道
// 判定放到**能跑**的地方（审计 §9.74.8 记的缺口，2026-10-03 补）。
//
// # 为什么要在这里再写一道（不是重复）
//
// 原有的 parity 门在 `cmd/gateway/dual_read_class_parity_test.go`，它是
// **integration 级**的（`TEST_PG_URL`），而 `db.MirrorDriftClassSQL` 本轮
// 改用了 `internal/internaltraffic` 渲染的谓词。那一轮 `cmd/gateway` 因为
// 一次他人在途的 cherry-pick 冲突而**无法编译**，于是：
//
//	SQL 侧改了、Go 侧改了，而唯一能判定「两者还一致」的那道门**跑不了**。
//
// 我当时用三道可运行的断言（字节等同 / 臂表 / 残留字面量扫描）替代它，并在
// §9.74.8 里明确写了「**替代不等于等价**」。本文件把那道缺口补上：
// parity 判定搬进 `db` 包——`db` 不依赖 `domains/dispatch`，因此不受那次冲突影响；
// 而 `telemetry` 的依赖闭包不含 `db`（`go list -deps` 实测），所以
// `db` 的**测试**可以 import `telemetry`，不产生循环。
//
// 两道断言，缺一不可：
//
//  1. 字节等同（**恒跑，不需要 DB**）：SQL 的 internal_loopback 臂必须与
//     `internaltraffic.SQLInternalLoopbackPredicate("rl")` 逐字相同。
//     这道把「SQL 侧的字面量只能来自 SSOT」变成可执行的，而不是靠注释提醒。
//  2. 真实执行 parity（要 TEST_PG_URL）：把同一批夹具分别喂给 SQL 与
//     Go 分类器，逐格比对分类结果。**夹具用 FROM (VALUES ...)，不需要任何 schema**，
//     所以它可以在任何 PG 上跑。
//
// ★ 第 2 道的夹具里刻意包含 `goal-audit` / `goal-continue` 两格：它们是
// §9.74.5 记的那个洞的直接回归用例（把 Go 的 actor 臂从 `IsGeneratorActor`
// 换成 `IsSyntheticActor` 时，函数性质测试全绿而语义已变）。
// **这两格不能删**——它们是本文件存在的主要理由之一。
package db

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/internal/internaltraffic"
)

// isAutoRequestGate 是 SQL 侧 internal_loopback 臂的第一道门。
//
// 单独提成常量是因为它有独立语义：`NULL` 的 is_auto_request **不是**内部回环
// （Go 侧 `entry.IsAutoRequest == nil` 直接返回 false），而 SQL 侧若写成
// `COALESCE(rl.is_auto_request, true)` 就会把 NULL 算进去。
const isAutoRequestGate = "COALESCE(rl.is_auto_request, false)"

// TestMirrorDriftClassSQLInternalLoopbackArmIsByteIdenticalToTheSSOT —— 恒跑这道。
//
// 判据是**逐字相同**而不是「包含那几个名字」：包含只能证明名字在，证明不了
// 谓词的形状（三条臂的连接方式、TRIM/COALESCE 的嵌套、括号层级）没被改。
// 而形状被改而名字没改，正是那种「所有以名字为键的门都绿着」的变化。
func TestMirrorDriftClassSQLInternalLoopbackArmIsByteIdenticalToTheSSOT(t *testing.T) {
	// ★ 比较前**归一化空白**，而不是逐字比。
	//
	// 这一版第一稿是逐字比较，当场红：实际 SQL 把这条臂摊成多行、每行有自己
	// 的缩进（`AND (   TRIM(...` 换行后再 `OR TRIM(rl.origin_actor...`），
	// 而 SSOT 渲染成一行 ⇒ 语义逐字相同、文本不同。
	// 逐字比较在这里**测不到任何东西**，只测到排版。
	//
	// 归一化后仍能抓住真正要抓的东西：括号层级、三条臂的顺序与连接方式、
	// TRIM/COALESCE 的嵌套、以及**字面量列表的内容与次序**。
	// 排版归本文件不关心（`cmd/gateway` 既有门也没有关心）。
	got := normalizeSQLWhitespace(MirrorDriftClassSQL)
	want := normalizeSQLWhitespace(isAutoRequestGate + " AND " +
		internaltraffic.SQLInternalLoopbackPredicate("rl"))
	if !strings.Contains(got, want) {
		t.Errorf("MirrorDriftClassSQL 的 internal_loopback 臂与 SSOT 渲染结果不同（已归一化空白）。\n"+
			"  期望片段：%s\n  归一化后实际：%s\n"+
			"  本轮重构的承诺是「SQL 侧的字面量只能来自 internal/internaltraffic」。\n"+
			"  若你确要改这条臂的形状，请先改 SSOT 的 SQLInternalLoopbackPredicate ——\n"+
			"  在 db 里手写一份就是本轮要消灭的第四份拼写。", want, got)
	}

	// 集合等值：origin_actor 的 IN 列表必须**恰好**是 SSOT 的那三个。
	//
	// 与上面那道正交：上面钉形状与次序，这道钉**成员**——
	// 「多加一个 actor」和「少一个 actor」都会被这道抓住，而它们都不会让
	// 上面那道变红（形状完全没变）。
	//
	// ⚠ 第一稿把这道写成了「SQL 里有 SSOT 之外的生成器名」，方向是反的：
	// 它遍历 SSOT 的三个名字、发现 SQL 里有、然后报「不在 SSOT 里」——
	// 自相矛盾，且**真正多余的那个名字反而检查不到**。
	actorListRE := regexp.MustCompile(`TRIM\(COALESCE\((?:\w+\.)?origin_actor, ''\)\) IN \(([^)]*)\)`)
	m := actorListRE.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("归一化后的 SQL 里找不到 `TRIM(COALESCE(rl.origin_actor, '')) IN (...)`。\n"+
			"  解析不出来却继续跑 ⇒ 集合检查形同虚设（同「解析失败伪装成通过」那一族）。\n"+
			"  当前归一化结果：%s", got)
	}
	gotActors := parseSQLStringList(m[1])
	wantActors := internaltraffic.GeneratorActors()
	if !equalStringSets(gotActors, wantActors) {
		t.Errorf("origin_actor 的 IN 列表与 SSOT 的 GeneratorActors 不一致。\n"+
			"  SQL：%v\n  SSOT：%v\n"+
			"  多一个 ⇒ SQL 侧把 SSOT 不认的 actor 当内部回环；\n"+
			"  少一个 ⇒ SQL 侧把 SSOT 认的 actor 放过去，session_turns 镜像会多出生成器行。\n"+
			"  两种都让 SQL 与 Go 分类器对同一行给出不同结果。", gotActors, wantActors)
	}
	if len(gotActors) == 0 {
		t.Error("origin_actor 的 IN 列表解析为空（自指断言：解析器坏了）。")
	}

	// work_type 不得成为排除键（沿用 cmd/gateway 既有门的判据，理由见其注释：
	// Go 侧判据从不读 work_type，按它判会吞掉真漏写）。
	if strings.Contains(MirrorDriftClassSQL, "work_type") {
		t.Error("MirrorDriftClassSQL 不得引用 work_type：Go 侧的排除判据从不读它，" +
			"按它排除会掩盖真漏写（work_type 未打戳的生成器行会被误判 genuine_loss，s4_ready 永远为假）。")
	}
}

// normalizeSQLWhitespace 把所有连续空白折叠成单个空格并去掉首尾空白。
//
// 只折叠空白，不动任何其他字符：反斜杠转义、引号、括号、逗号全部原样保留。
var sqlWhitespaceRE = regexp.MustCompile(`\s+`)

func normalizeSQLWhitespace(s string) string {
	return strings.TrimSpace(sqlWhitespaceRE.ReplaceAllString(s, " "))
}

func parseSQLStringList(list string) []string {
	items := strings.Split(list, ",")
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, strings.Trim(strings.TrimSpace(it), "'"))
	}
	return out
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, x := range a {
		seen[x]++
	}
	for _, y := range b {
		seen[y]--
		if seen[y] < 0 {
			return false
		}
	}
	return true
}

func sptr(s string) *string { return &s }

type mirrorDriftFixture struct {
	name        string
	isAuto      *bool
	originActor *string
	requestType *string
	taskType    *string
	workType    *string
	success     bool
	requestStat *string
	errorKind   *string
	// wantLoopback 是**独立写下的**期望值。测试会先核对 Go 侧与它一致，
	// 再核对 SQL 侧与 Go 侧一致 —— 两段不一致都会红，且红因不同
	// （夹具腐化 vs 两侧分歧）。
	wantLoopback bool
}

// TestMirrorDriftClassSQLMatchesIsInternalAutoEntry —— 真实执行那道。
//
// ⚠ **skip 不构成证据**（与 cmd/gateway 那道门同一句自述）：没有 TEST_PG_URL 时
// 本道报 skipped，不许被读成「parity 已验证」。
func TestMirrorDriftClassSQLMatchesIsInternalAutoEntry(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset —— skipping does NOT constitute evidence for the SQL/Go parity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	autoTrue, autoFalse := true, false
	cases := []mirrorDriftFixture{
		{name: "生成器 actor + task_type 非空", isAuto: &autoTrue,
			originActor: sptr("auto-title-generator"), requestType: sptr("main"), taskType: sptr("code"),
			success: true, requestStat: sptr("success"), wantLoopback: true},
		{name: "生成器 actor = session-summary", isAuto: &autoTrue,
			originActor: sptr("session-summary"), requestType: sptr("main"), taskType: sptr("code"),
			success: true, requestStat: sptr("success"), wantLoopback: true},
		{name: "request_type 臂", isAuto: &autoTrue,
			requestType: sptr("title_gen"), originActor: nil, taskType: sptr("code"),
			success: true, requestStat: sptr("success"), wantLoopback: true},
		{name: "taskless 兜底：actor 与 task_type 都空", isAuto: &autoTrue,
			originActor: nil, requestType: sptr("main"), taskType: nil,
			success: true, requestStat: sptr("success"), wantLoopback: true},
		{name: "taskless 兜底：task_type 空串", isAuto: &autoTrue,
			originActor: nil, requestType: sptr("main"), taskType: sptr("  "),
			success: true, requestStat: sptr("success"), wantLoopback: true},

		// ★★ 下面两格是 §9.74.5 那个洞的直接回归用例，**不能删**。
		// 当时把 Go 的 actor 臂从 IsGeneratorActor 换成 IsSyntheticActor，
		// 函数性质测试全绿，而 `goal-` 影子轮次已被算成内部回环。
		{name: "★ 回归：goal- 影子轮次**不是**内部回环（goal-audit）", isAuto: &autoTrue,
			originActor: sptr("goal-audit"), requestType: sptr("main"), taskType: sptr("code"),
			success: true, requestStat: sptr("success"), wantLoopback: false},
		{name: "★ 回归：goal- 影子轮次**不是**内部回环（goal-continue）", isAuto: &autoTrue,
			originActor: sptr("goal-continue"), requestType: sptr("main"), taskType: sptr("code"),
			success: true, requestStat: sptr("success"), wantLoopback: false},

		// ★ 这一格钉住「非 nil 空串 ≠ nil」。Go 侧写的是
		// `if entry.OriginActor != nil { switch TrimSpace(...) }`，
		// 而 SQL 侧写的是 `TRIM(COALESCE(col,'')) IN (...)`。
		// 两边对「非 nil 但空串」的路径不同：Go 会继续往下走，
		// SQL 直接判不在集合里。**结果**一致（都不是那条臂命中），
		// 但正因为结果一致，这一格最容易被"顺手清理"掉条件而无人察觉。
		{name: "★ 非 nil 但空串的 actor：不算该臂，落到 taskless 判定", isAuto: &autoTrue,
			originActor: sptr(""), requestType: sptr("main"), taskType: sptr(""),
			success: true, requestStat: sptr("success"), wantLoopback: true},

		{name: "is_auto_request 为 NULL ⇒ 不是内部回环", isAuto: nil,
			originActor: sptr("auto-title-generator"), requestType: sptr("main"), taskType: sptr("code"),
			success: true, requestStat: sptr("success"), wantLoopback: false},
		{name: "is_auto_request 为 false，即使 actor 命中", isAuto: &autoFalse,
			originActor: sptr("auto-title-generator"), requestType: sptr("main"), taskType: sptr("code"),
			success: true, requestStat: sptr("success"), wantLoopback: false},
		{name: "work_type 已打戳的业务轮次：不是内部回环", isAuto: &autoTrue,
			originActor: nil, requestType: sptr("main"), taskType: sptr("code"), workType: sptr("session_title"),
			success: true, requestStat: sptr("success"), wantLoopback: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := &telemetry.RequestLogEntry{
				IsAutoRequest: tc.isAuto,
				OriginActor:   tc.originActor,
				RequestType:   tc.requestType,
				TaskType:      tc.taskType,
				Success:       tc.success,
				RequestStatus: tc.requestStat,
				ErrorKind:     tc.errorKind,
			}
			goInternal := telemetry.IsInternalAutoEntry(entry)
			if goInternal != tc.wantLoopback {
				t.Fatalf("夹具腐化：Go 侧 IsInternalAutoEntry=%v，夹具声明 %v。\n"+
					"  Go 侧判据变了 ⇒ 请重读 internaltraffic.ClassifyInternalLoopback 的注释后刷新本表，\n"+
					"  **不要**直接把 wantLoopback 改成 Go 的输出 —— 那会让本道门变成自证。",
					goInternal, tc.wantLoopback)
			}

			var got string
			// FROM (VALUES ...) ⇒ 不需要任何表，所以这道门在任意 PG 上都能跑。
			err := pool.QueryRow(ctx, `
SELECT `+MirrorDriftClassSQL+`
FROM (VALUES
    ($1::boolean, $2::text, $3::text, $4::text, $5::text, $6::boolean, $7::text, $8::text)
) AS rl(is_auto_request, origin_actor, request_type, task_type, work_type,
        success, request_status, error_kind)`,
				tc.isAuto, tc.originActor, tc.requestType, tc.taskType, tc.workType,
				tc.success, tc.requestStat, tc.errorKind,
			).Scan(&got)
			if err != nil {
				t.Fatalf("class SQL 执行失败: %v", err)
			}
			sqlInternal := got == "internal_loopback"
			if sqlInternal != goInternal {
				t.Errorf("两侧分歧：SQL 给出 %q（internal=%v）而 Go 侧 IsInternalAutoEntry=%v\n"+
					"  这里的不一致要么掩盖真漏写、要么把安全的切换判成不安全（S4 停写判据依赖它）。\n"+
					"  常见成因：某一侧的字面量没从 internal/internaltraffic 取。", got, sqlInternal, goInternal)
			}
		})
	}
}
