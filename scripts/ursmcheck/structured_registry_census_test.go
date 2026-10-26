package ursm_test

// structured_registry_census_test.go —— 2026-10-28 R86 轮（非 `.tsv` 登记册的覆盖判据）
//
// # 这道门要钉住的事
//
// ★ 承接 §19.25.19.10 优先级 1 ③：「`.tsv` 之外的形态要不要纳入覆盖判据？」
//
// ★★ R85 轮的掏空法门只覆盖 `.tsv` ⇒ ⇒ 仓内**同一物种**的 `.json` 登记册
// ★★ （★ ★ web 那几道门自己的 `baseline.json` / `registry.json`）
// ★★ **一直不在覆盖判据里** ⇒ ⇒ 它们与 `.tsv` 登记册是**同一个物种**：
// ★★ 「**门读它；它被掏空 ⇒ 门就瞎**」⇒ ⇒ **必须登记**。
//
// ★ 本轮的实现分两层（★ ★ 诚实边界）：
//
//	① **覆盖判据**（★ 本文件）：★ 仓内每一个「像登记册」的结构化文件
//	   （★ `.json` / `.yaml` / `.yml` / `.csv` ＋ 文件名含 registry|baseline|inventory|ledger|manifest）
//	   都必须在 `structured-registry-census.json` 里**有归属**：
//	   ⇒ 要么 `kind=registry`（★ 并写出它的**读方**），
//	   ⇒ 要么 `kind=excluded`（★ 并写出**实质理由**）。
//	② **行为化掏空验证**：★ ★ 本轮**没有**做 ⇒ ⇒ 因为 JSON 的「掏空」不是「删几行」
//	   （★ 那会留下**语法错误**的 JSON ⇒ ⇒ 读方是因为**崩溃**才红 ⇒ ⇒ 不是「读到 0 条」）
//	   ⇒ ⇒ 需要一套**格式感知的掏空变换**（★ 例如把登记册替换成 `{}`）
//	   ⇒ ⇒ 每条 `behaviorally_verified` 都**如实标 false** ⇒ ⇒ **不假装覆盖**。
//
// # 候选集为什么按【文件名】定（★ 而不扫「谁引用了它」）
//
// ★ 候选集必须**机械可算且快** ⇒ ⇒ 门每轮都要跑 ⇒ ⇒ 扫引用者实测慢 30 倍
// ⇒ ⇒ 候选靠【文件名关键词】定 ⇒ ⇒ 代价是**会多收几个候选** ⇒ ⇒
// ★★ 所以每个多收进来的候选都**必须在表里被逐条判定**（★ 登记或写排除理由）
// ⇒ ⇒ **多收不是错**（★ 它被表覆盖了），**漏收才是错**（★ 新增登记册而没进表 ⇒ 门红）。

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// structuredCensusFile 是非 `.tsv` 登记册普查表的仓内相对路径。
const structuredCensusFile = "scripts/ursmcheck/structured-registry-census.json"

// structuredCensusExtensions 是候选集的扩展名。
var structuredCensusExtensions = []string{".json", ".yaml", ".yml", ".csv"}

// structuredCensusKeywords 是候选集的文件名关键词。
var structuredCensusKeywords = []string{"registry", "baseline", "inventory", "ledger", "manifest"}

// structuredCensusEntry 是普查表里的一条。
type structuredCensusEntry struct {
	Path                 string `json:"path"`
	Kind                 string `json:"kind"`
	Reader               string `json:"reader"`
	BehaviorallyVerified bool   `json:"behaviorally_verified"`
	Reason               string `json:"reason"`
	Note                 string `json:"note"`
}

// structuredCensus 是普查表。
type structuredCensus struct {
	Entries []structuredCensusEntry `json:"entries"`
}

// structuredCensusCandidates 返回候选集（★ 机械可算：★ git 追踪 ＋ 扩展名 ＋ 文件名关键词）。
func structuredCensusCandidates(t *testing.T, root string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "ls-files").Output()
	if err != nil {
		t.Fatalf("取 git 追踪的文件失败：%v", err)
	}
	var cands []string
	for _, p := range strings.Split(string(out), "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		lower := strings.ToLower(p)
		if strings.HasSuffix(lower, ".tsv") {
			continue // ★ `.tsv` 已在 tsv_hollowout_inventory.tsv 里登记 ⇒ 不重复
		}
		matchedExt := false
		for _, ext := range structuredCensusExtensions {
			if strings.HasSuffix(lower, ext) {
				matchedExt = true
				break
			}
		}
		if !matchedExt {
			continue
		}
		base := strings.ToLower(filepath.Base(p))
		matchedKw := false
		for _, kw := range structuredCensusKeywords {
			if strings.Contains(base, kw) {
				matchedKw = true
				break
			}
		}
		if !matchedKw {
			continue
		}
		cands = append(cands, p)
	}
	sort.Strings(cands)
	return cands
}

