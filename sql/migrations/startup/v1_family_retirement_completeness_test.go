package startup

// v1_family_retirement_completeness_test.go —— 2026-10-27「v1 族退役完备性审计轮」。
//
// # 这道门在守什么
//
// 迁移 890 把 v1 `request_logs` 族**物理删除**了。已有的门（migration_890_v1_family_drop_test.go）
// 守的是「**890 这一个文件**写对了没有」——它是**单文件**视角。
// ⇒ ★ **它管不到「全仓有没有别的脚本在链外建 v1 表」**。
//
// ⇒ 一个具体缺口（本轮实测）：
//
//	sql/init-complete-minimal.sql        ⇒ CREATE request_logs / request_logs_hot
//	sql/quick-init-request-logs.sql      ⇒ CREATE request_logs_hot
//	sql/fixes/fix-request-logs-bodies-partition-v2.sql ⇒ request_logs_bodies_default
//	scripts/audit/sql/min-prereqs.sql    ⇒ CREATE _request_logs_hot
//	tests/_deprecated_local_poc_.../seed/01-schema-bootstrap.sql ⇒ request_logs / request_logs_default
//
// 这 5 个文件**不在迁移链里** ⇒ 它们建出来的 v1 表**不会被 890 删掉**
// ⇒ 在一个**已经退役过**的库上跑它们 ⇒ v1 族**复活** ⇒ 42P01 的老问题原地复现。
//
// # 四条判据
//
//	① 链内**完备性**：全仓 .sql 建过的每一个 request_log* **表**，
//	   都必须落在 890 的退役清单里（直接 DROP），或是被清单内父表的**分区子表**
//	   （随父表一起删）⇒ 未覆盖数必须 = 0。
//	② **幸存面**：全仓出现过的 request_log* **关系**里，退役清单之外只允许剩下
//	   **一个** —— canonical 视图 `request_logs_with_current_month`
//	   （它不删，因为它是 ~40 个读方的 canonical 读面；删了＝整片面板 500）
//	   ⇒ 且它的视图体必须读会话族。
//	③ **链外台账**：链外建 v1 表的文件必须**全部登记**在下面的退役台账里，
//	   且每个都必须带机器可查的退役标头；台账条目按 scope 追加「谁可以执行它」的约束。
//	④ **真库腿**（可选）：指向一个**已退役**的库时，v1 表必须为 0、canonical 视图必须读会话族。
//
// # 判据纪律（沿用本仓既有规矩）
//
//   · **阳性对照**：① 先断言「扫描器确实看见了已知的 v1 表」，
//     否则「未覆盖 = 0」与「什么都没扫到」同步为真。
//   · **链的定义要写死**：链 = 基线快照 + sql/migrations/startup/ +
//     installer embeddata 孪生副本。判据落在**链外**才叫「链外」。
//   · **不替别的轮改账**：台账条目只登记 + 加标头，**不改**那些脚本的行为。
//
// # 为什么「退役台账」是台账而不是「就地删掉」
//
//   `scripts/audit/sql/min-prereqs.sql` 与 `tests/_deprecated_.../seed/*.sql`
//   是**历史复现夹具**：它们存在的意义就是能造出一个「890 之前的库」，
//   好让 890 之前的迁移能被验证。删了它们 = 删掉复现能力。
//   ⇒ 处置：**登记 + 标头 + 约束「谁可以执行」**，而不是删除。

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// 常量与正则
// ─────────────────────────────────────────────────────────────────────────────

// v1rcMig890 是 v1 族退役的最后一跳。
const v1rcMig890 = "890_drop_request_logs_v1_family.sql"

// v1rcCanonicalView 是**唯一允许幸存**的 request_log 命名关系。
//
// ★ 为什么它不许删：它是 canonical 读面（~40 个读方查它）。
//
//	退役的做法是**换掉视图体**（890 第 2 步），让它读会话族两臂 ——
//	名字保留是为了不动那 40 个读方。
const v1rcCanonicalView = "request_logs_with_current_month"

// v1rcBanner 是退役标头的**机器可查**记号。
//
// ★ 为什么标头要机器可查：一句「这个脚本已退役」写在注释里，
//
//	半年后没人说得清它还该不该跑 ⇒ 让门去查这个记号。
//	格式：`-- V1-FAMILY-RETIRED: <scope> | <一句话理由>`
const v1rcBanner = "-- V1-FAMILY-RETIRED:"

