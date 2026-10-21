package ursm_test

// R99 轮 · 段十一（§19.20.67）的**反回归门**：两个脚本里的「变量写端 → 管道 →
// **早退消费者**」必须**已消除**（修法统一为 herestring）。
//
// ── 缺陷是什么（实测，纯 shell）──────────────────────────────────────────────
//
//	原形态：`if echo "$out" | grep -q "^FAIL" || …; then actual=FAIL; fi`
//	  · 消费者 `grep -q` 是**早退**的（命中即退）
//	  · 写端 `echo "$out"` 的数据超过管道缓冲（Linux 65,536 B / macOS 16,384 B）时
//	    ⇒ 写端被 SIGPIPE 杀（141）
//	  · `set -o pipefail` ⇒ 管道码取**最右非零** = 141 ⇒ `if` 的条件变成非零
//	  · ⇒ ⇒ **判定被翻转**；且翻转方向**取决于命中在输出的第几行** ⇒ ⇒ **结论不可复现**。
//
//	★ 实测（12 次/组，85 KB 输出，`set -uo pipefail`）：
//	  样本「明明通过」⇒ 修前 **UNKNOWN 12/12**（假红：通过的腿被计成失败）
//	  样本「确实失败」⇒ 修前 **FAIL 1/12、UNKNOWN 11/12**（★ 同一输入两种结果 ⇒ 不可复现）
//	  修后（herestring 形态）⇒ 两组都 **12/12 正确且确定**。
//
//	★ 修法：`grep -q "^FAIL" <<<"$out"` —— bash 把 herestring 写到**临时文件** ⇒
//	  文件没有 SIGPIPE ⇒ 退出码**只反映 grep 自己的判定**（0/1）⇒ 语义等价。
//
//	★★ 本门是**定点**的（只看这两个文件的这几处），**不做全仓普查** ——
//	  全仓普查是并行会话那条线（`scripts/audit/measure-pipefail-race-surface.py` +
//	  `scripts/ursmcheck/pipefail_race_ledger.tsv`）的资产 ⇒ 本门不重复、也不抢口径。
//	  ★ 而定点门的**盲区**只有一处：这两处以外的同族形态（★ 本轮实测全仓还有 183 处
//	  `echo "$var" | grep` 形态，其中被对方量具漏检的两类已在通报里给出 ⇒ 属主项）。
//
//	⚠ **本门的已知边界（诚实登记，不藏）**：
//	  ① 判据 ① 只认「写端是 echo/printf + 变量」这一族（仓内 R53 轮确立的修法形态）；
//	     `cat "$f" | grep -q` 这类「写端是命令」**不在本门射程**。
//	  ② 判据 ① 按**行**匹配 ⇒ 跨行的管道形态（`echo "$v" |` 换行接 `grep -q`）不在射程。
//	  ③ 只认**早退消费者**（`grep -q` / `grep -m` / `head`）；`sort` / `cut` / `tr` / `tail`
//	     这类「读满整个输入」的消费者**不构成缺陷** ⇒ 本门**不会**误杀它们。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sc1967Targets 是本门盯的两个文件。
var sc1967Targets = []string{
	"scripts/audit/verify-session-family-p0-realdb.sh",
	"scripts/audit/run-integration-gate.sh",
}

// sc1967ReVarPipeEarlyExit 抓「**变量写端 → 单管道 → 早退消费者**」这个坏形态。
//
// ★ 三个条件缺一不可（★ 假阳性方向最危险 ⇒ 逐个收紧）：
//
//	① 写端是 `echo`/`printf` 且**带着变量引用**（否则 `docker ps | grep` 被误杀）
//	② 中间是**单个** `|`（`||` 是逻辑或，不是管道）
//	③ 消费者是**早退**的（`grep -q` / `grep -m` / `head`）—— `sort`/`cut`/`tr`/`tail`
//	   读满整个输入 ⇒ 不构成缺陷 ⇒ 不该被本门判红
var sc1967ReVarPipeEarlyExit = regexp.MustCompile(
	`(?:\becho\b|\bprintf\b)[^|\n;&]*\$[A-Za-z_{@*0-9][^|\n;&]*\|\s*(?:grep\b[^|\n;&]*-[a-zA-Z]*[qm]|head\b)`)

// sc1967StripComments 剥掉「整行都是注释」的行。
//
// ★ 必须剥：修法说明里**引用了旧形态** ⇒ 不剥就会匹配到注释（★ 本轮第三次栽在这条上：
//
//	§19.20.63 切分器、§19.20.66 的门、这里 —— 第三次 ⇒ 它是**高频陷阱**）。
func sc1967StripComments(src string) string {
	return strings.Join(scqStripComments(src, "shell"), "\n")
}

