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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
//
// ★ R87 轮新增的四个字段（★ 非 `.tsv` 登记册的**行为化掏空验证**所需）：
//
//	ReaderKind  ：★ 读方的**形态**（★ node / vitest / gotest）⇒ ⇒ 门据此选跑法。
//	ReaderSpec  ：★ 读方的**规格**（★ 路径或 `<pkg>|<TestName>`）。
//	HollowMode  ：★ 掏空**模式**（★ entries / unusable）⇒ ⇒ 门据此选变换。
//	RedSignal   ：★ 约定的**红信号**（★ 输出的子串）⇒ ⇒ 门据此判定「红因正确」。
type structuredCensusEntry struct {
	Path                 string `json:"path"`
	Kind                 string `json:"kind"`
	Reader               string `json:"reader"`
	ReaderKind           string `json:"reader_kind"`
	ReaderSpec           string `json:"reader_spec"`
	HollowMode           string `json:"hollow_mode"`
	RedSignal            string `json:"red_signal"`
	BehaviorallyVerified bool   `json:"behaviorally_verified"`
	HollowResult         string `json:"hollow_result"`
	MechCheck            string `json:"mech_check"`
	Reason               string `json:"reason"`
	Note                 string `json:"note"`
}

// structuredCensus 是普查表。
type structuredCensus struct {
	Entries []structuredCensusEntry `json:"entries"`
}