var (
	// v1rcRelRE 认「request_log 家族」的**关系名**：
	// `_request_logs` / `request_logs` 及其下划线后缀。
	v1rcRelRE = regexp.MustCompile(`(?i)^_?request_logs?(_[a-z0-9]+)*$`)

	v1rcCreateTableRE = regexp.MustCompile(`(?i)\bcreate\s+(?:unlogged\s+|temp(?:orary)?\s+|global\s+|local\s+)*table\s+` +
		`(?:if\s+not\s+exists\s+)?(?:only\s+)?(?:public\.)?"?([_a-zA-Z0-9$]+)"?`)

	v1rcCreateViewRE = regexp.MustCompile(`(?i)\bcreate\s+(?:or\s+replace\s+)?(?:materialized\s+)?view\s+` +
		`(?:if\s+not\s+exists\s+)?(?:public\.)?"?([_a-zA-Z0-9$]+)"?`)

	// v1rcPartitionOfRE 认「静态分区子表」：CREATE TABLE <child> PARTITION OF <parent>。
	v1rcPartitionOfRE = regexp.MustCompile(`(?i)\bcreate\s+(?:unlogged\s+|temp(?:orary)?\s+)*table\s+` +
		`(?:if\s+not\s+exists\s+)?(?:public\.)?"?([_a-zA-Z0-9$]+)"?\s+partition\s+of\s+(?:public\.)?"?([_a-zA-Z0-9$]+)"?`)

	// v1rcAttachRE 认「先建独立表、后挂成分区」的形态（基线就是这么干的）：
	// ALTER TABLE <parent> ATTACH PARTITION <child>。
	v1rcAttachRE = regexp.MustCompile(`(?i)alter\s+table\s+(?:only\s+)?(?:public\.)?"?([_a-zA-Z0-9$]+)"?\s+` +
		`attach\s+partition\s+(?:public\.)?"?([_a-zA-Z0-9$]+)"?`)

	// v1rcForeachRE 定位 890 里的 FOREACH 块。
	//
	// ★★ 为什么不用「一条正则把数组和后面的 DROP 动词配对」：
	//   Go 的 regexp 是 **RE2**，**不支持前瞻 `(?!...)`** ⇒ 「正文不许跨到下一个
	//   FOREACH」写不成正则。
	//   ⇒ 而不写这个约束就会踩一个真坑：890 里有**三个** FOREACH 块
	//   （§1 非空闸 / §4 视图 / §5 表），第 1 块的 LOOP 会一路吃到
	//   **§4 的第一个 DROP VIEW** ⇒ §1 的数组被贴上 "VIEW" 标签、
	//   **§4 的数组整个丢失** ⇒ 「4 张 v1-only 视图在清单里」这条事实消失
	//   ⇒ 门会把它们误报成幸存者。
	//   ⇒ 处置：**手工切块**（v1rcParseDropBlocks）—— 块内找 DROP 动词。
	v1rcForeachRE = regexp.MustCompile(`(?is)FOREACH\s+v_name\s+IN\s+ARRAY\s+ARRAY\[`)
	// v1rcDropKindRE 在**单个块内**找 DROP 动词。
	v1rcDropKindRE = regexp.MustCompile(`(?is)EXECUTE\s+format\('DROP\s+(VIEW|TABLE|SEQUENCE)`)
	// v1rcGateBlockRE 抽 890 的非空闸数组（FOREACH ... ARRAY[...] ... RAISE EXCEPTION）。
	v1rcGateBlockRE = regexp.MustCompile(`(?is)FOREACH\s+v_name\s+IN\s+ARRAY\s+ARRAY\[(.*?)\]\s*LOOP.*?RAISE\s+EXCEPTION`)
)

// ─────────────────────────────────────────────────────────────────────────────
// 基础工具
// ─────────────────────────────────────────────────────────────────────────────

// v1rcRepoRoot 返回仓库根（本包在 sql/migrations/startup/ 下，往上三层）。
func v1rcRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

