package schema

// baseline_view_refs_test.go —— **基线自洽门**：基线里的每个 `CREATE VIEW`
// 都不许引用「基线自己不创建的关系」。
//
// # 为什么需要这道门（2026-10-20 实测事故）
//
// 迁移 883 / 884 把 5 张视图的**部署形态**从 v1 族改读了会话族
// （`session_turns_hot` / `session_turns` / `session_turn_details*`），
// 并**顺手把基线 schema 也改了** ⇒ 基线里出现了
//
//	FROM public.session_turns_hot ...
//	FROM public.session_turn_details_hot ...
//
// 而这些关系**基线并不创建**：它们由**启动序列**的
// `session_turns_hot_bootstrap.sql`（`CREATE TABLE IF NOT EXISTS public.session_turns_hot`）
// 与迁移 734 建 ⇒ **基线时点还不存在**。
//
// ⇒ 基线是**逐语句**应用的，而 `CREATE VIEW` 会**急切校验**它引用的关系
// ⇒ 新装环境在这里报
//
//	ERROR:  relation "public.session_turns_hot" does not exist
//	LINE 24:            FROM public.session_turns_hot st
//
// 并**中止整个安装**（真库临时库实测，2026-10-20）。
// ⇒ 而**升级路径是绿的**（那些表早就存在）⇒
// **「升级路径绿」≠「新装路径绿」** —— 这正是本门要拦的东西。
//
// # 修法（照 canonical 视图的既有先例）
//
// 4 份基线副本一律保留 **v1 fallback 体**（基线自建 `request_logs` /
// `request_logs_hot` ⇒ 该体在基线时点有效），**部署形态由迁移 883 / 884 换上**
// ⇒ 两条路径（全新安装 / 存量升级）跑完启动序列后形态一致。
//
// # 本门怎么判
//
// ① 收集基线里**创建**的对象（TABLE / VIEW / MATERIALIZED VIEW / SEQUENCE /
//    TYPE / FUNCTION / PROCEDURE / DOMAIN / EXTENSION / FOREIGN TABLE，
//    允许 `OR REPLACE` / `IF NOT EXISTS` / `UNLOGGED`）。
// ② 逐个 `CREATE VIEW` 的定义体，抓 `FROM` / `JOIN` 后面的关系名。
// ③ 排除 **CTE**（`WITH x AS (...)` / `, x AS (...)`）—— CTE 不是关系。
// ④ 每个被引用的关系**必须**在 ① 里 ⇒ 否则报出来。
//
// ⚠ 这道门是**静态**的（不建库）；它拦的是「引用了基线不创建的关系」这一类
// 必然导致新装失败的问题。真正的端到端验证（临时库跑一遍基线）是 opt-in 的
// 手工步骤，本门是它的**常驻替身**。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// baselineCopiesReuse 与 baseline_ordering_test.go 里的 copies 同源：
// 三份基线副本（新装实际用的是 installer 那份）。
var baselineViewRefCopies = []struct {
	name string
	path string
}{
	{"canonical", "01-schema.sql"},
	{"installer-embeddata", "../../installer/cmd/llm-gw-installer/embeddata/01-schema.sql"},
	{"deploy-baseline", "../../deploy/sql/schemas/baseline/01-schema.sql"},
}

