package ursm_test

// R99 轮 · 段十（§19.20.66）的**反回归门**：`scripts/hotzone-request-mirror-reconcile.sh`
// 的「JSON 起始」判定**不得**让【管道的退出码】参与语义判断。
//
// ── 缺陷是什么（实测，PG 无关，纯 shell）────────────────────────────────────
//
//	原形态（`set -uo pipefail`）：
//	    if ! gunzip -c "$f" 2>/dev/null | head -c 1 | grep -q '[{[]'; then
//	      fail "镜像内容非 JSON 起始: $f"
//	    fi
//
//	★ 机制：`head -c 1` 读满 1 字节就退出 ⇒ 写端 `gunzip` 要写的数据**超过管道缓冲
//	  （65,536 B）**时会被 **SIGPIPE** 杀（退出码 141）⇒ `pipefail` 取**最右边的非零**
//	  = 141 ⇒ `! 141` **恒真** ⇒ 【有效 JSON 被误判为失败】。
//	★ 且同一管道里 `grep` 未命中（1）时，1 会盖过 141 ⇒ 【无效 JSON 反被漏判】。
//	★ ⇒ 两个方向都错 ⇒ 这个检查在**大文件**上完全不可信。
//	★ 实测（20 次/组）：解压后 500 KB 的**有效** JSON ⇒ pipefail 开时 FAIL **20/20**、
//	  关时 PASS 20/20；109 B 的小文件**两态都 PASS** ⇒ ⇒ 缺陷**与文件大小相关**
//	  （只有写端超缓冲才触发）。
//	★ 下游后果：`fail()` 会让 `FAILS > 0` ⇒ 脚本 `exit 1` ⇒ **一个大镜像文件就让整轮
//	  对账以「镜像内容非 JSON 起始」这个假理由失败**。
//
// ── 本门的三条判据 ───────────────────────────────────────────────────────────
//
//	① 形态：受测脚本里**不得**出现「`head -c` 管道 + `grep -q`」这个【管道退出码参与
//	   语义判断】的形态。（判据的收窄点见下）
//	② 行为：门自己**跑一遍两个形态**（坏形态 / 修好的形态），对**大文件**断言：
//	   坏形态判 FAIL（缺陷真的存在）、好形态判 PASS（修法真的有效）⇒ ⇒ **门自带对照
//	   实验** ⇒ 不会退化成恒绿门。
//	③ 量具自检：合成「含坏形态的源」必须被报出；合成「含好形态的源」必须不被报出。
//
// ★★ 为什么判据 ① 只守【这一个形态】而不是「所有管道」：
//   管道本身没有错 ⇒ 「有管道就判不合格」会把大量正确写法误杀（★ 假阳性方向最危险）。
//   ⇒ 本门只守**这个具体的坏形态**（`head -c … | grep -q` 出现在一个
//   **用管道退出码做判断**的 `if` 里）⇒ 精确、可复现、无假阳性。
//
// ★ 缺陷来源：并行会话的「SIGPIPE 竞态面」全仓普查（六轴判别）发现并通报；
//   本门是该缺陷的**反回归**守卫 ⇒ 与那条普查量具互补（它找新实例，本门守本处）。

import (
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	sc1966Target  = "scripts/hotzone-request-mirror-reconcile.sh"
	sc1966BadForm = `if ! gunzip -c "$f" 2>/dev/null | head -c 1 | grep -q '[{[]'; then`
)

// sc1966ReBadForm 抓「管道退出码参与语义判断」的那个具体形态。
//
// ★ 收窄到两个必要成分同时在场：① `head -c`（写端会早退 ⇒ 会被 SIGPIPE）
//
//	② `grep -q`（消费者的退出码是判定依据）⇒ 只出现其中一个不算缺陷。
//
// ★★ **匹配前必须先剥注释**（★ 本门第一版就栽在这里）：
//
//	受测脚本里那段「讲清楚旧形态长什么样」的注释里**写着**旧形态的字面量
//	⇒ 第一版判据匹配到了**注释** ⇒ **假阳性**（★ 假阳性方向最危险）。
//	⇒ 与主门同一套剥法（scqStripComments）：只清空「整行都是注释」的行、保留行数。
//	⇒ ★ 剥注释对**两个方向**都必要：判「有没有坏形态」时防假阳性；
//	  判「有没有好形态」时防**假阴性**（注释里写了修法就通过 ⇒ 等于没修）。
var sc1966ReBadForm = regexp.MustCompile(`!\s*[^\n]*\|\s*head\s+-c[^\n]*\|\s*grep\s+-q`)

