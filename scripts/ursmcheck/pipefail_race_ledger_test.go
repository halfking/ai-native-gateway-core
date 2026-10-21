package ursm_test

// R97 轮的门：**「SIGPIPE 竞态面」的台账完整性门**。
//
// # 这道门补的是哪块缺口
//
// R96 轮在 `scripts/audit/measure-selfcreated-cleanup.sh` 上抓到过一个真缺陷：
//
//	printf '%s' "$body" | grep -qE '…'      ← 写端是 bash 内建 printf
//	                                            管道退出码被写端的 SIGPIPE(141) 污染
//
// ⇒ 它的危险不在「那一个脚本」，而在**全仓有多少处同形状的管道**。
// R96 轮登记的待办是「≈100 脚本 / 240 处」——**那是估算，不是读数**。
// R97 轮把这件事做成了：静态普查量具 `scripts/audit/measure-pipefail-race-surface.py`
// 给出**候选集**，逐条人工/实测裁决落进**台账** `pipefail_race_ledger.tsv`。
//
// # 这道门**不**要求「零风险」
//
// ★ 理由：候选集里有 1 条**别人的**文件上的**确定性真缺陷**
//   （`scripts/hotzone-request-mirror-reconcile.sh:152`）。
//   ⇒ 若门要求「零 risk 行」，这个门会**永久红** ⇒⇒ 没人看它 ⇒⇒ 门就废了。
// ⇒⇒ 所以本门管的是**台账的完整性**，不是**风险的清零**：
//
//	① 候选集与台账**键集完全一致**（不漏、不多）——台账不能悄悄腐化；
//	② 每行都有**合法**的裁决，且**不允许 `pending`**（静态判据只能给候选集，
//	   终值必须逐条人工/实测做出）；
//	③ `risk-*` / `conditional` 行**必须带证据**（读数或实测）——不许空口断言。
//
// # ★ 为什么用「跑 python 量具」而不是在 Go 里重写一遍扫描
//
// ★ 同 R96 轮的「官方口径 = 直接调官方门未导出函数」同族：
//   **门与量具必须是同一个实现**，否则两边会各自漂移，
//   于是「门绿」只说明「门和自己一致」，不说明「和事实一致」。
// ⇒ 这里选择**直接执行仓内的普查量具**（python3）并解析它的 TSV：
//   量具一改，门立刻跟着变 ⇒ **零漂移**。
//   ⇒ 代价：门依赖 python3 ⇒ 不存在时**显式 SKIP 并写明理由**（不许静默通过）。
//
// # ★ 门自身的「有牙」证明
//
// `TestPipefailRaceLedgerGateHasTeeth` 会**删掉台账的一行**（只动临时副本），
// 然后断言比对函数**必须**报出那一行缺失 ⇒ 证明判据 ① 真的在检查，
// 而不是恒真。
//
// # ★★★★ R98 轮补的两块（R98 = 审计 R97 轮自己的门）
//
// ① **本门现在跑 carry-over 路径了**（R97 轮**只跑扫描路径** ⇒ 那条路径没人看守）：
//   · 门调用量具时**一次扫描、两个产物**（候选集 + 台账副本的重生成结果）
//   · 新增 `TestPipefailRaceLedgerCarryOverPreservesVerdicts`：逐键比对
//     verdict / reason / evidence ⇒ **carry-over 不得改动人工裁决**。
//   ⇒ 额外成本 = 0（量具单次运行实测 ≈ 170 s ⇒ 绝不能多跑一次）。
//
// ② **证据断言收紧到全表**（R97 轮按 `usage ∈ {bare,value}` 整段跳过
//   ⇒ 实测只覆盖 116/354 行 ⇒ 被跳过的行里有 34 行零证据）。
//
// ③ **carry-over 路径的不变量由**另一个门**用合成夹具 + 量具级变异守住**：
//   `scripts/ursmcheck/pipefail_race_tool_mutation_test.go`
//   ⇒ 三条核心不变量 + 5 条量具级变异（变异在**副本**上做 ⇒ 仓内真文件零改动）。

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

const (
	// 台账（与本文件同目录）
	r97LedgerFile = "pipefail_race_ledger.tsv"
	// 普查量具（仓内相对路径）
	r97ToolRel = "scripts/audit/measure-pipefail-race-surface.py"
)

