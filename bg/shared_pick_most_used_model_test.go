package bg

// R170 · R50 守卫：探针选型「most-used client_model」查询的读面形态。
//
// # 为什么这道门存在
//
// 这条查询此前读 canonical 视图 ⇒ R170 改成窄投影两臂（会话族原生源）。
// ★ 而「改读点」这件事本身是**高危**的：投影少一列、join 键差一个转型、
//   别名错一个字母 ⇒ 结果可能变、也可能变成 42703 ⇒ ⇒ 必须有【持续的门】，
//   而不是一次性的对拍。
//
// ★★ **收益的真实口径（2026-10-27 252 生产实测，推翻过一个错误前提）**：
//
//   ★ 错误前提：「视图有 118 列 ⇒ 每行物化 118 列 ⇒ 改窄投影能省十几秒」。
//     —— **不成立**：PostgreSQL 会**内联**视图并**裁剪**未引用的列。
//   ★ 计划代价：视图 2026.88..2027.14 vs 窄投影 2025.29..2025.55
//     ⇒ **执行阶段没有可省的量**；真正的差别在**规划**（25~91 ms vs 8~13 ms）。
//   ★ ★ 单次执行耗时**不能**用来比较形态：252 上九个**计划形状逐项相同**的
//     执行，耗时落在 368 ms ~ 16,194 ms（**44×**）⇒ 噪声主导。
//
// # 三条判据
//
//  1. **helper 产物形状**：恰好 7 列、两臂、join 键与视图**逐字相同**
//     （★ 含 ::text 转型）、credential_id 表达式与视图投影**逐字相同**。
//  2. **查询产物形状**：不得出现 canonical 视图名；别名前缀一致；
//     探针排除谓词需要的三列（quality_flags / task_type / origin_actor）
//     **必须**都在窄投影里（少一列 = 42703）；★ 且谓词**身份**必须是
//     quality_flags 变体（渲染后逐字比对）—— 换成 origin_stage 变体
//     同样会 42703，而「三列都在」那条判据**抓不到**。
//  3. **真库等价**：同一凭据同一窗口下，「视图形态」与「窄投影形态」的
//     **分组多重集**逐值相同（★ 比全部分组，不是只比 top-1 ——
//     ORDER BY count(*) DESC LIMIT 1 在并列时**本身不确定**）。

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

const canonicalViewName = "request_logs_with_current_month"

// ── 判据 1：helper 产物形状 ───────────────────────────────────────────────

