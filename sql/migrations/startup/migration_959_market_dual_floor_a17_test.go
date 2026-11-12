package startup

// migration_959_market_dual_floor_a17_test.go —— 迁移 959 的契约门。
//
// 959 = A-17 双币市场价地基（定稿 §3.3 / §4.1 的 M1-T2，编号顺延）。
// 判据四件（951/952 门同款）：
//
//	① 形态门：market_price_reference 17 列（P1 原生币 + 双向折算 + 双地板 +
//	  折算快照）、supplier_market_position 5 列（双 C2 读数）、registry 12
//	  平台补种（ON CONFLICT DO NOTHING）、视图 v3 追加 11 列、schema_migrations
//	  自登记。
//	② down 门：剥列 + 撤补种（带 959 notes 守卫，不误删 958 种子）+ 视图回
//	  941 版 + 删 schema_migrations 行。
//	③ 列追加纪律门：959 视图定义的输出列 = 941 定义的 33 列（名/序逐字一致）
//	  + 11 列追加 —— 「CREATE OR REPLACE VIEW 只能追加末尾列」纪律的机器化
//	  （前缀漂移即红）；消费方（sales/service.go / sales/quote.go /
//	  bg/routing_health_checks.go）不得出现对本视图的 SELECT *。
//	④ 字节一致门：canonical 与 installer embeddata 两份 SHA 一致 ——
//	  959 up/down **与 958 up/down 同门**（958 交付时漏了 installer 四面，
//	  本轮补登记 ⇒ 门把它钉住，防止再次只修一半）。
//
// 判据纪律：读 SQL 用字符串/别名提取，按标识符精确匹配。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	mig959Name     = "959_market_dual_floor_a17.sql"
	mig959DownName = "959_market_dual_floor_a17.down.sql"
	mig958Name     = "958_market_platform_registry.sql"
	mig958DownName = "958_market_platform_registry.down.sql"
)

// 视图 941 版的输出列别名清单按文件实测提取（下述断言以 29 为准；
// 941 头注写的「26 列」是旧基线陈旧口径 —— 以文件为准，不进口头数）。
var view941ColumnAliases = []string{
	"credential_id", "raw_model_name", "canonical_name", "provider_name",
	"supplier_currency", "billing_mode", "supplier_in_per_1m", "supplier_out_per_1m",
	"supplier_pricing_source", "supplier_pricing_updated_at",
	"baseline_currency", "baseline_in_per_1m", "baseline_out_per_1m",
	"input_price_ratio", "output_price_ratio", "currency_comparable", "has_baseline",
	"baseline_fetched_at", "baseline_cache_read_per_1m", "baseline_cache_write_per_1m",
	"cache_read_price_ratio", "cache_write_price_ratio",
	"market_ref_currency", "market_ref_in_per_1m", "market_ref_out_per_1m",
	"market_ref_platform_id", "market_ref_platform_count",
	"market_position", "price_confidence",
}

// 959 追加的 11 列（按定义顺序）。
var view959AppendedAliases = []string{
	"overseas_ref_in_per_1m", "overseas_ref_out_per_1m",
	"overseas_ref_platform_id", "overseas_ref_platform_count",
	"domestic_ref_in_per_1m", "domestic_ref_out_per_1m",
	"domestic_ref_platform_id", "domestic_ref_platform_count",
	"market_position_vs_overseas", "market_position_vs_domestic",
	"market_ref_used",
}

// view949AddedAlias —— 949 在 941 之后追加的列（binding 身份）。
//
// ★ 959 的定义必须原样保留它：CREATE OR REPLACE VIEW 按**位置**配对列，
//
//	少这一列 ⇒ 第 30 列由 provider_model_id 变成 overseas_ref_in_per_1m
//	⇒ PG 报 "cannot change name of view column ..." ⇒ 迁移必炸
//	（2026-11-11 211 acc-pg17 实测，该迁移卡死全部 211 部署）。
var view949AddedAlias = "provider_model_id"

