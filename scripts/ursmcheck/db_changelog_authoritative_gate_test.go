//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// db_changelog_authoritative_gate_test.go —— ★★ 「SQL 变更登记册的状态列」从此机器可判。
//
// # 这道门补的是哪块缺口
//
// ★ 原始目标里有一条：「需要记录所有的 sql 变更，并加入到部署的序列中，
//   ★★ **确保在多个环境下可以同样的标准来更新**」。
//
// ★ 而本仓的 SQL 变更登记册是 `docs/db-changelog.md`（★ **唯一登记册**），
//   ★ 它的第四列是「Status」—— ★★ **散文**（★ `applied+verified` / `pending deploy` /
//   `registered（此前从未被登记）` / `file-ready（…）` / 长段落 …）。
//
// ★★ **散文 ⇒ 机器判不了** ⇒ ★★ 于是登记册可以【一直错着】而没人知道。
// ★★★ 实测（R160 轮，★ 三环境只读对账）：
//
//   · `890_drop_request_logs_v1_family.sql` 的状态写着
//     「pending deploy（…**尚未部署生产**；本地已应用并复验）」
//     ⇒ ★★ 而 **252（2026-10-21 17:41）与 211（2026-10-21 14:19）的账本里都【已经记账】**
//     ⇒ ★★★ **⇒ 登记册与两个生产实例的真实账本【直接矛盾】**
//     ⇒ ★★ 而且它是**破坏性**迁移（★ 删 v1 `request_logs` 全族）⇒ ★★ 「它部署了没有」
//        正是最该有机器判据的一个事实。
//   · 全量：★ **107 条（迁移 × 环境）对**「声称未应用」但账本说【已 applied】
//     ⇒ 涉及 **59 个迁移号**（★ local 52 / 211 52 / 252 3）。
//
// # ★★ 本门怎么把它变成「机器可判」
//
// ① **结构化载体**：★ 登记册里新增一段「★ 权威状态（机器可判）」
//    （★ `<!-- R160-AUTHORITATIVE:BEGIN -->` … `END` 之间），
//    ★★ 一行一个迁移 × 三格（★ local / 252 / 211）⇒ 每格 `applied@<时间>` 或 `—`
//    ⇒ ★★ **一眼可判**，★ 且**可与账本快照逐格比对**。
// ② **生成器**：`scripts/audit/db-changelog-ledger-reconcile.py render --write`
//    ⇒ ★★ **由工具从账本快照生成** ⇒ ★★ 不手工维护 ⇒ ★★ 与散文【双轨并存】：
//    ★ 散文保留历史（★ 它记录了「为什么」），★ 结构化表负责「现在是什么」。
// ③ **棘轮**：★ 段落头记着「散文状态与账本的矛盾数」⇒ ★ 门要求现值 ≤ 该基线
//    ⇒ ★★ **只许降** ⇒ ★★ 重新生成段落 = 收紧基线。
//
// # 五条判据
//
//	G1 工具在位 + 纯 stdlib（★ `ast.parse` 过）+ ★ **自证绿**
//	G2 ★ 登记册里的权威段与【账本快照】现算结果**逐字一致**（★ 手改 ⇒ 红）
//	G3 快照覆盖**三个环境**（★ local / 252 / 211）且权威段三列齐全
//	    （★ 防止「悄悄少登记一个环境」）
//	G4 ★ **散文矛盾数只许降**（★ 棘轮）
//	G5 ★★ **反向夹具**：★ 故意把一条登记册状态改成「声称未应用」
//	    ⇒ ★★ 对账器**必须**多报出一条矛盾 ⇒ ★★ 证明判据不是恒绿
//
// ★ 诚实边界（本门**没有**做的事）：
//
// ① 本门**不连任何生产库** ⇒ ★ 真相仍在各环境的 `gateway_db_revision_sequences` 里
//    ⇒ ★★ 快照会过期 ⇒ ★★ 「快照过期」只能靠**重采**发现（★ `collect`）⇒ ★ 本门不判「新鲜度」。
// ② 「账本里没有」**推不出**「没应用过」（★ 旁路应用：★ 手工 psql / 另一条序列 / 另一个库）
//    ⇒ ★★ 故本门只判「**声称未应用但账本说已 applied**」这一个方向 ⇒ ★★ 另一方向只作报告。

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const (
	dcaToolRel      = "scripts/audit/db-changelog-ledger-reconcile.py"
	dcaSnapshotRel  = "scripts/audit/asset/db-changelog-ledger-snapshot.tsv"
	dcaChangelogRel = "docs/db-changelog.md"
)