func TestMostUsedClientModelSourceShape(t *testing.T) {
	src := dbpkg.SessionFamilyMostUsedClientModelSourceSQL()

	if strings.Contains(src, canonicalViewName) {
		t.Fatalf("窄投影里出现了 canonical 视图名 %q —— 这个 helper 的全部意义就是绕开它：\n%s",
			canonicalViewName, src)
	}

	// 两臂：hot 配 hot details，母表配母表 details（★ 与视图逐字同构）。
	for _, want := range []string{
		"FROM public.session_turns_hot t",
		"LEFT JOIN public.session_turn_details_hot d",
		"FROM public.session_turns t",
		"LEFT JOIN public.session_turn_details d",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("窄投影缺 %q（★ 两臂形态必须与视图逐字同构）：\n%s", want, src)
		}
	}

	// join 键：★ 必须保留 ::text 转型 —— 与视图的 join 谓词**逐字相同**。
	//
	// ★★ 为什么保留（2026-10-27 实测订正了本轮早先的判断）：
	//   ① **原先的理由是错的**：「varchar 比较忽略尾空格、text 不忽略 ⇒ 两者不等价」
	//      —— 实测 `'a'::varchar(255) = 'a '::varchar(255)` 是 **false**
	//      ⇒ **varchar 也不忽略尾空格**（只有 `char(n)` 忽略）⇒ 那条推理不成立。
	//   ② **「去掉能快 ~426 ms」也是噪声**：同计划形状下 252 的单次耗时波动 44×，
	//      该数字不可复现；且两形态的**计划代价与 buffer 计数完全相同** ⇒
	//      **转型是免费的**。
	//   ③ **真正的理由（更硬）**：`text` 相等是**字节相等**，`varchar` 相等走
	//      **排序规则**（确定性 ICU 排序规则下可能把规范等价的两种写法判相等）
	//      ⇒ `d.tenant_id::text = t.tenant_id::text` 是**字节精确**的，
	//      且与 canonical 视图的 join 谓词**逐字相同** ⇒ 不引入任何新的相等语义。
	//   ⇒ 既然转型**免费**，就按「与视图逐字同语义」这条最保守的形态钉住。
	if !strings.Contains(src, "ON d.tenant_id::text = t.tenant_id::text AND d.request_id = t.request_id AND d.partition_date = t.partition_date") {
		t.Errorf("窄投影的 join 键与视图不一致（★ 必须是 d.tenant_id::text = t.tenant_id::text + request_id + partition_date）：\n%s", src)
	}

	// credential_id 表达式：必须与视图投影**逐字相同**（唯一规范表达式）。
	if !strings.Contains(src, dbpkg.SessionFamilyCredentialIDExpr+" AS credential_id") {
		t.Errorf("credential_id 表达式不是唯一规范表达式（%q）：\n%s",
			dbpkg.SessionFamilyCredentialIDExpr, src)
	}

	// 列数：每臂**恰好 7 列**（★ 用精确计数，不用 Contains —— R168 教训）。
	for i, leg := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(src, "("), ")"), " UNION ALL ") {
		open := strings.Index(leg, "SELECT ")
		if open < 0 {
			t.Fatalf("第 %d 臂不是 SELECT 形态：%s", i+1, leg)
		}
		list := leg[open+len("SELECT "):]
		list = list[:strings.Index(list, " FROM ")]
		cols := strings.Split(list, ",")
		if len(cols) != 7 {
			t.Errorf("第 %d 臂投影了 %d 列（应为 7）：%s", i+1, len(cols), list)
		}
	}
}

// ── 判据 2：查询产物形状 ───────────────────────────────────────────────────

func TestMostUsedClientModelSQLShape(t *testing.T) {
	// ★★ 用**两个**别名跑同一套判据（"n" = 生产用的，"q7" = 故意不同的）。
	//
	// ★ 为什么必须第二个别名：若 SQL 模板里某处**硬编码**了别名（历史
	//   拼写里满地都是 "rl"），用生产别名 "n" 去测**测不出来** ⇒
	//   ⇒ 换一个别名，硬编码处与参数处就会**对不上** ⇒ 门才张嘴。
	//   ★ 只测一个别名 ⇒ 门有「恰好是 X」的假绿风险（与 R168 的教训同源）。
	for _, alias := range []string{"n", "q7"} {
		t.Run("alias="+alias, func(t *testing.T) {
			checkMostUsedClientModelSQLShape(t, alias)
		})
	}
}

