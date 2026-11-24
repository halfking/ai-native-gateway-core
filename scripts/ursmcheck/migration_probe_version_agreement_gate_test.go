//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// migration_probe_version_agreement_gate_test.go —— 部署通道「产物探针判据里的版本号」
// 与「迁移自登记的版本号」的一致性门。
//
// ── 判据 ──────────────────────────────────────────────────────────────────
//
// ★ 一条 startup 迁移的产物探针（★ scripts/apply-db-revision-sequence.sh 里的
//   `ledger_artifact_probe "<file>" "<判据>"`）★ 如果它的判据里查了台账
//   （★ 即含 `version = '<号>'`），★ 那么**那个号必须逐字等于该迁移自己
//   自登记进 schema_migrations 的号** ★★。
//
// ★ 例外清单：scripts/ursmcheck/migration_probe_version_exemptions.tsv
//   （★ 2 行，★ kind ∈ repair（刻意补记账）/ skew（真错位但存量豁免））
//
// ── 为什么要有这道门（★ 2026-11-24 第四轮 L1/L11 调查实测）────────────────
//
// ★ ★★ 背景：★ L1 的五条迁移把自登记版本号**写错一格**（★ 985 写 '984'、
// ★★ 986 写 '985'、…）★ ⇒ ★★ **探针判据里那个号是跟着错位一起错的** ★★
// ⇒ ★★ 于是 ★★ 「探针查的号」与「迁移写的号」**彼此自洽** ⇒ ★★
// ⇒ ★★ **任何以「两者一致」为前提的检查都会认为它们没问题** ⇒ ★★
// ⇒ ★★ **错位对彼此无法发现** ★★（★ ★★ 这就是 §C.17 记的「探针判据 ×4 无门兜」★★）。
//
// ★ ★★ 而 §C.17 还指出一个更隐蔽的事实：★ ★★ **改字节修不好 '984' 那行** ★★
// ★ （★ 984 自己写的是 '984'、★ 字节没变 ⇒ ★ 不重放 ⇒ ★ 那行永远不会被纠正）★★
// ★ ⇒ ★★ **而这条门能抓住它**（★ 因为 984 的探针查 '984'、★ 985 的探针也查 '984'
// ★★ ⇒ ★★ 两条探针**指向同一行** ⇒ ★★ 正是「抢同一行」的机械证据）★★。
//
// ── 门的形状（★ 同构 L1 身份门：★ 存量豁免 + 新增即红）────────────────────
//
// ★ R1「判据与自登记不一致」：★ 探针查的号 ≠ 该迁移自登记的号，★ 且不在豁免表里
//   ⇒ **红并点名**；
// ★ R2「豁免棘轮」：★ 豁免表行数**只许减不许增**（★ 上限 2）⇒ **红**；
// ★ R3「豁免已无用」：★ 豁免行对应的件已不存在、★ 或已不再查台账、★ 或已一致
//   ⇒ **红**（★ 提示摘掉该行，★ 基线更紧）；
// ★ R4「豁免表卫生」：★ 必须**按文件名有序且无重复**、★ `kind ∈ {repair, skew}` ⇒ **红**。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	probeVersionExemptionsFile = "scripts/ursmcheck/migration_probe_version_exemptions.tsv"
	deploySequenceScriptRel    = "scripts/apply-db-revision-sequence.sh"
	startupMigrationsDirRel    = "sql/migrations/startup"

	// probeVersionExemptionCap 是豁免表行数上限（★ 2026-11-24 基线 = 2 行）。
	probeVersionExemptionCap = 2
)

