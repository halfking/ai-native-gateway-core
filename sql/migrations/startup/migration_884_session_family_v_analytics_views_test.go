package startup

// migration_884_session_family_v_analytics_views_test.go —— 迁移 884 的契约门。
//
// # 这道门守的是什么
//
// 884 把 3 张**读 v1 裸母表**的分析视图改读会话族：
//
//   · v_continuation_effectiveness
//   · v_node_switch_analysis
//   · v_timeout_effectiveness
//
// 这 3 张原先读 `_request_logs`（v1 自 2026-10-13 停写且历史已清空 ⇒ 真库恒 0 行、
// **不报任何错**），同时是「后期删除 `_request_logs`」的**依赖阻塞项**
// （pg_depend 实测 `_request_logs` 被 7 个视图依赖，这 3 张是其中唯一
// **没有任何 API/Go 代码消费**的）。
//
// # 本门特有的第 0 条：**判据的判据**
//
// 上一轮判定「视图是否还读 v1 裸母表」用的写法是
//
//	pg_get_viewdef(v) LIKE '%_request_logs%'
//
// 而 SQL 的 `LIKE` 里 **`_` 是单字符通配符** ⇒ 这条判据会把
// `request_logs_with_current_month`（**canonical 视图名**）也判成命中
// ⇒ 上一轮「5 张 `v_*` 视图读 v1」里，**`v_model_health_dashboard` 与
// `v_probe_system_health` 是假阳性**（实测它们定义体里**没有** `_request_logs`
// 字面量）。上一轮与本轮都踩了这个坑。
//
// ⇒ 本门第 0 条断言**判据本身**：同一个字符串，两种匹配必须给出**不同**结论。
// 这条要是哪天又有人用回 `LIKE`，门会当场把「为什么 LIKE 不行」讲出来。
//
// 其余各条（与 883 的契约门同款）：
//
//	① **切了迁移、切了正典、忘了 baseline**（或反之）⇒ 新装与存量环境分叉。
//	   本门钉：canonical 视图文件、正典 schema dump、baseline schema dump、
//	   installer embeddata dump **四处**的视图正文**逐字相同**。
//	② **只改了 canonical，没改 installer embeddata** ⇒ 装出来的库与仓库分叉。
//	③ **迁移没进部署序列** ⇒ 任何环境都不会执行它。本门钉三处登记。
//	④ **类型/列名不匹配**（会话族的 6 个细节语义列在 `session_turn_details` 上）。
//	⑤ **down 必须能回滚**，且回滚体读的是 **860 之后的物理名** `_request_logs`
//	   （基线 schema 里的旧写法 `public.request_logs` 是建视图当时的名字）。
//
// 行为侧的真库门在 admin 包：
// `admin/v_analytics_views_session_source_test.go`
// （造夹具证明三张视图真的取到会话族的值，4 处变异全部转红）。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	mig884Name     = "884_session_family_v_analytics_views.sql"
	mig884DownName = "884_session_family_v_analytics_views.down.sql"
)

var mig884Views = []string{
	"v_continuation_effectiveness",
	"v_node_switch_analysis",
	"v_timeout_effectiveness",
}

// mig884ColsForView：每张视图**实际用到**的细节语义列。
//
// ⚠ 刻意**不**要求每张视图都有全部 6 列：continuation 视图不关心
// node_switch_count，node_switch 视图不关心 timeout_mode。
// 6 列的覆盖由「并集」那条断言负责（见 TestMigration884_ViewBodiesReadSessionFamily 末尾）。
var mig884ColsForView = map[string][]string{
	"v_continuation_effectiveness": {"is_continuation", "cached_response_id", "context_size_tokens"},
	"v_node_switch_analysis":       {"node_switch_count"},
	"v_timeout_effectiveness":      {"timeout_mode", "effective_timeout_seconds"},
}

func mig884Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// <root>/sql/migrations/startup → 上溯三级到 <root>
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig884Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// mig884ViewBody 取出文件里某条 CREATE OR REPLACE VIEW 的定义体。
func mig884ViewBody(t *testing.T, content, viewName string) string {
	t.Helper()
	marker := "CREATE OR REPLACE VIEW public." + viewName
	start := strings.Index(content, marker)
	if start < 0 {
		t.Fatalf("未找到 %s 的定义体", viewName)
	}
	rest := content[start:]
	end := strings.Index(rest, ";\n")
	if end < 0 {
		t.Fatalf("%s 的定义体没有以 ';' 结束", viewName)
	}
	return rest[:end]
}