func checkMostUsedClientModelSQLShape(t *testing.T, alias string) {
	t.Helper()
	sql := mostUsedClientModelSQL(alias)

	if strings.Contains(sql, canonicalViewName) {
		t.Errorf("探针选型查询又打回 canonical 视图 %q：\n%s", canonicalViewName, sql)
	}
	if !strings.Contains(sql, dbpkg.SessionFamilyMostUsedClientModelSourceSQL()) {
		t.Errorf("查询没有用窄投影 helper：\n%s", sql)
	}

	// 探针排除谓词需要的三列：★ 少一列就是 42703（R50 同族事故）。
	for _, col := range []string{"quality_flags", "task_type", "origin_actor"} {
		if !strings.Contains(dbpkg.SessionFamilyMostUsedClientModelSourceSQL(), col) {
			t.Errorf("窄投影缺列 %q —— 探针排除谓词引用了它 ⇒ 真库上会 42703", col)
		}
	}

	// 别名一致性：★ 外层别名必须**逐处**出现在列引用上。
	for _, want := range []string{
		"SELECT client_model",
		"WHERE " + alias + ".credential_id = $1",
		"AND " + alias + ".ts > now()",
		"AND " + alias + ".success = TRUE",
		"AND " + alias + ".client_model IS NOT NULL",
		"GROUP BY " + alias + ".client_model",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("查询产物缺 %q（★ 别名必须逐处一致）：\n%s", want, sql)
		}
	}
	// ★★ **口径钉死**（2026-10-27 补）：真库判据会把「产品形态的实际产出」与
	//   「视图形态的 top-1」直接比对 ⇒ ⇒ 只有当**两边问的是同一个问题**时，
	//   这个比对才是等价的 ⇒ ⇒ 口径（窗口 / 过滤 / 排序 / 取行）必须被**钉死**，
	//   否则「口径变了」会被误判成「不等价」（**假红**），而「口径变了但两边
	//   一起变」又会让真库判据**看不见**（**假绿**）。
	// ⇒ ⇒ 这里逐字钉住：7 天窗、按计数降序、取 1 行。
	for _, want := range []string{
		"interval '7 days'",
		"ORDER BY count(*) DESC",
		"LIMIT 1",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("查询产物的口径被改了（缺 %q）—— ★ 真库判据拿它与视图 top-1 直接比对，"+
				"口径不一致时那个比对**不成立**（既可能假红也可能假绿）：\n%s", want, sql)
		}
	}
	// ★ 反向：别处**不得**残留别的别名（历史拼写里的 "rl" 之类）。
	//   ⇒ 只查「逐处出现」不够 —— 残留的旧别名同样会让真库上 42703。
	for _, stale := range []string{"rl.", "n.", "q7."} {
		if stale == alias+"." {
			continue
		}
		if strings.Contains(sql, stale) {
			t.Errorf("查询产物里残留了别名 %q（当前别名是 %q）—— 残留别名在真库上是 42703：\n%s",
				stale, alias, sql)
		}
	}
	// 谓词渲染出的三处别名必须与外层别名**同一个**。
	if !strings.Contains(sql, alias+".quality_flags") ||
		!strings.Contains(sql, alias+".task_type") ||
		!strings.Contains(sql, alias+".origin_actor") {
		t.Errorf("探针排除谓词没有用外层别名 %q 渲染（★ 三个占位符必须都填它）：\n%s", alias, sql)
	}

	// ★★ 谓词**身份**（渲染后逐字比对）—— ★ 这条是「少一列就 42703」的那道闸：
	//   窄投影带 details 腿的 quality_flags，但**没有** origin_stage
	//   ⇒ ⇒ 必须用 quality_flags 变体；换成会话族变体（origin_stage）
	//   ⇒ ⇒ 真库上每次 42703。
	//   ★ 只断言「三列都在窄投影里」**不够**：会话族变体也引用 task_type /
	//     origin_actor，那条断言照样绿 ⇒ ⇒ 必须比对**渲染结果**。
	want := probePredicateForAlias(alias)
	if !strings.Contains(sql, want) {
		t.Errorf("查询用的不是 quality_flags 变体谓词（渲染后逐字比对失败）：\n%s", sql)
	}
	sessionForm := fmt.Sprintf(probeTrafficExclusionPredicateSession, alias, alias, alias)
	if strings.Contains(sql, sessionForm) {
		t.Errorf("查询用了会话族谓词（引用 origin_stage）—— 窄投影里**没有**这一列 ⇒ 真库上每次 42703：\n%s", sql)
	}
}

// ── 判据 3：真库等价 ───────────────────────────────────────────────────────