// r97ValidVerdicts = 台账里允许出现的**终值**裁决。
// ★ 与量具里的 VALID_VERDICTS 一一对应（改一处必须改两处 —— 门会校验）。
// ★ `pending` 不在这里：它是量具的**初稿态**，门明确**禁止**它出现在台账里
//
//	（见 r97PendingVerdict）⇒ 所以「量具的集合」= 这里的集合 ∪ {pending}。
var r97ValidVerdicts = map[string]bool{
	"benign-bounded":     true,
	"benign-if-bounded":  true,
	"benign-value":       true,
	"benign-bare":        true,
	"risk-deterministic": true,
	"risk-race":          true,
	"conditional":        true,
}

// r97PendingVerdict = 台账里**不允许**残留的初稿态裁决。
const r97PendingVerdict = "pending"

// r97RepoRoot 用 runtime.Caller 定位仓库根（不依赖测试的工作目录）。
func r97RepoRoot(t *testing.T) string {
	t.Helper()
	root, err := r97RepoRootErr()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func r97RepoRootErr() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller 失败 ⇒ 无法定位仓库根")
	}
	// scripts/ursmcheck/xxx_test.go ⇒ 上一级是 scripts ⇒ 再上一级是仓库根
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile))), nil
}

// r97Scan = 一次量具运行的**两个产物**。
//
// ★★ R98 轮修正：R97 轮的门调用量具时**只带 `--out`**（候选集）
//
//	⇒ ⇒ `build_ledger` / carry-over 六步级联 / 口径漂移作废 / 裁决串味自检
//	⇒ ⇒ **整条路径从来没被门跑到** ⇒ ⇒ 门守不住 R97 自己的核心不变量。
//
// ⇒ ⇒ 现在**一次扫描、两个产物**（候选集 + 台账副本的重生成结果）
//
//	⇒ ⇒ 额外成本 = 0（量具单次运行实测 ≈ 170 s ⇒ 绝不能再多跑一次）。
type r97Scan struct {
	cands   map[string]bool     // 候选集键集
	carried map[string][]string // 重生成台账：key → 列切片
}

var r97ScanOnce sync.Once
var r97ScanRes r97Scan
var r97ScanErr error

// r97ScanRepo 跑一次仓内普查量具（候选集 + 台账副本重生成），结果缓存。
func r97ScanRepo(t *testing.T) r97Scan {
	t.Helper()
	r97ScanOnce.Do(func() {
		r97ScanRes, r97ScanErr = r97ScanRun()
	})
	if r97ScanErr != nil {
		t.Fatalf("★ 普查量具不可用 ⇒ 分母不可信 ⇒ 拒绝出读数：\n%v", r97ScanErr)
	}
	return r97ScanRes
}

// r97Candidates = 候选集键集合（★ 历史接口，保持不变）。
func r97Candidates(t *testing.T) map[string]bool {
	t.Helper()
	return r97ScanRepo(t).cands
}