// view949ColumnAliases —— 949 版视图的**绝对**列形状（30 列，逐字逐序）。
//
// ★ 这是本门真正的「绝对钉」：它对齐的是**活库实测形状**（211 acc-pg17 的
//
//	information_schema.columns 实测 30 列，第 30 列 = provider_model_id），
//	不是手写想象。白名单自清洁：将来有迁移再追加本视图的列时，门变红并
//	要求更新本清单（与 sql/schema/installed_startup_migrations.tsv 同款纪律）。
//
// ★ 为什么绝对钉必须落在**前驱**（949）而不是 959：959 的形状已由
//
//	「前驱形状 + 11 列追加」完全确定 ⇒ 若绝对钉钉在 959 上，它在
//	「前驱与 959 同时被改」时永远先被前驱的等效断言挡住 ⇒ 不可达
//	⇒ 装饰性恒绿门（LESSONS：没做过变异验证的门只是装饰）。
var view949ColumnAliases = append(append([]string{}, view941ColumnAliases...),
	view949AddedAlias)

// viewDef = 迁移链上的一个「本视图定义」。
type viewDef struct {
	num     int
	name    string
	aliases []string
}

// viewDefinitionChain 扫描 sql/migrations/startup 下所有定义
// v_supplier_price_vs_baseline 的迁移（排除 .down.sql），按编号升序返回。
//
// ★ 为什么必须**机械发现**基线（★ 本轮真踩到）：旧门把 941（29 列）当基线，
//
//	而真实链是 826(18) → 844(22) → 941(29) → 949(30) → 959(41)。
//	949 在 941 与 959 之间追加了 provider_model_id（第 30 列），959 的定义
//	却少了这一列 ⇒ PG 按位置配对 ⇒ 迁移在已应用 949 的库上必炸。
//	旧门全绿 ⇒ 「缺覆盖的全绿」的同款。基线必须来自链上**前一版**，
//	否则任何夹在中间的迁移都会被无视。
func viewDefinitionChain(t *testing.T, root string) []viewDef {
	t.Helper()
	dir := filepath.Join(root, "sql", "migrations", "startup")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read startup migrations dir: %v", err)
	}
	var defs []viewDef
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		body := mig951Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		if findViewDDL(body) < 0 {
			continue
		}
		al := extractViewAliases(body)
		if len(al) == 0 {
			t.Fatalf("%s defines the view but column extraction returned 0 aliases", name)
		}
		num, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			t.Fatalf("migration filename %s has no numeric prefix: %v", name, err)
		}
		defs = append(defs, viewDef{num: num, name: name, aliases: al})
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].num < defs[j].num })
	return defs
}

// ★ 下面两处关键字守门（up 不得 DROP 视图 / down 不得带 CASCADE）用**代码行**
// 口径比对（stripSQLComments —— 见 migration_602_test.go 的既有实现，本包复用）：
// 头注里常出现「不加 CASCADE」「DROP VIEW + CREATE VIEW」等说明文字 ⇒ 不去注释
// 就会把**注释**当成代码命中（实测踩到：down 头注里那句「不加 CASCADE」直接把
// 新守门顶红了）。仓内既有惯例同样是「比对前去注释」。

// viewDDLMarker —— 本视图的 DDL 标记（两种形态都认）。
const viewDDLMarker = "public.v_supplier_price_vs_baseline"

// findViewDDL 返回该视图 CREATE 语句在 body 中的偏移。
//
// ★ 两种形态都认：CREATE OR REPLACE VIEW（**追加**列时用 ⇒ 保住授权、
//
//	注释与下游依赖）与 CREATE VIEW（DROP+CREATE **回滚**时用 ⇒ 因为
//	PG 硬规则「CREATE OR REPLACE VIEW 不能丢列」，211 acc-pg17 实测报
//	ERROR: cannot drop columns from view）。
func findViewDDL(body string) int {
	if i := strings.Index(body, "CREATE OR REPLACE VIEW "+viewDDLMarker); i >= 0 {
		return i
	}
	if i := strings.Index(body, "CREATE VIEW "+viewDDLMarker); i >= 0 {
		return i
	}
	return -1
}

