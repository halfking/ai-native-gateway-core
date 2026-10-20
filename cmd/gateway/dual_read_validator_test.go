package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── 退役态的假查询器 ────────────────────────────────────────────────────
//
// retiredFamilyQuerier 是一个**只记账**的 dualReadQuerier：它不查库，
// 只记下「有人试图发查询」这件事。
//
// 为什么需要它：v1 族退役后，正确的行为是**一条查询都不发**
// （详见 TestDualReadValidator_RetiredFamilyIssuesNoQuery）。
// 而「0 条」这个断言有个致命的形态问题：**判据没跑成的时候它同样绿**。
// ⇒ 必须配一条**正控制**：同一个计数器必须看得见真发出去的查询。
//
// ⚠ 这个类型定义在**无 build tag** 的文件里（就是本文件），因为三道门都要用它：
// 本文件（无 tag）、s4_gate_coverage_faces_test.go（无 tag）、
// dual_read_both_faces_test.go（!integration）。
// 无 tag 的定义在两个形状里都看得见；反过来（定义在带 tag 的文件里），
// 另一个形状就会「符号不存在」——同款坑见 sql_identifier_helpers_test.go 的注记。
type retiredFamilyQuerier struct {
	queries []string
}

func (q *retiredFamilyQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.queries = append(q.queries, sql)
	return nil, fmt.Errorf("retiredFamilyQuerier: 退役态下不应发查询：%s", sql)
}

func (q *retiredFamilyQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	q.queries = append(q.queries, sql)
	return retiredFamilyErrorRow{err: fmt.Errorf("retiredFamilyQuerier: 退役态下不应发查询：%s", sql)}
}

// retiredFamilyErrorRow 是一个「一 Scan 就报错」的 pgx.Row：
// 万一哪天有人在这条路上加了查询，它会**响亮地失败**而不是静默地返回零行。
type retiredFamilyErrorRow struct{ err error }

func (r retiredFamilyErrorRow) Scan(dest ...any) error { return r.err }

func TestDualReadValidator_Constructor(t *testing.T) {
	v := NewDualReadValidator(nil) // nil pool must not panic
	if v == nil {
		t.Fatal("nil validator")
	}
	// Verify the Compare method exists with the right signature via reflection.
	// NumIn on a bound method value excludes the receiver, so we expect
	// ctx + tenant + session + lastN = 4 inputs.
	rv := reflect.ValueOf(v)
	compare := rv.MethodByName("Compare")
	if !compare.IsValid() {
		t.Fatal("Compare method not set")
	}
	mt := compare.Type()
	if mt.NumIn() != 4 || mt.NumOut() != 2 {
		t.Fatalf("Compare signature mismatch: in=%d out=%d", mt.NumIn(), mt.NumOut())
	}
}

func TestDualReadValidator_NilPoolCompare(t *testing.T) {
	v := NewDualReadValidator(nil)
	_, err := v.Compare(context.Background(), "t1", "s1", 10)
	if err == nil {
		t.Fatal("expected error when pool is nil, got nil")
	}
	if err.Error() == "" {
		t.Fatalf("expected non-empty error message, got %v", err)
	}
}

// TestDualReadValidator_SummarizeSignature 钉住 population 级漂移度量的入口。
// Compare/CompareDetail 都是单会话的，必须先知道查哪个 session——这正是
// 2026-09-30 审计里镜像漏写长期没被发现的原因。Summarize 是全量视图，
// 少了它「S4 能不能开」就只能靠人临时写 SQL。
func TestDualReadValidator_SummarizeSignature(t *testing.T) {
	v := NewDualReadValidator(nil)
	rv := reflect.ValueOf(v)
	sum := rv.MethodByName("Summarize")
	if !sum.IsValid() {
		t.Fatal("Summarize method not set")
	}
	mt := sum.Type()
	// ctx + tenant + windowHours = 3 in, (*MirrorDriftSummary, error) = 2 out.
	if mt.NumIn() != 3 || mt.NumOut() != 2 {
		t.Fatalf("Summarize signature mismatch: in=%d out=%d", mt.NumIn(), mt.NumOut())
	}
}