var (
	baselineCreateRe = regexp.MustCompile(`(?im)^CREATE\s+(?:OR\s+REPLACE\s+)?(?:UNLOGGED\s+|TEMP\s+|TEMPORARY\s+)?` +
		`(TABLE|VIEW|MATERIALIZED\s+VIEW|SEQUENCE|TYPE|FUNCTION|PROCEDURE|DOMAIN|EXTENSION|FOREIGN\s+TABLE)\s+` +
		`(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_."]+)`)

	// ⚠ 三个标志缺一不可：`m` 让 `^` 逐行锚定（基线里视图定义不���文件开头），
	// `s` 让 `.` 跨行（视图体是多行 SQL），`i` 忽略大小写。
	//   （2026-10-20 实测：漏 `m` ⇒ 匹配数 0 ⇒ 门会「安静通过」——
	//    与「没有违规」不可区分，典型假绿。故本门自带「扫到 0 个视图就 Fatal」。）
	baselineViewRe = regexp.MustCompile(`(?ims)^CREATE\s+VIEW\s+([A-Za-z0-9_."]+)\s+AS\n(.*?);\n`)

	baselineRelRefRe = regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+(?:public\.)?([A-Za-z0-9_]+)`)

	// CTE 名字：`WITH x AS (` 或 `, x AS (`
	baselineCTERe = regexp.MustCompile(`(?is)(?:\bWITH|,)\s*([A-Za-z0-9_]+)\s+AS\s*\(`)
)

// baselineSQLKeywordNotRelations：出现在 FROM/JOIN 之后但**不是关系**的词。
var baselineSQLKeywordNotRelations = map[string]bool{
	"select": true, "lateral": true, "values": true, "only": true,
	"generate_series": true, "unnest": true, "pg_catalog": true, "public": true,
}

func baselineViewRefRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestBaselineViewsDoNotReferenceUncreatedRelations —— 基线自洽门。
func TestBaselineViewsDoNotReferenceUncreatedRelations(t *testing.T) {
	for _, copy := range baselineViewRefCopies {
		t.Run(copy.name, func(t *testing.T) {
			text := baselineViewRefRead(t, copy.path)

			// ① 基线创建了哪些对象
			created := map[string]bool{}
			for _, m := range baselineCreateRe.FindAllStringSubmatch(text, -1) {
				kind := strings.ToUpper(strings.Join(strings.Fields(m[1]), " "))
				if kind == "INDEX" || kind == "AGGREGATE" {
					continue
				}
				name := strings.ReplaceAll(m[2], `"`, "")
				if i := strings.LastIndex(name, "."); i >= 0 {
					name = name[i+1:]
				}
				created[strings.ToLower(name)] = true
			}
			if len(created) == 0 {
				t.Fatalf("采集到 0 个已创建对象 —— 解析器坏了，本门会安静通过（空集合同样满足「没有违规」）")
			}

			// ② 逐个视图查引用
			var violations []string
			viewCount := 0
			for _, m := range baselineViewRe.FindAllStringSubmatch(text, -1) {
				viewCount++
				viewName := strings.ReplaceAll(m[1], `"`, "")
				body := m[2]

				// ③ 排除该视图体内的 CTE 名
				ctes := map[string]bool{}
				for _, c := range baselineCTERe.FindAllStringSubmatch(body, -1) {
					ctes[strings.ToLower(c[1])] = true
				}

				seen := map[string]bool{}
				for _, r := range baselineRelRefRe.FindAllStringSubmatch(body, -1) {
					rel := strings.ToLower(r[1])
					if baselineSQLKeywordNotRelations[rel] || ctes[rel] || seen[rel] {
						continue
					}
					seen[rel] = true
					if !created[rel] {
						violations = append(violations, viewName+" → "+rel)
					}
				}
			}
			if viewCount == 0 {
				t.Fatalf("扫到 0 个视图 —— 解析器坏了，本门会安静通过（0 个视图自然没有违规）")
			}

			if len(violations) > 0 {
				t.Errorf("%s：%d 个基线视图引用了「基线自己不创建的关系」：\n  %s\n"+
					"  ⇒ `CREATE VIEW` 会**急切校验**它引用的关系 ⇒ 新装环境会在此报 "+
					"42P01 并**中止整个安装**（2026-10-20 真库临时库实测）。\n"+
					"  ⇒ 这些关系由**启动序列**建（session_turns_hot_bootstrap.sql / 迁移 734），"+
					"基线时点还不存在 ⇒ **基线必须保留 v1 fallback 体**，部署形态交给迁移换上。",
					copy.name, len(violations), strings.Join(violations, "\n  "))
			}
		})
	}
}

// TestBaselineCostAndAnalyticsViewsKeepFallbackBody —— 5 张视图的**基线副本**
// 必须是 v1 fallback 体（读 v1 族），且**部署形态**由迁移 883 / 884 换上。
//
// 这条把「基线时点有效」与「部署形态正确」两件事**分开钉**：
//
//	· 基线副本读 v1 族（基线自建 request_logs ⇒ 有效）；
//	· 迁移体读会话族（部署形态 ⇒ 活指标不再读冻结族）。
//
// ⇒ 两者的**衔接点**是「启动序列会跑这些迁移」（由 migration_883/884 的
// 登记门钉）。
func TestBaselineCostAndAnalyticsViewsKeepFallbackBody(t *testing.T) {
	views := []string{
		"customer_cost_view",
		"model_cost_per_task_view",
		"v_continuation_effectiveness",
		"v_node_switch_analysis",
		"v_timeout_effectiveness",
	}
	for _, copy := range baselineViewRefCopies {
		t.Run(copy.name, func(t *testing.T) {
			text := baselineViewRefRead(t, copy.path)
			for _, v := range views {
				marker := "CREATE VIEW public." + v
				i := strings.Index(text, marker)
				if i < 0 {
					t.Errorf("%s 里没有 %s 的定义体", copy.name, v)
					continue
				}
				rest := text[i:]
				j := strings.Index(rest, ";\n")
				if j < 0 {
					t.Errorf("%s 里 %s 的定义体没有以 ';' 结束", copy.name, v)
					continue
				}
				body := rest[:j]
				if strings.Contains(body, "session_turns_hot") || strings.Contains(body, "session_turn_details") {
					t.Errorf("%s 里 %s 引用了 session_turns_hot / session_turn_details —— "+
						"基线不创建它们（由启动序列建）⇒ 新装会报 42P01。基线必须保留 v1 fallback 体",
						copy.name, v)
				}
				if !strings.Contains(body, "request_logs") {
					t.Errorf("%s 里 %s 既不是 v1 fallback 体也没有会话族读源 —— 形状不明", copy.name, v)
				}
			}
		})
	}
}

// TestBaselineSchemaCopiesAreByteIdentical —— canonical 与 deploy-baseline
// 两份必须逐字节相同（仓库既有约定；新装与存量环境的形状来源要对齐）。
func TestBaselineSchemaCopiesAreByteIdentical(t *testing.T) {
	canonical := baselineViewRefRead(t, filepath.Join(".", "01-schema.sql"))
	baseline := baselineViewRefRead(t, filepath.Join("..", "..", "deploy", "sql", "schemas", "baseline", "01-schema.sql"))
	if canonical != baseline {
		t.Errorf("sql/schema/01-schema.sql 与 deploy/sql/schemas/baseline/01-schema.sql 不一致 —— " +
			"新装与存量环境会分叉")
	}
}