// v1rcStripComments 去掉行注释与块注释。
//
// ⚠ 与同包的 normalizeSQL885 **刻意不共用**：那台助手是给「文本等价」用的
// （会压掉所有空白），而本门要**保留结构**（要按 ARRAY[ ... ] 抽数组）⇒
// 两者的归一化口径不同，共用会把「结构」也压平。
//
// ⚠ 行注释用 `--[^\n]*`：字符串字面量里若含 `--` 会被截断 ——
//
//	本门只关心**关系名**，不关心字面量内容 ⇒ 不影响判据。
func v1rcStripComments(s string) string {
	s = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(s, " ")
	return regexp.MustCompile(`--[^\n]*`).ReplaceAllString(s, " ")
}

// v1rcQuoteNames 从一段 ARRAY[...] 正文里抽出所有单引号名字。
func v1rcQuoteNames(s string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(s, -1) {
		out = append(out, strings.ToLower(m[1]))
	}
	return out
}

// v1rcIsChainFile 判定一个**仓库相对路径**是否属于「迁移链」。
//
// ★ 链 = ① 基线快照（3 份：schema / deploy baseline / installer embeddata）
//
//	② sql/migrations/startup/
//	③ installer/cmd/llm-gw-installer/embeddata/startup/（②的孪生副本）
//
// ★ 为什么 installer 的孪生副本也算链：它与 ② 同源，installer's StartupFiles
//
//	投递的就是它 ⇒ 链外脚本建出来的表不会被它删。
func v1rcIsChainFile(rel string) bool {
	rel = filepath.ToSlash(rel)
	switch {
	case strings.Contains(rel, "sql/migrations/startup/"):
		return true
	case strings.Contains(rel, "embeddata/startup/"):
		return true
	case strings.HasSuffix(rel, "sql/schema/01-schema.sql"):
		return true
	case strings.HasSuffix(rel, "deploy/sql/schemas/baseline/01-schema.sql"):
		return true
	case strings.HasSuffix(rel, "installer/cmd/llm-gw-installer/embeddata/01-schema.sql"):
		return true
	}
	return false
}

// v1rcSkipDirs 是扫描时要跳过的目录。
var v1rcSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	".audit": true, ".audit-validation": true, ".audit-workspace": true, "web": true,
}

// v1rcScanSQL 扫全仓 .sql，产出「建过哪些 request_log* 关系」与「谁是谁的分区子表」。
type v1rcScan struct {
	// Tables: 关系名 -> 建过它的文件（仓库相对路径）
	Tables map[string][]string
	// Views: 关系名 -> 建过它的文件
	Views map[string][]string
	// ChildOf: 分区子表 -> 父表
	ChildOf map[string]string
	// Files: 扫过的 .sql 文件总数（用于阳性对照）
	Files int
}