func r97ScanRun() (r97Scan, error) {
	root, err := r97RepoRootErr()
	if err != nil {
		return r97Scan{}, err
	}
	tool := filepath.Join(root, r97ToolRel)
	if _, err := os.Stat(tool); err != nil {
		return r97Scan{}, fmt.Errorf("普查量具不存在：%s ⇒ %v", tool, err)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		return r97Scan{}, fmt.Errorf("★ 门依赖 python3（执行仓内普查量具 %s），本机没有 ⇒ 显式 SKIP：%w", r97ToolRel, err)
	}
	dir, err := os.MkdirTemp("", "r97cand")
	if err != nil {
		return r97Scan{}, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "cand.tsv")

	// ★★ 台账副本：把**已提交台账**复制一份交给量具重生成
	//   ⇒ 量具走的是「带 --ledger」的路径 ⇒ **carry-over 路径被真正跑到**
	//   ⇒ ⇒ 产出的台账可与已提交台账逐键比对（见 TestPipefailRaceLedgerCarryOverPreservesVerdicts）。
	//   ⇒ 真台账**只读**（量具只写副本）⇒ 对仓零写入。
	ledgerSrc := filepath.Join(root, "scripts", "ursmcheck", r97LedgerFile)
	raw, err := os.ReadFile(ledgerSrc)
	if err != nil {
		return r97Scan{}, fmt.Errorf("读已提交台账失败（%s）：%v", ledgerSrc, err)
	}
	ledgerCopy := filepath.Join(dir, "ledger.tsv")
	if err := os.WriteFile(ledgerCopy, raw, 0o644); err != nil {
		return r97Scan{}, fmt.Errorf("写台账副本失败：%v", err)
	}

	cmd := exec.Command("python3", tool, "--root", root, "--out", out, "--ledger", ledgerCopy)
	// ★ 量具自检失败 ⇒ exit 3 ⇒ 门必须红（分母不可信 ⇒ 拒绝出读数）
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return r97Scan{}, fmt.Errorf("★ 普查量具执行失败（自检没过 ⇒ 分母不可信）⇒ 拒绝出读数：\n%s", string(combined))
	}
	f, err := os.Open(out)
	if err != nil {
		return r97Scan{}, fmt.Errorf("打不开候选集 %s：%v", out, err)
	}
	defer f.Close()
	cands := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			if !strings.HasPrefix(line, "file\t") {
				return r97Scan{}, fmt.Errorf("候选集表头不对：%q", line)
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 12 {
			return r97Scan{}, fmt.Errorf("候选集行列数 = %d，不足 12（末列必须是 fingerprint）：%q", len(cols), line)
		}
		// ★★★ 主键 = (file, fingerprint, occ)。
		//
		// ★★ 为什么是三段而不是两段（★ 本轮实测踩出来的**真缺陷**）：
		//   fingerprint 是**形态**指纹（kind|usage|volume|consumer，不含行号），
		//   它**抗行号漂移**，但它**在同一个文件内不唯一** ——
		//   实测 352 行只有 281 个唯一指纹，41 个 (file,fingerprint) 组含 >1 行，
		//   另有 8 组连源码文本都完全相同 ⇒ `full_line` 也不能当区分键。
		//   ⇒ 只用 (file,fingerprint) 当键 ⇒ carry-over 时**同形行互相冒用裁决**
		//   ⇒ 真缺陷的 risk 裁决可能被邻行的 benign 裁决**洗掉**（本轮核心不变量被破）。
		//   ⇒ 第三维 occ（同形组内按行号排序的序号）把它变成**真主键**：
		//   唯一 ✓ / 抗行号漂移 ✓（顺序位不随行号变）/ 顺序稳定 ✓。
		key := cols[0] + "\t" + cols[11] + "\t" + cols[2]
		if cands[key] {
			return r97Scan{}, fmt.Errorf("★ 候选集主键重复：%q ⇒ 主键不唯一 ⇒ 键方案已退化", key)
		}
		cands[key] = true
	}
	if err := sc.Err(); err != nil {
		return r97Scan{}, fmt.Errorf("读候选集出错：%v", err)
	}
	if len(cands) == 0 {
		return r97Scan{}, fmt.Errorf("★ 候选集为空 ⇒ 分母为 0 ⇒ 空集上的「零风险」是恒等式，不是发现")
	}

	// ★ 重生成台账 ⇒ 逐键索引（主键与台账**逐字同构**：(file, fingerprint, occ)）。
	carried := map[string][]string{}
	lf, err := os.Open(ledgerCopy)
	if err != nil {
		return r97Scan{}, fmt.Errorf("打不开重生成台账 %s：%v", ledgerCopy, err)
	}
	defer lf.Close()
	lsc := bufio.NewScanner(lf)
	lsc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for lsc.Scan() {
		line := lsc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(strings.TrimRight(line, "\n"), "\t")
		if cols[0] == "file" {
			continue
		}
		if len(cols) < 13 {
			return r97Scan{}, fmt.Errorf("重生成台账行列数 = %d，不足 13：%q", len(cols), line)
		}
		key := cols[0] + "\t" + cols[12] + "\t" + cols[2]
		if _, dup := carried[key]; dup {
			return r97Scan{}, fmt.Errorf("★ 重生成台账主键重复：%q", key)
		}
		carried[key] = cols
	}
	if err := lsc.Err(); err != nil {
		return r97Scan{}, fmt.Errorf("读重生成台账出错：%v", err)
	}
	if len(carried) == 0 {
		return r97Scan{}, fmt.Errorf("★ 重生成台账为空 ⇒ 拒绝出结论")
	}
	return r97Scan{cands: cands, carried: carried}, nil
}

