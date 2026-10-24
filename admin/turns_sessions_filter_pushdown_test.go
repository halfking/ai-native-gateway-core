package admin

// turns_sessions_filter_pushdown_test.go —— R90 的契约门：
// `?api_key_id=` / `?model=` / `?provider=` / `?status_code=` 四条会话级过滤
// 从「视图 ⋈ 113 列全量 source」改成 **hot ∪ 父表两腿 EXISTS** 之后，
// 形状与口径分别由谁钉住。
//
// # 一句话背景（★ 全部为 2026-10-24 生产只读实测，结论可复算）
//
// 【症状】GET /api/admin/turns/sessions 带上述任一过滤参数时
//
//	生产恒 500@30s（角色级 statement_timeout=30s ⇒ SQLSTATE 57014 ⇒ 网关
//	「rows.Err after iteration」）。不带过滤的参数是 0.034 s。
//
// 【根因 ①】model/provider/status_code 三条过滤读的是
//	public.session_turns_with_current_month —— ★ 它的 hot 腿带
//	`NOT EXISTS (SELECT 1 FROM session_turns archived WHERE …)` 反连接
//	⇒ 外层**每一行**都要在 hot 上跑一次 Nested Loop Anti Join
//	⇒ EXPLAIN ANALYZE：**36,415 ms**。
//
// 【根因 ②】api_key_id 那条还额外 JOIN 了 113 列的**全量 source**
//	（db.SessionFamilyTurnsSourceSQL()，hot ∪ 父表全部历史 ≈1.1 M 行），
//	而它在**关联子查询内部** ⇒ 外层每一行都把它**物化一遍**
//	⇒ EXPLAIN ANALYZE：**> 90 s 未跑完**（被 30s 超时杀掉）。
//
// 【修法】两条过滤都改成 db.SessionFamilyTurnsSessionExistsSQL 渲染的
//	「hot 腿 OR 父表腿」EXISTS ⇒ 谓词落在索引上
//	⇒ 实测 52,854 ms → **192~212 ms**（api_key 族）、36,415 ms → **198 ms**（model 族）。
//
// # 这道门钉什么（★ 关键：口径不是靠「形状像」钉的，是靠**前提**钉的）
//
//	① **形状门**：helper 必须渲染两腿、每腿带 session_id+tenant_id 谓词、
//	   整体带括号、**不得**引用视图或 113 列 source、**不得**含 `%`
//	   （含 `%` 会被 fmt.Sprintf 当格式动词吃掉 ⇒ §5.5.4 事故）。
//	② **单一真值源门**：api_key 的归一表达式必须与**投影**里那一列**逐字相同**
//	   （否则「展示的 api_key」与「过滤用的 api_key」会分叉）。
//	③ **语义门**：`api_key_id` 参数非法（0 / 负数 / 非数字）⇒ 整个过滤子句
//	   **被跳过**（等价于不过滤）—— 这是改动前就有的语义，必须逐字保留。
//	④ ★★ **前提门（真库）**：**形状相同不等于口径相同**。
//	   本改写的等价性依赖两条**结构前提**，它们必须被**单独钉住**：
//	   (a) `request_id → (session_id, tenant_id)` 是函数依赖
//	       —— 旧形态用 `rl.request_id = ft.request_id` 关联（**不带 tenant_id**），
//	       新形态按 (session_id, tenant_id) 匹配 ⇒ 没有 (a) 就不等价；
//	       ★ 而新形态同时消掉了旧形态的**潜在跨租户串味**。
//	   (b) hot 与父表的**同一 request_id** 在被比较的列上取值一致
//	       —— 视图的 anti-join 会把 hot 里的重复行剔掉 ⇒ 若两侧取值不同，
//	       「视图看到的集合」与「两腿看到的集合」就不同。
//	⑤ ★★ **等价门（真库）**：在**同一个 REPEATABLE READ 快照**内，
//	   对真实候选会话**逐个**比较新旧两形态的判定结果 ⇒ 必须逐条相等。
//	   ★ 两侧都必须有可比数据（行数守卫）⇒ 否则显式 SKIP 并写明理由
//	   （★ 纪律：量具必须能自证「空集」不是「判定通过」）。
//
// # 判据纪律（本门自身）
//
// 每条负向断言都配一条**阳性对照**：先证明检测器能认得出「不合格」
// （喂一份合成样本，断言它被判红），再证明真实现被判绿。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/db"
)

// ── ① 形状门 ───────────────────────────────────────────────────────────────