func v1rcScanSQL(t *testing.T, root string) v1rcScan {
	t.Helper()
	scan := v1rcScan{
		Tables:  map[string][]string{},
		Views:   map[string][]string{},
		ChildOf: map[string]string{},
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if v1rcSkipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".sql") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scan.Files++
		body := v1rcStripComments(string(raw))
		for _, m := range v1rcCreateTableRE.FindAllStringSubmatch(body, -1) {
			name := strings.ToLower(m[1])
			if v1rcRelRE.MatchString(name) {
				scan.Tables[name] = append(scan.Tables[name], filepath.ToSlash(rel))
			}
		}
		for _, m := range v1rcCreateViewRE.FindAllStringSubmatch(body, -1) {
			name := strings.ToLower(m[1])
			if v1rcRelRE.MatchString(name) {
				scan.Views[name] = append(scan.Views[name], filepath.ToSlash(rel))
			}
		}
		for _, m := range v1rcPartitionOfRE.FindAllStringSubmatch(body, -1) {
			child, parent := strings.ToLower(m[1]), strings.ToLower(m[2])
			if v1rcRelRE.MatchString(child) || v1rcRelRE.MatchString(parent) {
				scan.ChildOf[child] = parent
			}
		}
		for _, m := range v1rcAttachRE.FindAllStringSubmatch(body, -1) {
			parent, child := strings.ToLower(m[1]), strings.ToLower(m[2])
			if v1rcRelRE.MatchString(child) || v1rcRelRE.MatchString(parent) {
				scan.ChildOf[child] = parent
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan repo sql: %v", err)
	}
	return scan
}

// v1rcManifest 是 890 的退役清单（按关系名索引）。
type v1rcManifest struct {
	// Dropped: 会被 890 直接 DROP 的关系名
	Dropped map[string]bool
	// GateChecked: 被非空闸逐个计数的表名
	GateChecked map[string]bool
	// DropKinds: 关系名 -> DROP 种类（table / view / sequence）
	DropKinds map[string]string
}

// v1rcParseDropBlocks 把 890 里的 FOREACH 块逐个切开，每个块取「数组 + 块内的 DROP 动词」。
//
// ★ 手工切块的理由见 v1rcForeachRE 的注释（RE2 不支持前瞻；不切块会跨块配对）。
func v1rcParseDropBlocks(text string) []struct {
	Names []string
	Kind  string
} {
	type block = struct {
		Names []string
		Kind  string
	}
	var out []block
	locs := v1rcForeachRE.FindAllStringIndex(text, -1)
	for i, loc := range locs {
		// 数组体：从 `[` 之后到**第一个** `]`（名字都是单引号串，里面不会有 `]`）。
		arrStart := loc[1]
		arrEnd := strings.Index(text[arrStart:], "]")
		if arrEnd < 0 {
			continue
		}
		arrEnd += arrStart
		names := v1rcQuoteNames(text[arrStart:arrEnd])

		// 块体：从本块 LOOP 到**下一个 FOREACH**（或文件末）。
		bodyEnd := len(text)
		if i+1 < len(locs) {
			bodyEnd = locs[i+1][0]
		}
		body := text[arrEnd:bodyEnd]
		kind := ""
		if m := v1rcDropKindRE.FindStringSubmatch(body); m != nil {
			kind = strings.ToLower(m[1])
		}
		if kind == "" {
			continue // 非空闸块（它没有 DROP 动词）⇒ 不进 DROP 清单
		}
		out = append(out, block{Names: names, Kind: kind})
	}
	return out
}

// v1rcReadManifest 抽 890 的退役清单。
func v1rcReadManifest(t *testing.T, root string) v1rcManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "sql", "migrations", "startup", v1rcMig890))
	if err != nil {
		t.Fatalf("read %s: %v", v1rcMig890, err)
	}
	text := string(raw)
	m := v1rcManifest{
		Dropped:     map[string]bool{},
		GateChecked: map[string]bool{},
		DropKinds:   map[string]string{},
	}
	for _, b := range v1rcParseDropBlocks(text) {
		for _, name := range b.Names {
			m.Dropped[name] = true
			m.DropKinds[name] = b.Kind
		}
	}
	if g := v1rcGateBlockRE.FindStringSubmatch(text); g != nil {
		for _, name := range v1rcQuoteNames(g[1]) {
			m.GateChecked[name] = true
		}
	}
	return m
}

// ─────────────────────────────────────────────────────────────────────────────
// ① 链内完备性
// ─────────────────────────────────────────────────────────────────────────────