// dcaRepoRoot 返回仓根（★ 用 runtime.Caller：★ 写死 "../.." 会在 cwd 变化时扫不到文件）。
func dcaRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return root
}

// dcaPython 返回可用的 python 解释器（★ CI 的 windows runner 只有 `python` ⇒ ★ 两个都试）。
func dcaPython(t *testing.T) string {
	t.Helper()
	for _, cand := range []string{"python3", "python"} {
		if p, err := exec.LookPath(cand); err == nil {
			return p
		}
	}
	// ★★ 没有就**红**，★ 不静默跳过（★ 「不许静默降级」的落点是退出码）
	t.Fatal("★ 找不到 python3 / python ⇒ ★ 本门依赖纯 stdlib 的对账器 ⇒ ★ 请装 Python 3（★ CI 镜像自带）")
	return ""
}

// dcaRun 跑对账器，返回 stdout / stderr / 退出码。
func dcaRun(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	root := dcaRepoRoot(t)
	argv := append([]string{filepath.Join(root, dcaToolRel)}, args...)
	cmd := exec.Command(dcaPython(t), argv...)
	cmd.Dir = root
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("跑对账器失败（不是退出码问题）：%v\nstderr:\n%s", err, errBuf.String())
		}
		code = ee.ExitCode()
	}
	return outBuf.String(), errBuf.String(), code
}

// dcaReconcileReport 跑对账并解析 JSON 报告。
type dcaReport struct {
	HardContradictions []struct {
		Env       string `json:"env"`
		Migration int    `json:"migration"`
		Filename  string `json:"filename"`
		Kind      string `json:"kind"`
	} `json:"hard_contradictions"`
	SoftContradictions []map[string]any `json:"soft_contradictions"`
	Unjudgeable        []map[string]any `json:"unjudgeable"`
	AppliedButNotReg   []map[string]any `json:"applied_but_not_registered"`
	EnvRowCounts       map[string]int   `json:"envs"`
}

func dcaReconcile(t *testing.T, changelog string) dcaReport {
	t.Helper()
	args := []string{"reconcile", "--snapshot", filepath.Join(dcaRepoRoot(t), dcaSnapshotRel), "--json"}
	if changelog != "" {
		args = append(args, "--changelog", changelog)
	}
	stdout, stderr, code := dcaRun(t, args...)
	// ★★ 对账器在「有矛盾」时退出码是 2（★ 那正是本门要量化的东西）⇒ ★ 不当失败
	if code != 0 && code != 2 {
		t.Fatalf("对账器退出码 %d（★ 应为 0 或 2）\nstderr:\n%s", code, stderr)
	}
	var rep dcaReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("解析对账器 JSON 失败：%v\nstdout:\n%s", err, stdout)
	}
	return rep
}

// ─────────────────────────── G1 · 工具自证 ───────────────────────────

func TestDbChangelogReconcileToolSelfProves(t *testing.T) {
	root := dcaRepoRoot(t)
	tool := filepath.Join(root, dcaToolRel)
	if _, err := os.Stat(tool); err != nil {
		t.Fatalf("对账器缺失：%s（%v）", tool, err)
	}
	// ★ 纯 stdlib 校验（★ 仓内没有 python 包管理文件 ⇒ ★ 不许引入第三方依赖）
	py := dcaPython(t)
	cmd := exec.Command(py, "-c",
		"import ast,sys;ast.parse(open(sys.argv[1],encoding='utf-8').read());print('ast ok')",
		tool)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("对账器 ast.parse 失败：%v\n%s", err, string(out))
	}
	// ★★ 自证必须绿（★ 工具自己的解析面/分类/矛盾检出 四组自检）
	stdout, stderr, code := dcaRun(t, "selftest")
	if code != 0 {
		t.Fatalf("对账器自证红（退出码 %d）\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "自证通过") {
		t.Errorf("对账器自证输出异常：%q", stdout)
	}
}

// ─────────────────────────── G2 · 权威段与快照逐字一致 ───────────────────────────

