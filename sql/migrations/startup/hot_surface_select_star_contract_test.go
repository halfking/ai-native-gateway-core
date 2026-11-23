package startup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 跨面 SELECT * 禁令（2026-10-06，审计 §10.48；★ 2026-11-24 L34 八轮升级口径 ★）
//
// 背景：`X_hot` 与其分区父表 `X` 是两张**独立演进的物理表**，`UNION ALL`
// 两侧靠**位置**对齐。§10.46 实测二者的列序确实不同：
//
//	session_turns_hot 106 列 vs session_turns 106 列 → 45 个位置的名字不同
//	request_logs_hot 156 列 vs request_logs 154 列 → 66 个位置的名字不同
//	usage_ledger_hot  25 列 vs usage_ledger  20 列 →  6 个位置的名字不同
//
// 所以一旦有人写出 `SELECT * FROM x_hot UNION ALL SELECT * FROM x`，
// 正确性就完全押在「两侧列序恰好相同」这个从未被任何门记录的巧合上。
//
// 本门把该巧合从「约定」降级为「事实检查」：新建的跨面视图必须逐列枚举。
// 已存在的历史写法进 legacyCrossSurfaceSelectStar 登记表钉死，并由
// TestLegacyCrossSurfaceSelectStarRegistryIsExact 反向守住，防止登记表
// 空转或写错名字后让本门恒绿。
//
// ★★★ ★★★ L34 八轮的口径升级（★ 本轮唯一的自主动作 ★）★★★ ★★★
//
// ★★ 原实现有两处**口径洞**，★ 本轮实测坐实并修掉：★★
//
// ★★ 洞①（★ 检测器是【名字模式型】、★ 不是【形状型】 ★★）★★
//   ★ 原检测器靠 `_hot` 后缀识别「跨面」⇒ ★★ **只拦 hot↔parent 一种命名** ★★
//   ★ ⇒ 全仓形状型扫描实测：**31 条**语句／**17 个文件**／**11 个视图名**是跨面
//   ★   `SELECT *` 并集，★ 而其中 **11 条**（`340`×8、`342`×7、`341.down`×1）
//   ★   **一个 `_hot` 都没有** ⇒ ★★ **原检测器对它们完全失明** ★★
//   ★ ⇒ ★★ **更要命的是前瞻性**：★ 下一条**新写的**、★ 不带 `_hot` 的跨面
//   ★   `SELECT *` 并集 ⇒ ★★ **原检测器永远看不见 ⇒ 静默通过** ★★
//
// ★★ 洞②（★ 扫描根不全 ⇒ 一整个迁移目录从未被扫过 ★★）★★
//   ★ 原扫描根只有 `.`（＝`sql/migrations/startup`）与 `../../schema`（＝`sql/schema`）
//   ★ ⇒ ★★ **`sql/migrations/domain/` 整个目录从未被扫过** ★★
//   ★ ⇒ ★★ 而 `sql/migrations/domain/330_hot_partition_union_views.sql` 正是
//   ★   `request_logs_hot ∪ request_logs` 与 `usage_ledger_hot ∪ usage_ledger`
//   ★   两个热面并集 ⇒ ★★ **恰恰是 L34 五/六轮在 211 与 252 实测到列序漂移的那一族** ★★
//
// ★★ 本轮的三处修法 ★★
//   ① **检测器改形状型**：★ 一条语句里 `SELECT *` 的并集分支引用了 **≥2 个不同关系**
//      ⇒ ★★ **与「谁叫什么名字」彻底解耦** ★★
//   ② **扫描根扩全**：★ `..`（＝`sql/migrations` 全子目录）＋ `../../schema`
//      ＋ `../../../installer/cmd/llm-gw-installer/embeddata`（★ 安装器通道的 embeddata ★）
//   ③ **登记表升级为【语句级】**（★ 本轮最关键的一处 ★）：★
//      ★★ 原登记表键是**视图名** ⇒ ★★ **同名不同形会互相借名过关** ★★
//      ★★ （★ `340`／`342`／`330` 建的视图名与登记条目**完全相同** ★★）
//      ★★ ⇒ 本轮键改为 **`<迁移文件 basename>|<视图名>`**、★ 值改为
//      ★★ **分支关系序列**（★ 顺序＝UNION ALL 分支顺序 ★★）★★
//      ★★ ⇒ ★★ **「同名不同形」再也借不了名**、★ 且**分支集合被逐字钉死** ★★
//
// ★★★ 登记的是**迁移文件里的语句**（★ 一次性应用、已被后续显式定义取代 ★），
// ★★★ 不是活的视图。生产侧这些视图的**运行时列序**核验由
// ★★★ `scripts/checks/legacy-select-star-column-order-gate.sh` 负责（★ 只读 ★★），
// ★★★ 而**那道门与本门共用这同一张登记表** ⇒ ★★ **两张门在结构上不可能分叉** ★★。