// TestEveryRequestLogTableEverCreatedIsCoveredByTheRetirement ——
// 全仓建过的每一个 request_log* 表，都必须被 890 覆盖（直接 DROP，或随被覆盖的父表删）。
func TestEveryRequestLogTableEverCreatedIsCoveredByTheRetirement(t *testing.T) {
	root := v1rcRepoRoot(t)
	scan := v1rcScanSQL(t, root)
	manifest := v1rcReadManifest(t, root)

	// ★ 阳性对照：扫描器必须**看得见**已知的 v1 表。
	//   否则「未覆盖 = 0」与「什么都没扫到」同步为真。
	for _, must := range []string{"request_logs", "_request_logs", "request_logs_bodies"} {
		if len(scan.Tables[must]) == 0 {
			t.Fatalf("★ 扫描器阳性对照失败：全仓 .sql 里没扫到已知 v1 表 %q ⇒ "+
				"下面的「未覆盖 = 0」是恒真空，不是结论。扫了 %d 个 .sql 文件。", must, scan.Files)
		}
	}
	if len(manifest.Dropped) == 0 {
		t.Fatalf("★ 890 的退役清单抽出来是空的 ⇒ 判据失效（抽取器坏了，不是仓库干净）")
	}
	// ★ 抽取器阳性对照：清单必须**确实**含那 4 张 v1-only 视图与 10 张 v1 底表。
	//   否则「幸存面只有一个」这句话可能只是「抽取器一个都没抽到」。
	for _, must := range []string{
		"request_logs_bodies_progress",
		"request_logs_bodies_with_current_month",
		"request_logs_with_current_month_without_customer_id",
		"request_logs_with_current_month_without_request_class_due_at",
		"request_logs", "_request_logs",
		"request_logs_hot", "_request_logs_hot",
		"request_logs_bodies", "_request_logs_bodies",
		"request_logs_bodies_hot", "_request_logs_bodies_hot",
		"request_logs_archive", "_request_logs_archive",
	} {
		if !manifest.Dropped[must] {
			t.Fatalf("★ 抽取器阳性对照失败：退役清单里没有 %q ⇒ 抽取器漏了一块（不是仓库干净）", must)
		}
	}

	var uncovered []string
	for name := range scan.Tables {
		if manifest.Dropped[name] {
			continue
		}
		if parent, ok := scan.ChildOf[name]; ok && manifest.Dropped[parent] {
			continue // 分区子表：随父表一起删
		}
		uncovered = append(uncovered, name)
	}
	sort.Strings(uncovered)
	if len(uncovered) > 0 {
		var b strings.Builder
		b.WriteString("★ 有 request_log 族的表建过，但 890 的退役清单没覆盖它：\n")
		for _, n := range uncovered {
			b.WriteString("    " + n + "  ← " + strings.Join(scan.Tables[n], ", ") + "\n")
		}
		b.WriteString("  ⇒ 这些表建出来就不会被删（除非它们是某个被删父表的分区子表）。\n")
		b.WriteString("  ⇒ 修法：把它加进 890 的退役数组，或把建它的脚本挪进退役台账（判据③）。\n")
		t.Error(b.String())
	}
}