func TestMostUsedClientModelEquivalenceRealDB(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("TEST_DB_URL"))
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— ★ 真库等价判据**没跑**（不是通过）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("真库不可达：%v", err)
	}

	// 找一个近窗有成功轮次的凭据。
	//
	// ★ 两臂都要找（hot ∪ 母表）：被测查询本身读的就是这两张表，
	//   而本地真库的 hot 表常常是空的（母表才是有数据的那一腿）⇒
	//   只查 hot 会让真库判据**恒 SKIP** ⇒ ⇒ 「没跑」被误读成「通过」。
	credID, err := findNumericCredential(ctx, pool)
	if err != nil {
		t.Skipf("真库近窗没有可用凭据（本地库常为空）：%v", err)
	}

	viewForm := `
		SELECT client_model, count(*) AS n
		  FROM public.` + canonicalViewName + ` rl
		 WHERE rl.credential_id = $1
		   AND rl.ts > now() - interval '7 days'
		   AND rl.success = TRUE
		   AND ` + probePredicateForAlias("rl") + `
		   AND rl.client_model IS NOT NULL
		 GROUP BY rl.client_model`
	narrowForm := `
		SELECT client_model, count(*) AS n
		  FROM ` + dbpkg.SessionFamilyMostUsedClientModelSourceSQL() + ` n
		 WHERE n.credential_id = $1
		   AND n.ts > now() - interval '7 days'
		   AND n.success = TRUE
		   AND ` + probePredicateForAlias("n") + `
		   AND n.client_model IS NOT NULL
		 GROUP BY n.client_model`

	viewRows, err := pool.Query(ctx, viewForm, credID)
	if err != nil {
		t.Fatalf("视图形态查询失败：%v", err)
	}
	view, err := collectPairs(viewRows)
	if err != nil {
		t.Fatalf("读取视图形态结果失败：%v", err)
	}
	narrowRows, err := pool.Query(ctx, narrowForm, credID)
	if err != nil {
		t.Fatalf("窄投影形态查询失败：%v", err)
	}
	narrow, err := collectPairs(narrowRows)
	if err != nil {
		t.Fatalf("读取窄投影结果失败：%v", err)
	}

	if len(view) == 0 && len(narrow) == 0 {
		t.Skip("该凭据在真库近窗没有可分组行（口径为空 ⇒ 对拍无意义）")
	}
	t.Logf("凭据 %d：视图形态 %d 组 · 窄投影形态 %d 组", credID, len(view), len(narrow))
	for _, d := range diffPairs(view, narrow) {
		t.Errorf("分组结果不一致（client_model=%q：视图 %d · 窄投影 %d）", d.key, d.viewN, d.narrowN)
	}

	// ★★★ **第三段：直接跑【产品形态】**（`mostUsedClientModelSQL("n")`）。
	//
	// ★ 为什么必须有这一段：上面两段比的是**手写**的窄投影形态 —— 它证明的是
	//   「窄投影这个想法与视图等价」，**没有**证明「生产函数实际发出的那条 SQL
	//   与视图等价」⇒ ⇒ 产品形态若与手写形态不同（例如调用点忘了换 helper、
	//   别名写错、谓词拼错、FROM 的别名与 WHERE 不一致），上面两段**照样绿** ⇒ ⇒ 必须**把产品本身**跑一遍。
	//
	// ★ **这个比对为什么成立**（2026-10-27 补上的一环）：它拿「产品形态的实际产出」
	//   与「视图形态的 top-1」直接比 ⇒ ⇒ 只有两边**问同一个问题**时才等价
	//   ⇒ ⇒ 口径（7 天窗 / success=TRUE / 按计数降序 / 取 1 行）由**判据 2 逐字钉死**
	//   ⇒ ⇒ 口径一变，判据 2 先红 ⇒ ⇒ 这里不会因为口径漂移而误判。
	//
	// ★ 口径说明：产品形态带 `GROUP BY … ORDER BY count(*) DESC LIMIT 1` ⇒
	//   它只产出**一行**。而 `LIMIT 1` 在**并列**时**本身不确定**
	//   （既有性质，不是本轮引入）⇒ ⇒ 并列时**只要求**产品落在并列集合里，
	//   不要求与视图的 top-1 逐字相同；不并列时才做严格相等。
	var productModel string
	err = pool.QueryRow(ctx, mostUsedClientModelSQL("n"), credID).Scan(&productModel)
	if err != nil {
		t.Fatalf("★ 产品形态查询失败（mostUsedClientModelSQL）：%v", err)
	}
	topCount, tieSize := topOf(view)
	t.Logf("产品形态产出 model=%q（视图 top 计数 %d，并列规模 %d）", productModel, topCount, tieSize)
	switch {
	case tieSize == 0:
		t.Errorf("视图形态没有任何分组（口径为空 ⇒ 前面已跳过，这里不该到得了）")
	case tieSize == 1:
		// 视图的 top-1 唯一 ⇒ 产品必须与它逐字相同。
		var wantTop string
		for _, p := range view {
			if p.n == topCount {
				wantTop = p.key
				break
			}
		}
		if productModel != wantTop {
			t.Errorf("★ 产品形态产出 %q，视图 top-1 是 %q —— 生产函数实际发出的 SQL 与视图不等价",
				productModel, wantTop)
		}
	default:
		// 并列 ⇒ LIMIT 1 不确定 ⇒ 只要求落在并列集合里。
		inTie := false
		for _, p := range view {
			if p.n == topCount && p.key == productModel {
				inTie = true
				break
			}
		}
		if !inTie {
			t.Errorf("★ 产品形态产出 %q，不在视图的并列 top 集合里（并列规模 %d）", productModel, tieSize)
		}
	}
}