// sc1966ReCaptured 抓「判定基于【取到的值】」的形态（修好的那个）。
var sc1966ReCaptured = regexp.MustCompile(`=\s*\$\([^)]*head\s+-c`)

// sc1966StripComments 剥掉「整行都是注释」的行。
func sc1966StripComments(src string) string {
	return strings.Join(scqStripComments(src, "shell"), "\n")
}

// sc1966ContainsBadForm 判一个源里有没有坏形态（**已剥注释**）。
func sc1966ContainsBadForm(src string) bool {
	return sc1966ReBadForm.MatchString(sc1966StripComments(src))
}

// ── 判据 ①：受测脚本里没有坏形态 ────────────────────────────────────────────

func TestSection1966HotzoneJSONJudgementNotByPipelineExitCode(t *testing.T) {
	path := filepath.Join("../..", sc1966Target)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", sc1966Target, err)
	}
	src := string(data)
	if sc1966ContainsBadForm(src) {
		t.Errorf("★ %s 里仍有「管道退出码参与语义判断」的坏形态\n"+
			"  ⇒ 机制：head -c 早退 ⇒ 写端 gunzip 超 64 KiB 管道缓冲时被 SIGPIPE 杀（141）\n"+
			"  ⇒   ⇒ pipefail 取最右非零 = 141 ⇒ `! 141` 恒真 ⇒ 有效 JSON 被误判为失败\n"+
			"  ⇒ 判定必须只取【取到的首字符】，不让管道退出码参与", sc1966Target)
	}
	if !sc1966ReCaptured.MatchString(sc1966StripComments(src)) {
		t.Errorf("★ %s 里找不到「先把首字符取到变量里再判定」的形态\n"+
			"  ⇒ 修法形态：first_char=$(gunzip -c \"$f\" 2>/dev/null | head -c 1 || true)\n"+
			"  ⇒   ⇒ 判据只取【值】，管道退出码被 `|| true` 丢弃", sc1966Target)
	}
}

// ── 判据 ②：行为 —— 门自己跑两个形态，对【大文件】断言 ──────────────────────

// sc1966WriteBigJSONGz 写一个「解压后超过管道缓冲」的 gzip 文件。
//
// ★ 为什么要【大】：小文件时写端能在管道缓冲里写完 ⇒ 收不到 SIGPIPE ⇒ 缺陷不触发
//
//	⇒ ⇒ 门若用小样本就会变成恒绿门。★ 500 KB 解压后 ⇒ 确定超 65,536 B。
func sc1966WriteBigJSONGz(t *testing.T, dir, name string, valid bool) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	lead := "{"
	if !valid {
		lead = "X"
	}
	payload := lead + strings.Repeat("v", 500000) + "}"
	if _, err := zw.Write([]byte(payload)); err != nil {
		t.Fatalf("写 gzip 失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("落盘 %s 失败: %v", p, err)
	}
	return p
}