// r92CheckExistsSQL 校验 helper 的渲染形态，返回问题描述列表（空 = 合格）。
// ★ 抽成纯函数 ⇒ 阳性对照喂坏样本走同一把尺子。
//
// ★ R92 起形态是「**派生表 UNION ALL + 单一 EXISTS**」（V2）。
//   R90 的「两条 EXISTS 用 OR 组合」**锁死了驱动方向**（只能会话驱动）
//   ⇒ 稀有过滤值时外层要扫 35 万个会话才凑够 LIMIT 21 ⇒ 生产 500@30s
//   （2026-10-24 build 2841 实测：model 仍 500×3，provider/status_code 已 200）。
func r92CheckExistsSQL(s string) []string {
	var problems []string

	if strings.Contains(s, "session_turns_with_current_month") {
		problems = append(problems, "渲染结果引用了 session_turns_with_current_month 视图 —— "+
			"它的 hot 腿带反连接 ⇒ 外层每行一次 Nested Loop Anti Join ⇒ 生产 500@30s")
	}
	if strings.Contains(s, "SessionFamilyTurnsSourceSQL") {
		problems = append(problems, "渲染结果引用了 113 列全量 source —— 它在关联子查询内部会被物化 ⇒ > 90 s")
	}
	if strings.Contains(s, "%") {
		problems = append(problems, "渲染结果含 '%' —— 会被 fmt.Sprintf 当格式动词吃掉（§5.5.4 事故）")
	}
	// ★★ R92 新增判据：必须是「派生表 UNION ALL」形态。
	//   派生表可被 planner pull-up ⇒ 半连接的两个驱动方向都可选
	//   （稀有过滤 ⇒ 轮次驱动；常见过滤 ⇒ 会话驱动）。
	if !strings.Contains(s, "UNION ALL") {
		problems = append(problems, "缺 UNION ALL（派生表形态）—— 退回 OR 两腿 ⇒ 锁死会话驱动 ⇒ 稀有过滤值仍 500@30s")
	}
	if strings.Contains(s, "OR EXISTS") {
		problems = append(problems, "出现 OR EXISTS —— 那是 R90 形态，锁死驱动方向 ⇒ 稀有过滤值仍 500@30s")
	}
	// 派生表必须**两条腿都在**（少一条 ⇒ 漏读已 promote 的轮次）。
	for _, leg := range []string{
		"FROM public.session_turns t",
		"FROM public.session_turns_hot t",
	} {
		if !strings.Contains(s, leg) {
			problems = append(problems, "缺两腿之一："+leg)
		}
	}
	// 关联谓词（缺一条 ⇒ 跨租户 + 全表扫）。
	for _, pred := range []string{
		"ft.session_id = s.session_id",
		"ft.tenant_id = s.tenant_id",
	} {
		if strings.Count(s, pred) != 1 {
			problems = append(problems, fmt.Sprintf(
				"谓词 %q 出现 %d 次（应为 1 = 派生表上一次）—— 缺一条腿的 session/tenant 约束 ⇒ 全表扫",
				pred, strings.Count(s, pred)))
		}
	}
	// 整体必须带括号（调用方用 AND 拼接 ⇒ `a AND b OR c` 与 `a AND (b OR c)` 不等价）。
	if !strings.HasPrefix(s, "(") || !strings.HasSuffix(s, ")") {
		problems = append(problems, "整体必须带括号 —— 调用方以 AND 拼接子句，不带括号会改变布尔优先级")
	}
	return problems
}

func TestR90_SessionExistsSQLShape(t *testing.T) {
	// ★ R92：helper 签名改为 (colExpr, colName, pred) —— 派生表要投影过滤列。
	got := db.SessionFamilyTurnsSessionExistsSQL("t.model", "model", "ft.model = $1")
	if problems := r92CheckExistsSQL(got); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("helper 渲染形态：%s", p)
		}
	}
}