// TestRetirementManifestCoversEveryV1TableWithBothSpellings ——
// 退役清单里的**每张表**都必须同时带两种拼写（`request_logs` 与 `_request_logs`）。
//
// ★ 为什么这是硬要求：三条投递通道对「改名后」的处理**不一致**
//
//	（部署通道做词法改写、安装器逐字执行）⇒ 只写一种拼写 ⇒ 另一条通道上表还在。
func TestRetirementManifestCoversEveryV1TableWithBothSpellings(t *testing.T) {
	root := v1rcRepoRoot(t)
	manifest := v1rcReadManifest(t, root)

	for name, kind := range manifest.DropKinds {
		if kind != "table" {
			continue
		}
		if strings.HasPrefix(name, "_") {
			continue // 已是下划线拼写
		}
		underscored := "_" + name
		if !manifest.Dropped[underscored] {
			t.Errorf("★ 退役清单里的表 %q 缺下划线拼写 %q ⇒ "+
				"在不做词法改写的投递通道（安装器）上可能删不掉。", name, underscored)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ② 幸存面
// ─────────────────────────────────────────────────────────────────────────────

// TestTheOnlySurvivingRequestLogRelationIsTheCanonicalView ——
// 退役清单之外，只允许剩下 canonical 视图一个；且它必须读会话族。
func TestTheOnlySurvivingRequestLogRelationIsTheCanonicalView(t *testing.T) {
	root := v1rcRepoRoot(t)
	scan := v1rcScanSQL(t, root)
	manifest := v1rcReadManifest(t, root)

	survivors := map[string]bool{}
	for name := range scan.Tables {
		if manifest.Dropped[name] {
			continue
		}
		// ★ 分区子表随父表一起删 ⇒ 不算幸存者
		//   （判据与门①同一条规则：两处口径必须一致，否则两门会互相打脸）。
		if parent, ok := scan.ChildOf[name]; ok && manifest.Dropped[parent] {
			continue
		}
		survivors[name] = true
	}
	for name := range scan.Views {
		if manifest.Dropped[name] {
			continue
		}
		survivors[name] = true
	}
	delete(survivors, v1rcCanonicalView)

	if len(survivors) > 0 {
		names := make([]string, 0, len(survivors))
		for n := range survivors {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Errorf("★ 退役清单之外还剩 %d 个 request_log 命名关系：%v\n"+
			"  ⇒ 唯一被允许幸存的是 canonical 视图 %q（它换视图体、不删名字）。\n"+
			"  ⇒ 其余的要么进退役清单，要么根本不该存在。", len(names), names, v1rcCanonicalView)
	}

	// canonical 视图必须**读会话族**：它若还带 v1 腿 ⇒ 42P01 ⇒ 整片面板 500。
	for _, rel := range []string{
		filepath.Join("sql", "objects", "views", v1rcCanonicalView+".sql"),
		filepath.Join("sql", "migrations", "startup", v1rcMig890),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		body := v1rcStripComments(string(raw))
		if !strings.Contains(body, "session_turns") {
			t.Errorf("★ %s 里找不到 session_turns ⇒ canonical 视图可能仍带 v1 腿。", rel)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ③ 链外退役台账
// ─────────────────────────────────────────────────────────────────────────────

// v1rcExternalEntry 是「链外建 v1 表的文件」的一条登记。
//
// ★ Scope 决定「谁可以执行它」：
//
//	"isolated-audit" —— 只允许在**隔离库**里执行（审计脚本复现 890 之前的库）。
//	"manual-only"     —— 只允许人工在**890 之前的遗留库**上手跑；无自动入口引用它。
//	"deprecated-poc"  —— 废弃 POC 自带的种子文件；路径必须在 deprecated 目录里。
type v1rcExternalEntry struct {
	Path  string
	Scope string
	Why   string
}

// v1rcExternalRegistry 是链外退役台账。
//
// ★ 这是一份**显式登记簿**：链外建 v1 表的文件必须全部在册，
//
//	且每条都要带机器可查的退役标头（v1rcBanner）。
//	⇒ 新来一个链外脚本 ⇒ 门红 ⇒ 要么进台账，要么把 v1 改成会话族。
var v1rcExternalRegistry = []v1rcExternalEntry{
	{
		Path:  "sql/init-complete-minimal.sql",
		Scope: "manual-only",
		Why: "「完整的最小化数据库初始化脚本」—— 建 request_logs / request_logs_hot。" +
			"全仓零可执行入口引用它（只有两处注释）⇒ 保留作历史脚手架，但不许再在正式库跑。",
	},
	{
		Path:  "sql/quick-init-request-logs.sql",
		Scope: "manual-only",
		Why: "「快速创建 request_logs_hot 表（简化版，仅用于测试）」—— " +
			"全仓零引用；其唯一目的就是造一张已退役的表。",
	},
	{
		Path:  "sql/fixes/fix-request-logs-bodies-partition-v2.sql",
		Scope: "manual-only",
		Why: "R89-DU 的手跑修复脚本（自插探针行 + dry-run 闸 + RAISE_EXCEPTION）；" +
			"由 sql/schema/handrun_sql_inventory_test.go 管形状。对 890 之后的库会 42P01 ⇒ 失效。",
	},
	{
		Path:  "scripts/audit/sql/min-prereqs.sql",
		Scope: "isolated-audit",
		Why: "审计脚本的前置夹具（造出 890 之前的库，好验证 890 之前的迁移）。" +
			"只允许经 scripts/audit/psql-isolated.sh 在隔离库里执行。",
	},
	{
		Path:  "tests/_deprecated_local_poc_2026-07-12/seed/01-schema-bootstrap.sql",
		Scope: "deprecated-poc",
		Why:   "废弃本地 POC 的种子文件；只被同目录那个废弃 Makefile 引用。",
	},
}

// TestNoUnregisteredSQLOutsideTheChainCreatesAV1Table ——
// 链外建 v1 表的文件必须全部登记在退役台账里，且每个都带机器可查的退役标头。
func TestNoUnregisteredSQLOutsideTheChainCreatesAV1Table(t *testing.T) {
	root := v1rcRepoRoot(t)
	scan := v1rcScanSQL(t, root)

	registered := map[string]v1rcExternalEntry{}
	for _, e := range v1rcExternalRegistry {
		registered[e.Path] = e
	}

	// 链外建了 v1 表的文件
	outside := map[string][]string{}
	for name, files := range scan.Tables {
		for _, f := range files {
			if !v1rcIsChainFile(f) {
				outside[f] = append(outside[f], name)
			}
		}
	}

	var unregistered []string
	for f := range outside {
		if _, ok := registered[f]; !ok {
			unregistered = append(unregistered, f)
		}
	}
	sort.Strings(unregistered)
	if len(unregistered) > 0 {
		var b strings.Builder
		b.WriteString("★ 有链外脚本会建 v1 族的表，但没登记在退役台账里：\n")
		for _, f := range unregistered {
			sort.Strings(outside[f])
			b.WriteString("    " + f + "  ⇒ " + strings.Join(outside[f], ", ") + "\n")
		}
		b.WriteString("  ⇒ 它们建出来的表**不会被 890 删掉**（890 只在链里投递）。\n")
		b.WriteString("  ⇒ 处置：登记进 v1rcExternalRegistry 并加退役标头，或改成会话族。\n")
		t.Error(b.String())
	}

	// 台账里登记了、但文件已经不在了 ⇒ 台账过期（也是红的：台账在骗人）
	for _, e := range v1rcExternalRegistry {
		if _, err := os.Stat(filepath.Join(root, e.Path)); err != nil {
			t.Errorf("★ 退役台账里的 %q 不存在了 ⇒ 台账过期，请删条目或恢复文件。", e.Path)
		}
	}

	// 每个台账条目都必须带机器可查的退役标头
	for _, e := range v1rcExternalRegistry {
		raw, err := os.ReadFile(filepath.Join(root, e.Path))
		if err != nil {
			continue // 上面已报「文件不存在」
		}
		if !strings.Contains(string(raw), v1rcBanner) {
			t.Errorf("★ 退役台账条目 %q 缺机器可查的退役标头 %q\n"+
				"  ⇒ 半年后没人说得清它还能不能跑。标头格式：%s <scope> | <理由>", e.Path, v1rcBanner, v1rcBanner)
		}
	}
}

// TestRetiredExternalScriptsHaveNoAutomaticEntryPoint ——
// 台账条目的 scope 必须在代码里兑现：
//
//	manual-only     ⇒ 不得被任何**可执行**入口引用（.sh / Makefile / CI / 非测试 Go）
//	isolated-audit  ⇒ 可执行引用只允许出现在 scripts/audit/ 下，且那个脚本必须走隔离 psql
//	deprecated-poc  ⇒ 路径必须在 deprecated 目录里
func TestRetiredExternalScriptsHaveNoAutomaticEntryPoint(t *testing.T) {
	root := v1rcRepoRoot(t)

	// 收集「可执行入口」对台账条目的引用。
	// ★ 注释里的提及不算 ⇒ 先剥注释再搜。
	type ref struct {
		Path string
		Exec bool // 是否可执行入口
	}
	var refs []ref
	execExt := map[string]bool{".sh": true, ".bash": true}
	execName := map[string]bool{"Makefile": true, "makefile": true}

	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if v1rcSkipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		base := info.Name()
		isGo := strings.HasSuffix(base, ".go")
		isTest := strings.HasSuffix(base, "_test.go")
		isShell := execExt[strings.ToLower(filepath.Ext(base))]
		isMake := execName[base]
		isCI := strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")
		if !isGo && !isShell && !isMake && !isCI {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(raw)
		if isGo {
			text = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(text, " ")
			text = regexp.MustCompile(`//[^\n]*`).ReplaceAllString(text, " ")
		} else if isShell || isMake {
			text = regexp.MustCompile(`#[^\n]*`).ReplaceAllString(text, " ")
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, e := range v1rcExternalRegistry {
			if !strings.Contains(text, e.Path) {
				continue
			}
			// 可执行入口 = shell / Makefile / CI / 非测试 Go
			exec := isShell || isMake || isCI || (isGo && !isTest)
			refs = append(refs, ref{Path: rel, Exec: exec})
		}
		return nil
	})

	for _, e := range v1rcExternalRegistry {
		switch e.Scope {
		case "manual-only":
			for _, r := range refs {
				if r.Path == e.Path {
					continue
				}
				if r.Exec && strings.Contains(r.Path, e.Path) {
					t.Errorf("★ 退役台账条目 %q 的 scope 是 manual-only（无自动入口），\n"+
						"  但可执行入口 %q 引用了它 ⇒ 有人在自动流程里跑一个已退役的脚本。", e.Path, r.Path)
				}
			}
		case "isolated-audit":
			for _, r := range refs {
				if r.Path == e.Path || !strings.Contains(r.Path, e.Path) {
					continue
				}
				if !strings.HasPrefix(r.Path, "scripts/audit/") {
					t.Errorf("★ 退役台账条目 %q 的 scope 是 isolated-audit，\n"+
						"  但引用它的 %q 不在 scripts/audit/ 下 ⇒ 隔离约束失效。", e.Path, r.Path)
				}
			}
		case "deprecated-poc":
			if !strings.Contains(e.Path, "deprecated") {
				t.Errorf("★ 退役台账条目 %q 的 scope 是 deprecated-poc，\n"+
					"  但路径里没有 deprecated ⇒ 它可能被当成活资产。", e.Path)
			}
		default:
			t.Errorf("★ 退役台账条目 %q 的 scope %q 不认识（只认 isolated-audit / manual-only / deprecated-poc）。",
				e.Path, e.Scope)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ④ 真库腿
// ─────────────────────────────────────────────────────────────────────────────

// TestRetiredV1TablesAreAbsentInRealDB —— 真库确认：退役后的库里 v1 表 = 0。
//
// ⚠ **无 DSN ⇒ 显式 SKIP**（不当 0 处理）。
//
// ⚠ 判据的**方向**：
//
//	· 库里**没有**会话族 ⇒ 这个库早于会话族 ⇒ 与本审计无关 ⇒ SKIP。
//	· 库里有会话族**且**还有 v1 表 ⇒ ★ 真没退役 ⇒ FAIL。
func TestRetiredV1TablesAreAbsentInRealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 真库腿跳过（★ Skip ≠ 通过）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		// ★ 连不上 ⇒ **显式 SKIP**，不是 FAIL。
		//   理由：「连不上」与「连上了但 v1 表还在」是**两个不同的命题** ——
		//   前者是「没跑成」，后者才是「真没退役」。
		//   ⇒ 仓内既有真库门（db/r1039_request_logs_provider_id_column_test.go）
		//   在同样情形下是 SKIP ⇒ 本门必须与它**同口径**，
		//   否则「DSN 写错了」会被读成「退役没做」。
		//   ⚠ 但 SKIP ≠ 通过：本行把「没跑成」写进了测试输出。
		t.Skipf("TEST_DATABASE_URL 指向的库连不上（%v）—— 真库腿**没跑成**（★ Skip ≠ 通过）；"+
			"本次**没有**验证「退役后的库里 v1 表为 0」", err)
	}
	defer conn.Close(ctx)

	// 库里有没有会话族？⇒ 决定这条腿**适用**不适用。
	var sessionFamily int
	err = conn.QueryRow(ctx, `
		SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r','p')
		  AND c.relname IN ('session_turns','session_turns_hot')`).Scan(&sessionFamily)
	if err != nil {
		t.Fatalf("probe session family: %v", err)
	}
	if sessionFamily == 0 {
		t.Skip("该库里没有会话族（早于会话族的库）⇒ 与 v1 族退役无关，跳过")
	}

	rows, err := conn.Query(ctx, `
		SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r','p')
		  AND c.relname ~ '(^|_)request_logs?(_|$)' ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("list v1 relations: %v", err)
	}
	defer rows.Close()
	var alive []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		alive = append(alive, name)
	}
	if len(alive) > 0 {
		t.Errorf("★ 库里还有 %d 个 v1 族的表：%v\n"+
			"  ⇒ 迁移 890 没有在这个库上生效（或生效后又被链外脚本重建了）。", len(alive), alive)
	}

	// canonical 视图必须在，且读会话族。
	var def string
	err = conn.QueryRow(ctx,
		`SELECT pg_get_viewdef('public.request_logs_with_current_month'::regclass)`).Scan(&def)
	if err != nil {
		t.Fatalf("★ canonical 视图缺失或不可读：%v\n"+
			"  ⇒ 读方会 42P01（整片面板 500）。", err)
	}
	if !strings.Contains(def, "session_turns") {
		t.Errorf("★ canonical 视图的视图体不含 session_turns ⇒ 它仍带 v1 腿 ⇒ 42P01。")
	}
}