var (
	// probeVersionCallRe 抽出「探针调用」：文件名 ＋ 判据。
	// ★ ★★ 必须容忍判据里的转义引号（★ 实测 926 那条判据含 `\"`）★★。
	probeVersionCallRe = regexp.MustCompile(`ledger_artifact_probe\s+"([^"]+)"\s*\\\s*\n\s*"(.*?)"\s*(?:\\)?\s*\n`)
	// probeVersionVerRe 抽判据里查台账的版本号（★ 每条探针至多一个、★ 实测分布 0/1）。
	probeVersionVerRe = regexp.MustCompile(`version\s*=\s*'([^']+)'`)
	// probeVersionSelfRegRe 抽迁移自己的自登记版本号（★ 与 L1 身份门同口径）。
	probeVersionSelfRegRe = regexp.MustCompile(
		`(?i)INSERT\s+INTO\s+(?:public\.)?schema_migrations\s*\([^)]*\)\s*VALUES\s*\(\s*'([^']+)'`)
)

// ── 纯函数核心（★ 便于变异自证）──────────────────────────────────────────

// probeVersionAgreement 计算「探针查的号 ≠ 迁移自登记的号」的件。
// probes: 文件名 → 判据文本；selfReg: 文件名 → 自登记版本号列表；exempt: 文件名 → 是否豁免。
func probeVersionAgreement(probes map[string]string, selfReg map[string][]string,
	exempt map[string]bool) []string {
	var out []string
	names := make([]string, 0, len(probes))
	for n := range probes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		vers := probeVersionVerRe.FindAllStringSubmatch(probes[n], -1)
		if len(vers) == 0 {
			continue // ★ 判据不查台账 ⇒ 不在判据视野内（★ 不是「通过」，★ 是「无关」）
		}
		if len(vers) > 1 {
			out = append(out, n+"\x00MULTI\x00")
			continue
		}
		want := vers[0][1]
		reg, ok := selfReg[n]
		if !ok || len(reg) == 0 {
			continue // ★ 该迁移没有自登记段 ⇒ 本门无从比对
		}
		agree := false
		for _, r := range reg {
			if r == want {
				agree = true
				break
			}
		}
		if !agree && !exempt[n] {
			out = append(out, n+"\x00"+want+"\x00"+strings.Join(reg, ","))
		}
	}
	sort.Strings(out)
	return out
}

// ── 采集（★ 只读）─────────────────────────────────────────────────────────

// probeVersionCollectProbes 抽部署脚本里的全部产物探针（★ 文件名 → 判据）。
func probeVersionCollectProbes(t *testing.T, root string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, deploySequenceScriptRel))
	if err != nil {
		t.Fatalf("★ 读 %s 失败：%v", deploySequenceScriptRel, err)
	}
	out := map[string]string{}
	for _, m := range probeVersionCallRe.FindAllStringSubmatch(string(data), -1) {
		out[m[1]] = m[2]
	}
	if len(out) == 0 {
		t.Fatalf("★ 从 %s 抽不到任何产物探针 ⇒ 量具失效", deploySequenceScriptRel)
	}
	return out
}

// probeVersionCollectSelfReg 抽各迁移自己的自登记版本号。
func probeVersionCollectSelfReg(t *testing.T, root string) map[string][]string {
	t.Helper()
	dir := filepath.Join(root, startupMigrationsDirRel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("★ 读 %s 失败：%v", dir, err)
	}
	out := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") || strings.HasSuffix(e.Name(), ".down.sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("★ 读 %s 失败：%v", e.Name(), err)
		}
		var vs []string
		for _, m := range probeVersionSelfRegRe.FindAllStringSubmatch(string(data), -1) {
			vs = append(vs, m[1])
		}
		if len(vs) > 0 {
			out[e.Name()] = vs
		}
	}
	return out
}

// probeVersionReadExemptions 读豁免表（★ 读不到必须硬失败 ⇒ 不许 Skip）。
func probeVersionReadExemptions(t *testing.T, root string) ([]string, map[string]bool, map[string]string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, probeVersionExemptionsFile))
	if err != nil {
		t.Fatalf("★ 读豁免表 %s 失败（★ 读不到不许 Skip，★ 那是假绿）：%v",
			probeVersionExemptionsFile, err)
	}
	var order []string
	exempt := map[string]bool{}
	kind := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			t.Fatalf("★ 豁免表行格式不对（★ 期望 file<TAB>kind<TAB>reason）：%q", line)
		}
		order = append(order, parts[0])
		exempt[parts[0]] = true
		kind[parts[0]] = parts[1]
	}
	if len(order) == 0 {
		t.Fatalf("★ 豁免表为空 ⇒ ★ 判据的输入集合不对")
	}
	return order, exempt, kind
}