// legacyCrossSurfaceSelectStar 是**语句级**登记表。
//
// ★ 键：`<迁移文件 basename>|<视图名>`（★ 用 basename ⇒ 安装器 embeddata 的
//
//	同一份拷贝与 `sql/migrations/startup` 的原件天然同键 ★）
//
// ★ 值：**分支关系序列**（★ 逗号分隔、★ 顺序＝UNION ALL 的分支顺序 ★★）
//
//	★★ 这个值不只是说明、★ **它同时是判据**：★★
//	★★   · 正向门：★ 登记值的分支序列必须与文件里**逐字相同** ★★
//	★★     ⇒ 有人改了分支（★ 例如把 `*_default` 换成 `*_2026_08` ★）
//	★★       而登记表没跟上 ⇒ ★★ **当场报红**（★ 形状被改过 ⇒ 风险面变了 ★★）
//	★★   · 反向门：★ 每条登记必须真的命中一条语句 ★★
//	★★   · 运行时门：★ `legacy-select-star-column-order-gate.sh` 直接读这份分支
//	★★     序列去核列序 ⇒ ★★ **它不需要自己解析 SQL**（★ 少一份解析 ⇒ 少一处分叉 ★★）
//
// ★★ 本表由 `scripts/audit/` 外的**一次性生成器**产出后人工核对（★ 30 条 ★★）；
// ★★ 任何增删改都**必须**让 `TestLegacyCrossSurfaceSelectStarRegistryIsExact`
// ★★ 与 `TestHotSurfaceViewsMustEnumerateColumns` 同时绿 ⇒ ★★ **表与仓永远一致** ★★。
var legacyCrossSurfaceSelectStar = map[string]string{
	// ★ sql/migrations/domain/330（★ 热面并集；★ ★★ 该文件过去**根本不在扫描根里** ★★）★★
	"330_hot_partition_union_views.sql|request_logs_with_current_month": "request_logs_hot,request_logs",
	"330_hot_partition_union_views.sql|usage_ledger_with_current_month": "usage_ledger_hot,usage_ledger",
	// ★ sql/migrations/startup/340（★ ★★ 三路：★ 父表 ∪ 当月分区 ∪ default ★★）★★
	"340_create_partition_query_views.sql|credential_model_index_with_current_month": "credential_model_index,credential_model_index_2026_07,credential_model_index_default",
	"340_create_partition_query_views.sql|credit_ledger_with_current_month":          "credit_ledger,credit_ledger_2026_07,credit_ledger_default",
	"340_create_partition_query_views.sql|request_logs_bodies_with_current_month":    "request_logs_bodies,request_logs_bodies_2026_07,request_logs_bodies_default",
	"340_create_partition_query_views.sql|request_logs_with_current_month":           "request_logs,request_logs_2026_07,request_logs_default",
	"340_create_partition_query_views.sql|request_wal_with_current_month":            "request_wal,request_wal_2026_07,request_wal_default",
	"340_create_partition_query_views.sql|routing_decision_log_with_current_month":   "routing_decision_log,routing_decision_log_2026_07,routing_decision_log_default",
	"340_create_partition_query_views.sql|tool_usage_stats_with_current_month":       "tool_usage_stats,tool_usage_stats_2026_07,tool_usage_stats_default",
	"340_create_partition_query_views.sql|usage_ledger_with_current_month":           "usage_ledger,usage_ledger_2026_07,usage_ledger_default",
	"341_hot_table_independence.down.sql|request_logs_with_current_month":            "request_logs,request_logs_default",
	"341_hot_table_independence.sql|request_logs_with_current_month":                 "request_logs_hot,request_logs",
	// ★ sql/migrations/startup/342（★ 两路：★ 父表 ∪ 当月分区 ★★）
	"342_create_other_table_views.sql|credential_model_index_with_current_month": "credential_model_index,credential_model_index_2026_07",
	"342_create_other_table_views.sql|credit_ledger_with_current_month":          "credit_ledger,credit_ledger_2026_07",
	"342_create_other_table_views.sql|request_logs_bodies_with_current_month":    "request_logs_bodies,request_logs_bodies_2026_07",
	"342_create_other_table_views.sql|request_wal_with_current_month":            "request_wal,request_wal_2026_07",
	"342_create_other_table_views.sql|routing_decision_log_with_current_month":   "routing_decision_log,routing_decision_log_2026_07",
	"342_create_other_table_views.sql|tool_usage_stats_with_current_month":       "tool_usage_stats,tool_usage_stats_2026_07",
	"342_create_other_table_views.sql|usage_ledger_with_current_month":           "usage_ledger,usage_ledger_2026_07",
	// ★ sql/migrations/startup/34x-35x／386（★ 热面 ∪ 父表 ★★）
	"344_usage_ledger_hot_independence.sql|usage_ledger_with_current_month":                      "usage_ledger_hot,usage_ledger",
	"345_request_wal_hot_independence.sql|request_wal_with_current_month":                        "request_wal_hot,request_wal",
	"346_routing_decision_log_hot_independence.sql|routing_decision_log_with_current_month":      "routing_decision_log_hot,routing_decision_log",
	"347_credential_model_index_hot_independence.sql|credential_model_index_with_current_month":  "credential_model_index_hot,credential_model_index",
	"348_tool_usage_stats_hot_independence.sql|tool_usage_stats_with_current_month":              "tool_usage_stats_hot,tool_usage_stats",
	"349_credit_ledger_hot_independence.sql|credit_ledger_with_current_month":                    "credit_ledger_hot,credit_ledger",
	"353_request_logs_bodies_hot_independence.sql|request_logs_bodies_with_current_month":        "request_logs_bodies_hot,request_logs_bodies",
	"354_credential_model_index_hot_independence.sql|credential_model_index_with_current_month":  "credential_model_index_hot,credential_model_index",
	"386_model_probe_runs_hot_independence.sql|model_probe_runs_with_current_month":              "model_probe_runs_hot,model_probe_runs",
	"392_candidate_failure_logs_monthly_partition.sql|candidate_failure_logs_with_current_month": "candidate_failure_logs_hot,candidate_failure_logs",
	"625_session_bodies_unified_explicit.down.sql|session_bodies_unified":                        "session_bodies_hot,session_bodies",
}