// extractViewAliases 提取一条 `CREATE [OR REPLACE] VIEW public.v_supplier_price_vs_baseline AS`
// 定义里的顶层输出列别名（AS 别名优先；无别名的裸列用其列名）。
// 只统计顶层 SELECT 清单（到第一个 FROM keyword 行为止），不进 LATERAL。
func extractViewAliases(body string) []string {
	start := findViewDDL(body)
	if start < 0 {
		return nil
	}
	fromIdx := strings.Index(body[start:], "\nFROM ")
	if fromIdx < 0 {
		return nil
	}
	head := body[start : start+fromIdx]
	// 去掉行注释，避免注释里的示例别名干扰。
	var lines []string
	for _, ln := range strings.Split(head, "\n") {
		if i := strings.Index(ln, "--"); i >= 0 {
			ln = ln[:i]
		}
		lines = append(lines, ln)
	}
	clean := strings.Join(lines, "\n")
	// 顶层 SELECT 清单 = 第一个 SELECT 之后的内容（CTE 不存在于此视图）。
	sel := strings.Index(clean, "SELECT")
	if sel < 0 {
		return nil
	}
	list := clean[sel+len("SELECT"):]
	// 按顶层逗号切分（本视图顶层清单无函数内逗号嵌套问题 —— CASE/round 的
	// 逗号都在括号内；这里用深度计数切分）。
	var items []string
	depth := 0
	cur := strings.Builder{}
	for _, r := range list {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, cur.String())
				cur.Reset()
				continue
			}
		}
		cur.WriteRune(r)
	}
	items = append(items, cur.String())

	aliasRe := regexp.MustCompile(`(?is)\bas\s+([a-z_][a-z0-9_]*)\s*$`)
	bareRe := regexp.MustCompile(`(?is)([a-z_][a-z0-9_]*)\s*$`)
	var aliases []string
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		if m := aliasRe.FindStringSubmatch(it); m != nil {
			aliases = append(aliases, m[1])
			continue
		}
		if m := bareRe.FindStringSubmatch(it); m != nil {
			aliases = append(aliases, m[1])
		}
	}
	return aliases
}

func TestMigration959Shape(t *testing.T) {
	root := mig951Tree(t)
	body := mig951Read(t, root, "sql/migrations/startup/"+mig959Name)

	for _, want := range []string{
		// ① market_price_reference 17 列（抽样钉关键名 + 全量数列）
		"ADD COLUMN IF NOT EXISTS vendor_list_currency        text NOT NULL DEFAULT 'USD'",
		"ADD COLUMN IF NOT EXISTS overseas_min_in_per_1m      numeric(14,6)",
		"ADD COLUMN IF NOT EXISTS overseas_min_platform_count integer NOT NULL DEFAULT 0",
		"ADD COLUMN IF NOT EXISTS domestic_min_in_per_1m      numeric(14,6)",
		"ADD COLUMN IF NOT EXISTS domestic_min_usd_out_per_1m numeric(14,6)",
		// ② supplier_market_position 5 列
		"ADD COLUMN IF NOT EXISTS market_position_vs_overseas numeric(10,4)",
		"ADD COLUMN IF NOT EXISTS market_ref_used             text NOT NULL DEFAULT ''",
		"ADD COLUMN IF NOT EXISTS fx_used                     numeric(12,6)",
		// ③ registry 补种：12 平台 + 幂等 + 对拍证据
		"INSERT INTO public.market_platform_registry",
		"ON CONFLICT (platform_id) DO NOTHING",
		"'aws-bedrock'", "'azure-foundry'", "'deepinfra'",
		"'baidu-qianfan'", "'bailian'", "'huawei-modelarts'",
		"'iflytek-spark'", "'jdcloud-joybuilder'", "'ppio'",
		"'qiniu'", "'tencent-hunyuan'", "'volcengine-ark'",
		// ④ 视图 v3 与自登记
		"CREATE OR REPLACE VIEW public.v_supplier_price_vs_baseline",
		"INSERT INTO public.schema_migrations (version, description)",
		"'959'",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("959 up missing %q", want)
		}
	}
	// 列数守卫：market_price_reference 的 ALTER 恰 17 个 ADD COLUMN，
	// supplier_market_position 的 ALTER 恰 5 个（多列少列都红）。
	alterCount := func(table string) int {
		idx := strings.Index(body, "ALTER TABLE public."+table)
		if idx < 0 {
			t.Fatalf("959 up missing ALTER TABLE public.%s", table)
		}
		end := strings.Index(body[idx:], ";")
		seg := body[idx : idx+end]
		return strings.Count(seg, "ADD COLUMN IF NOT EXISTS")
	}
	if got := alterCount("market_price_reference"); got != 17 {
		t.Fatalf("market_price_reference ALTER must add exactly 17 columns, got %d", got)
	}
	if got := alterCount("supplier_market_position"); got != 5 {
		t.Fatalf("supplier_market_position ALTER must add exactly 5 columns, got %d", got)
	}
	// 补种行数守卫：VALUES 段恰 12 个平台行（每个 platform_id 字面量一次）。
	insStart := strings.Index(body, "INSERT INTO public.market_platform_registry")
	insEnd := strings.Index(body[insStart:], "ON CONFLICT (platform_id) DO NOTHING")
	insSeg := body[insStart : insStart+insEnd]
	if got := strings.Count(insSeg, "('"); got != 12 {
		t.Fatalf("959 registry seeds must insert exactly 12 platforms, got %d", got)
	}
}