// sc1967Site 是「修法必须在场」的一条断言：某个具体判定点必须用 herestring。
type sc1967Site struct {
	file string
	re   *regexp.Regexp
	why  string
}

// sc1967RequiredSites 列出本轮修掉的**全部**判定点（★ 一处都不能少）。
//
// ★ 为什么要有「修法在场」这条独立判据：判据 ① 只禁旧形态 ⇒ **「把检查删掉」也能让它变绿**
//
//	⇒ 那是「让门不再报错」而不是「修缺陷」⇒ ⇒ 必须同时钉住「修法在位」。
var sc1967RequiredSites = []sc1967Site{
	// ── verify-session-family-p0-realdb.sh：check() 的 4 处 + baseline 1 处 ──
	{"scripts/audit/verify-session-family-p0-realdb.sh",
		regexp.MustCompile(`grep\s+-q\s+"\^FAIL"\s+<<<\s*"\$out"`),
		"check()：go test 输出里的 FAIL 判定"},
	{"scripts/audit/verify-session-family-p0-realdb.sh",
		regexp.MustCompile(`grep\s+-q\s+"\^--- FAIL"\s+<<<\s*"\$out"`),
		"check()：go test 输出里的 `--- FAIL` 判定"},
	{"scripts/audit/verify-session-family-p0-realdb.sh",
		regexp.MustCompile(`grep\s+-q\s+"\^ok"\s+<<<\s*"\$out"`),
		"check()：go test 输出里的 ok 判定"},
	{"scripts/audit/verify-session-family-p0-realdb.sh",
		regexp.MustCompile(`grep\s+-q\s+"\^PASS"\s+<<<\s*"\$out"`),
		"check()：go test 输出里的 PASS 判定"},
	{"scripts/audit/verify-session-family-p0-realdb.sh",
		regexp.MustCompile(`grep\s+-q\s+"\^FAIL"\s+<<<\s*"\$BASE_OUT"`),
		"baseline 全绿判定（8 条腿的合并输出）"},

	// ── run-integration-gate.sh：5 处（正向 2 + 反向 stale 2 + baseline 预算 1）──
	{"scripts/audit/run-integration-gate.sh",
		regexp.MustCompile(`grep\s+-qxF\s+"\$f"\s+<<<\s*"\$gap_known_txt"`),
		"「这次失败是不是已登记的已知缺口」（正向）"},
	{"scripts/audit/run-integration-gate.sh",
		regexp.MustCompile(`grep\s+-qxF\s+"\$known"\s+<<<\s*"\$gap_actual_txt"`),
		"「已登记的已知缺口本轮是不是还在」（反向 / stale）"},
	{"scripts/audit/run-integration-gate.sh",
		regexp.MustCompile(`grep\s+-qxF\s+"\$f"\s+<<<\s*"\$br_listed_txt"`),
		"「重跑失败的文件在不在预算清单里」（命中 ⇒ 进 die 条件 ⇒ 假红失败）"},
	{"scripts/audit/run-integration-gate.sh",
		regexp.MustCompile(`grep\s+-qxF\s+"\$f"\s+<<<\s*"\$rr_known_txt"`),
		"「重跑失败是不是已登记的已知缺口」（正向）"},
	{"scripts/audit/run-integration-gate.sh",
		regexp.MustCompile(`grep\s+-qxF\s+"\$known"\s+<<<\s*"\$rr_actual_txt"`),
		"「已登记的重跑缺口本轮是不是还在」（反向 / stale）"},
}

// ── 判据 ①：两个目标文件里不再有「变量写端 → 管道 → 早退消费者」的形态 ──────

func TestSection1967NoEarlyExitPipeJudgement(t *testing.T) {
	for _, rel := range sc1967Targets {
		data, err := os.ReadFile(filepath.Join("../..", rel))
		if err != nil {
			t.Fatalf("读 %s 失败: %v", rel, err)
		}
		src := sc1967StripComments(string(data))
		for _, ln := range strings.Split(src, "\n") {
			if !sc1967ReVarPipeEarlyExit.MatchString(ln) {
				continue
			}
			t.Errorf("★ %s 里仍有「变量写端 → 管道 → 早退消费者」的形态\n"+
				"  ⇒ 行：%s\n"+
				"  ⇒ 机制：grep -q 命中即退 ⇒ 写端超管道缓冲时被 SIGPIPE 杀（141）\n"+
				"  ⇒   ⇒ pipefail 取最右非零 = 141 ⇒ 判定被翻转且【不可复现】\n"+
				"  ⇒ 修法：herestring（grep -q … <<<\"$var\"）⇒ bash 写临时文件 ⇒ 无 SIGPIPE",
				rel, strings.TrimSpace(ln))
		}
	}
}