// loadStructuredCensus 读普查表。
func loadStructuredCensus(t *testing.T, root string) structuredCensus {
	t.Helper()
	path := filepath.Join(root, structuredCensusFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读非 .tsv 登记册普查表失败（%s）：%v\n★ 这张表是「同一物种的登记册不许漏登」的对冲机制 ⇒ ⇒ 必须存在",
			path, err)
	}
	var c structuredCensus
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("解析普查表失败：%v", err)
	}
	if len(c.Entries) == 0 {
		t.Fatalf("普查表是空的 ⇒ ⇒ 覆盖判据恒绿（形同虚设）")
	}
	return c
}

// TestStructuredRegistryCensusCoversEveryCandidate 是【覆盖判据】：
// ★ 每一个候选都必须在普查表里**有归属** ⇒ ⇒ 新增一个像登记册的结构化文件而没登记 ⇒ 门红。
func TestStructuredRegistryCensusCoversEveryCandidate(t *testing.T) {
	root := hollowRepoRoot(t)
	cands := structuredCensusCandidates(t, root)
	if len(cands) == 0 {
		t.Fatalf("候选集为空 ⇒ 量具坏了")
	}
	census := loadStructuredCensus(t, root)

	listed := map[string]structuredCensusEntry{}
	for _, e := range census.Entries {
		if _, dup := listed[e.Path]; dup {
			t.Errorf("普查表里有重复条目：%s", e.Path)
		}
		listed[e.Path] = e
	}

	var missing []string
	for _, c := range cands {
		if _, ok := listed[c]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("有 %d 个「像登记册」的结构化文件没在 %s 里归属：\n  %s\n"+
			"★ 新增这样的文件必须二选一：\n"+
			"  ① kind=registry ⇒ ★ 写出它的**读方**（★ 它是门读的输入 ⇒ ⇒ 掏空它会让门瞎）\n"+
			"  ② kind=excluded ⇒ ★ 写出**实质理由**（★ 文档证据 / 测试夹具 / 规则文件……）",
			len(missing), structuredCensusFile, strings.Join(missing, "\n  "))
	}
}

// TestStructuredRegistryCensusEntriesAreWellFormed 是【普查表自身的形状判据】。
func TestStructuredRegistryCensusEntriesAreWellFormed(t *testing.T) {
	root := hollowRepoRoot(t)
	census := loadStructuredCensus(t, root)
	cands := map[string]bool{}
	for _, c := range structuredCensusCandidates(t, root) {
		cands[c] = true
	}
	for _, e := range census.Entries {
		if e.Path == "" {
			t.Errorf("有条目没有填 path")
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Path)); err != nil {
			t.Errorf("条目指向不存在的文件：%s（%v）", e.Path, err)
		}
		switch e.Kind {
		case "registry":
			if strings.TrimSpace(e.Reader) == "" {
				t.Errorf("%s：kind=registry ⇒ 必须写出它的**读方**（★ 没有读方的登记册不是登记册）", e.Path)
			}
		case "excluded":
			if len(strings.TrimSpace(e.Reason)) < 8 {
				t.Errorf("%s：kind=excluded ⇒ 必须写实质理由（现在 %d 字：%q）", e.Path,
					len(strings.TrimSpace(e.Reason)), e.Reason)
			}
			for _, ph := range []string{"todo", "TBD", "n/a", "NA", "待补", "无", "?"} {
				if strings.TrimSpace(e.Reason) == ph {
					t.Errorf("%s：排除理由是占位符 %q", e.Path, ph)
				}
			}
		default:
			t.Errorf("%s：kind 只能是 registry / excluded（实际 %q）", e.Path, e.Kind)
		}
		// ★ 表里不该出现**不是候选**的文件（★ 文件被删了 ⇒ 表要跟着改）。
		if !cands[e.Path] {
			t.Errorf("%s：它已不在候选集里（★ 文件被删/改名了）⇒ ⇒ 普查表过期 ⇒ 请删掉这条", e.Path)
		}
	}
}

// TestStructuredRegistryCensusBehaviorFlagIsHonest 是【诚实边界判据】：
// ★★ 「behaviorally_verified」一旦被标 true ⇒ ⇒ 必须**真的**有行为化验证证据。
//
// ★★ 本轮**没有**为这些登记册做行为化掏空验证 ⇒ ⇒ 一律 false
// ⇒ ⇒ **任何人**把它改成 true 而没有证据 ⇒ ⇒ 门红（★ 防「假装覆盖」）。
func TestStructuredRegistryCensusBehaviorFlagIsHonest(t *testing.T) {
	root := hollowRepoRoot(t)
	census := loadStructuredCensus(t, root)
	for _, e := range census.Entries {
		if e.Kind != "registry" {
			continue
		}
		if e.BehaviorallyVerified {
			// ★ 标 true ⇒ 必须能指出**哪条**行为化验证的证据。
			// ★★ 本轮没有 ⇒ ⇒ 要么补证据（★ 在注记里写清是哪条门/哪次实测），要么标回 false。
			if strings.TrimSpace(e.Note) == "" || !strings.Contains(e.Note, "行为化") {
				t.Errorf("%s：behaviorally_verified=true ⇒ 必须在 note 里写明**哪条**行为化验证的证据", e.Path)
			}
		}
	}
}