func TestMigration959Down(t *testing.T) {
	root := mig951Tree(t)
	body := mig951Read(t, root, "sql/migrations/startup/"+mig959DownName)
	for _, want := range []string{
		"DROP COLUMN IF EXISTS overseas_min_in_per_1m",
		"DROP COLUMN IF EXISTS domestic_min_usd_out_per_1m",
		"DROP COLUMN IF EXISTS market_position_vs_overseas",
		"DROP COLUMN IF EXISTS fx_used",
		"DELETE FROM public.market_platform_registry",
		"notes LIKE '959 对拍补种%'",
		"DELETE FROM public.schema_migrations WHERE version = '959'",
		"DROP VIEW IF EXISTS public.v_supplier_price_vs_baseline",
		"CREATE VIEW public.v_supplier_price_vs_baseline",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("959 down missing %q", want)
		}
	}
	// ★ down 必须用 DROP VIEW + CREATE VIEW：CREATE OR REPLACE VIEW **不能丢列**
	//   （PG 硬规则；211 acc-pg17 实测原写法报 ERROR: cannot drop columns from
	//   view —— 959 应用后视图 41 列，回滚目标是 30 列 ⇒ 少 11 列）。
	//   且不得带 CASCADE：CASCADE 会把下游依赖**静默毁掉**（[M10] 的教训：
	//   不能靠 CASCADE 消掉）⇒ 无依赖时 DROP 成功，有依赖时**响亮失败**。
	if strings.Contains(body, "CREATE OR REPLACE VIEW "+viewDDLMarker) {
		t.Fatalf("959 down must use DROP VIEW IF EXISTS + CREATE VIEW (CREATE OR REPLACE VIEW cannot drop columns — PG hard rule)")
	}
	code := stripSQLComments(body)
	if !strings.Contains(code, "DROP VIEW IF EXISTS "+viewDDLMarker) {
		t.Fatalf("959 down must drop the view before recreating it (to shrink 41 -> 30 columns)")
	}
	if regexp.MustCompile(`DROP\s+VIEW[^;]*CASCADE`).MatchString(code) {
		t.Fatalf("959 down must not use CASCADE (it would silently destroy downstream dependents)")
	}
	// down 的视图恢复必须恰好回到 **949 版形状**（30 列 = 941 的 29 列 +
	// 949 追加的 provider_model_id）—— 回滚目标是「959 之前的那一版」，
	// 不是 941（少 provider_model_id ⇒ CREATE OR REPLACE VIEW 同样报
	// "cannot change name of view column" ⇒ 回滚也炸）。
	chain := viewDefinitionChain(t, root)
	var prev949 []string
	for _, d := range chain {
		if d.num == 949 {
			prev949 = d.aliases
			break
		}
	}
	if prev949 == nil {
		t.Fatalf("migration 949 not found in view definition chain")
	}
	downAliases := extractViewAliases(body)
	if len(downAliases) != len(prev949) {
		t.Fatalf("959 down view restore must have exactly %d columns (949 shape), got %d: %v",
			len(prev949), len(downAliases), downAliases)
	}
	for i, a := range prev949 {
		if downAliases[i] != a {
			t.Fatalf("959 down view column %d = %q, 949 shape has %q (rollback must restore the pre-959 shape)", i+1, downAliases[i], a)
		}
	}
}