// r97Ledger 读台账 ⇒ key → 列切片。
func r97Ledger(t *testing.T) map[string][]string {
	t.Helper()
	root := r97RepoRoot(t)
	path := filepath.Join(root, "scripts", "ursmcheck", r97LedgerFile)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打不开台账 %s：%v", path, err)
	}
	defer f.Close()
	out := map[string][]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(strings.TrimRight(line, "\n"), "\t")
		if cols[0] == "file" {
			continue
		}
		if len(cols) < 13 {
			t.Fatalf("台账行列数 = %d，不足 13（末列必须是 fingerprint）：%q", len(cols), line)
		}
		// ★ 主键与候选集**逐字同构**：(file, fingerprint, occ)
		key := cols[0] + "\t" + cols[12] + "\t" + cols[2]
		if _, dup := out[key]; dup {
			t.Fatalf("★ 台账主键重复：%q ⇒ 主键不唯一 ⇒ 同形行会互相冒用裁决", key)
		}
		out[key] = cols
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读台账出错：%v", err)
	}
	if len(out) == 0 {
		t.Fatalf("★ 台账为空 ⇒ 分母为 0 ⇒ 拒绝出结论")
	}
	return out
}

// r97Diff 比对候选集与台账 ⇒ (候选里有但台账没有, 台账里有但候选没有)。
func r97Diff(cands map[string]bool, ledger map[string][]string) (missing, extra []string) {
	for k := range cands {
		if _, ok := ledger[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range ledger {
		if !cands[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return
}

// TestPipefailRaceLedgerCoversCandidateSet：候选集与台账键集**完全一致**。
func TestPipefailRaceLedgerCoversCandidateSet(t *testing.T) {
	cands := r97Candidates(t)
	ledger := r97Ledger(t)
	missing, extra := r97Diff(cands, ledger)
	if len(missing) > 0 {
		t.Errorf("★ 台账缺少 %d 个候选点（静态判据给了候选集，但没被逐条裁决）：\n%s",
			len(missing), strings.Join(missing, "\n"))
	}
	if len(extra) > 0 {
		t.Errorf("★ 台账有 %d 个候选集里不存在的行（上游已改，或台账腐化）：\n%s",
			len(extra), strings.Join(extra, "\n"))
	}
	t.Logf("候选物理行 = %d / 台账行 = %d", len(cands), len(ledger))
}

// TestPipefailRaceLedgerOccIsContiguous：同形组内的 occ 必须恰好是 1..N（连续、不重、不缺）。
//
// ★ 这条断言守的是**键方案本身**：
//
//	主键 = (file, fingerprint, occ)，其中 occ = 「同 (file,fingerprint) 组内按行号排序的序号」。
//	⇒ occ 一旦不连续（例如被手工改成两行同号、或某个序号缺失），
//	**同形行就会共用主键** ⇒ carry-over 时互相冒用裁决
//	⇒ 真缺陷的 risk 裁决可能被邻行的 benign 裁决洗掉（本轮核心不变量被破）。
//
// ⇒ 所以「键唯一」还不够 ⇒ 还要「序连续」⇒ 才保证 occ 是**真序号**。
func TestPipefailRaceLedgerOccIsContiguous(t *testing.T) {
	ledger := r97Ledger(t)
	groups := map[string][]int{}
	for k, cols := range ledger {
		var occ int
		if _, err := fmt.Sscanf(cols[2], "%d", &occ); err != nil {
			t.Errorf("★ 台账 %s 的 occ 列 %q 不是整数", k, cols[2])
			continue
		}
		if occ < 1 {
			t.Errorf("★ 台账 %s 的 occ = %d（应从 1 起）", k, occ)
			continue
		}
		gk := cols[0] + "\t" + cols[12]
		groups[gk] = append(groups[gk], occ)
	}
	bad := 0
	for gk, occs := range groups {
		sort.Ints(occs)
		for i, o := range occs {
			if o != i+1 {
				bad++
				if bad <= 10 {
					t.Errorf("★ 组 %s 的 occ 序列 = %v（应为 1..%d 连续）", gk, occs, len(occs))
				}
			}
		}
	}
	if bad > 10 {
		t.Errorf("★ 共 %d 处 occ 不连续", bad)
	}
	t.Logf("同形组 %d 个，occ 全部连续 ✔", len(groups))
}

// TestPipefailRaceLedgerHasNoPending：不允许残留 pending。
func TestPipefailRaceLedgerHasNoPending(t *testing.T) {
	ledger := r97Ledger(t)
	var pend []string
	for k, cols := range ledger {
		v := cols[8]
		if v == r97PendingVerdict {
			pend = append(pend, k)
		}
		if !r97ValidVerdicts[v] {
			t.Errorf("台账 %s 的裁决值 %q 不在允许集合 %v", k, v, r97ValidVerdicts)
		}
	}
	sort.Strings(pend)
	if len(pend) > 0 {
		t.Errorf("★ 台账仍有 %d 行 pending（静态判据只能给候选集，终值必须逐条人工/实测做出）：\n%s",
			len(pend), strings.Join(pend, "\n"))
	}
}

// TestPipefailRaceLedgerRiskRowsHaveEvidence：每一行终值裁决都**必须带证据**。
// ★★ **本轮的关键教训落成门**：本轮曾出现过「86 行裁决的 evidence 列是空的」
//
//	（根因是量具的写端量级正则**末位分支为空 ⇒ 匹配一切 ⇒ 判据恒真**，
//	而这种缺陷从输出格式上完全看不出来）⇒⇒ **量具输出「看起来正常」不是它没坏的证据**
//	⇒⇒ 所以这里把判据放宽到【**所有 judgment 行的证据都必须非空**】，
//	而不只是 risk/conditional 行 ⇒⇒ 空证据会当场暴露。
//
// ★★★★ **R98 轮再收紧一格**：原实现按 `usage ∈ {bare, value}` **整段跳过**
//
//	（理由：这两类「退出码不参与语义判断 ⇒ 体量与证据都无关」）
//	⇒ ⇒ 实测该跳过让本断言**只覆盖 116/354 行**，被跳过的 238 行里有
//	⇒ ⇒ **34 行 evidence 为空**（R97 之前的旧行，carry-over 原样带过来的）
//	⇒ ⇒ **「空证据 = 不许」这条纪律在三分之二的行上从未生效**。
//	⇒ ⇒ 现在改成：**全表 354 行都要有非空证据**（体量无关的判据也必须写出依据）。
func TestPipefailRaceLedgerRiskRowsHaveEvidence(t *testing.T) {
	ledger := r97Ledger(t)
	for k, cols := range ledger {
		v, reason, evid, usage := cols[8], cols[9], cols[10], cols[5]
		if v == "pending" {
			continue // 另一条断言专门查它
		}
		if strings.TrimSpace(evid) == "" {
			t.Errorf("★ %s 的裁决是 %s（usage=%s），但**证据列为空** ⇒ "+
				"不许空口断言（★ 历史上就有过一整批空证据的行，根因是量具判据恒真）", k, v, usage)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("★ %s 的裁决是 %s，但没有理由", k, v)
		}
	}
}

// TestPipefailRaceLedgerCarryOverPreservesVerdicts：**carry-over 不得改动人工裁决**。
//
// ★★ R98 轮新增（★ 补的是 R97 轮的门**根本没覆盖**的那条路径）：
//
//	R97 的门调用量具时**只带 `--out`** ⇒ `build_ledger` / carry-over 六步 /
//	口径漂移作废 / 裁决串味自检**整条路径没人跑** ⇒ ⇒ 门守不住 R97 自己的核心不变量。
//
// ⇒ ⇒ 现在门**一次扫描、两个产物**：候选集 + **台账副本的重生成结果**
//
//	⇒ ⇒ 本断言逐键比对「已提交台账」与「重生成台账」的 verdict / reason / evidence。
//
// ★ 职责分离（★ 别和「键集一致」那条混）：
//
//	· 键集漂移（上游改了文件）⇒ 由 TestPipefailRaceLedgerCoversCandidateSet 报
//	· **裁决被 carry-over 改坏**（同一批键、内容却变了）⇒ 由本断言报
//	⇒ 两条断言各管一件事 ⇒ 上游推进时不会「一起红」而失去指向性。
func TestPipefailRaceLedgerCarryOverPreservesVerdicts(t *testing.T) {
	sc := r97ScanRepo(t)
	ledger := r97Ledger(t)
	var diffs []string
	checked := 0
	for k, cols := range ledger {
		carried, ok := sc.carried[k]
		if !ok {
			continue // 键集漂移由另一条断言负责
		}
		checked++
		for _, ci := range []int{8, 9, 10} { // verdict / reason / evidence
			if cols[ci] != carried[ci] {
				diffs = append(diffs, fmt.Sprintf("%s 的第 %d 列：已提交=%q / 重生成=%q",
					k, ci, cols[ci], carried[ci]))
			}
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		t.Errorf("★ carry-over 改动了人工裁决（%d 处 / 比对 %d 个共有键）⇒ "+
			"台账不再是「人工裁决的 SSOT」：\n%s", len(diffs), checked, strings.Join(diffs, "\n"))
	}
	t.Logf("carry-over 幂等 ✔ 比对 %d 个共有键，verdict/reason/evidence 全部一致", checked)
}

// TestPipefailRaceLedgerGateHasTeeth：删掉台账一行 ⇒ 比对**必须**报出缺行。
// ★ 这是「门绿 ≠ 门有牙」的自检：证明判据 ① 真的在检查，而不是恒真。
func TestPipefailRaceLedgerGateHasTeeth(t *testing.T) {
	cands := r97Candidates(t)
	ledger := r97Ledger(t)
	// 挑一行删掉（只动内存副本，不碰真台账）
	var victim string
	for k := range ledger {
		victim = k
		break
	}
	if victim == "" {
		t.Fatal("台账为空 ⇒ 无法验证门有牙")
	}
	cut := map[string][]string{}
	for k, v := range ledger {
		if k == victim {
			continue
		}
		cut[k] = v
	}
	missing, _ := r97Diff(cands, cut)
	found := false
	for _, m := range missing {
		if m == victim {
			found = true
		}
	}
	if !found {
		t.Fatalf("★ 门没有牙：删掉台账行 %q 之后，比对**没有**报出该行缺失 ⇒ 判据 ① 恒真", victim)
	}
	t.Logf("门有牙 ✔ 删掉 %q 后报出缺行", victim)
}

// TestPipefailRaceLedgerToolAndGateAgreeOnScope：量具与门对「裁决值集合」的理解必须一致。
// ★ 具体检查：量具的 `VALID_VERDICTS = { … }` 块与门的 r97ValidVerdicts **逐字一致**。
//
//	（两处各写一份是刻意的 —— 一处改了另一处没改 ⇒ 本门必须发现。）
//
// ★ 只解析那个**块**：扫全文的引号串会把中文说明误判成裁决值（实测踩过）。
func TestPipefailRaceLedgerToolAndGateAgreeOnScope(t *testing.T) {
	root := r97RepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, r97ToolRel))
	if err != nil {
		t.Fatalf("读量具失败：%v", err)
	}
	src := string(raw)
	start := strings.Index(src, "VALID_VERDICTS")
	if start < 0 {
		t.Fatal("量具里找不到 VALID_VERDICTS 定义 ⇒ 量具形态变了 ⇒ 门必须复核")
	}
	rest := src[start:]
	lb := strings.Index(rest, "{")
	rb := strings.Index(rest, "}")
	if lb < 0 || rb < 0 || rb < lb {
		t.Fatal("量具里 VALID_VERDICTS 的定义块解析失败 ⇒ 门必须复核")
	}
	block := rest[lb+1 : rb]
	toolV := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(block, -1) {
		toolV[m[1]] = true
	}
	if len(toolV) == 0 {
		t.Fatal("量具的 VALID_VERDICTS 块里解析出 0 个裁决值 ⇒ 门必须复核")
	}
	for v := range toolV {
		if v == r97PendingVerdict {
			// ★ 量具把 pending 列为「初稿态」⇒ 门也必须知道它是 pending 且**禁止**
			continue
		}
		if !r97ValidVerdicts[v] {
			t.Errorf("★ 裁决值 %q 只在量具里，门里没有 ⇒ 两处口径漂移", v)
		}
	}
	for v := range r97ValidVerdicts {
		if !toolV[v] {
			t.Errorf("★ 裁决值 %q 只在门里，量具的 VALID_VERDICTS 里没有 ⇒ 两处口径漂移", v)
		}
	}
	if !toolV[r97PendingVerdict] {
		t.Errorf("★ 量具的 VALID_VERDICTS 里没有 %q ⇒ 门对它「禁止」的约定在量具侧不存在 ⇒ 两处口径漂移",
			r97PendingVerdict)
	}
	t.Logf("量具裁决值集合 = %d 项（终值 %d + 初稿态 %q），与门逐字一致 ✔",
		len(toolV), len(r97ValidVerdicts), r97PendingVerdict)
}