// ── 判据 ②：修法必须在场（★ 防止「把检查删掉」也能让门变绿） ────────────────

func TestSection1967HerestringFixIsPresent(t *testing.T) {
	cache := map[string]string{}
	for _, site := range sc1967RequiredSites {
		src, ok := cache[site.file]
		if !ok {
			data, err := os.ReadFile(filepath.Join("../..", site.file))
			if err != nil {
				t.Fatalf("读 %s 失败: %v", site.file, err)
			}
			src = sc1967StripComments(string(data))
			cache[site.file] = src
		}
		if !site.re.MatchString(src) {
			t.Errorf("★ %s 里找不到「%s」的 herestring 修法\n"+
				"  ⇒ 期望匹配：%s\n"+
				"  ⇒ ⇒ 要么判定被回退成管道形态，要么**这条检查被删了**\n"+
				"  ⇒ ⇒ 后者更危险：门会「不再报错」，但问题没修",
				site.file, site.why, site.re.String())
		}
	}
}

// ── 判据 ③：行为 —— 门自己跑「新形态」，断言判定【正确且确定】 ────────────────

// sc1967MakeBigOutput 造一个「超过管道缓冲」的输出（首行是判定锚点）。
func sc1967MakeBigOutput(head string) string {
	var b strings.Builder
	b.WriteString(head)
	for i := 0; i < 1200; i++ {
		b.WriteString(strings.Repeat("x", 70) + "\n")
	}
	return b.String()
}

// sc1967PipeBufferFloor 是管道缓冲的**上界**（Linux 65,536 B；macOS 更小）。
const sc1967PipeBufferFloor = 65536

// sc1967Judge 跑一次完整判定。herestring=true ⇒ 修后形态；false ⇒ 修前形态。
func sc1967Judge(herestring bool, out string) string {
	if sc1967RunGrep("^FAIL", out, herestring) || sc1967RunGrep("^--- FAIL", out, herestring) {
		return "FAIL"
	}
	if sc1967RunGrep("^ok", out, herestring) || sc1967RunGrep("^PASS", out, herestring) {
		return "PASS"
	}
	return "UNKNOWN"
}

// sc1967RunGrep 在 bash 里跑一次「grep 是否命中」。
//
// ★ 大内容走 **argv（$1）** 而不是环境变量：环境变量与 argv **共用同一个 ARG_MAX**
//
//	（macOS 1,048,576 B）⇒ 走 env 没有任何额外好处 ⇒ 用 argv 更直白、也不必拼 env。
func sc1967RunGrep(pattern, out string, herestring bool) bool {
	var body string
	if herestring {
		body = `grep -q ` + sc1967ShellQuote(pattern) + ` <<<"$OUT"`
	} else {
		body = `echo "$OUT" | grep -q ` + sc1967ShellQuote(pattern)
	}
	script := "set -uo pipefail\nOUT=\"$1\"\nif " + body + "; then exit 0; else exit 1; fi\n"
	cmd := exec.Command("bash", "-c", script, "bash", out)
	return cmd.Run() == nil
}

