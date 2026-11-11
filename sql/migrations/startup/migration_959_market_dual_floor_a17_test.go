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
	"os"
	"path/filepath"
	"regexp"
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

// extractViewAliases 提取一条 `CREATE OR REPLACE VIEW public.v_supplier_price_vs_baseline AS`
// 定义里的顶层输出列别名（AS 别名优先；无别名的裸列用其列名）。
// 只统计顶层 SELECT 清单（到第一个 FROM keyword 行为止），不进 LATERAL。
func extractViewAliases(body string) []string {
	start := strings.Index(body, "CREATE OR REPLACE VIEW public.v_supplier_price_vs_baseline")
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
		"CREATE OR REPLACE VIEW public.v_supplier_price_vs_baseline",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("959 down missing %q", want)
		}
	}
	// down 的视图恢复必须恰好 29 列（= 941 版实测形状）。
	if aliases := extractViewAliases(body); len(aliases) != 29 {
		t.Fatalf("959 down view restore must have exactly 29 columns (941 shape), got %d: %v", len(aliases), aliases)
	}
}

func TestMigration959ViewAppendOnly(t *testing.T) {
	root := mig951Tree(t)
	up941 := mig951Read(t, root, "sql/migrations/startup/941_supplier_view_market_columns.sql")
	up959 := mig951Read(t, root, "sql/migrations/startup/"+mig959Name)

	base := extractViewAliases(up941)
	next := extractViewAliases(up959)
	if len(base) == 0 || len(next) == 0 {
		t.Fatalf("alias extraction failed: 941=%d 959=%d", len(base), len(next))
	}
	// 前缀逐字一致（名+序）：941 的 29 列在 959 里必须原样出现在最前。
	if len(base) != 29 {
		t.Fatalf("941 view baseline must be 29 columns, extracted %d: %v", len(base), base)
	}
	if len(next) != 40 {
		t.Fatalf("959 view must have 40 columns (29+11), got %d", len(next))
	}
	for i, a := range base {
		if next[i] != a {
			t.Fatalf("view column %d drifted: 941=%q 959=%q (append-only violation)", i+1, a, next[i])
		}
	}
	for i, a := range view959AppendedAliases {
		if next[len(base)+i] != a {
			t.Fatalf("appended column %d mismatch: want %q got %q", i+1, a, next[len(base)+i])
		}
	}
	// 提取清单与 941 文件实测清单双向对账（手写清单漂移即红 —— LESSONS §684）。
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
