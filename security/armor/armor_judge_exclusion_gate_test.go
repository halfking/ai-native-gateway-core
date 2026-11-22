package armor

// 2026-11-23（211 配置优化遗留 N1）· 守卫：
//
// 「安全通过率」这类统计面**必须排除** judge_model 的占位值。
//
// ★ 为什么需要这道门（★ 背景见 unconfigured_judge_gate_test.go）：
//   · 未配置判官时，生产写进 armor_judgments 的行是
//     judge_model='unconfigured'（或旧实现的 'mock'）/ score=0 / decision='safe'
//     ⇒ ⇒ 与真实判官给出的「安全」**在表里长得一样**。
//   · 211 实测（2026-11-23 当次）：共 394,714 条判定，
//     其中 judge_model='mock' 392,870 条、'unconfigured' 1,844 条（仍在新增）
//     ⇒ ⇒ **没有一条来自真实判官**。
//   ⇒ ★ 于是任何「按 decision='safe' 统计安全通过率」的面，
//     都会把「**根本没判过**」算成「**判过且安全**」。
//
// ★ 本门的作用：★ **在统计面被写出来的那一刻**就把它拦住。
//   ⇒ ★ 当前仓内**没有任何**「安全通过率」统计面（★ admin 与 web 前端均无引用）
//   ⇒ ★ 本门现在量的是「**零站点**」★ —— ★ 即「还没人踩这个坑」。
//   ⇒ ★ 等有人写这个面板时，★ 本门会当场红（★ 除非他带上了排除）。
//
// ★ 判据（★ 故意保守、★ 只抓「读 armor_judgments 且按 decision 统计」的语句）：
//   ① 语句里出现 armor_judgments；② 出现 decision；③ 是 SELECT（★ 写面不算）；
//   ④ **没有**同时出现 judge_model 与排除语义（<> / != / not in / except）。
//
// ★ 棘轮：★ `armor_judge_exclusion_sites.tsv` 登记「已知且已被复核的统计面」
//   （★ 当前为空 ⇒ ★ 仓内必须零违规站点）★ ⇒ ★ 将来若有人写了带排除的统计面，
//   ★ 必须把它登记进来（★ 登记行必须**仍然带**排除）★ ⇒ ★ 登记册只增不减地
//   记录「已经人工确认过的口径」，★ 而门负责挡住「没登记的违规口径」。

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// armorSafetyRateSite 描述一处「没排除 judge_model 占位值」的统计面。
type armorSafetyRateSite struct {
	File string
	SQL  string
}

// findArmorSafetyRateSites 扫 root 下的 .go 文件，返回**违规**统计面。
//
// ★ 做成可喂夹具的纯函数（★ 与仓内其它守卫同一形态：★ 判据能被单独测）。
func findArmorSafetyRateSites(root string) []armorSafetyRateSite {
	var out []armorSafetyRateSite
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git", "node_modules", "dist", "build", ".venv", "web":
				return filepath.SkipDir
			}
			return nil
		}
		// ★ 跳过测试文件：★ 本门自己的夹具里就故意包含违规 SQL
		//   ⇒ ★ 若不跳过，★ 门会抓到自己的夹具而永远红。
		//   ⇒ ★ 代价是「写在 _test.go 里的统计面」不受本门管
		//   ⇒ ★ 可接受（★ 生产统计面不在测试文件里）。
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		for _, block := range extractBacktickBlocks(string(b)) {
			low := strings.ToLower(block)
			if !strings.Contains(low, "armor_judgments") {
				continue
			}
			if !strings.Contains(low, "decision") {
				continue
			}
			if !strings.Contains(low, "select") {
				continue
			}
			// ★ 写面不算统计面（★ INSERT/UPDATE/DELETE 只是在落库）。
			if strings.Contains(low, "insert into") ||
				strings.Contains(low, "update ") ||
				strings.Contains(low, "delete from") {
				continue
			}
			if hasJudgeModelExclusion(low) {
				continue
			}
			out = append(out, armorSafetyRateSite{File: rel, SQL: firstLine(block)})
		}
		return nil
	})
	return out
}

// extractBacktickBlocks 抽出源码里的所有反引号块（Go 里 SQL 常量的惯用形态）。
func extractBacktickBlocks(src string) []string {
	var blocks []string
	rest := src
	for {
		i := strings.Index(rest, "`")
		if i < 0 {
			return blocks
		}
		j := strings.Index(rest[i+1:], "`")
		if j < 0 {
			return blocks
		}
		blocks = append(blocks, rest[i+1:i+1+j])
		rest = rest[i+1+j+1:]
	}
}