func sc1967ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func TestSection1967HerestringJudgementIsCorrectAndDeterministic(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("本机没有 bash ⇒ 行为判据跳过（形态判据仍在跑）: %v", err)
	}
	okOut := sc1967MakeBigOutput("ok  \tgithub.com/x/pkg\t1.234s\nPASS\n")
	failOut := sc1967MakeBigOutput("--- FAIL: TestFoo (0.00s)\nFAIL\n")

	// ★★ 量具自检：样本必须**真的**超过管道缓冲 ⇒ 否则「旧形态判错」这个前提不成立
	for _, s := range []struct {
		name string
		out  string
	}{
		{"大输出·通过", okOut},
		{"大输出·失败", failOut},
	} {
		if len(s.out) <= sc1967PipeBufferFloor {
			t.Fatalf("★ 量具自检失败：样本 %s 只有 %d B，**不超过**管道缓冲上界 %d B\n"+
				"  ⇒ ⇒ 旧形态不会触发 SIGPIPE ⇒ 本门赖以成立的前提不成立 ⇒ 请调大样本",
				s.name, len(s.out), sc1967PipeBufferFloor)
		}
	}

	for _, tc := range []struct {
		name string
		out  string
		want string
	}{
		{"大输出·通过", okOut, "PASS"},
		{"大输出·失败", failOut, "FAIL"},
	} {
		// ★★ 新形态：12 次都必须给出同一个、且正确的判定
		seen := map[string]int{}
		for i := 0; i < 12; i++ {
			seen[sc1967Judge(true, tc.out)]++
		}
		if len(seen) != 1 {
			t.Errorf("★ 新形态在大输出上判定【不确定】（12 次得到 %d 种结果：%v）⇒ 结论不可复现", len(seen), seen)
		}
		if seen[tc.want] != 12 {
			t.Errorf("★ 新形态在大输出上判 %s，期望 %s（12 次读数 %v）", tc.want, tc.want, seen)
		}

		// ★★ 对照：旧形态在【同一个样本】上**不能** 12/12 判对
		//    ⇒ 这一条同时是「门守的东西是真的」的证据 + 前提守卫
		seenOld := map[string]int{}
		for i := 0; i < 12; i++ {
			seenOld[sc1967Judge(false, tc.out)]++
		}
		if seenOld[tc.want] == 12 {
			t.Errorf("★ 对照实验前提被打破：旧形态在样本「%s」上 12 次都判 %s\n"+
				"  ⇒ ⇒ 本门赖以成立的前提（写端超缓冲时被 SIGPIPE 杀）在本机不成立\n"+
				"  ⇒ ⇒ 不能拿它当「缺陷已复现」的证据 ⇒ 请核环境",
				tc.name, tc.want)
		}
	}
}

// ── 判据 ④：量具自检（证明判据 ① 有牙、且不误杀） ──────────────────────────

func TestSection1967DetectorHasTeeth(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantHit bool
		why     string
	}{
		{
			name:    "坏形态·echo 变量喂早退消费者",
			src:     `  if echo "$out" | grep -q "^FAIL"; then` + "\n" + `    actual=FAIL` + "\n" + `  fi`,
			wantHit: true,
			why:     "这正是 check() 修前的形态 ⇒ 必须被抓",
		},
		{
			name:    "坏形态·printf 数组喂早退消费者",
			src:     `      if printf '%s\n' "${gap_known[@]}" | grep -qxF "$f"; then`,
			wantHit: true,
			why:     "这正是 run-integration-gate.sh 修前的形态（数组 + printf）⇒ 必须被抓",
		},
		{
			name:    "好形态·herestring",
			src:     `  if grep -q "^FAIL" <<<"$out"; then` + "\n" + `    actual=FAIL` + "\n" + `  fi`,
			wantHit: false,
			why:     "herestring 是修法本身 ⇒ 不能被当成坏形态",
		},
		{
			name:    "不该误杀·写端是命令输出",
			src:     `  if docker ps --format '{{.Names}}' | grep -qx "$c"; then` + "\n" + `    ok=1` + "\n" + `  fi`,
			wantHit: false,
			why:     "写端不是变量 ⇒ 不是本门要守的形态",
		},
		{
			name:    "不该误杀·消费者读满整个输入",
			src:     `  done < <(for e in "${arr[@]}"; do echo "${e%% :: *}"; done | sort -u)`,
			wantHit: false,
			why:     "sort 读满整个输入 ⇒ 写端不会被 SIGPIPE 杀 ⇒ 不构成缺陷",
		},
		{
			name:    "不该误杀·普通消费者（tail）",
			src:     `  echo "$BASE_OUT" | tail -40 >&2`,
			wantHit: false,
			why:     "tail 读满整个输入 ⇒ 不构成缺陷",
		},
		{
			name:    "注释里的旧形态",
			src:     `# 旧形态：if echo "$out" | grep -q "^FAIL"; then` + "\n" + `  actual=FAIL` + "\n" + `fi`,
			wantHit: false,
			why:     "判据必须先剥注释 ⇒ 否则修法说明里引用的旧形态会被误判",
		},
	}
	for _, tc := range cases {
		hit := sc1967ReVarPipeEarlyExit.MatchString(sc1967StripComments(tc.src))
		if hit != tc.wantHit {
			if tc.wantHit {
				t.Errorf("★ 量具自检失败：%s 没被抓到 ⇒ 判据 ① 没有牙（%s）", tc.name, tc.why)
			} else {
				t.Errorf("★ 量具自检失败：%s 被误判成坏形态 ⇒ 判据 ① 会假阳性（%s）", tc.name, tc.why)
			}
		}
	}
}