func TestMigration959ViewAppendOnly(t *testing.T) {
	root := mig951Tree(t)
	chain := viewDefinitionChain(t, root)
	if len(chain) < 3 {
		t.Fatalf("view definition chain must have >= 3 entries, got %d", len(chain))
	}
	chainNames := func() []string {
		out := make([]string, 0, len(chain))
		for _, d := range chain {
			out = append(out, fmt.Sprintf("%d(%d cols)", d.num, len(d.aliases)))
		}
		return out
	}
	// ① 链门：每一版都必须是前一版的**前缀扩展**（名+序逐字一致，列数不减）。
	//    「CREATE OR REPLACE VIEW 只追加末尾列」纪律的机械化 —— 任何一版
	//    丢列 / 改名 / 重排即红，且定位到具体迁移号。
	for i := 1; i < len(chain); i++ {
		prev, cur := chain[i-1], chain[i]
		if len(cur.aliases) < len(prev.aliases) {
			t.Fatalf("view column count shrank at %s: %d -> %d (append-only violation)",
				cur.name, len(prev.aliases), len(cur.aliases))
		}
		for j := range prev.aliases {
			if cur.aliases[j] != prev.aliases[j] {
				t.Fatalf("view column %d drifted at %s: %s=%q -> %s=%q (append-only violation: CREATE OR REPLACE VIEW pairs columns by position)",
					j+1, cur.name, prev.name, prev.aliases[j], cur.name, cur.aliases[j])
			}
		}
	}
	// ② 959 的基线 = 链上**前一版**（949），不是 941。
	idx959 := -1
	for i, d := range chain {
		if d.num == 959 {
			idx959 = i
			break
		}
	}
	if idx959 < 1 {
		t.Fatalf("migration 959 not found in view definition chain (or is the first entry): %v", chainNames())
	}
	prev, cur := chain[idx959-1], chain[idx959]
	if prev.num != 949 {
		t.Fatalf("959's view-definition predecessor must be 949, got %d (%s) — 链变了，基线要重取: %v",
			prev.num, prev.name, chainNames())
	}
	// ★ up 必须走 append-only（CREATE OR REPLACE VIEW）：不得 DROP 视图 ——
	//   DROP 会毁掉授权、注释与下游依赖（211 实测该视图当下零下游依赖，
	//   但「当下零依赖」不是可以 DROP 的理由）。
	upBody := mig951Read(t, root, filepath.Join("sql", "migrations", "startup", mig959Name))
	upCode := stripSQLComments(upBody)
	if !strings.Contains(upCode, "CREATE OR REPLACE VIEW "+viewDDLMarker) {
		t.Fatalf("959 up must use CREATE OR REPLACE VIEW (append-only: it only appends columns)")
	}
	if regexp.MustCompile(`DROP\s+VIEW`).MatchString(upCode) {
		t.Fatalf("959 up must not DROP the view (would destroy grants/comments/dependents)")
	}
	// ② 绝对形状钉（落在**前驱** 949 上，对齐**活库实测**的 30 列）：
	//    211 acc-pg17 的 information_schema.columns 实测 = 30 列，第 30 列
	//    = provider_model_id。钉前驱而不是 959：959 的形状已由
	//    「前驱 + 11 列追加」完全确定，钉在 959 上就永远不可达（装饰门）。
	if len(prev.aliases) != len(view949ColumnAliases) {
		t.Fatalf("949 view baseline (%d cols) drifts from the live-verified shape (%d cols): %v",
			len(prev.aliases), len(view949ColumnAliases), prev.aliases)
	}
	for i, a := range view949ColumnAliases {
		if prev.aliases[i] != a {
			t.Fatalf("949 view column %d = %q, live-verified shape has %q",
				i+1, prev.aliases[i], a)
		}
	}
	// ③ 959 = 前驱形状 + 11 列追加（名/序逐字）。
	if len(cur.aliases) != len(prev.aliases)+len(view959AppendedAliases) {
		t.Fatalf("959 view must have %d columns (%d prior + %d appended), got %d: %v",
			len(prev.aliases)+len(view959AppendedAliases), len(prev.aliases),
			len(view959AppendedAliases), len(cur.aliases), cur.aliases)
	}
	for i, a := range view959AppendedAliases {
		if cur.aliases[len(prev.aliases)+i] != a {
			t.Fatalf("appended column %d mismatch: want %q got %q",
				i+1, a, cur.aliases[len(prev.aliases)+i])
		}
	}
	// ④ 提取清单与 941 文件实测清单双向对账（手写清单漂移即红 —— LESSONS §684）。
	base := extractViewAliases(mig951Read(t, root, "sql/migrations/startup/941_supplier_view_market_columns.sql"))
	if len(base) != 29 {
		t.Fatalf("941 view baseline must be 29 columns, extracted %d: %v", len(base), base)
	}
	if len(view941ColumnAliases) != len(base) {
		t.Fatalf("hand-written 941 alias list (%d) drifts from extracted (%d)", len(view941ColumnAliases), len(base))
	}
	for i, a := range view941ColumnAliases {
		if base[i] != a {
			t.Fatalf("hand-written 941 alias %d = %q, file has %q", i+1, a, base[i])
		}
	}

	// 消费方纪律：三个显式列名消费方不得引入对本视图的 SELECT *
	// （比对前去注释 —— routing_health_checks.go:129 的注释里有
	// "`SELECT * FROM v_model_modality_verification_rollup`" 示例文本）。
	for _, rel := range []string{
		filepath.Join("sales", "service.go"),
		filepath.Join("sales", "quote.go"),
		filepath.Join("bg", "routing_health_checks.go"),
	} {
		raw := mig951Read(t, root, rel)
		var codeLines []string
		for _, ln := range strings.Split(raw, "\n") {
			if i := strings.Index(ln, "//"); i >= 0 {
				ln = ln[:i]
			}
			codeLines = append(codeLines, ln)
		}
		if strings.Contains(strings.Join(codeLines, "\n"), "SELECT *") {
			t.Fatalf("%s uses SELECT * (view consumers must list columns explicitly)", rel)
		}
	}
}