// ── 主门 ──────────────────────────────────────────────────────────────────

func TestMigrationProbeVersionAgreementGate(t *testing.T) {
	root := envRepoRoot(t)
	probes := probeVersionCollectProbes(t, root)
	selfReg := probeVersionCollectSelfReg(t, root)
	order, exempt, kind := probeVersionReadExemptions(t, root)

	// ★ R1：判据与自登记不一致且未豁免 ⇒ 红并点名。
	bad := probeVersionAgreement(probes, selfReg, exempt)
	if len(bad) > 0 {
		var lines []string
		for _, s := range bad {
			p := strings.Split(s, "\x00")
			if p[1] == "MULTI" {
				lines = append(lines, "    "+p[0]+"  判据里查了**多个**版本号 ⇒ ★ 请拆成多条探针")
				continue
			}
			lines = append(lines, "    "+p[0]+"  探针查 '"+p[1]+"'  但该迁移自登记的是 '"+p[2]+"'")
		}
		t.Errorf("★★ 发现 %d 处「产物探针判据里查的台账版本号 ≠ 该迁移自己自登记的版本号」且**不在豁免表里** ⇒ ★★ "+
			"★ **探针与错位是配套的**（★ 探针查的号跟着错位一起错）⇒ ★★ **两者彼此自洽、★ 互相发现不了** ⇒ ★★ "+
			"★ 新增这样的迁移必须立刻改正（★ 存量见 %s）★★\n%s",
			len(bad), probeVersionExemptionsFile, strings.Join(lines, "\n"))
	}

	// ★ R2：豁免表行数只许减不许增。
	if len(order) > probeVersionExemptionCap {
		t.Errorf("★★ 豁免表有 %d 行，★ 超过上限 %d 行 ⇒ ★★ 豁免只许减不许增 ★★\n"+
			"★★ （★ 真正的错位要**改迁移字节让它归位**，★ 不是往豁免表里加一行）★★",
			len(order), probeVersionExemptionCap)
	}

	// ★ R3：豁免行若已不再需要 ⇒ 红（★ 提示摘掉，★ 基线更紧）。
	for _, f := range order {
		sql, stillProbed := probes[f]
		if !stillProbed {
			t.Errorf("★★ 豁免表里的 %s **已不在任何产物探针里** ⇒ ★★ 请摘掉这一行 ★★", f)
			continue
		}
		vers := probeVersionVerRe.FindAllStringSubmatch(sql, -1)
		if len(vers) == 0 {
			t.Errorf("★★ 豁免表里的 %s **探针已不查台账了** ⇒ ★★ 请摘掉这一行 ★★", f)
			continue
		}
		want := vers[0][1]
		reg, ok := selfReg[f]
		if !ok || len(reg) == 0 {
			t.Errorf("★★ 豁免表里的 %s **已没有自登记段** ⇒ ★★ 请摘掉这一行 ★★", f)
			continue
		}
		agree := false
		for _, r := range reg {
			if r == want {
				agree = true
				break
			}
		}
		if agree {
			t.Errorf("★★ 豁免表里的 %s 的探针判据（查 '%s'）**已与自登记一致** ⇒ ★★ 请摘掉这一行 ★★\n"+
				"★★ （★ 该迁移可能已被改成正确的版本号 ⇒ ★ 豁免就该收掉）★★", f, want)
		}
	}

	// ★ R4：豁免表卫生（★ 按文件名有序 ＋ 无重复 ＋ kind 合法）。
	if !sort.StringsAreSorted(order) {
		t.Errorf("★★ 豁免表**不是按文件名有序的** ⇒ ★ 请按文件名升序排列（★ %s）★★",
			probeVersionExemptionsFile)
	}
	seen := map[string]bool{}
	for _, f := range order {
		if seen[f] {
			t.Errorf("★★ 豁免表里有**重复**条目 %s ⇒ ★ 请去重（★ %s）★★", f, probeVersionExemptionsFile)
		}
		seen[f] = true
	}
	for f, kd := range kind {
		if kd != "repair" && kd != "skew" {
			t.Errorf("★★ 豁免表里 %s 的 kind='%s' ⇒ ★★ 只允许 'repair' 或 'skew' ★★", f, kd)
		}
	}

	withLedger, judged, consistent := 0, 0, 0
	for f, sql := range probes {
		vers := probeVersionVerRe.FindAllStringSubmatch(sql, -1)
		if len(vers) == 0 {
			continue
		}
		withLedger++
		reg, ok := selfReg[f]
		if !ok || len(reg) == 0 {
			continue
		}
		judged++
		agree := false
		for _, r := range reg {
			if r == vers[0][1] {
				agree = true
				break
			}
		}
		if agree {
			consistent++
		}
	}
	t.Logf("★ 探针版本一致性门：抽 %d 条产物探针；★ 判据里查台账的 %d 条、★ 与自登记逐字一致 %d 条；"+
		"★ 在判据视野内（★ 有自登记段可比对）%d 条；★ 豁免 %d 行（★ 上限 %d）★",
		len(probes), withLedger, consistent, judged, len(order), probeVersionExemptionCap)
}