// hasJudgeModelExclusion 判「这条语句排除了 judge_model 的占位值」。
//
// ★ 只认**排除语义**（<> / != / not in / except）★ ——
//
//	★ 光出现 judge_model **不算**（★ 把它 select 出来 ≠ 排除它）。
func hasJudgeModelExclusion(low string) bool {
	if !strings.Contains(low, "judge_model") {
		return false
	}
	for _, op := range []string{"<>", "!=", "not in", "except"} {
		if strings.Contains(low, op) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// ── 门 1：夹具——★ 违规必须被抓、★ 合规必须放过 ──────────────────────────
func TestFindArmorSafetyRateSitesCatchesUnfilteredStats(t *testing.T) {
	dir := t.TempDir()

	// ★ 违规：按 decision 统计安全通过率、★ **没有**排除 judge_model。
	bad := `package x
const badSQL = `
	bad += "`SELECT decision, count(*) FROM armor_judgments GROUP BY decision`"
	if err := os.WriteFile(filepath.Join(dir, "bad.go"), []byte(bad), 0o600); err != nil {
		t.Fatalf("写夹具: %v", err)
	}

	// ★ 合规：同样统计、★ 但排除了占位值。
	good := `package x
const goodSQL = `
	good += "`SELECT decision, count(*) FROM armor_judgments " +
		"WHERE judge_model <> 'unconfigured' AND judge_model <> 'mock' GROUP BY decision`"
	if err := os.WriteFile(filepath.Join(dir, "good.go"), []byte(good), 0o600); err != nil {
		t.Fatalf("写夹具: %v", err)
	}

	sites := findArmorSafetyRateSites(dir)
	if len(sites) != 1 {
		t.Fatalf("违规统计面应恰好被报 1 处，实为 %d（%+v）", len(sites), sites)
	}
	if !strings.Contains(sites[0].File, "bad.go") {
		t.Errorf("报出的站点不是 bad.go：%+v", sites[0])
	}
}

// ── 门 2：对照臂——★ 把排除拿掉，★ 合规块立刻变成违规 ──────────────────
func TestFindArmorSafetyRateSitesDetectsRemovedExclusion(t *testing.T) {
	dir := t.TempDir()
	// ★ 只出现 judge_model、★ 但**没有排除语义** ⇒ ★ 仍然算违规
	//   （★ 这是最容易写漏的一种：★ 以为「select 一下 judge_model 就算处理了」）。
	only := `package x
const onlySQL = `
	only += "`SELECT judge_model, decision FROM armor_judgments`"
	if err := os.WriteFile(filepath.Join(dir, "only.go"), []byte(only), 0o600); err != nil {
		t.Fatalf("写夹具: %v", err)
	}
	sites := findArmorSafetyRateSites(dir)
	if len(sites) != 1 {
		t.Fatalf("只 select judge_model 而没排除的统计面应被报 1 处，实为 %d", len(sites))
	}
}

// ── 门 3：真实仓根——★ 当前零违规站点（★ 还没人写这个统计面）───────────
func TestNoUnfilteredArmorSafetyRateSiteInRepo(t *testing.T) {
	root := repoRoot(t)
	sites := findArmorSafetyRateSites(root)

	// ★ 登记册里的行数 == 仓内违规站点数（★ 棘轮：★ 登记了就能放过、
	//   ★ 没登记的违规当场红）。
	reg := readSiteRegistry(t, root)
	if len(sites) != len(reg) {
		for _, s := range sites {
			t.Errorf("★ 有统计面在统计 armor_judgments 的 decision 却没排除 judge_model："+
				"★ %s（★ %s）\n★ 请在 SQL 里排除 judge_model 的占位值"+
				"（★ 'unconfigured' / 'mock'），★ 或把它登记进 armor_judge_exclusion_sites.tsv",
				s.File, s.SQL)
		}
		t.Fatalf("违规统计面 %d 处、★ 登记册 %d 处 ⇒ ★ 两者必须一致", len(sites), len(reg))
	}
}

// ── 门 4：棘轮——★ 登记册里的每一行**必须仍然带**排除 ────────────────────
func TestSiteRegistryEntriesStillExcludeJudgeModel(t *testing.T) {
	root := repoRoot(t)
	reg := readSiteRegistry(t, root)
	for _, rel := range reg {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("登记册里的文件读不到：%s（%v）", rel, err)
			continue
		}
		for _, block := range extractBacktickBlocks(string(b)) {
			low := strings.ToLower(block)
			if !strings.Contains(low, "armor_judgments") || !strings.Contains(low, "decision") {
				continue
			}
			if !strings.Contains(low, "select") {
				continue
			}
			if strings.Contains(low, "insert into") || strings.Contains(low, "update ") ||
				strings.Contains(low, "delete from") {
				continue
			}
			if !hasJudgeModelExclusion(low) {
				t.Errorf("★ 登记册里的 %s 现在**不再**排除 judge_model 了 ⇒ "+
					"★ 要么把排除加回去、★ 要么从登记册里删掉它", rel)
			}
		}
	}
}

// repoRoot 定位仓根（★ 本文件在 security/armor/ 下 ⇒ ../../）。
func repoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("解析仓根: %v", err)
	}
	return abs
}

// readSiteRegistry 读登记册（★ 返回相对路径列表）。
func readSiteRegistry(t *testing.T, root string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "security", "armor", "armor_judge_exclusion_sites.tsv"))
	if err != nil {
		t.Fatalf("读登记册: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.SplitN(line, "\t", 2)[0])
	}
	return out
}