func TestDualReadValidator_NilPoolSummarize(t *testing.T) {
	v := NewDualReadValidator(nil)
	if _, err := v.Summarize(context.Background(), "", 168); err == nil {
		t.Fatal("nil pool must error rather than report zero drift; a false zero would read as S4-ready")
	}
}

// TestDualReadValidator_RetiredFamilyIssuesNoQuery 钉住退役态的**行为**：
// 对账路径一条查询都不发，且如实报「未测量」。
//
// # 为什么这道门存在
//
// 迁移 890 把 v1 族整体 DROP 之后，对账（v1 ↔ 会话族）在**语义上**就没有对象了：
// 差集需要「另一侧」，而另一侧已经不存在。剩下的风险有两个方向：
//
//	(a) **还在量**：把「没量」报成「量了 0」——`zero_drift=true`、
//	    `v1_coverage_pp=0` 读起来像「两侧一致、窗口干净」。
//	    这正是本仓反复吃亏的「真空为绿」，只是这次发生在**退役态**。
//	(b) **还在查**：任何提到 v1 族的语句都是 42P01（整条语句失败，不是那条腿失败）。
//
// ⇒ (b) 由「零查询」这条断言兜底（不发查询就不会 42P01）；
// (a) 由下面那些字段断言兜底（必须带「未测量」的标记）。
//
// ★ 「0 条查询」这个断言本身有个致命形态：**判据没跑成的时候它同样绿**。
// 末尾有一条**正控制**：同一个计数器必须看得见真发出去的查询。
func TestDualReadValidator_RetiredFamilyIssuesNoQuery(t *testing.T) {
	q := &retiredFamilyQuerier{}
	v := NewDualReadValidatorOn(q)
	ctx := context.Background()
	now := time.Now()

	sum, err := v.SummarizeFrom(ctx, "", now, 168)
	if err != nil {
		t.Fatalf("SummarizeFrom: %v", err)
	}
	if len(q.queries) != 0 {
		t.Errorf("退役态的对账发出了 %d 条查询（%v）—— v1 族已被 DROP，任何提到它的语句都是 "+
			"42P01；而「没有对账可做」的正确实现是**一条都不发**", len(q.queries), q.queries)
	}
	// 「未测量」的标记必须齐全：少一个，读者就会把 0 读成「量了 0」。
	if !sum.V1Retired {
		t.Error("MirrorDriftSummary.V1Retired 必须为 true —— 它是「本次运行没有做任何对账」的显式标记")
	}
	if sum.V1CoveragePP != -1 {
		t.Errorf("V1CoveragePP = %v，期望 -1 —— -1 是「**未测量**」的哨兵值；"+
			"0 会被读成「测到了 0%% 覆盖」", sum.V1CoveragePP)
	}
	if sum.S4Ready {
		t.Error("S4Ready 必须为 false —— 退役态下没有第二侧可比，恒 false 的理由是「没有东西可比」")
	}
	if !sum.S4GateVoid {
		t.Error("S4GateVoid 必须为 true —— 「这次没法评估漂移」与「评估了、有漂移」必须可区分")
	}
	if sum.S4GateVoidReason != s4GateReasonV1FamilyRetired {
		t.Errorf("S4GateVoidReason = %q，期望 %q —— 复用前三条（证据不足）会把读者引向"+
			"「把 v1 写打开」这个退役后有害的 remedy", sum.S4GateVoidReason, s4GateReasonV1FamilyRetired)
	}

	detail, err := v.CompareDetail(ctx, "", "sess-retired", 10)
	if err != nil {
		t.Fatalf("CompareDetail: %v", err)
	}
	if len(q.queries) != 0 {
		t.Errorf("CompareDetail 发出了 %d 条查询（%v）—— 同样不该发", len(q.queries), q.queries)
	}
	if !detail.V1Retired {
		t.Error("DualReadDetail.V1Retired 必须为 true")
	}
	if detail.ZeroDriftEvaluable {
		t.Error("ZeroDriftEvaluable 必须为 false —— 含义是「本次运行没有验证任何漂移」")
	}
	if detail.ZeroDrift {
		t.Error("ZeroDrift 必须为 false —— 没有证据说漂移为零；「没做对账」与「对账结果为零」必须长得不一样")
	}

	// Compare 走的是不走 HTTP 的那条路：它必须**响亮地失败**，
	// 而不是返回一份「零差异」的 diff —— 零差异在这个语义下是**假的**。
	diff, err := v.Compare(ctx, "", "sess-retired", 10)
	if err == nil {
		t.Errorf("Compare 必须返回错误，实际返回 nil（diff=%+v）—— 「零差异」会被读成"+
			"「两侧一致」，而事实是「只有一侧，而且它没被读过」", diff)
	}
	if len(q.queries) != 0 {
		t.Errorf("Compare 发出了 %d 条查询（%v）—— 退役后没有对账可做", len(q.queries), q.queries)
	}

	// ★ 正控制：计数器必须看得见查询，否则上面三条「0 条」可能只是判据没跑成。
	before := len(q.queries)
	if _, err := q.Query(ctx, "SELECT 1"); err == nil {
		t.Error("正控制失效：retiredFamilyQuerier 对任何查询都应当报错")
	}
	if len(q.queries) != before+1 {
		t.Errorf("正控制失效：发了一条查询后计数从 %d 变成 %d —— 「0 条」这个断言没有牙",
			before, len(q.queries))
	}
}