// mig884GuardRe 是本仓自己的门 `internal/sqlreadguard` 用的那条正则
// （**正则 + 词边界** ⇒ 它能分辨 v1 族与 canonical 视图名）。
var mig884GuardRe = regexp.MustCompile(`(?i)\b(FROM|JOIN)\s+(public\.)?_?request_logs\b`)

// TestMigration884_JudgmentInstrumentDistinguishesV1FromCanonical
// ★ 第 0 条：**判据的判据**。
//
// 断言「用来判定视图是否读 v1 裸母表的那个匹配」必须能把
//
//	`_request_logs`（v1 族）  与  `request_logs_with_current_month`（canonical）
//
// 分开；而 SQL 的 `LIKE '%_request_logs%'`（上一轮用过的写法）**做不到**
// —— 因为 `LIKE` 里的 `_` 是**单字符通配符**。
//
// 为什么要钉这一条：上一轮与本轮都踩过这个坑，导致「5 张 `v_*` 视图读 v1」
// 里混进 **2 张假阳性**（`v_model_health_dashboard` / `v_probe_system_health`
// 其实只提到 canonical 视图名）⇒ 上一轮的结论**多算了**。
// 判据自己错了的时候，它钉的一切都是假的 ⇒ 判据必须被自己的判据钉一次。
//
// 本门不执行 SQL 去演示 LIKE 的语义（那要真库），而是**用仓库自己的门**
// 的那条正则来证明「精确仪器能分辨」，
// 并**同时核对仓库的门里仍然是这条正则**（防止它哪天被换成一个分辨不出的）。
func TestMigration884_JudgmentInstrumentDistinguishesV1FromCanonical(t *testing.T) {
	const v1Literal = "FROM public._request_logs rl"
	const canonical = "FROM public.request_logs_with_current_month v"

	// 精确仪器（正则 + 词边界）：能分辨
	if !mig884GuardRe.MatchString(v1Literal) {
		t.Fatalf("量具自检失败：仓库那条正则应当命中 v1 族字面量 %q", v1Literal)
	}
	if mig884GuardRe.MatchString(canonical) {
		t.Fatalf("量具自检失败：仓库那条正则**不该**命中 canonical 视图名 %q —— "+
			"若命中了，说明它已经分辨不出 v1 族与 canonical ⇒ 本门的前提失效", canonical)
	}

	// 精确子串（strpos 语义）同样能分辨
	if !strings.Contains(v1Literal, "_request_logs") {
		t.Fatalf("量具自检失败：v1 字面量里应含 %q", "_request_logs")
	}
	if strings.Contains(canonical, "_request_logs") {
		t.Fatalf("量具自检失败：canonical 视图名里**不该**含 %q", "_request_logs")
	}

	// 仓库的门里必须还是这条正则
	root := mig884Tree(t)
	guardSrc := mig884Read(t, root, filepath.Join("internal", "sqlreadguard", "guard_test.go"))
	if !strings.Contains(guardSrc, `(?i)\b(FROM|JOIN)\s+(public\.)?_?request_logs\b`) {
		t.Errorf("internal/sqlreadguard 的裸母表匹配正则已变（不再是带词边界的那条）—— " +
			"请复核它是否仍能分辨 v1 族与 canonical 视图名，并同步更新本门")
	}
}