func TestDbChangelogAuthoritativeSectionMatchesSnapshot(t *testing.T) {
	stdout, stderr, code := dcaRun(t, "render",
		"--snapshot", filepath.Join(dcaRepoRoot(t), dcaSnapshotRel), "--check")
	if code != 0 {
		t.Fatalf("权威状态段与快照不一致（退出码 %d）\nstdout:\n%s\nstderr:\n%s",
			code, stdout, stderr)
	}
}

// ─────────────────────────── G3 · 三环境覆盖 ───────────────────────────

func TestDbChangelogSnapshotCoversThreeEnvironments(t *testing.T) {
	rep := dcaReconcile(t, "")
	want := []string{"local", "252", "211"}
	for _, e := range want {
		n, ok := rep.EnvRowCounts[e]
		if !ok {
			t.Errorf("快照里没有环境 %s（★ 现有：%v）", e, rep.EnvRowCounts)
			continue
		}
		if n == 0 {
			t.Errorf("环境 %s 的账本行数为 0 ⇒ ★ 快照没采到东西", e)
		}
	}
	// ★ 权威段里也必须三列齐全（★ 防止「段落里少写一个环境」）
	body, err := os.ReadFile(filepath.Join(dcaRepoRoot(t), dcaChangelogRel))
	if err != nil {
		t.Fatalf("读登记册失败：%v", err)
	}
	sec := string(body)
	for _, e := range want {
		if !strings.Contains(sec, "| "+e+" |") && !strings.Contains(sec, "| "+e+" | `applied@") {
			// ★ 表头形态是 `| 211 | 252 | local |` ⇒ ★ 用列头判据兜底
			if !strings.Contains(sec, "|"+e+"|") {
				t.Errorf("权威状态段里看不到环境列 %s", e)
			}
		}
	}
}

// ─────────────────────────── G4 · 散文矛盾数只许降 ───────────────────────────

func TestDbChangelogProseBacklogIsRatchet(t *testing.T) {
	root := dcaRepoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, dcaChangelogRel))
	if err != nil {
		t.Fatalf("读登记册失败：%v", err)
	}
	sec := string(body)
	idx := strings.Index(sec, "散文状态与账本的矛盾数")
	if idx < 0 {
		t.Fatal("★ 登记册的权威段里没有「散文状态与账本的矛盾数」⇒ ★ 棘轮基线丢失 ⇒ ★ 门失效")
	}
	seg := sec[idx:min(idx+300, len(sec))]
	// ★★ 基线写在反引号里（★ Markdown 装饰 ⇒ ★★ 不能靠 Fields 切 ⇒ ★ 用正则）
	m := backlogRe.FindStringSubmatch(seg)
	if m == nil {
		t.Fatalf("★ 解析不出棘轮基线（★ 段落片段：%q）", seg)
	}
	baseline := dcaAtoiSafe(m[1])
	if baseline < 0 {
		t.Fatalf("★ 棘轮基线不是非负整数：%q", m[1])
	}
	rep := dcaReconcile(t, "")
	cur := len(rep.HardContradictions)
	if cur > baseline {
		t.Errorf("★ 散文矛盾数 %d > 登记册里的棘轮基线 %d ⇒ ★★ 新增了「声称未应用但账本已 applied」的过期登记\n"+
			"★ 修法：★ ① 订正对应登记册行的状态；★ 或 ② 重新生成权威段（★ `render --write`）并写明理由",
			cur, baseline)
	}
	t.Logf("★ 散文矛盾数 %d / 基线 %d（★ 棘轮）", cur, baseline)
}

// ─────────────────────────── G6 · 双口径互证（★ 堵「自证可被拆」的洞） ───────────────────────────

// dcaParseEffective 独立解析登记册 ⇒ {迁移号: 行号}。
//
// ★★ 为什么要有这份【独立实现】：
// ★ 工具（Python）自己有自证，★ 但自证【可以被拆掉】（★ 变异 M5 实测：
// ★ 把自证里那条断言改成恒真 ⇒ ★★ 门仍绿）⇒ ★★ 门不能只信工具的自证。
// ⇒ ★★ 于是门自己再解析一遍（★ Go 实现，★ 与 Python 实现【互不知情】），
// ★★ 再用工具的 `locate` 去对拍 ⇒ ★★ 两份口径一致才绿。
//
// ★★ 口径（★ 与工具同源但独立实现）：★ 同号取【节日期最新】的行（★ 同节取行号最后）。
func dcaParseEffective(t *testing.T, path string) map[int]int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读登记册失败：%v", err)
	}
	lines := strings.Split(string(body), "\n")
	curKey := 0
	best := map[int]int{}
	bestKey := map[int]int{}
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## ") {
			curKey = dcaDateKey(ln)
			continue
		}
		if !strings.HasPrefix(ln, "|") {
			continue
		}
		cells := strings.Split(strings.TrimSpace(strings.Trim(ln, "|")), "|")
		if len(cells) < 4 {
			continue
		}
		num := dcaAtoiSafe(strings.TrimSpace(cells[0]))
		if num < 0 {
			continue
		}
		if _, ok := best[num]; !ok || curKey > bestKey[num] ||
			(curKey == bestKey[num] && i+1 > best[num]) {
			best[num] = i + 1
			bestKey[num] = curKey
		}
	}
	return best
}