func TestMigration959And958ByteIdenticalEmbeddata(t *testing.T) {
	root := mig951Tree(t)
	// 959 全套 + 958 补登记（958 交付时漏 installer 面，本轮补齐 ⇒ 门钉住）。
	for _, name := range []string{mig959Name, mig959DownName, mig958Name, mig958DownName} {
		a, err := os.ReadFile(filepath.Join(root, "sql", "migrations", "startup", name))
		if err != nil {
			t.Fatalf("read canonical %s: %v", name, err)
		}
		b, err := os.ReadFile(filepath.Join(root, "installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if err != nil {
			t.Fatalf("read embeddata %s: %v（六面登记缺口）", name, err)
		}
		sa, sb := sha256.Sum256(a), sha256.Sum256(b)
		if hex.EncodeToString(sa[:]) != hex.EncodeToString(sb[:]) {
			t.Fatalf("%s canonical and embeddata copies differ (sha %x vs %x)", name, sa, sb)
		}
	}
}

// TestMigration959SyncScriptCrossPin —— 959 的消费方钉：同步器必须读 registry
// 裁定区域/币种，且拒绝族 platform_unmapped_region 必须存在（A-17 原则 2）。
func TestMigration959SyncScriptCrossPin(t *testing.T) {
	root := mig951Tree(t)
	body := mig951Read(t, root, filepath.Join("scripts", "market-price-sync.py"))
	for _, want := range []string{
		"FROM public.market_platform_registry",
		"platform_unmapped_region",
		"overseas_min_in_per_1m",
		"domestic_min_in_per_1m",
		"vendor_list_currency",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("market-price-sync.py missing %q (A-17 sync cross-pin drift)", want)
		}
	}
}