// TestMigration884_ViewBodiesReadSessionFamily 钉 ④ + 读源。
func TestMigration884_ViewBodiesReadSessionFamily(t *testing.T) {
	root := mig884Tree(t)
	up := mig884Read(t, root, filepath.Join("sql", "migrations", "startup", mig884Name))

	allCols := map[string]bool{}
	for _, viewName := range mig884Views {
		body := mig884ViewBody(t, up, viewName)

		// v1 母表读必须消失（两代物理名都算）
		for _, v1 := range []string{
			"FROM public.request_logs", "FROM public._request_logs",
			"FROM request_logs", "FROM _request_logs",
		} {
			if strings.Contains(body, v1) {
				t.Errorf("%s 的定义体仍读 v1 族（%q）—— v1 已停写并清空 ⇒ 这张视图恒 0 行且不报任何错", viewName, v1)
			}
		}

		// 会话族两条腿都必须在位
		if !strings.Contains(body, "FROM public.session_turns_hot") {
			t.Errorf("%s 的定义体没有 session_turns_hot 腿", viewName)
		}
		if !strings.Contains(body, "FROM public.session_turns ") && !strings.Contains(body, "FROM public.session_turns\n") {
			t.Errorf("%s 的定义体没有 session_turns（parent）腿 —— 24h 窗跨过 hot 保留期，必须带 parent 腿", viewName)
		}
		if !strings.Contains(body, "UNION ALL") {
			t.Errorf("%s 的定义体没有 UNION ALL（两条腿的合并方式）", viewName)
		}

		// details 的 join 键必须与 canonical 视图的会话臂**逐字相同**
		// （`db/request_logs_view_schema.go` 的 composer 用的就是这组键）
		for _, want := range []string{
			"LEFT JOIN public.session_turn_details_hot d_hot",
			"d_hot.tenant_id::text = t_hot.tenant_id::text",
			"d_hot.request_id = t_hot.request_id",
			"d_hot.partition_date = t_hot.partition_date",
			"LEFT JOIN public.session_turn_details d_par",
			"d_par.tenant_id::text = t_par.tenant_id::text",
			"d_par.request_id = t_par.request_id",
			"d_par.partition_date = t_par.partition_date",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s 的定义体缺少 join 键 %q —— 与 canonical 视图的会话臂不一致 ⇒ 详情行会错配", viewName, want)
			}
		}

		// 6 个细节语义列必须从 **details** 侧取（不是 turns 侧）。
		// ⚠ 每张视图**只需要自己用到的那几列**（continuation 用
		//   is_continuation/cached_response_id/context_size_tokens；
		//   node_switch 用 node_switch_count；timeout 用 timeout_mode/
		//   effective_timeout_seconds）⇒ 断言「每张都有全部 6 列」是错的
		//   （本门第一版就写成了那样，12 条假阳性）。
		//   正确形态：**并集**覆盖 6 列，且每张视图用到的列在位。
		for _, col := range mig884ColsForView[viewName] {
			allCols[col] = true
			if !strings.Contains(body, col) {
				t.Errorf("%s 的定义体没有 %q —— 会话族的该语义列在 session_turn_details 上", viewName, col)
			}
		}

		// 24 小时窗口必须保留（删掉它 ⇒ 窗口外的数据被计入）
		if !strings.Contains(body, "now() - '24:00:00'::interval") {
			t.Errorf("%s 的定义体没有 24 小时窗口条件", viewName)
		}
	}

	// 6 个细节语义列的**并集**必须覆盖（每张视图各取所需 ⇒ 合起来才完整）
	for _, col := range []string{
		"is_continuation", "cached_response_id", "context_size_tokens",
		"node_switch_count", "timeout_mode", "effective_timeout_seconds",
	} {
		if !allCols[col] {
			t.Errorf("三张视图合起来没有用到 %q —— 会话族侧的细节语义列掉了一个", col)
		}
	}
}

// TestMigration884_DownRestoresV1Bodies 钉 ⑤。
func TestMigration884_DownRestoresV1Bodies(t *testing.T) {
	root := mig884Tree(t)
	down := mig884Read(t, root, filepath.Join("sql", "migrations", "startup", mig884DownName))

	for _, viewName := range mig884Views {
		if !strings.Contains(down, "CREATE OR REPLACE VIEW public."+viewName) {
			t.Errorf("down 缺少 %s 的还原定义体", viewName)
		}
	}
	// down 必须用 860 之后的**物理名**（基线 schema 里的 public.request_logs
	// 是建视图当时的名字；860 改名后 PG 按 OID 跟随 ⇒ 实际读 _request_logs）
	if !strings.Contains(down, "FROM public._request_logs") {
		t.Errorf("down 必须用 860 之后的物理名 public._request_logs —— " +
			"用旧名 public.request_logs 会因「表不存在」而回滚失败")
	}
	if strings.Contains(down, "FROM public.request_logs\n") {
		t.Errorf("down 里出现了 860 之前的旧表名 public.request_logs")
	}
}