// dcaDateKey 从节标题里取一个可比较的日期键（★ 解析不出来 ⇒ 0 ⇒ 最小）。
func dcaDateKey(title string) int {
	re := regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})`)
	m := re.FindStringSubmatch(title)
	if m == nil {
		return 0
	}
	y := dcaAtoiSafe(m[1])
	mo := dcaAtoiSafe(m[2])
	d := dcaAtoiSafe(m[3])
	if y < 0 || mo < 0 || d < 0 {
		return 0
	}
	return y*10000 + mo*100 + d
}

// TestDbChangelogEffectiveRowRuleAgreesAcrossTwoImplementations —— ★★ 双口径互证。
func TestDbChangelogEffectiveRowRuleAgreesAcrossTwoImplementations(t *testing.T) {
	root := dcaRepoRoot(t)
	eff := dcaParseEffective(t, filepath.Join(root, dcaChangelogRel))
	if len(eff) == 0 {
		t.Fatal("★ 独立解析没拿到任何有效登记行 ⇒ ★ 解析器坏了")
	}
	// ★ 挑【登记过多次】的号（★ 同号规则只有在这种情况下才有判别力）
	counts := map[int]int{}
	body, err := os.ReadFile(filepath.Join(root, dcaChangelogRel))
	if err != nil {
		t.Fatalf("读登记册失败：%v", err)
	}
	for _, ln := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(ln, "|") {
			continue
		}
		cells := strings.Split(strings.TrimSpace(strings.Trim(ln, "|")), "|")
		if len(cells) < 4 {
			continue
		}
		if n := dcaAtoiSafe(strings.TrimSpace(cells[0])); n >= 0 {
			counts[n]++
		}
	}
	var dups []int
	for n, c := range counts {
		if c > 1 {
			dups = append(dups, n)
		}
	}
	if len(dups) == 0 {
		t.Fatal("★ 登记册里没有同号多次登记的行 ⇒ ★ 同号规则无从判别 ⇒ 本门恒绿")
	}
	sort.Ints(dups)
	// ★ 取首尾各若干个（★ 确定性 ⇒ 不依赖 map 迭代序）
	picks := map[int]bool{}
	for i := 0; i < 3 && i < len(dups); i++ {
		picks[dups[i]] = true
	}
	for i := len(dups) - 3; i < len(dups); i++ {
		if i >= 0 {
			picks[dups[i]] = true
		}
	}
	checked := 0
	for num := range picks {
		out, _, code := dcaRun(t, "locate", "--migration", dcaItoa(num))
		if code != 0 {
			t.Errorf("locate 迁移号 %d 失败（退出码 %d）", num, code)
			continue
		}
		var loc struct {
			Line int `json:"line"`
		}
		if err := json.Unmarshal([]byte(out), &loc); err != nil {
			t.Errorf("解析 locate 输出失败：%v", err)
			continue
		}
		want, ok := eff[num]
		if !ok {
			t.Errorf("★ 独立解析里没有迁移号 %d", num)
			continue
		}
		checked++
		if loc.Line != want {
			t.Errorf("★ 迁移号 %d 的【有效行】两份口径不一致（★ Go 独立解析 = %d，★ 工具 locate = %d）\n"+
				"★ ⇒ ★★ 「同号取哪一行」有两套逻辑 ⇒ ★★ 必然漂移 ⇒ ★（★ 变异实测：★ 把工具改成"+
				"「取文件里最后一行」时 ★ 本条会红）", num, want, loc.Line)
		}
	}
	if checked == 0 {
		t.Fatal("★ 一条也没对拍成 ⇒ 本门恒绿")
	}
	t.Logf("★ 双口径互证：%d 个同号多次登记的迁移号对拍一致", checked)
}

// ─────────────────────────── G5 · 反向夹具：判据有牙 ───────────────────────────

func TestDbChangelogReconcileDetectsInjectedContradiction(t *testing.T) {
	root := dcaRepoRoot(t)
	base := dcaReconcile(t, "")
	baseN := len(base.HardContradictions)

	// ★★ 夹具要改的是【有效登记行】（★ 同一号可能登记过多次 ⇒ ★ 有效行 = 最新节那行）
	//   ⇒ ★★ 所以必须用**工具自己的 locate** 拿行号 ⇒ ★ 不在门里重写一遍解析器
	//   （★★ 否则门与工具会各有一份「哪行算数」的逻辑 ⇒ ★★ 两份逻辑必然漂移）
	snapBody, err := os.ReadFile(filepath.Join(root, dcaSnapshotRel))
	if err != nil {
		t.Fatalf("读快照失败：%v", err)
	}
	pick := ""
	pickNum := 0
	for _, ln := range strings.Split(string(snapBody), "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		f := strings.Split(ln, "\t")
		if len(f) < 3 || f[0] == "env" {
			continue
		}
		pick = f[2]
		pickNum = dcaAtoiSafe(f[1])
		break
	}
	if pick == "" || pickNum <= 0 {
		t.Fatal("★ 快照里没有可用文件 ⇒ 夹具无法构造")
	}

	locOut, _, code := dcaRun(t, "locate", "--migration", dcaItoa(pickNum))
	if code != 0 {
		t.Fatalf("locate 迁移号 %d 失败（退出码 %d）", pickNum, code)
	}
	var loc struct {
		Line     int    `json:"line"`
		File     string `json:"filename"`
		Basename string `json:"basename"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal([]byte(locOut), &loc); err != nil {
		t.Fatalf("解析 locate 输出失败：%v\n%s", err, locOut)
	}
	// ★★ 登记册的文件列**可能带路径**（★ 实测有一行带 `sql/migrations/domain/…`）
	//   ⇒ ★ 比较一律用 basename（★ 与对账器同口径 ⇒ ★ 不在门里造第二套口径）
	if loc.Basename != pick {
		t.Fatalf("★ locate 给的文件 %s（basename %s）与快照里挑的 %s 不一致",
			loc.File, loc.Basename, pick)
	}

	changelogBody, err := os.ReadFile(filepath.Join(root, dcaChangelogRel))
	if err != nil {
		t.Fatalf("读登记册失败：%v", err)
	}
	// ★★ 把【有效行】的 Status 整段换成「pending deploy」⇒ ★ 制造矛盾
	lines := strings.Split(string(changelogBody), "\n")
	if loc.Line < 1 || loc.Line > len(lines) {
		t.Fatalf("★ locate 给的行号 %d 越界（★ 登记册共 %d 行）", loc.Line, len(lines))
	}
	target := lines[loc.Line-1]
	cells := strings.Split(target, "|")
	if len(cells) < 5 {
		t.Fatalf("★ 有效行 %d 的单元格不足：%q", loc.Line, target)
	}
	cells[4] = " pending deploy"
	lines[loc.Line-1] = strings.Join(cells, "|")
	tmp := filepath.Join(t.TempDir(), "db-changelog-mutated.md")
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatalf("写变异登记册失败：%v", err)
	}

	mut := dcaReconcile(t, tmp)
	gotN := len(mut.HardContradictions)
	if gotN <= baseN {
		t.Errorf("★ 把 %s 的状态改成「pending deploy」后，硬矛盾数没有增加（★ %d → %d）\n"+
			"★ ⇒ ★★ 对账器的矛盾判据【没有牙】⇒ ★ 本门是恒绿门", pick, baseN, gotN)
	}
}

// backlogRe 取登记册权威段里的棘轮基线（★ 数字写在反引号里 ⇒ ★ 不能靠 Fields 切）。
var backlogRe = regexp.MustCompile("矛盾数[^`]*`([0-9]+)`")

// itoa 把非负整数转成字符串。
func dcaItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// dcaAtoiSafe 解析非负整数，失败返回 -1。
func dcaAtoiSafe(s string) int {
	if s == "" {
		return -1
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}