var (
	reSelectStar        = regexp.MustCompile(`(?i)select\s+\*`)
	reUnionAll          = regexp.MustCompile(`(?i)\bunion\s+all\b`)
	reCreateView        = regexp.MustCompile(`(?i)create\s+(or\s+replace\s+)?view\s+([a-z0-9_.]+)`)
	reSelectStarFromRel = regexp.MustCompile(`(?i)select\s+\*\s+from\s+([a-z0-9_.]+)`)
	reLineComment       = regexp.MustCompile(`--[^\n]*`)
	reBlockCmt          = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// ★★ 扫描根（★ L34 八轮扩全 ★★）★★
// ★   `..` ＝ `sql/migrations`（★ **含 domain/、local/、operations/ 等全部子目录** ★）
// ★   `../../schema` ＝ `sql/schema`
// ★   `../../../installer/cmd/llm-gw-installer/embeddata` ＝ 安装器通道的 embeddata
var crossSurfaceScanRoots = []string{
	"..",
	filepath.Join("..", "..", "schema"),
	filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata"),
}

// sqlWithoutComments 把注释剥掉，否则注释里的 `SELECT *` 会把门变成恒假
// （§10.46 的 625 与其 down 文件都因为注释命中而被误判过）。
func sqlWithoutComments(src string) string {
	src = reBlockCmt.ReplaceAllString(src, " ")
	return reLineComment.ReplaceAllString(src, " ")
}

// crossSurfaceStatement 是一条被判定为「跨面 `SELECT *` 并集」的语句。
type crossSurfaceStatement struct {
	key   string // "<basename>|<view>"
	view  string
	rels  []string // 分支关系、★ 顺序＝出现顺序 ★
	paths []string // 同键语句所在的路径（★ 供报错时点名 ★）
}

// branchRels 取语句里 `SELECT * FROM <rel>` 的关系序列（★ 顺序＝UNION ALL 分支顺序 ★★）。
func branchRels(stmt string) []string {
	rels := []string{}
	for _, m := range reSelectStarFromRel.FindAllStringSubmatch(stmt, -1) {
		rel := m[1]
		rel = strings.TrimPrefix(rel, "public.")
		dup := false
		for _, seen := range rels {
			if seen == rel {
				dup = true
				break
			}
		}
		if !dup {
			rels = append(rels, rel)
		}
	}
	return rels
}

// crossSurfaceSelectStar 返回「语句键 → 语句」的集合。
//
// ★★ 口径（★ L34 八轮 ★★）：★★
// ★★   一条语句满足「**含 `SELECT *`**」＋「**含 `UNION ALL`**」＋
// ★★   「**分支引用了 ≥2 个不同关系**」⇒ ★★ **它就是一条跨面 `SELECT *` 并集** ★★
// ★★ ⇒ ★★ **与关系叫什么名字完全无关**（★ ★★ 这是对原「`_hot` 名字型检测器」的根本修正 ★★）★★
func crossSurfaceSelectStar(t *testing.T) map[string]*crossSurfaceStatement {
	t.Helper()
	return crossSurfaceSelectStarIn(t, crossSurfaceScanRoots)
}

// crossSurfaceSelectStarIn 是 detector 的可测形态（★ 扫描根可注入 ★★）★★
// ★★ ⇒ ★★ 门可以**在自己的临时目录里**造样本、★ 对自己的判定做断言 ★★
// ★★ ⇒ ★★ **不必改仓内任何文件**（★ ★★ 「变异臂去改仓内文件」这种做法 ★★
// ★★   ★ **在 CI 上危险**（★ 中途被打断 ⇒ ★ 仓里留下一个坏迁移 ★★）★★）
func crossSurfaceSelectStarIn(t *testing.T, roots []string) map[string]*crossSurfaceStatement {
	t.Helper()
	found := map[string]*crossSurfaceStatement{}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				switch info.Name() {
				case ".git", "vendor", "node_modules":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".sql") {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read %s: %v", path, readErr)
			}
			base := filepath.Base(path)
			for _, stmt := range strings.Split(sqlWithoutComments(string(raw)), ";") {
				if !reSelectStar.MatchString(stmt) || !reUnionAll.MatchString(stmt) {
					continue
				}
				rels := branchRels(stmt)
				if len(rels) < 2 {
					continue
				}
				name := "(anonymous)"
				if m := reCreateView.FindStringSubmatch(stmt); m != nil {
					name = m[2]
				}
				// 去掉 schema 前缀，否则 `public.x` 与登记表里的 `x` 对不上
				// （§10.48 首次运行即因此误报，且反向门同时报「登记条目失配」）。
				if i := strings.LastIndex(name, "."); i >= 0 {
					name = name[i+1:]
				}
				key := base + "|" + name
				want := strings.Join(rels, ",")
				if prev, ok := found[key]; ok {
					// ★★ 同一个键（★ basename + 视图名 ★）在不同路径上形状必须一致 ★★
					// ★★ （★ 安装器 embeddata 的拷贝 vs `sql/migrations/startup` 的原件 ★★）
					if strings.Join(prev.rels, ",") != want {
						t.Errorf("同一键 %q 出现**两种形状** ⇒ ★★ 表与仓已经分叉 ★★：\n"+
							"  %s ⇒ %s\n  %s ⇒ %s\n"+
							"★ 请把两处改到一致，★ 或拆成两个登记条目。",
							key, strings.Join(prev.paths, " / "), strings.Join(prev.rels, ","),
							path, want)
					}
					prev.paths = append(prev.paths, path)
					continue
				}
				found[key] = &crossSurfaceStatement{
					key:   key,
					view:  name,
					rels:  rels,
					paths: []string{path},
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return found
}

func TestHotSurfaceViewsMustEnumerateColumns(t *testing.T) {
	found := crossSurfaceSelectStar(t)
	for key, stmt := range found {
		want, ok := legacyCrossSurfaceSelectStar[key]
		if !ok {
			t.Errorf("跨面视图 %s（★ 文件 %s ★）使用了 SELECT * UNION ALL —— ★★ 它的分支是"+
				"**%d 个不同关系**（%s）★★ ★★ 分支之间靠**位置**对齐、★★ 而各分支的列序"+
				"实测会随各自 ALTER ADD COLUMN 的先后而漂移（§10.46）。请逐列枚举，勿依赖列序巧合。\n"+
				"★ 若这是**已知的历史写法**、★ 请登记进 legacyCrossSurfaceSelectStar"+
				"（★ 键＝「<迁移文件 basename>|<视图名>」、★ 值＝分支关系序列 ★★）。",
				stmt.view, strings.Join(stmt.paths, " / "), len(stmt.rels),
				strings.Join(stmt.rels, ","))
			continue
		}
		got := strings.Join(stmt.rels, ",")
		if got != want {
			t.Errorf("登记条目 %q 的**分支序列**与文件里的实际写法不一致 ⇒ ★★ 形状被改过、★★ "+
				"而登记表没跟上 ⇒ ★★ 风险面变了却没人知道 ★★：\n  登记：%s\n  实际：%s\n"+
				"★ 请把登记表改成与文件一致（★ 或把文件改回登记的形状 ★）。",
				key, want, got)
		}
	}
}

// 反向门：登记表里每一条都必须真的命中，否则登记表会因改名/删除而空转，
// 让上面的门在「实际仍有违规」时也报绿。
func TestLegacyCrossSurfaceSelectStarRegistryIsExact(t *testing.T) {
	found := crossSurfaceSelectStar(t)
	for key := range legacyCrossSurfaceSelectStar {
		if _, ok := found[key]; !ok {
			t.Errorf("登记表条目 %q 在仓内已无对应写法（★ 迁移被删、★ 被改写、★ 或扫描根漏了 ★）—— "+
				"请确认后删除该条目，否则本门失去意义", key)
		}
	}
}

// ★★★ ★★★ 门**自证**（★ L34 八轮新增 ★★★ ★★★）★★★ ★★★
//
// ★★ 为什么必须自证（★ 本轮的核心教训 ★★）★★
//
//	★ 原检测器是**名字模式型**（★ 靠 `_hot` 后缀 ★）⇒ ★ 它**漏掉**了一整族
//	★   不含 `_hot` 的跨面写法（★ `340`／`342`／`341.down` 共 11 条 ★★）
//	★ ⇒ ★★ **而这件事在门自己的输出里看不出来**（★ 门报绿、★ 而它其实没在看 ★★）★★
//	★ ⇒ ★★ **「门能咬住东西」必须被证明、★ 而不能被假定** ⇒ ★★ **⇒ 本测试 ★★**
//
// ★★ 本测试在**临时目录**里造样本（★ t.TempDir ★★）⇒ ★★
// ★★   ★ **不改仓内任何文件**（★ ★★ CI 上改仓内迁移文件是危险的 ★★）★★
// ★★   ★ **不依赖 PG**、★ 不依赖 docker、★ 不依赖网络 ★★
func TestCrossSurfaceDetectorIsShapeBased(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	// ★ 臂 1：**不含 `_hot`** 的跨面并集 ⇒ ★★ 原检测器对它**完全失明** ★★
	write("non_hot.sql", `
CREATE OR REPLACE VIEW v_non_hot AS
SELECT * FROM sessions
UNION ALL
SELECT * FROM session_summaries;
`)
	// ★ 臂 2：hot ∪ parent（★ 老口径唯一认得的那一族 ★）
	write("hot_pair.sql", `
CREATE OR REPLACE VIEW v_hot_pair AS
SELECT * FROM usage_ledger_hot
UNION ALL
SELECT * FROM usage_ledger;
`)
	// ★ 臂 3：**三路**（★ 父表 ∪ 月分区 ∪ default ★★）⇒ ★ 老口径看不见
	write("three_way.sql", `
CREATE OR REPLACE VIEW v_three_way AS
SELECT * FROM request_logs
UNION ALL
SELECT * FROM request_logs_2026_07
UNION ALL
SELECT * FROM request_logs_default;
`)
	// ★ 臂 4：**同一个关系**的并集 ⇒ ★ 不是跨面（★ 分支只有一个不同关系 ★★）
	write("same_rel.sql", `
CREATE OR REPLACE VIEW v_same_rel AS
SELECT * FROM sessions
UNION ALL
SELECT * FROM sessions;
`)
	// ★ 臂 5：注释里的写法 ⇒ ★ 剥掉注释后**不算**（★ §10.46 的老坑 ★★）
	write("commented.sql", `
-- SELECT * FROM a UNION ALL SELECT * FROM b
CREATE OR REPLACE VIEW v_clean AS
SELECT id FROM sessions;
`)

	found := crossSurfaceSelectStarIn(t, []string{dir})

	// ★★ 臂 1／2／3 都要在 ★★
	for _, key := range []string{
		"non_hot.sql|v_non_hot",
		"hot_pair.sql|v_hot_pair",
		"three_way.sql|v_three_way",
	} {
		if _, ok := found[key]; !ok {
			t.Errorf("★ 检测器漏掉了 %q ⇒ ★★ **形状型检测没生效** ★★（★ ★★ 这一族在原「`_hot` 名字型」口径下是看不见的 ★★）★★", key)
		}
	}
	// ★★ 臂 4／5 都不该在 ★★
	for _, key := range []string{
		"same_rel.sql|v_same_rel",
		"commented.sql|v_clean",
	} {
		if _, ok := found[key]; ok {
			t.Errorf("★ 检测器把 %q 算成了跨面 ⇒ ★★ **误报** ★★（★ 同一关系不跨面 / 注释不算 ★★）", key)
		}
	}
	// ★★ 三路那条的**分支序列**必须**逐字**对得上（★ 顺序＝UNION ALL 顺序 ★★）★★
	if st, ok := found["three_way.sql|v_three_way"]; ok {
		want := "request_logs,request_logs_2026_07,request_logs_default"
		if got := strings.Join(st.rels, ","); got != want {
			t.Errorf("★ 三路分支序列应是 %q、★ 实得 %q ⇒ ★★ **顺序或集合错了** ★★", want, got)
		}
	}
}

// ★★★ 门**自证**之二：★ 台账的「键 + 分支序列」必须能钉住形状 ★★★
// ★★ ⇒ ★★ 「同名不同形」再也借不了名 ★★
func TestLegacyRegistryPinsBranchShape(t *testing.T) {
	// ★ 台账里每一条的值都必须**至少两个分支**（★ 那是「跨面并集」的定义 ★★）
	for key, val := range legacyCrossSurfaceSelectStar {
		rels := strings.Split(val, ",")
		if len(rels) < 2 {
			t.Errorf("★ 台账条目 %q 的分支序列只有 %d 个（%q）⇒ ★★ 那不是跨面并集 ★★", key, len(rels), val)
		}
		seen := map[string]bool{}
		for _, r := range rels {
			r = strings.TrimSpace(r)
			if r == "" {
				t.Errorf("★ 台账条目 %q 的分支序列里有**空分支**（%q）⇒ ★★ 解析面会踩空 ★★", key, val)
				continue
			}
			if seen[r] {
				t.Errorf("★ 台账条目 %q 的分支序列里**重复**了 %q ⇒ ★★ 那不是并集 ★★", key, r)
			}
			seen[r] = true
		}
	}
	// ★★ 键必须是 `<basename>|<视图名>`（★ **同名不同形靠它区分** ★★）★★
	for key := range legacyCrossSurfaceSelectStar {
		parts := strings.SplitN(key, "|", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			t.Errorf("★ 台账键 %q 不是「<迁移文件 basename>|<视图名>」形态 ⇒ ★★ 「同名不同形」会借名过关 ★★", key)
			continue
		}
		if !strings.HasSuffix(parts[0], ".sql") {
			t.Errorf("★ 台账键 %q 的文件部分不是 .sql（%q）⇒ ★★ 键的形态走偏了 ★★", key, parts[0])
		}
	}
}