// TestR90_ExistsShapeDetectorHasTeeth 阳性对照：五份坏样本都必须被判红。
func TestR90_ExistsShapeDetectorHasTeeth(t *testing.T) {
	good := db.SessionFamilyTurnsSessionExistsSQL("t.model", "model", "ft.model = $1")
	if len(r92CheckExistsSQL(good)) != 0 {
		t.Fatal("阳性对照失败：真实现被判为不合格 —— 检测器坏了")
	}

	// 坏样本①：退化成单腿（只剩 hot）⇒ 漏读已 promote 的轮次。
	bad1 := strings.Replace(good, " UNION ALL SELECT t.tenant_id, t.session_id, t.model FROM public.session_turns_hot t",
		"", 1)
	if len(r92CheckExistsSQL(bad1)) == 0 {
		t.Error("坏样本①（只剩单腿）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本②：丢掉 tenant_id 谓词 ⇒ 跨租户 + 全表扫。
	bad2 := strings.Replace(good, "ft.tenant_id = s.tenant_id", "1=1", 1)
	if len(r92CheckExistsSQL(bad2)) == 0 {
		t.Error("坏样本②（丢掉 tenant_id 谓词）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本③：改回读视图 ⇒ 回到 500@30s。
	bad3 := strings.Replace(good, "public.session_turns_hot t", "public.session_turns_with_current_month t", 1)
	if len(r92CheckExistsSQL(bad3)) == 0 {
		t.Error("坏样本③（改回读视图）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本④：含 '%' ⇒ 会被 Sprintf 吃掉。
	bad4 := strings.Replace(good, "t.model", "t.model LIKE 'sys:%' AND t.model", 1)
	if len(r92CheckExistsSQL(bad4)) == 0 {
		t.Error("坏样本④（含 '%'）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本⑤：整体不带括号 ⇒ AND/OR 优先级被改。
	//
	// ★ 第一版写的是 `"(" + TrimSuffix(TrimPrefix(good, "("), ")")` ⇒ ★ 那等于
	//   **把括号加了回去** ⇒ 变异体 == 好样本 ⇒ 检测器报「没有鉴别力」。
	//   ★ 这就是阳性对照的用处：它先证明**尺子**坏了，不是被测对象坏了。
	bad5 := strings.TrimSuffix(strings.TrimPrefix(good, "("), ")")
	if len(r92CheckExistsSQL(bad5)) == 0 {
		t.Error("坏样本⑤（整体不带括号）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本⑥（R92 新增）：退回 R90 的 OR 两腿形态 ⇒ 锁死驱动方向 ⇒ 稀有过滤 500@30s。
	bad6 := "(EXISTS (SELECT 1 FROM public.session_turns_hot t" +
		" WHERE t.session_id = s.session_id AND t.tenant_id = s.tenant_id AND t.model = $1)" +
		" OR EXISTS (SELECT 1 FROM public.session_turns t" +
		" WHERE t.session_id = s.session_id AND t.tenant_id = s.tenant_id AND t.model = $1))"
	if len(r92CheckExistsSQL(bad6)) == 0 {
		t.Error("坏样本⑥（退回 OR 两腿形态）被判合格 —— 检测器没有鉴别力")
	}
}

// ── ② 单一真值源门（api_key 归一表达式 == 投影那一列） ────────────────────

// TestR90_FilterUsesDBAPIKeyExpr 断言**过滤侧**用的是 db 包的规范常量。
//
// ★ 「常量 == 投影里那一列」这条断言住在 **db 包**
//   （db/request_logs_view_schema_apikey_expr_test.go）—— 因为投影表
//   projectionExprsV2 是**未导出**的 ⇒ 只有同包测试能读到它。
//   ⇒ 两边合起来才构成「投影 ≡ 过滤」的单一真值源。
func TestR90_FilterUsesDBAPIKeyExpr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions?api_key_id=42", nil)
	where, _, _ := buildTurnsSessionWhere(req, "t1", time.Time{}, time.Time{}, time.Time{}, "", 1)
	if !strings.Contains(where, db.SessionFamilyAPIKeyIDExpr) {
		t.Errorf("过滤侧没有用 db.SessionFamilyAPIKeyIDExpr —— 归一表达式会与投影分叉:\n%s", where)
	}
}

// ── ③ 语义门（非法参数 ⇒ 过滤子句被跳过） ──────────────────────────────────

func TestR90_InvalidAPIKeyIDSkipsFilter(t *testing.T) {
	for _, v := range []string{"0", "-1", "abc", "1.5", ""} {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions?api_key_id="+v, nil)
		where, args, _ := buildTurnsSessionWhere(req, "t1", time.Time{}, time.Time{}, time.Time{}, "", 1)
		if strings.Contains(where, "api_key_id") {
			t.Errorf("api_key_id=%q 是非法/零值 ⇒ 整个过滤子句必须被跳过（等价于不过滤），但 WHERE 里有 api_key_id:\n%s",
				v, where)
		}
		if len(args) != 1 {
			t.Errorf("api_key_id=%q 被跳过时不应绑定参数，实际绑了 %d 个", v, len(args))
		}
	}
	// 合法值必须仍然生成子句，并绑定 int64。
	req := httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions?api_key_id=42", nil)
	where, args, _ := buildTurnsSessionWhere(req, "t1", time.Time{}, time.Time{}, time.Time{}, "", 1)
	if !strings.Contains(where, "t.api_key_id") {
		t.Errorf("api_key_id=42 必须生成过滤子句:\n%s", where)
	}
	if len(args) != 2 {
		t.Fatalf("api_key_id=42 应绑定 2 个参数（tenant + api_key），实际 %d", len(args))
	}
	switch v := args[1].(type) {
	case int64:
		if v != 42 {
			t.Errorf("api_key_id 参数类型/值 = %v，期望 int64(42)", v)
		}
	default:
		t.Errorf("api_key_id 参数类型 = %T，期望 int64", v)
	}
}

// ── ④ 过滤分支不得引用视图 / 全量 source（源码级） ─────────────────────────

func TestR90_FilterBranchHasNoViewOrFullSource(t *testing.T) {
	// ★★ 必须**剥注释**：本函数体里就写着「这三条**不再**走
	//   `session_turns_with_current_month` 视图」「113 列全量 source」这些说明
	//   ⇒ 不剥的话，**注释里的字面量**会把这两条断言喂绿
	//   ⇒ ⇒ 门恒红（LESSONS §353.3：断言对象必须是可执行体，不是文件里出现过的文本）。
	body := stripGoComments(r90ReadFile(t, "turns_sessions.go"))

	// 只看过滤构造块（buildTurnsSessionWhere 的函数体）。
	lo := strings.Index(body, "func buildTurnsSessionWhere(")
	hi := strings.Index(body[lo:], "\nfunc ")
	if lo < 0 || hi < 0 {
		t.Fatal("读不到 buildTurnsSessionWhere 的函数体 —— 实现形态漂移了，请人工复核")
	}
	block := body[lo : lo+hi]

	if strings.Contains(block, "session_turns_with_current_month") {
		t.Error("过滤构造块仍在读 session_turns_with_current_month 视图 ⇒ 生产 500@30s")
	}
	if strings.Contains(block, "SessionFamilyTurnsSourceSQL") {
		t.Error("过滤构造块仍在用 113 列全量 source ⇒ 生产 > 90 s")
	}
	// 必须走单一真值源的 helper（不许各写一份 SQL）。
	if !strings.Contains(block, "db.SessionFamilyTurnsSessionExistsSQL") {
		t.Error("过滤构造块没有用 db.SessionFamilyTurnsSessionExistsSQL —— 各调用点各写一份 SQL 会漂移")
	}
	// api_key 这一支必须用 db 侧的规范表达式（不许内联复制 CASE）。
	if !strings.Contains(block, "db.SessionFamilyAPIKeyIDExpr") {
		t.Error("api_key 过滤没有用 db.SessionFamilyAPIKeyIDExpr —— 与投影分叉的风险")
	}
}

// ── ⑤ ★★ 真库：前提门 + 等价门 ────────────────────────────────────────────

// r90DSN 取真库 DSN；没有就 SKIP（并写明理由）。
func r90DSN(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"TEST_ADMIN_500_DSN", "LLM_GATEWAY_DATABASE_URL", "DATABASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	t.Skip("set TEST_ADMIN_500_DSN (or `source /tmp/lgw-env.sh` and use LLM_GATEWAY_DATABASE_URL)")
	return ""
}

// r90HasSessionFamily 报告真库里会话族表是否齐备。
func r90HasSessionFamily(ctx context.Context, pool *pgxpool.Pool) bool {
	var n int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_class c JOIN pg_namespace ns ON ns.oid = c.relnamespace
		WHERE ns.nspname = 'public'
		  AND c.relname IN ('session_turns', 'session_turns_hot',
		                    'session_turns_with_current_month', 'sessions')`).Scan(&n)
	if err != nil {
		return false
	}
	return n == 4
}

// TestR90_RealDB_FilterPremises 钉住等价性依赖的两条结构前提。
//
// ★ 这道门是本轮**最重要**的一道：形状门只能证明「改成了两腿」，
//   **证明不了「口径没变」** ⇒ 口径靠前提钉。
func TestR90_RealDB_FilterPremises(t *testing.T) {
	dsn := r90DSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()
	if !r90HasSessionFamily(ctx, pool) {
		t.Skip("真库没有会话族四张表 ⇒ 无法验证前提（显式 SKIP，不是「通过」）")
	}

	// (a) request_id → (session_id, tenant_id) 必须是函数依赖（跨租户）。
	var multi int
	if err := pool.QueryRow(ctx, `
		WITH allrows AS (
			SELECT request_id, session_id, tenant_id FROM public.session_turns_hot
			UNION ALL
			SELECT request_id, session_id, tenant_id FROM public.session_turns
		)
		SELECT count(*) FROM (
			SELECT request_id FROM allrows
			GROUP BY request_id
			HAVING count(DISTINCT (session_id, tenant_id)) > 1
		) x`).Scan(&multi); err != nil {
		t.Fatalf("(a) 前提查询失败: %v", err)
	}
	if multi != 0 {
		t.Errorf("前提 (a) 被破坏：有 %d 个 request_id 对应多个 (session_id, tenant_id) ⇒ "+
			"「按 (session_id, tenant_id) 匹配」与「按 request_id 关联」不等价 ⇒ "+
			"本改写的口径等价性不再成立", multi)
	}

	// (b) hot 与父表的同一 request_id 在被比较的列上必须一致。
	var pairs, modelDiff, providerDiff, statusDiff int
	if err := pool.QueryRow(ctx, `
		WITH h AS (SELECT request_id, tenant_id, model, provider, status_code
		           FROM public.session_turns_hot),
		     p AS (SELECT request_id, tenant_id, model, provider, status_code
		           FROM public.session_turns)
		SELECT count(*),
		       count(*) FILTER (WHERE h.model IS DISTINCT FROM p.model),
		       count(*) FILTER (WHERE h.provider IS DISTINCT FROM p.provider),
		       count(*) FILTER (WHERE h.status_code IS DISTINCT FROM p.status_code)
		FROM h JOIN p ON p.request_id = h.request_id AND p.tenant_id = h.tenant_id`).
		Scan(&pairs, &modelDiff, &providerDiff, &statusDiff); err != nil {
		t.Fatalf("(b) 前提查询失败: %v", err)
	}
	if modelDiff != 0 || providerDiff != 0 || statusDiff != 0 {
		t.Errorf("前提 (b) 被破坏：hot∩父表 配对 %d 行，其中 model 差异 %d、provider 差异 %d、status_code 差异 %d ⇒ "+
			"视图（去重后）与两腿看到的集合可能不同 ⇒ 口径等价性不再成立",
			pairs, modelDiff, providerDiff, statusDiff)
	}
}

// TestR90_RealDB_FilterEquivalence 在**同一个快照**内逐个候选会话比较新旧两形态。
func TestR90_RealDB_FilterEquivalence(t *testing.T) {
	dsn := r90DSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()
	if !r90HasSessionFamily(ctx, pool) {
		t.Skip("真库没有会话族四张表 ⇒ 无法验证等价（显式 SKIP，不是「通过」）")
	}

	// 选一个真实存在且纯数字的 api_key_id 与它对应的真实 model。
	var apiKey string
	var model string
	if err := pool.QueryRow(ctx, `
		SELECT api_key_id FROM (
			SELECT api_key_id FROM public.session_turns_hot
			UNION ALL
			SELECT api_key_id FROM public.session_turns
		) x WHERE api_key_id ~ '^[0-9]+$' LIMIT 1`).Scan(&apiKey); err != nil {
		t.Skipf("真库没有可用的 api_key_id 样本（%v）⇒ 显式 SKIP，不是「通过」", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT model FROM (
			SELECT model FROM public.session_turns_hot
			UNION ALL
			SELECT model FROM public.session_turns
		) x WHERE model IS NOT NULL AND model <> '' LIMIT 1`).
		Scan(&model); err != nil {
		t.Skipf("真库没有可用的 model 样本（%v）⇒ 显式 SKIP，不是「通过」", err)
	}

	// ★ 同一 REPEATABLE READ 快照内逐条比对（快照保证两侧看到同一批数据）。
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("begin repeatable-read tx: %v", err)
	}
	defer tx.Rollback(ctx)

	type verdict struct {
		sessionID string
		tenantID  string
		oldMatch  bool
		newMatch  bool
	}
	var verdicts []verdict

	// api_key 族：旧形态 = 视图 ⋈ 113 列 source（按 request_id 关联）。
	rows, err := tx.Query(ctx, `
		WITH k AS (SELECT $1::text AS k),
		cand AS (
			SELECT session_id, tenant_id FROM public.session_turns_hot WHERE api_key_id = (SELECT k FROM k)
			UNION
			SELECT session_id, tenant_id FROM public.session_turns WHERE api_key_id = (SELECT k FROM k)
		)
		SELECT c.session_id, c.tenant_id,
			EXISTS (
				SELECT 1 FROM public.session_turns_with_current_month ft
				JOIN (`+db.SessionFamilyTurnsSourceSQL()+`) rl ON rl.request_id = ft.request_id
				WHERE ft.session_id = c.session_id AND ft.tenant_id = c.tenant_id
				  AND rl.api_key_id = (SELECT k FROM k)::bigint
			) AS old_match,
			(
				EXISTS (SELECT 1 FROM public.session_turns_hot t
				        WHERE t.session_id = c.session_id AND t.tenant_id = c.tenant_id
				          AND (CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END) = (SELECT k FROM k)::bigint)
				OR EXISTS (SELECT 1 FROM public.session_turns t
				        WHERE t.session_id = c.session_id AND t.tenant_id = c.tenant_id
				          AND (CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END) = (SELECT k FROM k)::bigint)
			) AS new_match
		FROM cand c`, apiKey)
	if err != nil {
		t.Fatalf("api_key 等价查询失败: %v", err)
	}
	for rows.Next() {
		var v verdict
		if err := rows.Scan(&v.sessionID, &v.tenantID, &v.oldMatch, &v.newMatch); err != nil {
			rows.Close()
			t.Fatalf("scan api_key 等价结果: %v", err)
		}
		verdicts = append(verdicts, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("api_key 等价查询迭代: %v", err)
	}

	// model 族：旧形态 = 视图 EXISTS。
	rows2, err := tx.Query(ctx, `
		WITH k AS (SELECT $1::text AS k),
		cand AS (
			SELECT session_id, tenant_id FROM public.session_turns_hot WHERE model = (SELECT k FROM k)
			UNION
			SELECT session_id, tenant_id FROM public.session_turns WHERE model = (SELECT k FROM k)
		)
		SELECT c.session_id, c.tenant_id,
			EXISTS (SELECT 1 FROM public.session_turns_with_current_month ft
			        WHERE ft.session_id = c.session_id AND ft.tenant_id = c.tenant_id AND ft.model = (SELECT k FROM k)) AS old_match,
			(
				EXISTS (SELECT 1 FROM public.session_turns_hot t
				        WHERE t.session_id = c.session_id AND t.tenant_id = c.tenant_id AND t.model = (SELECT k FROM k))
				OR EXISTS (SELECT 1 FROM public.session_turns t
				        WHERE t.session_id = c.session_id AND t.tenant_id = c.tenant_id AND t.model = (SELECT k FROM k))
			) AS new_match
		FROM cand c`, model)
	if err != nil {
		t.Fatalf("model 等价查询失败: %v", err)
	}
	for rows2.Next() {
		var v verdict
		if err := rows2.Scan(&v.sessionID, &v.tenantID, &v.oldMatch, &v.newMatch); err != nil {
			rows2.Close()
			t.Fatalf("scan model 等价结果: %v", err)
		}
		verdicts = append(verdicts, v)
	}
	rows2.Close()
	if err := rows2.Err(); err != nil {
		t.Fatalf("model 等价查询迭代: %v", err)
	}

	// ★ 行数守卫：两侧都必须有可比数据 ⇒ 否则「全部相等」是空集自证。
	if len(verdicts) == 0 {
		t.Skip("真库没有可比对的候选会话 ⇒ 显式 SKIP（★ 空集不是「等价通过」）")
	}
	mismatch := 0
	for _, v := range verdicts {
		if v.oldMatch != v.newMatch {
			mismatch++
			t.Errorf("口径不等价：session=%s tenant=%s 旧形态=%v 新形态=%v",
				v.sessionID, v.tenantID, v.oldMatch, v.newMatch)
		}
	}
	t.Logf("api_key=%s model=%s：比对候选会话 %d 条，口径不一致 %d 条", apiKey, model, len(verdicts), mismatch)
}

// r90ReadFile 读同目录文件。
func r90ReadFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读 %s: %v", name, err)
	}
	return string(b)
}

// ── ⑥ ★★ R92：V2 形态（派生表 UNION ALL + 单一 EXISTS）与 R90 形态的口径等价 ──

// TestR92_RealDB_V2vsR90_Equivalence 在**同一个 REPEATABLE READ 快照**内，
// 逐个候选会话比较「R90 形态（两条 OR 腿 EXISTS）」与「R92 V2 形态」。
//
// ★ 为什么必须真库逐行比（而不是「形状像 ⇒ 等价」）：
//   两者只差「求值顺序」，**取哪些行**必须证明一致 ⇒ 形状门证明不了。
//   ⇒ 252 生产同快照双向 EXCEPT ALL 实测（2026-10-24）：
//     model（稀有）: only_r90=0 / only_v2=0 / r90_rows=21 / v2_rows=21
//     provider='14': only_r90=0 / only_v2=0 / r90_rows=21 / v2_rows=21
//
// ★ 「空集」与「通过」必须能被区分 ⇒ 无候选数据时显式 SKIP 并写明理由
//   （LESSONS §354.2：空集自证不是证据）。
func TestR92_RealDB_V2vsR90_Equivalence(t *testing.T) {
	dsn := r90DSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()
	if !r90HasSessionFamily(ctx, pool) {
		t.Skip("真库没有会话族四张表 ⇒ 无法验证等价（显式 SKIP，不是「通过」）")
	}

	// 取真实存在的 model 与 provider 样本。
	var model, provider string
	if err := pool.QueryRow(ctx, `
		SELECT model FROM (
			SELECT model FROM public.session_turns_hot
			UNION ALL
			SELECT model FROM public.session_turns
		) x WHERE model IS NOT NULL AND model <> '' LIMIT 1`).Scan(&model); err != nil {
		t.Skipf("真库没有可用的 model 样本（%v）⇒ 显式 SKIP，不是「通过」", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT provider FROM (
			SELECT provider FROM public.session_turns_hot
			UNION ALL
			SELECT provider FROM public.session_turns
		) x WHERE provider IS NOT NULL AND provider <> '' LIMIT 1`).Scan(&provider); err != nil {
		t.Skipf("真库没有可用的 provider 样本（%v）⇒ 显式 SKIP，不是「通过」", err)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("begin repeatable-read tx: %v", err)
	}
	defer tx.Rollback(ctx)

	type family struct {
		name  string
		col   string
		value string
	}
	for _, fam := range []family{
		{"model", "model", model},
		{"provider", "provider", provider},
	} {
		// ★ 候选集 = 命中该过滤值的全部会话（两侧都必须覆盖这些会话）。
		//   ★ 候选表别名必须是 `s` ⇒ helper 渲染的 SQL 可以**原样**嵌进来。
		q := `
			WITH cand AS (
				SELECT session_id, tenant_id FROM public.session_turns_hot WHERE ` + fam.col + ` = $1
				UNION
				SELECT session_id, tenant_id FROM public.session_turns      WHERE ` + fam.col + ` = $1
			)
			SELECT s.session_id, s.tenant_id,
			  (
				EXISTS (SELECT 1 FROM public.session_turns_hot t
				        WHERE t.session_id = s.session_id AND t.tenant_id = s.tenant_id AND t.` + fam.col + ` = $1)
				OR EXISTS (SELECT 1 FROM public.session_turns t
				        WHERE t.session_id = s.session_id AND t.tenant_id = s.tenant_id AND t.` + fam.col + ` = $1)
			  ) AS r90_match,
			  ` + db.SessionFamilyTurnsSessionExistsSQL("t."+fam.col, fam.col, "ft."+fam.col+" = $1") + ` AS v2_match
			FROM cand s`
		rows, err := tx.Query(ctx, q, fam.value)
		if err != nil {
			t.Fatalf("%s 等价查询失败: %v", fam.name, err)
		}
		total, mismatch := 0, 0
		for rows.Next() {
			var sid, tid string
			var r90m, v2m bool
			if err := rows.Scan(&sid, &tid, &r90m, &v2m); err != nil {
				rows.Close()
				t.Fatalf("scan %s 等价结果: %v", fam.name, err)
			}
			total++
			if r90m != v2m {
				mismatch++
				if mismatch <= 5 {
					t.Errorf("%s 口径不一致：session=%s tenant=%s r90=%v v2=%v",
						fam.name, sid, tid, r90m, v2m)
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("%s 等价查询迭代: %v", fam.name, err)
		}
		if total == 0 {
			t.Logf("%s 族在真库上没有候选会话 ⇒ 本族未做逐行比对（不是「通过」）", fam.name)
			continue
		}
		if mismatch != 0 {
			t.Errorf("%s 族：%d 个候选会话里 %d 个口径不一致", fam.name, total, mismatch)
		}
		t.Logf("%s 族：%d 个候选会话逐条比对，口径不一致 0 条", fam.name, total)
	}
}

// ── ⑦ ★★ R92：调用点三元组自洽（**精确 token**，不是子串） ──────────────────
//
// ★★ 这一段是 **R92 变异臂 M6 当场暴露的真实门禁漏洞**的修复。
//
// 【漏洞形态】第一版门禁（现已删）是这样写的：
//
//	if !strings.Contains(where, "AS "+c.col)          { … }
//	if !strings.Contains(where, "ft."+c.col+" = $")    { … }
//
// ⇒ ★ **子串匹配让「更长的名字」冒充「目标名字」**：
//   把 colName 从 "model" 改成 "model_x" ⇒ 渲染出 `t.model AS model_x`，
//   而 "AS model_x" **包含** "AS model" ⇒ 第一条断言照样通过；
//   谓词仍是 `ft.model = $1` ⇒ 第二条也通过 ⇒ **门禁绿**。
//   ⇒ 而这个错误在生产上表现为 **SQL 运行期报 column does not exist ⇒ 端点 500**。
// ⇒ ⇒ **断言「某个 token 存在」必须按 token 边界抽出来再比，不能用子串。**
//
// 【与既有通用式的关系】与 LESSONS §355①（门从被测对象取被测集合 ⇒
//   改表即改断言 ⇒ 变异静默通过）同族：那一条是「集合取错」，
//   这一条是「**匹配口径太宽**」⇒ 两者都会让变异静默通过。

// r92AliasRe 抽「AS <标识符>」形态的投影别名。
var r92AliasRe = regexp.MustCompile(`\bAS\s+([A-Za-z_][A-Za-z0-9_]*)`)

// r92PredColRe 抽「ft.<标识符> = $N」形态的谓词列。
//
// ★ 只匹配右侧是占位符的等式 ⇒ `ft.session_id = s.session_id`
//   与 `ft.tenant_id = s.tenant_id` 这两条**关联谓词**不会被误当成过滤谓词。
var r92PredColRe = regexp.MustCompile(`\bft\.([A-Za-z_][A-Za-z0-9_]*)\s*=\s*\$\d+`)

func r92ExtractAliases(s string) []string {
	var out []string
	for _, m := range r92AliasRe.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func r92ExtractPredCols(s string) []string {
	var out []string
	for _, m := range r92PredColRe.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// r92CheckCallSiteClause 校验「派生表形态的过滤子句」自洽，返回问题描述列表。
//
// 判据（两条都必须是**精确 token 比对**）：
//  1. 派生表**必须**投影出过滤列 wantCol（别名精确等于 wantCol）。
//  2. 过滤谓词**恰好一条**，且它引用的 ft.<col> **必须等于** wantCol。
//
// ★ 这两条合起来才是「投影别名 ≡ 谓词列名」⇒ SQL 才可能跑通。
func r92CheckCallSiteClause(clause string, wantCol string) []string {
	var problems []string

	aliases := r92ExtractAliases(clause)
	hasAlias := false
	for _, a := range aliases {
		if a == wantCol {
			hasAlias = true
			break
		}
	}
	if !hasAlias {
		problems = append(problems, fmt.Sprintf(
			"派生表没有投影列 %q（实际别名集合 = %v）—— colName 与谓词不一致 ⇒ SQL 报 column does not exist ⇒ 端点 500",
			wantCol, aliases))
	}

	predCols := r92ExtractPredCols(clause)
	if len(predCols) != 1 {
		problems = append(problems, fmt.Sprintf(
			"过滤子句里 ft.<col> = $N 的个数 = %d（必须恰好 1），实际 = %v", len(predCols), predCols))
	}
	for _, c := range predCols {
		if c != wantCol {
			problems = append(problems, fmt.Sprintf(
				"谓词引用的是 ft.%s，但投影别名是 %q —— 不一致 ⇒ SQL 运行期报错 ⇒ 端点 500", c, wantCol))
		}
	}
	return problems
}

// TestR92_FilterCallSitesProjectTheirColumn 断言四个调用点的
// (colExpr, colName, pred) 三元组自洽 —— 投影的列名必须与谓词引用的列名一致。
//
// ★ 这是「派生表形态」特有的失效面：colName 与 pred 里的列名不一致时
//   **SQL 直接报错**（column does not exist）⇒ 端点 500。
func TestR92_FilterCallSitesProjectTheirColumn(t *testing.T) {
	cases := []struct {
		query string
		col   string
	}{
		{"model=glm-5.3-flash", "model"},
		{"provider=openai", "provider"},
		{"status_code=200", "status_code"},
		{"api_key_id=42", "api_key_id"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions?"+c.query, nil)
		where, _, _ := buildTurnsSessionWhere(req, "", time.Time{}, time.Time{}, time.Time{}, "", 1)
		for _, p := range r92CheckCallSiteClause(where, c.col) {
			t.Errorf("%s：%s\n渲染结果：\n%s", c.query, p, where)
		}
	}
}

// TestR92_CallSiteDetectorRejectsBadSample 阳性对照：
// 坏样本必须被判红（含**第一版门禁漏掉的**那个形态）。
func TestR92_CallSiteDetectorRejectsBadSample(t *testing.T) {
	good := db.SessionFamilyTurnsSessionExistsSQL("t.model", "model", "ft.model = $1")
	if problems := r92CheckCallSiteClause(good, "model"); len(problems) != 0 {
		t.Fatalf("阳性对照失败：真形态被判为不合格 —— 检测器坏了：%v", problems)
	}
	// 坏样本①：**第一版门禁漏掉的形态** —— 投影别名 model_x，谓词仍引用 model。
	bad1 := db.SessionFamilyTurnsSessionExistsSQL("t.model", "model_x", "ft.model = $1")
	if len(r92CheckCallSiteClause(bad1, "model")) == 0 {
		t.Error("坏样本①（投影别名 model_x、谓词引用 model）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本②：谓词引用了另一个列（投影与谓词分叉）。
	bad2 := db.SessionFamilyTurnsSessionExistsSQL("t.model", "model", "ft.provider = $1")
	if len(r92CheckCallSiteClause(bad2, "model")) == 0 {
		t.Error("坏样本②（谓词列与投影列分叉）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本③：别名整体改名（model_x 冒充 model 的极端形态）。
	bad3 := db.SessionFamilyTurnsSessionExistsSQL("t.model", "model_x", "ft.model_x = $1")
	if len(r92CheckCallSiteClause(bad3, "model")) == 0 {
		t.Error("坏样本③（别名整体改名）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本④：谓词缺失（过滤条件没进 SQL ⇒ 端点不过滤）。
	bad4 := db.SessionFamilyTurnsSessionExistsSQL("t.model", "model", "1 = 1")
	if len(r92CheckCallSiteClause(bad4, "model")) == 0 {
		t.Error("坏样本④（没有过滤谓词）被判合格 —— 检测器没有鉴别力")
	}
}

// TestR92_RenderedFilterSQLExecutesOnRealDB —— ★★ 把渲染出来的过滤子句
// **真的跑一次**（真库）。
//
// ★ 为什么形状门不够：colName 与谓词列名不一致，**只有运行期**才报错
//   （column "ft.model" does not exist）⇒ 静态子串断言看不出来
//   ⇒ 生产上表现为端点 500。
// ⇒ 真库执行是唯一能「以生产同样的方式」暴露这类错误的判据。
// ★ 真库不可用时显式 SKIP 并写明理由（空集 / 无库都不是「通过」）。
func TestR92_RenderedFilterSQLExecutesOnRealDB(t *testing.T) {
	dsn := r90DSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()
	if !r90HasSessionFamily(ctx, pool) {
		t.Skip("真库没有会话族四张表 ⇒ 无法执行验证（显式 SKIP，不是「通过」）")
	}

	for _, c := range []struct{ query, col string }{
		{"model=glm-5.3-flash", "model"},
		{"provider=openai", "provider"},
		{"status_code=200", "status_code"},
		{"api_key_id=42", "api_key_id"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions?"+c.query, nil)
		where, args, _ := buildTurnsSessionWhere(req, "", time.Time{}, time.Time{}, time.Time{}, "", 1)

		// 静态判据先行（精确 token）。
		for _, p := range r92CheckCallSiteClause(where, c.col) {
			t.Errorf("%s：%s\n渲染结果：\n%s", c.query, p, where)
		}

		// ★ 运行期判据：真的执行一次。
		q := "SELECT 1 FROM public.sessions s WHERE " + where + " LIMIT 1"
		rows, err := pool.Query(ctx, q, args...)
		if err != nil {
			t.Errorf("%s：渲染出的过滤 SQL 在真库上执行失败 ⇒ 端点会 500：\n%s\n%v", c.query, q, err)
			continue
		}
		rows.Close()
	}
}