// TestMigration884_CanonicalAndBaselineInSync 钉 ①：
// canonical 视图文件 + 正典 schema dump + baseline schema dump +
// installer embeddata dump 四处的视图正文**逐字相同**。
func TestMigration884_CanonicalAndBaselineInSync(t *testing.T) {
	root := mig884Tree(t)

	for _, viewName := range mig884Views {
		var bodies []string
		for _, rel := range []string{
			filepath.Join("sql", "objects", "views", viewName+".sql"),
			filepath.Join("sql", "schema", "01-schema.sql"),
			filepath.Join("deploy", "sql", "schemas", "baseline", "01-schema.sql"),
			filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "01-schema.sql"),
		} {
			content := mig884Read(t, root, rel)
			marker := "CREATE VIEW public." + viewName
			start := strings.Index(content, marker)
			if start < 0 {
				t.Errorf("%s 里没有 %s 的定义体", rel, viewName)
				continue
			}
			rest := content[start:]
			end := strings.Index(rest, ";\n")
			if end < 0 {
				t.Errorf("%s 里 %s 的定义体没有以 ';' 结束", rel, viewName)
				continue
			}
			bodies = append(bodies, rest[:end])
			if strings.Contains(rest[:end], "_request_logs") {
				t.Errorf("%s 里 %s 仍读 v1 族 —— 新装环境会拿到与存量环境不同的视图形状", rel, viewName)
			}
		}
		for i := 1; i < len(bodies); i++ {
			if bodies[i] != bodies[0] {
				t.Errorf("%s 的定义体在 4 份文件之间**不一致**（第 %d 份不同）—— "+
					"新装与存量环境会分叉", viewName, i+1)
			}
		}
	}

	// 两份 01-schema.sql 必须逐字节相同
	canonical := mig884Read(t, root, filepath.Join("sql", "schema", "01-schema.sql"))
	baseline := mig884Read(t, root, filepath.Join("deploy", "sql", "schemas", "baseline", "01-schema.sql"))
	if canonical != baseline {
		t.Errorf("sql/schema/01-schema.sql 与 deploy/sql/schemas/baseline/01-schema.sql 不一致 —— " +
			"新装与存量环境会分叉")
	}
}

// TestMigration884_EmbeddataCopyIdentical 钉 ②。
func TestMigration884_EmbeddataCopyIdentical(t *testing.T) {
	root := mig884Tree(t)
	for _, name := range []string{mig884Name, mig884DownName} {
		canonical := mig884Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embedded := mig884Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canonical != embedded {
			t.Errorf("%s 的 canonical 与 installer embeddata 两份不一致 —— 只改一份会让装出来的库与仓库分叉", name)
		}
	}
}

// TestMigration884_RegisteredInDeploymentSequence 钉 ③。
func TestMigration884_RegisteredInDeploymentSequence(t *testing.T) {
	root := mig884Tree(t)

	mainGo := mig884Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	if !strings.Contains(mainGo, "embeddata/startup/"+mig884Name) {
		t.Errorf("installer main.go 缺少 884 的 go:embed 指令")
	}
	if !strings.Contains(mainGo, `"startup/`+mig884Name+`"`) {
		t.Errorf("installer main.go 的 startup map 缺少 884 的条目")
	}

	runner := mig884Read(t, root, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runner, mig884Name) {
		t.Errorf("installer dbinit runner.go 的 StartupFiles 缺少 884")
	}

	channel := mig884Read(t, root, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	if !strings.Contains(channel, "sql/migrations/startup/"+mig884Name) {
		t.Errorf("部署通道 apply-db-revision-sequence.sh 的 files=() 缺少 884")
	}
}

// TestMigration883_PostConditionUsesPreciseMatcher 钉住**上一轮自己**引入的坏仪器：
// 883 的 POST_CONDITION 里那条 `LIKE '%_request_logs%'` 已被订正为 strpos。
// 本门防止它再回来。
func TestMigration883_PostConditionUsesPreciseMatcher(t *testing.T) {
	root := mig884Tree(t)
	up := mig884Read(t, root, filepath.Join("sql", "migrations", "startup", "883_session_family_cost_view_metrics.sql"))

	// ⚠ 分两处查，理由不同：
	//   ① **可执行部分**（剥掉 `--` 注释行）里**不许**出现坏写法
	//      `LIKE '%_request_logs%'`（注释里可以出现——883 的注释**故意**写了
	//      那条坏写法的原文当反面教材，按整份文件 grep 会命中它 ⇒ 假阳性）；
	//   ② **全文**里必须出现好写法（POST_CONDITION 整块**就是注释**，
	//      它是给人在库里手跑的自检清单 ⇒ 剥注释会把它一起剥掉）。
	var code []string
	for _, line := range strings.Split(up, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		code = append(code, line)
	}
	codeText := strings.Join(code, "\n")

	if strings.Contains(codeText, "LIKE '%_request_logs%'") {
		t.Errorf("883 的**可执行部分**还在用 `LIKE '%%_request_logs%%'` —— " +
			"LIKE 的 `_` 是单字符通配符 ⇒ 会把 canonical 视图名 request_logs_with_current_month 也判成命中 " +
			"（上一轮据此多算了 2 张假阳性视图）")
	}
	for _, want := range []string{
		"strpos(pg_get_viewdef('public.customer_cost_view'::regclass, true), 'request_logs') > 0",
		"strpos(pg_get_viewdef('public.model_cost_per_task_view'::regclass, true), 'request_logs') > 0",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("883 的 POST_CONDITION 缺少精确判定 %q", want)
		}
	}
}