// TestMirrorDriftClassSQLCoversThreeBuckets 守住「按设计排除」与「真漏写」的区分。
//
// 这个区分是 S4 判据的核心：把 internal_loopback / non_terminal 也算成漏写，
// 会让 93% 的噪声淹没真正的问题行；反过来把 genuine_loss 混进「按设计排除」，
// 就会在数据仍然丢的情况下判定 S4 可开。
func TestMirrorDriftClassSQLCoversThreeBuckets(t *testing.T) {
	// 两个按设计排除臂必须逐条对上 Go 侧的判据来源。2026-09-30 实测踩中：
	// 初版只按 work_type 认内部回环，而 IsInternalAutoEntry 认的是
	// is_auto_request + request_type / origin_actor / task_type。work_type
	// 未打戳的生成器行因此被误判 genuine_loss，s4_ready 永远为假。
	for _, want := range []string{
		"is_auto_request", // IsInternalAutoEntry 首道门（NULL 不算内部回环）
		"origin_actor",    // auto-title/auto-summary/session-summary
		"request_type",    // title_gen / summary
		"task_type",       // taskless auto entry 兜底
		"internal_loopback",
		"non_terminal",
		"genuine_loss",
	} {
		if !strings.Contains(mirrorDriftClassSQL, want) {
			t.Fatalf("class SQL missing %q", want)
		}
	}
	// work_type 不是 Go 侧的排除判据。把它当键会在
	// 「is_auto_request=TRUE + task_type 非空 + work_type=session_title」
	// （业务轮次，hook 照常镜像）上吞掉真漏写。
	if strings.Contains(mirrorDriftClassSQL, "work_type") {
		t.Fatal("class SQL must not key internal_loopback on work_type — the Go gate never reads it, so it would mask real loss")
	}
	// 内部回环臂必须被 is_auto_request 显式门控：NULL 不能算内部回环。
	if !strings.Contains(mirrorDriftClassSQL, "COALESCE(rl.is_auto_request, false)") {
		t.Fatal("internal_loopback arm must be gated on COALESCE(is_auto_request,false); a NULL is_auto_request is not an internal loopback")
	}
	// ★ 2026-10-20（迁移 890，v1 族整体退役）：本函数原先还有**三段**关于
	// 「对账范围 SQL」（mirrorDriftScopeSQL）的断言：
	//
	//	- 范围必须反连接会话族的**两条腿**（NOT EXISTS × 2）；
	//	- 范围必须投影分类表达式读到的每一列（is_auto_request / origin_actor / ...）。
	//
	// 它们随**被测对象**一起退役：那条 SQL 已从 dual_read_validator.go 删除
	// （v1 族被 DROP ⇒ 任何提到它的语句 42P01），而「范围」这个集合在退役后
	// **根本不存在**——没有第二侧就没有漂移可比。
	//
	// ⇒ 断言删除，理由是「被测对象没了」，不是「断言不重要了」。
	// 删掉的风险由另外两道门接手：
	//
	//	- 「退役后不许再发查询、不许再报成已测量」：
	//	  TestDualReadValidator_RetiredFamilyIssuesNoQuery（本文件）；
	//	- 「对账这条路上不许再出现 v1 族的关系名」：
	//	  dual_read_both_faces_test.go 的 TestDriftReconciliationMentionsNoRetiredV1Relation。
	//
	// 分类表达式本身**没有退役**：db.MirrorDriftClassSQL 仍在 admin 侧使用，
	// 上面的断言继续守它。
}