// structuredCensusCandidates 返回候选集（★ 机械可算：★ git 追踪 ＋ 扩展名 ＋ 文件名关键词
// ★ ＋ ★★ 被【已登记的读方】按路径读取的结构化文件）。
//
// ★★ R87 轮新增的**第二条**候选来源（★ 承接 §19.25.19.10 优先级 1 ③）：
//
//	★ 第一条（★ 文件名关键词）会**漏收**「名字不像登记册」的登记册
//	  ⇒ ⇒ 实测漏收 `web/scripts/cjk-native-simplified-chars.json`
//	  （★ 那是简体残留门的**Big5 字表** ⇒ ⇒ 门读它 ⇒ ⇒ 它是同一物种 ⇒ ⇒ 必须登记）。
//	★ 第二条：★ **被【普查表已登记的读方】按路径读取**的结构化文件
//	  ⇒ ⇒ 「门读它 ⇒ 它被掏空 ⇒ 门就瞎」⇒ ⇒ 同物种 ⇒ ⇒ 必须登记。
//
// ★★ 为什么第二条只扫「**已登记的读方**」而不是全仓：
//
//	★ 全仓扫「按路径读取」实测得到 **121** 个候选（★  compose / prometheus 规则 /
//	  testdata / locale 文件……）⇒ ⇒ 绝大多数**不是登记册** ⇒ ⇒ 表会变成噪音。
//	★ 而「**已登记的读方**」是**门** ⇒ ⇒ 它按路径读的结构化文件**就是**门的输入
//	  ⇒ ⇒ 判据既精确又廉价 ⇒ ⇒ 新登记册在**被登记为读方的那一刻**就会被自动纳入。
func structuredCensusCandidates(t *testing.T, root string, census structuredCensus) []string {
	t.Helper()
	tracked := structuredTrackedFiles(t, root)
	cands := map[string]bool{}
	for _, p := range structuredCensusNameCandidates(t, root) {
		cands[p] = true
	}
	for _, p := range structuredReaderReferencedPaths(root, census, tracked) {
		cands[p] = true
	}
	out := make([]string, 0, len(cands))
	for p := range cands {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// structuredCensusNameCandidates 返回【文件名关键词】那一部分的候选集。
func structuredCensusNameCandidates(t *testing.T, root string) []string {
	t.Helper()
	var cands []string
	for _, p := range structuredTrackedFiles(t, root) {
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

// structuredTrackedFiles 返回仓内【git 追踪】的文件（★ 相对仓根，已排序）。
func structuredTrackedFiles(t *testing.T, root string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("取 git 追踪的文件失败：%v", err)
	}
	var files []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	return files
}

// structuredPathLiteralRE 匹配「引号里、以结构化扩展名结尾的路径字面量」。
//
// ★ 只认**引号里**的（★ ⇒ ⇒ 不会把注释里的路径也当成引用）。
var structuredPathLiteralRE = regexp.MustCompile("[\"'`]([^\"'`\\n]*\\.(?:json|ya?ml|csv))[\"'`]")

// structuredReaderReferencedPaths 返回【被已登记的读方按路径读取】的结构化文件。
//
// ★ 解析方式：★ 字面量往往只是**片段**（★ `join(ROOT, 'scripts', 'xxx.json')`
// ⇒ ⇒ 字面量是 `xxx.json`）⇒ ⇒ 用**路径后缀**去撞仓内被追踪的文件。
//
// ★★ 单词段字面量（★ 如 `package.json`）只在**唯一命中**时才算
// ⇒ ⇒ 否则 `package.json` 会把 3 个无关文件拖进来 ⇒ ⇒ 候选集变噪音。
func structuredReaderReferencedPaths(root string, census structuredCensus, tracked []string) []string {
	// ★ 索引：后缀（1~3 段）→ 命中的文件。
	bySuffix := map[string][]string{}
	for _, p := range tracked {
		lower := strings.ToLower(p)
		ok := false
		for _, ext := range structuredCensusExtensions {
			if strings.HasSuffix(lower, ext) {
				ok = true
				break
			}
		}
		if !ok {
			continue
		}
		parts := strings.Split(p, "/")
		for n := 1; n <= 3 && n <= len(parts); n++ {
			tail := strings.Join(parts[len(parts)-n:], "/")
			bySuffix[tail] = append(bySuffix[tail], p)
		}
	}
	seen := map[string]bool{}
	for _, e := range census.Entries {
		if e.Kind != "registry" || strings.TrimSpace(e.Reader) == "" {
			continue
		}
		readerAbs := filepath.Join(root, e.Reader)
		data, err := os.ReadFile(readerAbs)
		if err != nil {
			continue // ★ 读方文件不在（★ 表过期）⇒ ⇒ 由别的判据报
		}
		for _, m := range structuredPathLiteralRE.FindAllStringSubmatch(string(data), -1) {
			lit := strings.TrimSpace(m[1])
			if lit == "" {
				continue
			}
			parts := strings.Split(lit, "/")
			for n := 1; n <= 3 && n <= len(parts); n++ {
				tail := strings.Join(parts[len(parts)-n:], "/")
				hits := bySuffix[tail]
				if len(hits) == 0 {
					continue
				}
				if n == 1 && len(hits) > 1 {
					continue // ★ 单词段字面量命中多个 ⇒ ⇒ 太泛 ⇒ ⇒ 不收
				}
				for _, h := range hits {
					seen[h] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// loadStructuredCensus 读普查表。
func loadStructuredCensus(t *testing.T, root string) structuredCensus {
	t.Helper()
	c, err := readStructuredCensusFile(root)
	if err != nil {
		t.Fatalf("%v\n★ 这张表是「同一物种的登记册不许漏登」的对冲机制 ⇒ ⇒ 必须存在", err)
	}
	if len(c.Entries) == 0 {
		t.Fatalf("普查表是空的 ⇒ ⇒ 覆盖判据恒绿（形同虚设）")
	}
	return c
}

// readStructuredCensusFile 读普查表（★ 可返回错误的形态 ⇒ 供非测试路径用）。
func readStructuredCensusFile(root string) (structuredCensus, error) {
	path := filepath.Join(root, structuredCensusFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return structuredCensus{}, fmt.Errorf("读非 .tsv 登记册普查表失败（%s）：%w", path, err)
	}
	var c structuredCensus
	if err := json.Unmarshal(data, &c); err != nil {
		return structuredCensus{}, fmt.Errorf("解析普查表失败：%w", err)
	}
	if len(c.Entries) == 0 {
		return structuredCensus{}, fmt.Errorf("普查表是空的 ⇒ ⇒ 覆盖判据恒绿（形同虚设）")
	}
	return c, nil
}

// structuredRegistryPaths 返回【普查表里登记为 registry 的那些路径】（★ 已排序）。
//
// ★ 用途：★ 自愈预检（★ 只还原本表登记过的登记册 ⇒ ⇒ 不会误伤仓里其它备份文件）。
func structuredRegistryPaths(root string) ([]string, error) {
	c, err := readStructuredCensusFile(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range c.Entries {
		if e.Kind == "registry" {
			out = append(out, e.Path)
		}
	}
	sort.Strings(out)
	return out, nil
}

// TestStructuredRegistryCensusCoversEveryCandidate 是【覆盖判据】：
// ★ 每一个候选都必须在普查表里**有归属** ⇒ ⇒ 新增一个像登记册的结构化文件而没登记 ⇒ 门红。
func TestStructuredRegistryCensusCoversEveryCandidate(t *testing.T) {
	root := hollowRepoRoot(t)
	census := loadStructuredCensus(t, root)
	cands := structuredCensusCandidates(t, root, census)
	if len(cands) == 0 {
		t.Fatalf("候选集为空 ⇒ 量具坏了")
	}

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
	for _, c := range structuredCensusCandidates(t, root, census) {
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
// ★★ R87 轮：★ 证据不再只是「note 里写了『行为化』」⇒ ⇒ 必须给出
// ★★ **机器可核**的四件套：★ 读方形态 ＋ 读方规格 ＋ 掏空模式 ＋ 红信号。
// ⇒ ⇒ 「标 true 但没给读方」⇒ ⇒ 门红（★ 防「假装覆盖」）。
func TestStructuredRegistryCensusBehaviorFlagIsHonest(t *testing.T) {
	root := hollowRepoRoot(t)
	census := loadStructuredCensus(t, root)
	for _, e := range census.Entries {
		if e.Kind != "registry" {
			continue
		}
		if !e.BehaviorallyVerified {
			// ★ 标 false ⇒ 必须**说清为什么**（★ 「还没验」还是「验不了」）。
			if strings.TrimSpace(e.Note) == "" {
				t.Errorf("%s：behaviorally_verified=false ⇒ 必须在 note 里写明「为什么还没验」", e.Path)
			}
			continue
		}
		// ★ 标 true ⇒ 必须给出机器可核的四件套。
		if !structuredReaderKinds[e.ReaderKind] {
			t.Errorf("%s：behaviorally_verified=true ⇒ 必须给出 reader_kind（★ 可用：node / vitest / gotest / shell，实际 %q）",
				e.Path, e.ReaderKind)
		}
		if strings.TrimSpace(e.ReaderSpec) == "" {
			t.Errorf("%s：behaviorally_verified=true ⇒ 必须给出 reader_spec（★ 读方的路径或 <pkg>|<TestName>）", e.Path)
		}
		if strings.TrimSpace(e.HollowMode) == "" {
			t.Errorf("%s：behaviorally_verified=true ⇒ 必须给出 hollow_mode（★ 可用：%s）",
				e.Path, strings.Join(structuredHollowModeList(), " / "))
		} else {
			known := false
			for _, m := range structuredHollowModeList() {
				if e.HollowMode == m {
					known = true
				}
			}
			if !known {
				t.Errorf("%s：未知的 hollow_mode %q（★ 可用：%s）", e.Path, e.HollowMode,
					strings.Join(structuredHollowModeList(), " / "))
			}
		}
		if strings.TrimSpace(e.RedSignal) == "" {
			t.Errorf("%s：behaviorally_verified=true ⇒ 必须给出 red_signal（★ 约定的红因子串；"+
				"★ 没有它，「红了」只说明退出码非 0，★ 说不出红的是不是该红的那件事）", e.Path)
		}
		if strings.TrimSpace(e.Note) == "" || !strings.Contains(e.Note, "行为化") {
			t.Errorf("%s：behaviorally_verified=true ⇒ 必须在 note 里写明**哪条**行为化验证的证据", e.Path)
		}
		// ★★ mech_check 必须是**共享词表**里的名字（★ 与 `.tsv` 例外表同一份词表）
		// ⇒ ⇒ 两侧的「机械可核断言」不会各说各话。
		mech := strings.TrimSpace(e.MechCheck)
		if mech == "" {
			t.Errorf("%s：behaviorally_verified=true ⇒ 必须给出 mech_check（★ 本条成立的机械可核断言；"+
				"★ 结构化侧用的是 %q ⇒ ⇒ 与 `.tsv` 例外表**同一份词表**）",
				e.Path, "reader-red-on-hollow")
		} else {
			for _, raw := range strings.Split(mech, ",") {
				kind := strings.TrimSpace(raw)
				if kind == "" {
					continue
				}
				if !hollowMechKinds[kind] {
					t.Errorf("%s：未知的机械断言 %q（★ 可用：%s）", e.Path, kind,
						strings.Join(hollowMechKindList(), " / "))
				}
			}
		}
		if e.HollowResult != "red" {
			t.Errorf("%s：behaviorally_verified=true ⇒ hollow_result 必须是 %q（★ 本轮实测的结果；"+
				"★ 若实测是「仍绿」或「不可判定」，★ 那就**不能**标 true ⇒ ⇒ 请如实标 false 并写明理由）",
				e.Path, "red")
		}
	}
}