func TestSection1966HotzoneJSONJudgementBehaviourOnBigFile(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("本机没有 bash ⇒ 行为判据跳过（形态判据仍在跑）: %v", err)
	}
	dir := t.TempDir()
	good := sc1966WriteBigJSONGz(t, dir, "good_big.json.gz", true)
	bad := sc1966WriteBigJSONGz(t, dir, "bad_big.json.gz", false)

	// ★ 坏形态（现行缺陷形态）：管道退出码参与判断
	badForm := `
first_char=$(gunzip -c "$f" 2>/dev/null | head -c 1)
if ! gunzip -c "$f" 2>/dev/null | head -c 1 | grep -q '[{[]'; then
  echo VERDICT_FAIL
fi`
	// ★ 好形态（修法）：判定只取【取到的首字符】
	goodForm := `
first_char=$(gunzip -c "$f" 2>/dev/null | head -c 1 || true)
if [[ "$first_char" != "{" && "$first_char" != "[" ]]; then
  echo VERDICT_FAIL
fi`

	// ★★ 对照实验：坏形态在【大文件 + 有效 JSON】上必须判 FAIL（缺陷真的存在）
	//   ⇒ 若这里判 PASS ⇒ 说明环境里 gunzip 的行为不同 ⇒ 门的前提被打破 ⇒ 必须报出来。
	run := func(form, file string) bool {
		cmd := exec.Command("bash", "-c", "set -uo pipefail\nf=\"$1\"\n"+form+"\n", "bash", file)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("跑形态失败: %v\n输出: %s", err, out)
		}
		return strings.Contains(string(out), "VERDICT_FAIL")
	}

	if !run(badForm, good) {
		t.Errorf("★ 对照实验前提被打破：坏形态在【大文件 + 有效 JSON】上竟然判 PASS\n"+
			"  ⇒ ⇒ 本门赖以成立的前提（gunzip 超缓冲时被 SIGPIPE 杀）在本机不成立\n"+
			"  ⇒ ⇒ 不能拿它当「缺陷已复现」的证据 ⇒ 请核环境（样本 %s）", good)
	}
	// ★ 好形态在【大文件 + 有效 JSON】上必须判 PASS（修法真的有效）
	if run(goodForm, good) {
		t.Errorf("★ 好形态在【大文件 + 有效 JSON】上判 FAIL ⇒ 修法无效")
	}
	// ★ 好形态在【大文件 + 无效 JSON】上必须判 FAIL（**反向**那条也修好了）
	if !run(goodForm, bad) {
		t.Errorf("★ 好形态在【大文件 + 无效 JSON】上判 PASS ⇒ 漏判没修好")
	}
}

// ── 判据 ③：量具自检（证明判据 ① 有牙）─────────────────────────────────────

func TestSection1966BadFormDetectorHasTeeth(t *testing.T) {
	// ① 含坏形态 ⇒ 必须被抓
	if !sc1966ContainsBadForm(`if ! gunzip -c "$f" 2>/dev/null | head -c 1 | grep -q '[{[]'; then
      fail "x"
    fi`) {
		t.Errorf("★ 量具自检失败：含坏形态的源没被抓到 ⇒ 判据 ① 没有牙")
	}
	// ② 只有 head -c、没有 grep -q ⇒ **不是**这个坏形态（别误杀）
	if sc1966ContainsBadForm(`first_char=$(gunzip -c "$f" 2>/dev/null | head -c 1 || true)`) {
		t.Errorf("★ 量具自检失败：「取首字符」的好形态被误判成坏形态 ⇒ 判据 ① 会假阳性")
	}
	// ③ 只有 grep -q、没有 head -c ⇒ **不是**这个坏形态（别误杀）
	if sc1966ContainsBadForm(`if ! grep -q 'x' "$f"; then fail "y"; fi`) {
		t.Errorf("★ 量具自检失败：「普通 grep -q」被误判成坏形态 ⇒ 判据 ① 会假阳性")
	}
	// ④ 好形态（含捕获赋值）⇒ 必须被判为「有捕获」
	if !sc1966ReCaptured.MatchString(`first_char=$(gunzip -c "$f" 2>/dev/null | head -c 1 || true)`) {
		t.Errorf("★ 量具自检失败：好形态没被「捕获赋值」正则认出来")
	}
	// ★ 判据 ① 用的那条具体形态串，必须与受测脚本里**修好后**的形态不匹配
	//   （否则「形态断言」与「自检」会互相打架）
	if sc1966ContainsBadForm(sc1966BadForm) != true {
		t.Errorf("★ 量具自检失败：常量里的坏形态自己都认不出来")
	}
}