// TestMirrorDriftClassSQLHandlesNullWorkType 守住一个会把「问题已修复」伪装出来的
// SQL 三值逻辑陷阱（2026-09-30 审计实测踩中两次）。
//
// genuine_loss 的行 work_type 全是 NULL。若分类写成
// `NOT (work_type IN ('session_title','session_summary'))`：
// NULL IN (...) → NULL，NOT NULL → NULL，整行被 WHERE 丢弃，
// 查询稳定返回 genuine_loss = 0，看上去像漏写已修完。
//
// 正确形态是 CASE WHEN … ELSE 'genuine_loss'：ELSE 分支兜住 NULL。
// 判据用「ELSE 兜底存在」而不是「字符串里没有 NOT (」——后者会因为
// 排版差异而给出假绿。
func TestMirrorDriftClassSQLHandlesNullWorkType(t *testing.T) {
	if !strings.Contains(mirrorDriftClassSQL, "ELSE 'genuine_loss'") {
		t.Fatal("class SQL must fall through to genuine_loss via ELSE so NULL work_type is still counted as loss")
	}
	// 两个按设计排除的分支都必须在 ELSE 之前，且用 IN/= 形式（它们对 NULL
	// 求值为 NULL 从而落入 ELSE，正是我们要的行为）。
	loopbackAt := strings.Index(mirrorDriftClassSQL, "THEN 'internal_loopback'")
	nonTerminalAt := strings.Index(mirrorDriftClassSQL, "THEN 'non_terminal'")
	elseAt := strings.Index(mirrorDriftClassSQL, "ELSE 'genuine_loss'")
	if loopbackAt < 0 || nonTerminalAt < 0 || elseAt < 0 {
		t.Fatalf("class SQL missing a branch: loopback=%d non_terminal=%d else=%d", loopbackAt, nonTerminalAt, elseAt)
	}
	if !(loopbackAt < nonTerminalAt && nonTerminalAt < elseAt) {
		t.Fatalf("branch order wrong: by-design exclusions must precede the genuine_loss ELSE (%d, %d, %d)", loopbackAt, nonTerminalAt, elseAt)
	}
	// 显式禁止把排除条件写成可被 NULL 吞掉的形式。
	if strings.Contains(mirrorDriftClassSQL, "NOT (") {
		t.Fatal("class SQL must not use NOT (... IN ...) — NULL work_type would be silently dropped")
	}
}

// TestDualReadValidator_NilStaysNil pins the typed-nil guard.
//
// `db` is an interface, so assigning a nil *pgxpool.Pool to it yields a
// **non-nil interface holding a nil pointer** — `v.db == nil` is then false and
// Summarize's "not configured" guard stops working, handing a nil pool to a
// method call. Widening the field to an interface introduced exactly that
// panic, and only the pre-existing nil-pool test caught it.
//
// Both constructors are covered because NewDualReadValidatorOn takes the
// interface directly and can be handed a typed nil just as easily.
func TestDualReadValidator_NilStaysNil(t *testing.T) {
	fromPool := NewDualReadValidator(nil)
	if fromPool.db != nil {
		t.Errorf("NewDualReadValidator(nil) 装出了非 nil 的接口（%T），Summarize 的 "+
			"未配置守卫会失效", fromPool.db)
	}
	var typedNil *pgxpool.Pool
	fromIface := NewDualReadValidatorOn(typedNil)
	if fromIface.db != nil {
		t.Errorf("NewDualReadValidatorOn(typed nil) 同上（%T）", fromIface.db)
	}
	// And the observable consequence: Summarize must return the error, not panic.
	if _, err := fromPool.Summarize(context.Background(), "", 24); err == nil {
		t.Error("nil validator 的 Summarize 必须返回错误而不是崩溃")
	}
}