// ── 变异自证（★ 门必须真的能红）────────────────────────────────────────

func TestMigrationProbeVersionAgreementGateBites(t *testing.T) {
	probes := map[string]string{
		"985_ok.sql":     "(SELECT count(*) FROM public.schema_migrations WHERE version = '985') = 1",
		"986_skewed.sql": "(SELECT count(*) FROM public.schema_migrations WHERE version = '985') = 1",
	}
	selfReg := map[string][]string{
		"985_ok.sql":     {"985"},
		"986_skewed.sql": {"986"},
	}

	// ★ 变异臂 1：★ 判据查的号 ≠ 自登记的号 ⇒ 必须被抓出并点名。
	bad := probeVersionAgreement(probes, selfReg, map[string]bool{})
	if len(bad) != 1 {
		t.Fatalf("★ 变异自证失败：★ 期望恰好 1 处不一致，★ 实得 %d（%v）", len(bad), bad)
	}
	if !strings.Contains(bad[0], "986_skewed.sql") {
		t.Fatalf("★ 变异自证失败：★ 期望点名 986_skewed.sql，★ 实得 %q", bad[0])
	}

	// ★ 变异臂 2：★ 加进豁免 ⇒ 必须不再报（★ 证明豁免路径是通的、★ 不是无条件放过）。
	bad2 := probeVersionAgreement(probes, selfReg, map[string]bool{"986_skewed.sql": true})
	if len(bad2) != 0 {
		t.Fatalf("★ 变异自证失败：★ 豁免后不该再报，★ 实得 %v", bad2)
	}

	// ★ 变异臂 3：★ 判据不查台账 ⇒ 不在视野内（★ 「无关」≠「不一致」）。
	p3 := map[string]string{"990_artifact_only.sql": "to_regclass('public.x') IS NOT NULL"}
	if got := probeVersionAgreement(p3, selfReg, map[string]bool{}); len(got) != 0 {
		t.Fatalf("★ 变异自证失败：★ 不查台账的探针不该被判不一致，★ 实得 %v", got)
	}

	// ★ 变异臂 4：★ 抽出的判据必须能容纳转义引号（★ 实测 926 那条含 `\"`）★★。
	esc := `to_regclass('public.x') IS NOT NULL AND (SELECT count(*) FROM public.schema_migrations WHERE version = '999') = 1`
	p4 := map[string]string{"926_escaped.sql": esc}
	got4 := probeVersionAgreement(p4, map[string][]string{"926_escaped.sql": {"999"}}, map[string]bool{})
	if len(got4) != 0 {
		t.Fatalf("★ 变异自证失败：★ 版本号一致的件不该被判不一致，★ 实得 %v", got4)
	}
}