// topOf 返回分组集合里最大的计数，以及**并列**它的分组数。
func topOf(pairs []pairCount) (top int64, tie int) {
	for _, p := range pairs {
		if p.n > top {
			top = p.n
			tie = 1
		} else if p.n == top && top > 0 {
			tie++
		}
	}
	return top, tie
}

// findNumericCredential 在「hot ∪ 母表」两腿里找一个近窗有成功轮次、
// 且 credential_id 是纯数字的凭据。
//
// ★ 为什么两腿都要找：被测查询读的就是这两张表；本地真库通常**母表才有数据**
//
//	（hot 表只是写缓冲，常空）⇒ 只查 hot ⇒ 判据恒 SKIP ⇒ 「没跑」会被
//	误读成「通过」——★ 那正是本轮反复出现的「门绿而事没做」形态。
func findNumericCredential(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	const q = `
		SELECT DISTINCT (CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END)
		  FROM %s
		 WHERE ts > now() - interval '7 days' AND success = TRUE
		   AND credential_id ~ '^[0-9]+$'
		 LIMIT 1`
	var lastErr error
	for _, table := range []string{"public.session_turns_hot", "public.session_turns"} {
		var id int64
		err := pool.QueryRow(ctx, fmt.Sprintf(q, table)).Scan(&id)
		if err == nil {
			return id, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

// probePredicateForAlias 用探针排除谓词渲染指定别名（★ 视图与窄投影共用一条）。
//
// ★ 用 fmt.Sprintf（与生产调用点**逐字同形**）而不是 strings.NewReplacer ——
//
//	NewReplacer 只能替换 "%s" 这一个 token，若谓词将来多出别的动词
//	（如 %d / %I），替换器会静默放行而 fmt.Sprintf 会直接报错。
func probePredicateForAlias(alias string) string {
	return fmt.Sprintf(ProbeTrafficExclusionPredicateView, alias, alias, alias)
}

type pairCount struct {
	key string
	n   int64
}

type pairDiff struct {
	key     string
	viewN   int64
	narrowN int64
}

func collectPairs(rows interface {
	Close()
	Next() bool
	Scan(...any) error
}) ([]pairCount, error) {
	var out []pairCount
	for rows.Next() {
		var p pairCount
		if err := rows.Scan(&p.key, &p.n); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// diffPairs 返回两组之间不一致的项。
//
// ★ 判据是「**分组多重集逐值相同**」⇒ 必须比对**两边的并集**：
//   - 只在视图里有、窄投影里没有的键 ⇒ 差异；
//   - 只在窄投影里有、视图里没有的键 ⇒ 差异；
//   - 两边都有但计数不同 ⇒ 差异。
//
// ★ 第一版写成「扫到第一条差异就 return」⇒ **两侧都有差异时会漏报第二条**
//
//	⇒ 漏报方向的错误比误报更危险（门会显示绿）⇒ 改成并集全量比对。
func diffPairs(view, narrow []pairCount) []pairDiff {
	viewMap := map[string]int64{}
	for _, p := range view {
		viewMap[p.key] += p.n
	}
	narrowMap := map[string]int64{}
	for _, p := range narrow {
		narrowMap[p.key] += p.n
	}
	keys := make([]string, 0, len(viewMap)+len(narrowMap))
	seen := map[string]bool{}
	for k := range viewMap {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range narrowMap {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var out []pairDiff
	for _, k := range keys {
		v, okV := viewMap[k]
		n, okN := narrowMap[k]
		if okV && okN && v == n {
			continue
		}
		out = append(out, pairDiff{key: k, viewN: v, narrowN: n})
	}
	return out
}
