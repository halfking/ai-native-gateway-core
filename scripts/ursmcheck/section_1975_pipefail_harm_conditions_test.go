package ursm_test

// R99 轮 · 段十四（§19.20.75）的**门**：「危害成立的五个必要条件」被实测钉死
// ＋ **修法形态被钉住**（那 8 处必须是 herestring ⇒ 不许回退成管道形态）。
//
// ── 为什么要有这道门 ────────────────────────────────────────────────────────
//
// ★ 段十三（§19.20.72）钉的是「危害成立的三个必要条件」⇒ 探针 8 条读数（远离边界）。
// ★ 段十四补了两维，并**订正了段十三表格里一处过宽表述**：
//
//   ① ★★ **新增条件④：管道退出码必须【真被消费】** ⇒ 否则 141 只是一条被忽略的退出码
//      ⇒ 无害。★ 消费方式只有四种：`if/elif/while/until` 判据 ｜ `&&/||` 左侧 ｜
//        `x="$(管道)"` 赋值（赋值语句的状态 = 命令替换的状态）｜ ★★ **`echo "$(管道)"`**
//        （命令替换的退出码**被丢弃** ⇒ **不消费** ⇒ 对方的 `usage=judgment` 在这里**是假阳性**）。
//   ② ★★ **新增条件②的另一半：写端【上界】** ⇒ `write_end_form=bulk` 只说明「多行」，
//      **不说明「能超 64 KiB」** ⇒ 抽样核对抓到两个方向的错（假阳性/假阴性各一）。
//   ③ ★★ **订正：消费者是 `head` 时，危害同样要求「写端 > 64 KiB」** ⇒ 段十三表格写的
//      「任意 ⇒ 141」**过宽** ⇒ 实测 60,000 B ⇒ 0。
//   ④ ★★ **边界竞态（⇒ 门不能钉竞态读数）**：`head` 在**紧邻 64 KiB** 处**有竞态**
//      （70,000 B 连跑 8 次 ⇒ 0 出现 3 次、141 出现 5 次）⇒ ★ **只钉远离边界的点**。
//      ★★★★★ **【R85 轮 · §19.25.17 订正】**：★ 本条原文接着写「而 `grep -q` 的同一边界
//      是【确定的】（65,537 B ⇒ 0、70,000 B ⇒ 141，6/6 稳定）」—— ★★ **这一句被实测证伪**：
//      ★★ ① `grep -q` 65,537 B **就是**竞态（实测中等负载 300 次里 43 次翻成 141）；
//      ★★ ② `grep -q` 70,000 B 在**中等负载下 300/300 稳定**，★★ **却在超载宿主上翻了**
//      （120 次里 2 次）⇒ ⇒ ⇒ ★★ **「6/6 稳定」与「300/300 稳定」都只证明【那 N 次的
//      宿主状态下稳定】，不证明【机制上确定】** ⇒ ⇒ ⇒ ★★ **⇒ 判据①改成【判定式】**
//      （见下方判据①）⇒ ★ **文档里写着一个【当时就已被推翻】的判断比没写更糟**。
//
// ── 六条判据 ────────────────────────────────────────────────────────────────
//
// ① ★★★★★ **机制读数按【判定式】钉，不按【单次定值】钉**（★ R85 轮 · §19.25.17 改判据形态）：
//    跑 `scripts/audit/probe-pipefail-harm-conditions.sh`（仓内可重跑）⇒
//    ★ 探针现在**每条读数采样 N 次**（默认 5）⇒ 输出【众数】+【取值集合】⇒
//    ★ 门据此把 9 条读数分成**三类**，逐类用**不同的判据**：
//      · 【稳定-0】/【稳定-141】（6 条）⇒ ★ **取值集合必须恰好是 {期望值}**
//        ⇒ ★ 这是**比原判据更强**的断言（原判据只看一次读数）。
//      · 【竞态】（3 条：grep-q 65,537 / grep-q 70,000 / head-c 60,000）⇒
//        ★ **取值集合必须 ⊆ {0, 141}** ⇒ ★ **不钉定值** ⇒ ★ 门不再有 flaky 面。
//    ⇒ ★ **门真正守的是【区分度】**：无害档（恒 0）与有害档（恒 141）必须可区分。
//
//    ★★★★ 【为什么必须是判定式 —— 实测依据（R85 轮，handoff §19.25.17）】：
//    · 载荷落在【管道缓冲附近】时，**写端的尾部写入**与**消费者的退出**赛跑
//      ⇒ 两种结果**都可能出现** ⇒ ★ 单次读数是【一次观测】，不是【性质】。
//    · ★★ 实测：**在中等负载下 300/300 稳定**的那一格（grep-q 70,000），
//      **在超载宿主上翻了**（120 次里 2 次 ⇒ 0）⇒ ⇒ ⇒ ★★ **「连跑 N 次一致」
//      只证明【那 N 次的宿主状态下稳定】⇒ 不证明【机制上确定】**（LESSONS §265）
//      ⇒ ⇒ ⇒ ★★ **任何「定值」钉法都不可靠 ⇒ 只有【判定式】可靠。**
//    · ⇒ ★★ 而「危害成立」的**真正必要条件**不是「写端超 64 KiB」，而是
//      **「消费者在写端写完之前退出」** ⇒ ★ 「超 64 KiB」只是
//      **「写端必然写不完」的【充分代理】** ⇒ ★★ **64 KiB 不是一个【阈值】，
//      而是一个【概率穿越点】**。
//
//    ★★ 【表头/数据列形状被钉住】（★ R85 轮一并修掉段十四的错位一列）：
//    探针的 TSV 表头与数据行必须**同宽且逐列对齐**（7 列）⇒
//    ★ 否则「按表头取列」会取错列（段十四的 `emit` 只有 6 个 `%s`、表头 7 个词）。
//
// ② ★★★ **量具自检（防恒绿门）**：把探针拷到临时目录、注入一条**错的读数** ⇒
//    门自己的比较逻辑必须能判红 ⇒ ★ 证明判据①不是恒绿。
//    ★★ R85 轮补了**反向自检**：★ 把**竞态类**读数改成另一个值 ⇒ 门**必须仍然绿**
//    ⇒ ★ 证明判据①真的**不再依赖**竞态读数的定值 ⇒ ★（否则「判定式」只是嘴上说说）。
// ③ **修法形态被钉住（防回退）**：段十四修掉的 8 处必须是 herestring 形态，
//    且**旧管道形态不得再出现** ⇒ ★ 用【内容锚】而不是行号 ⇒ 行号漂移不影响。
// ④ **清单进度基线**：候选清单行数必须等于基线（120）⇒ ★ 回落即真脱节（要红）。
// ⑤ ★★★ **清单 ↔ 生成器【逐字一致】（强判据）**：本轮自查挖出来的洞 ——
//    段十四初版清单是【跨一次 merge 未重跑】的过期值（110 vs 实际 120），
//    而当时的判据④**只比行数**、基线又是【从清单抄的】⇒ ★ **门绿、量具瞎了**。
//    ⇒ 本判据把生成器跑一遍、与已装清单**逐字比对** ⇒ ★ 内容过期（哪怕行数不变）也判红。
// ⑥ ★★★ **errexit × pipefail 的交互读数被钉住**：段十四第十一小节实测发现，
//    我段十四写的「候选文件里没有一处 `set -e`」是**错的** ⇒ 当时的检查只匹配裸
//    `set -e` ⇒ 漏掉 `set -euo pipefail` ⇒ ⇒ ★★ **43/43 个候选文件全部启用 errexit**
//    ⇒ ⇒ ★★ **危害因此有【两档】**：条件上下文 ⇒ errexit 豁免（只翻转判定）；
//    赋值 / 简单命令上下文 ⇒ **脚本当场中止**（更硬）⇒ ★ 探针 6 条读数逐条钉住。
//
// ★ 对生产的写入 = 0（只读仓内文件 + 只跑纯 shell 探针 + 跑仓内生成器到临时目录）。

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	sc1975ProbeRel = "scripts/audit/probe-pipefail-harm-conditions.sh"
	// ★★ 第十一小节新增：测 errexit × pipefail 的交互（危害有【两档】）
	sc1975ErrexitProbeRel = "scripts/audit/probe-pipefail-errexit-interaction.sh"
	// ★★ 用例体放在【数据文件】里（既非 .sh、又无 shell shebang）⇒ 两个扫描量具都看不见它
	//   ⇒ ⇒ 本探针对下游的共享扫描面是【零候选】（否则会打红别人的台账门）
	sc1975ErrexitCasesRel = "scripts/audit/probe-pipefail-errexit-cases.txt"
	sc1975ListRel         = "docs/audit/evidence/2026-10-21-pipefail-scope-drop-candidates.tsv"
	sc1975GenRel          = "scripts/audit/measure-pipefail-scope-drop.py"
	// ★ 基线：清单行数（剥注释 + 顶层收紧 + 量具自身排除）
	//   ★★ **订正（段十四定稿时）**：基线原写 110 —— 那是【跨一次 merge 未重跑】的过期值
	//     （对方的 R82 `459d80a3c` 与 R75 `dea4eda4d` 在段十四期间并入本树，
	//      带进 10 处新站点：9 处 verify-nonplain-migration-delivery.sh
	//      ＋ 1 处 ursm-snapshot-slot-refresh.sh）⇒ 实际 120 / 43 文件。
	//   ★ **差值不变**（剥注释 −2、顶层收紧 ±0）⇒ 变的只是绝对值 ⇒ 口径没错、树变了。
	//   ★★ **基线不得再【从清单抄】** ⇒ 那等于自证 ⇒ 判据⑤才是真牙。
	sc1975ListRowsBaseline = 84
	// ★★ 段十六（R87 轮 · 合并 `origin/main` 后）：82 ⇒ 83（+1 行）
	//   ★ 上游新增了 `scripts/deploy-seamless.sh` 的 `prune_releases_safe`（★ 第 689 行 ⇒ 一处新的 var-write 站点）
	//     ⇒ ⇒ 清单与生成器产出因此差 58 行（★ 判据⑤【逐字一致】判红）。
	//   ★★ **取证**：★ 在 `origin/main` 的**纯净 worktree** 上跑同一道门 ⇒ ⇒ **同样红**（★ 同样 58 行不同）
	//     ⇒ ⇒ ★★ **上游自己就是红的** ⇒ ⇒ 与本轮改动**零关系**。
	//   ★ 修法：★ 重跑生成器（★ 该清单是生成器的【逐字产物】⇒ 禁止手改）＋ 同步本基线。
	//   ★★ 仍是【弱判据】的例行更新 ⇒ 真正的守门是【逐字一致】判据（★ 判据⑤）。
	// ★★ 段十七（r1039 后续轮第三轮 · 收尾轮 · 合并上游第 8 批后）：83 ⇒ 84（+1 行）
	//   ★ 上游新增了 `tests/deploy_seamless_version_identity_test.sh`（★ `build_machine_gate` 里
	//     `printf '%s' "$gate" | grep -q 'rev-parse --short=8 HEAD'` ⇒ 一处新的 var-write 站点）
	//     ⇒ ⇒ 该清单（★ 生成器的【逐字产物】）因此落后一行 ⇒ 判据⑤【逐字一致】判红
	//     ＋ 本门与 §19.20.76 门的【行数基线】判红。
	//   ★★ **取证**：★ 上游那批提交**零 shell 文件** ⇒ ⇒ 这条红与本轮改动**零关系**。
	//   ★ 修法：★ 重跑生成器（★ diff = **纯新增一行**，★ 既有 84 行逐字未变）＋ 同步本基线。
	//   ★★ 仍是【弱判据】的例行更新 ⇒ 真正的守门是【逐字一致】判据。
	// ★★ 段十五（§19.20.76）：120 ⇒ 82（53 处写端改 herestring ⇒ 38 行消失 + 15 行内容变）
	//   ★ 这是【弱判据】的例行更新（只提醒“清单变过”）⇒ 真正的守门是【逐字一致】判据。
)

// sc1975RepoRoot 从测试工作目录（scripts/ursmcheck）回到仓根。
func sc1975RepoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

// sc1975ProbeHeader 是探针 TSV 的【表头列名】（★ R85 轮：表头与数据行同宽 ⇒ 逐列对齐）。
//
// ★★ 为什么要有它：★ 段十四的探针 `emit` 只有 6 个 `%s`、表头却写了 7 个词
//   ⇒ 【表头与数据列错位一列】⇒ 对外 TSV 产物「按表头取列」会取错列
//   ⇒ ★ Go 门原先按【位置】索引才免疫 ⇒ ★⇒ ★ 本表把「形状」显式钉死。
var sc1975ProbeHeader = []string{
	"write_end", "consumer", "body_form", "size", "match_at", "exit_code", "observed",
}

// sc1975RunProbe 跑探针，返回 (表头列名, 数据行)。
//
// ★★ 形状纪律：★ 第 1 个非空行 = 表头，其余 = 数据行 ⇒ ★ **判据读自己的输入时，
//   先核对【读出来的形状】与【写进去的形状】一致**（本线的纪律）⇒ 表头由判据①断言。
func sc1975RunProbe(t *testing.T, scriptPath string) ([]string, [][]string) {
	t.Helper()
	cmd := exec.Command("bash", scriptPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("跑探针 %s: %v\n%s", scriptPath, err, string(out))
	}
	var header []string
	var rows [][]string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if header == nil {
			header = f
			continue
		}
		rows = append(rows, f)
	}
	return header, rows
}

// sc1975Key 把一行压成可比对的键（5 字段：写端|消费者|body形态|尺寸|命中位置）。
func sc1975Key(f []string) string {
	if len(f) < 5 {
		return ""
	}
	return strings.Join(f[:5], "|")
}

// ── 判据①：机制读数按【判定式】钉（★ R85 轮 · §19.25.17 改判据形态）────────────

// ★★★ 读数的三种类别（R85 轮）★★★
//
//	★ 【稳定-0】/【稳定-141】：★ **机制上确定** ⇒ 取值集合必须【恰好是单元素 {期望值}】
//	  ⇒ ★ 这是**比原判据更强**的断言（原判据只看【一次】读数）。
//	★ 【竞态】：★ **机制上是赛跑** ⇒ **不钉定值** ⇒ ★ 只钉【退出码仍属 pipefail 派生值】。
const (
	sc1975ClassStable0   = "stable-0"
	sc1975ClassStable141 = "stable-141"
	sc1975ClassRace      = "race"
)

// sc1975Observed 取【取值集合】列（第 8 列）⇒ 切成集合。
//
// ★★ 这是 R85 轮新增的一列：★ 探针对每条读数**采样 N 次**⇒ 输出 N 次取值的
//   【去重排序集合】（形如 `0` 或 `0,141`）⇒ ★ ⇒ 门据此把读数分成
//   【稳定】与【竞态】⇒ ★ ⇒ **门不再拿单次读数当性质**。
func sc1975Observed(f []string) []string {
	if len(f) < 7 {
		return nil
	}
	parts := strings.Split(f[6], ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sc1975Keys 把 byKey 的键列出来（报错信息用）。
func sc1975Keys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sc1975Expect 是【期望表】：★ **按【类别】**给，而不是按【定值】给（R85 轮）。
//
// ★★ 【三类分别对应三条机制】（★ R85 轮实测；依据见探针文件头）：
//	· 稳定-0：★ 消费者【不会早退】（单行 ⇒ grep 必须读到 EOF；命中靠后 ⇒ 同理）
//	  或写端【一次写完】（986 B）⇒ ⇒ 写端必然写完 ⇒ 必然 0。
//	· 稳定-141：★ 写端载荷【≫ 管道缓冲】⇒ 写端【必然写不完】⇒ 消费者早退 ⇒ 必然 141。
//	· 竞态：★ 载荷【≈ 管道缓冲】⇒ ★ 写端的尾部写入与消费者的退出【赛跑】
//	  ⇒ ★ 两种结果都可能 ⇒ ★ **不钉定值**。
var sc1975Expect = []struct {
	key   string
	class string
	rc    string
	why   string
}{
	{"printf|grep-q|long|200000|yes", sc1975ClassStable0, "0",
		"写端单行 ⇒ grep 必须读到 EOF 才敢判 ⇒ 写端必然写完 ⇒ 无危害（条件①）"},
	{"printf|grep-q|lines|1000|yes", sc1975ClassStable0, "0",
		"载荷 986 B ⇒ 一次写完 ⇒ 无危害（条件②）"},
	{"printf|grep-q|lines|65537|yes", sc1975ClassRace, "",
		"★ 竞态区：载荷 ≈ 管道缓冲 ⇒ 写端尾部与消费者退出赛跑"},
	{"printf|grep-q|lines|70000|yes", sc1975ClassRace, "",
		"★ 竞态区：★ 超载宿主实测会翻（120 次里 2 次 ⇒ 0）⇒ 不可钉定值"},
	{"printf|grep-q|lines|200000|yes", sc1975ClassStable141, "141",
		"多行+超缓冲+命中靠前 ⇒ 写端必然写不完 ⇒ 危害成立"},
	{"printf|grep-q|lines|200000|no", sc1975ClassStable0, "0",
		"命中靠后 ⇒ grep 读到末尾 ⇒ 写端写完 ⇒ 无害（条件③）"},
	{"printf|head-c|lines|60000|yes", sc1975ClassRace, "",
		"★ 竞态区：head 读到 10 B 即退 ⇒ 与写端尾部赛跑"},
	{"printf|head-c|lines|200000|yes", sc1975ClassStable141, "141",
		"head 超缓冲 ⇒ 写端必然写不完 ⇒ 危害"},
	{"printf|herestring|lines|200000|yes", sc1975ClassStable0, "0",
		"修法形态 ⇒ 写端不在管道里 ⇒ 恒 0"},
}

// sc1975Verdict 是一次判定的裁决。
type sc1975Verdict struct {
	errs      []string
	stable0   int
	stable141 int
	races     int
	rows      int
}

// sc1975Judge 跑探针并按【判定式】判定，返回裁决（错误列表 + 分类统计）。
//
// ★★ 为什么把它抽成函数（★ R85 轮）：★ 变异自检必须驱动**门自己的比较逻辑**，
//   而不是写一份【等价实现】去测（★ 本线的通用式：★ 要实测读侧行为时
//   ⇒ 必须驱动仓内真实的代码）⇒ ⇒ ★★ 本函数既是真门的判据，也是变异自检的被测对象。
func sc1975Judge(t *testing.T, probePath string) sc1975Verdict {
	t.Helper()
	var v sc1975Verdict
	add := func(format string, args ...interface{}) {
		v.errs = append(v.errs, fmt.Sprintf(format, args...))
	}

	header, rows := sc1975RunProbe(t, probePath)
	if len(rows) == 0 {
		add("探针没有任何读数 ⇒ 量具坏了")
		return v
	}
	v.rows = len(rows)

	// ★★ 判据①的【第 0 格】：表头与数据列必须同宽且逐列对齐。
	//   ⇒ ★ 段十四的探针是 6 个 `%s` + 7 个词的表头 ⇒ 【错位一列】
	//   ⇒ ★⇒ 对外 TSV「按表头取列」会取错列（Go 门原先按位置索引才免疫）。
	if len(header) != len(sc1975ProbeHeader) {
		add("探针表头列数 = %d，期望 %d ⇒ 表头与数据列错位 ⇒ 对外 TSV 按表头取列会取错列\n表头: %v",
			len(header), len(sc1975ProbeHeader), header)
		return v
	}
	for i, want := range sc1975ProbeHeader {
		if header[i] != want {
			add("探针表头第 %d 列 = %q，期望 %q ⇒ 表头与数据列错位", i+1, header[i], want)
		}
	}
	for _, f := range rows {
		if len(f) != len(header) {
			add("读数列数 = %d，表头列数 = %d ⇒ 形状不一致: %v", len(f), len(header), f)
		}
	}

	byKey := map[string][]string{}
	for _, f := range rows {
		if len(f) < 7 {
			add("探针读数列数不足（应为 7 列）: %v", f)
			continue
		}
		byKey[sc1975Key(f)] = f
	}

	for _, w := range sc1975Expect {
		f, ok := byKey[w.key]
		if !ok {
			add("探针缺读数 %q ⇒ 当前读数集合：%v", w.key, sc1975Keys(byKey))
			continue
		}
		observed := sc1975Observed(f)
		switch w.class {
		case sc1975ClassStable0, sc1975ClassStable141:
			if w.class == sc1975ClassStable0 {
				v.stable0++
			} else {
				v.stable141++
			}
			// ★★★ 【稳定类】= 机制上确定 ⇒ 取值集合必须【恰好是单元素 {期望值}】。
			//   ⇒ ★ 比原判据强：原判据只看【一次】读数；本判据要求 N 次采样**全部**一致。
			if len(observed) != 1 || observed[0] != w.rc {
				add("机制读数变了：%s（%s）⇒ 期望取值集合恰好 {%s}，实得 %v\n"+
					"★ 这一格是【机制上确定】的 ⇒ 若它漂了 ⇒ 是 bash/OS 行为真的变了\n"+
					"★ ⇒ 必须重新实测并更新本门与 §19.20.75 / §19.25.17",
					w.key, w.why, w.rc, observed)
			}
		case sc1975ClassRace:
			v.races++
			// ★★★ 【竞态类】= 机制上是赛跑 ⇒ **不钉定值** ⇒ 但退出码必须仍是
			//   【pipefail 派生值】（0 或 141）⇒ 越界 = bash/OS 语义真的变了。
			for _, o := range observed {
				if o != "0" && o != "141" {
					add("竞态区读数越界：%s（%s）⇒ 取值 %q 不在 {0, 141} 内\n"+
						"★ 竞态类【不钉定值】，但退出码必须仍是【pipefail 派生值】⇒ 越界 = 语义真变了",
						w.key, w.why, o)
				}
			}
		}
	}

	// ★★★ 判据①的【区分度】部分：★ 门真正要守的是【无害档与有害档可区分】⇒
	//   ⇒ 稳定-0 类必须【恒 0】、稳定-141 类必须【恒 141】（上面已逐条钉）、
	//   ⇒ ★ 并且**两档都必须非空** ⇒ 否则「探针被阉割成恒 0 / 恒 141」时门会失明。
	if v.stable0 == 0 || v.stable141 == 0 {
		add("区分度判据不成立：稳定-0 类 %d 条、稳定-141 类 %d 条 ⇒ 必须【两档都在】\n"+
			"★ 否则探针可能被阉割成恒 0 / 恒 141 ⇒ 门会失去区分能力", v.stable0, v.stable141)
	}
	if v.races == 0 {
		add("判据表里【竞态类】为空 ⇒ 门不再覆盖竞态区 ⇒ 探针形态可能已变")
	}

	// ★ 反向：探针【多出】读数 ⇒ 判据表可能已过期（有人改了探针却没更新本门）。
	if len(byKey) != len(sc1975Expect) {
		add("探针读数条数 = %d，期望 %d ⇒ 探针被改过而本门没跟上（或是探针坏了）",
			len(byKey), len(sc1975Expect))
	}
	return v
}

func TestSection1975HarmConditionReadingsArePinned(t *testing.T) {
	root := sc1975RepoRoot(t)
	probe := filepath.Join(root, sc1975ProbeRel)
	if _, err := os.Stat(probe); err != nil {
		t.Fatalf("探针不存在: %v", err)
	}
	v := sc1975Judge(t, probe)
	for _, e := range v.errs {
		t.Error(e)
	}
	t.Logf("探针读数 %d 条：稳定-0 %d 条 / 稳定-141 %d 条 / 竞态 %d 条（★ 竞态类不钉定值）；"+
		"★ 区分度：无害档恒 0、有害档恒 141 ⇒ 两档可区分 ✔",
		v.rows, v.stable0, v.stable141, v.races)
}

// ── 判据②：量具自检（证明判据①不是恒绿） ───────────────────────────────────

// sc1975MutateProbe 把探针源码按 old→new 替换一次，写进临时目录并返回路径。
// ★ 【变异没生效】是「门没红」的第一嫌疑 ⇒ ⇒ 这里**断言替换确实发生**。
func sc1975MutateProbe(t *testing.T, src, old, new string) string {
	t.Helper()
	if strings.Count(src, old) != 1 {
		t.Fatalf("锚点出现 %d 次（应为 1）⇒ 变异不可靠（锚点漂移？）\n锚点: %s",
			strings.Count(src, old), old)
	}
	mutated := strings.Replace(src, old, new, 1)
	if mutated == src {
		t.Fatal("变异没有发生 ⇒ 自检无效")
	}
	path := filepath.Join(t.TempDir(), "probe-mutated.sh")
	if err := os.WriteFile(path, []byte(mutated), 0o755); err != nil {
		t.Fatalf("写变异探针: %v", err)
	}
	return path
}

// ── 判据②：量具自检（证明判据①不是恒绿） ───────────────────────────────────

// ★ 变异 M1：把探针的【取值集合】整列改成 999 ⇒ 门自己的比较必须判红。
//   ⇒ ★ 变异的是**门真正读的那一列**（`observed`）⇒ 不是改一个「没人读的字段」。
func TestSection1975HarmConditionGateHasTeeth(t *testing.T) {
	root := sc1975RepoRoot(t)
	probe := filepath.Join(root, sc1975ProbeRel)
	src, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("读探针: %v", err)
	}
	mutated := sc1975MutateProbe(t, string(src),
		`  observed=$(printf '%s' "$vals" | sort -u | paste -sd, -)`,
		`  observed=999`)
	v := sc1975Judge(t, mutated)
	if len(v.errs) == 0 {
		t.Fatal("★ 变异后门自己的比较【没有】判红 ⇒ 判据①是恒绿门")
	}
	t.Logf("变异（把探针的取值集合整列改成 999）⇒ 门自己的比较判红 ✔（%d 条错误）⇒ 判据①有牙", len(v.errs))
}

// ★★ 判据②的【反向自检】：★ 把【竞态类】读数的取值集合改成另一个**合法**的
//   pipefail 派生值（0）⇒ ⇒ 门**必须仍然绿**。
//
// ★★★★ 为什么必须有它：★★ 「判定式」如果只是【嘴上说说】（判据表里还偷偷留着
//   「竞态格必须等于某个定值」），那这道门仍然是 flaky 的 ⇒ ⇒ ★ 本自检用
//   **一个可复算的事实**把这件事钉死：★ 竞态格的取值**改成 0 也不判红**
//   ⇒ ⇒ ⇒ ★★ **即：门对竞态区【真的不依赖任何定值】**。
//
// ★★ 而它同时证明【判据不是恒绿】：★ 同一个变异器把整列改成 999 时门会红（见上一条）
//   ⇒ ⇒ ★★ 【同一条判据逻辑】：对【越界值】红、对【另一个合法值】绿
//   ⇒ ⇒ ⇒ ★★ **这正是「判定式」的定义**。
func TestSection1975RaceClassIsNotPinnedToAValue(t *testing.T) {
	root := sc1975RepoRoot(t)
	probe := filepath.Join(root, sc1975ProbeRel)
	src, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("读探针: %v", err)
	}
	mutated := sc1975MutateProbe(t, string(src),
		`  observed=$(printf '%s' "$vals" | sort -u | paste -sd, -)`,
		`  observed=$(printf '%s' "$vals" | sort -u | paste -sd, -)
  # ★ 变异：只把【竞态区】那三格的取值集合改成 0（★ 仍是合法的 pipefail 派生值）
  case "$size" in 65537|70000|60000) observed=0 ;; esac`)
	v := sc1975Judge(t, mutated)
	if len(v.errs) != 0 {
		t.Fatalf("★ 把竞态区的取值改成 0 竟被判红 ⇒ 判据仍然依赖竞态格的定值 ⇒ 门仍是 flaky 的\n错误：%v", v.errs)
	}
	t.Logf("变异（只把竞态区三格的取值集合改成 0）⇒ 门【仍然绿】✔（%d 条错误）⇒ 判据真的不依赖竞态定值", len(v.errs))
}

// ── 判据③：修法形态被钉住（防回退） ────────────────────────────────────────

func TestSection1975FixedSitesUseHerestring(t *testing.T) {
	root := sc1975RepoRoot(t)
	// ★ 内容锚（不是行号 ⇒ 行号漂移不影响判据）。
	//   每条 = (文件, 必须存在的【新形态】, 必须【不再出现】的【旧形态】)
	type site struct {
		file string
		want string
		old  string
	}
	// ★★ **锚必须是【整行】（含模式）**，不能只锚形态前缀 ⇒ 第一版锚成
	//   `echo "$output" | grep -qE` 就把【同文件里另一个无害站点】
	//   （`--- FAIL:` 那种【命中靠后 ⇒ 无害】⇒ 故意保留的管道）也算成「回退」⇒
	//   ⇒ **门当场判红 ⇒ 抓到了我自己的判据假阳性** ⇒ 改整行锚后转绿。
	//   ⇒ ★★ 这是本仓纪律的又一次同型：「判据的【假阳性方向】最危险」。
	sites := []site{
		{"scripts/audit/verify-g1-route-contract.sh",
			`if grep -qE '\[build failed\]|^# |cannot use|undefined:' <<<"$output"; then`,
			`if echo "$output" | grep -qE '\[build failed\]|^# |cannot use|undefined:'; then`},
		{"scripts/audit/verify-l11b-cost-view-rowset.sh",
			`if grep -qE '\[build failed\]|^# |cannot use|undefined:' <<<"$output"; then`,
			`if echo "$output" | grep -qE '\[build failed\]|^# |cannot use|undefined:'; then`},
		{"scripts/deploy-seamless.sh",
			`if grep -q "gateway listen failed" <<<"$probe_fail_tail"; then`,
			`if printf '%s' "$probe_fail_tail" | grep -q "gateway listen failed"; then`},
		{"scripts/deploy-seamless.sh",
			`elif grep -q "schema migrations failed transiently" <<<"$probe_fail_tail"; then`,
			`elif printf '%s' "$probe_fail_tail" | grep -q "schema migrations failed transiently"; then`},
		{"scripts/deploy-seamless.sh",
			`elif grep -q "gateway listening" <<<"$probe_fail_tail"; then`,
			`elif printf '%s' "$probe_fail_tail" | grep -q "gateway listening"; then`},
		{"scripts/deploy-seamless.sh",
			`elif grep -qE "(schema ensured|CHECKPOINT)" <<<"$probe_fail_tail"; then`,
			`elif printf '%s' "$probe_fail_tail" | grep -qE "(schema ensured|CHECKPOINT)"; then`},
		{"scripts/verify-ursm-v2-rollout.sh",
			`if grep -Eq '^ursm_shadow_diff_total\{[^}]*type="(availability_mismatch|order_mismatch)"' <<<"$metrics"; then`,
			`if printf '%s\n' "$metrics" | grep -Eq '^ursm_shadow_diff_total\{[^}]*type="(availability_mismatch|order_mismatch)"'; then`},
		{"scripts/verify-local-deployment.sh",
			`if grep -qE '"data"|"models"' <<<"$MODELS_RESPONSE"; then`,
			`if echo "$MODELS_RESPONSE" | grep -qE '"data"|"models"'; then`},
	}
	for _, s := range sites {
		p := filepath.Join(root, s.file)
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s: 读不到: %v", s.file, err)
			continue
		}
		body := string(raw)
		if !strings.Contains(body, s.want) {
			t.Errorf("%s: 找不到修法形态 %q\n"+
				"★ 段十四把这处改成了 herestring（写端无管道 ⇒ 退出码只反映消费者）⇒\n"+
				"★ 若被回退成管道形态 ⇒ 写端超 64 KiB 时会被 SIGPIPE ⇒ 141 ⇒ 判定被翻转 ⇒ 必须重改",
				s.file, s.want)
		}
		if strings.Contains(body, s.old) {
			t.Errorf("%s: 旧管道形态又出现了：%q\n"+
				"★ 同上 ⇒ 这是【回退】⇒ 请改回 herestring 形态", s.file, s.old)
		}
	}
	t.Logf("8 处真缺陷的修法形态（herestring）逐处核对通过")
}

// ── 判据④：清单进度基线 ────────────────────────────────────────────────────

func TestSection1975CandidateListRowCountBaseline(t *testing.T) {
	root := sc1975RepoRoot(t)
	list := filepath.Join(root, sc1975ListRel)
	data, err := os.ReadFile(list)
	if err != nil {
		t.Fatalf("读清单: %v", err)
	}
	rows := 0
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "file\t") {
			continue
		}
		rows++
	}
	if rows != sc1975ListRowsBaseline {
		t.Errorf("清单行数 = %d，基线 = %d\n"+
			"★ 段十四修掉 8 处真缺陷 ⇒ 118 → 110 是【预期的】⇒ 回落即真脱节 ⇒ 请重跑生成器\n"+
			"★ 提示：基线只挡【行数变化】⇒ 内容过期（行数不变）由判据⑤（逐字一致）挡",
			rows, sc1975ListRowsBaseline)
	}
	t.Logf("清单 %s：%d 行（基线 %d）", sc1975ListRel, rows, sc1975ListRowsBaseline)
}

// ── 判据⑤：清单 ↔ 生成器【逐字一致】（强判据）────────────────────────────
//
// ★★★ **为什么必须有它（本轮自查挖出来的洞）**：
//   段十四初版清单是【跨一次 merge 未重跑】的过期值（110 行，而当前树实际 120 行）——
//   对方的 R82/R75 在段十四期间并入本树，带进 10 处新站点。
//   ★ 而当时的判据④**只比行数**、基线常数又是【从那份清单抄的】⇒ ★ **门绿、量具瞎了**。
//   ⇒ 教训：**基线取自被检对象本身 = 自证** ⇒ 行数基线是【弱判据】（只挡行数变化）。
//   ⇒ 本判据把仓内生成器跑一遍，与已装清单**逐字比对** ⇒ 内容过期（哪怕行数不变）也判红。
//
// ★ 依赖 python3：仓内门禁包已有先例（`pipefail_race_ledger_test.go`）⇒
//   本机没有 python3 时**显式 SKIP 并写明理由**（不许静默通过）。

// sc1975ListDiffSummary 比两份清单的内容，返回 (差异行数, 前 8 条差异描述)。
func sc1975ListDiffSummary(a, b []byte) (int, []string) {
	al := strings.Split(string(a), "\n")
	bl := strings.Split(string(b), "\n")
	max := len(al)
	if len(bl) > max {
		max = len(bl)
	}
	diff := 0
	var msgs []string
	for i := 0; i < max; i++ {
		x, y := "", ""
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x == y {
			continue
		}
		diff++
		if len(msgs) < 8 {
			msgs = append(msgs, fmt.Sprintf("      第 %d 行\n        已装: %s\n        生成: %s",
				i+1, sc1975Clip(x), sc1975Clip(y)))
		}
	}
	return diff, msgs
}

// sc1975Clip 把一行裁到 200 字符（防差异刷屏）。
func sc1975Clip(s string) string {
	const max = 200
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + " …（截断）"
}

func TestSection1975CandidateListMatchesGeneratorByteForByte(t *testing.T) {
	root := sc1975RepoRoot(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skipf("★ 门依赖 python3（执行仓内生成器 %s），本机没有 ⇒ 显式 SKIP：%v",
			sc1975GenRel, err)
	}
	gen := filepath.Join(root, sc1975GenRel)
	if _, err := os.Stat(gen); err != nil {
		t.Fatalf("生成器不存在: %v", err)
	}
	want, err := os.ReadFile(filepath.Join(root, sc1975ListRel))
	if err != nil {
		t.Fatalf("读已装清单: %v", err)
	}
	tmp := filepath.Join(t.TempDir(), "candidates.tsv")
	cmd := exec.Command("python3", gen, "--out", tmp)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("跑生成器 %s: %v\n%s", sc1975GenRel, err, string(out))
	}
	got, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("读生成器产出: %v", err)
	}
	diff, msgs := sc1975ListDiffSummary(want, got)
	if diff != 0 {
		t.Errorf("清单与生成器产出【不逐字一致】：%d 行不同\n%s\n"+
			"★ 本文件是生成器的【逐字产物】⇒ 禁止手改 ⇒ 请重跑：\n"+
			"    python3 %s --out %s",
			diff, strings.Join(msgs, "\n"), sc1975GenRel, sc1975ListRel)
		return
	}
	t.Logf("清单 %s 与生成器产出逐字一致（%d 字节）", sc1975ListRel, len(got))

	// ★ 自检：证明本判据不是恒绿门 —— 往已装内容里注入一行差异，比较逻辑必须能判红。
	doctored := append(append([]byte{}, want...), []byte("# 自检注入的一行\n")...)
	if d, _ := sc1975ListDiffSummary(want, doctored); d == 0 {
		t.Fatal("★ 自检失败：注入了一行差异，比较逻辑仍报 0 ⇒ 判据⑤是恒绿门")
	}
}

// ── 判据⑥：errexit × pipefail 的交互读数被钉住（第十一小节）──────────────────
//
// ★★★ **为什么要有它**：段十四第十一小节实测发现，我段十四写的一句
//   「旁证：候选文件里【没有一处 `set -e`】」是**错的** ⇒
//   原因：当时的检查**只匹配裸 `set -e`** ⇒ ⇒ **完全漏掉了 `set -euo pipefail`**
//   （`-euo` 一次开了 errexit + nounset + pipefail）⇒ ⇒ ★★ **43/43 个候选文件
//   全部启用了 errexit** ⇒ ⇒ ★★ **危害因此有【两档】**：
//
//	· 条件上下文（`if <管道>` / `while` / `&&` 左侧）⇒ errexit **豁免**
//	  ⇒ 后果只是「判定翻转」（141 被当成「没匹配」）；
//	· 赋值上下文（`x="$(管道)"`）/ 简单命令上下文（裸管道）⇒ errexit **生效**
//	  ⇒ 后果是**脚本当场中止**（rc=141）⇒ ★ **比「判定翻转」更硬**。
//
// ⇒ 本判据把这两档变成**可复算的读数**（而不是一句话），并把「herestring 修法
//   在 errexit 开着时同样成立」也钉住 ⇒ ★★ **修法的普适性因此不是口述**。
//
// ★ 探针：scripts/audit/probe-pipefail-errexit-interaction.sh
//   （6 条读数 × 连跑 3 次逐字一致；载荷 = 200,000 B 的真文件 ⇒ 写端必须被 SIGPIPE 杀掉）
// ★★ 载荷为什么必须是【真文件】而不是 `python3 | head`：
//   ⇒ 写端必须是【被 SIGPIPE 杀掉】的进程（`cat`）⇒ 管道退出码才是 141；
//   ⇒ 若写端是 python，它会把 BrokenPipeError 吃掉并以 120/1 退出 ⇒ ⇒ **141 不会出现**。

// sc1975ErrKey 取 errexit 探针读数的键（context|errexit）。
func sc1975ErrKey(f []string) string {
	if len(f) < 4 {
		return ""
	}
	return f[1] + "|" + f[2]
}

// sc1975ErrRC 取退出码列（第 4 列）。
func sc1975ErrRC(f []string) string {
	if len(f) < 4 {
		return ""
	}
	return f[3]
}

// sc1975ErrReached 取「是否到达脚本末尾」列（第 5 列）。
func sc1975ErrReached(f []string) string {
	if len(f) < 5 {
		return ""
	}
	return f[4]
}

func TestSection1975ErrexitInteractionReadingsArePinned(t *testing.T) {
	root := sc1975RepoRoot(t)
	probe := filepath.Join(root, sc1975ErrexitProbeRel)
	if _, err := os.Stat(probe); err != nil {
		t.Fatalf("errexit 探针不存在: %v", err)
	}
	_, rows := sc1975RunProbe(t, probe)
	if len(rows) == 0 {
		t.Fatal("errexit 探针没有任何读数 ⇒ 量具坏了")
	}
	expect := []struct {
		key     string
		rc      string
		reached string
		why     string
	}{
		{"assign|errexit-on", "141", "no", "★ 赋值上下文 + errexit ⇒ 脚本当场中止（比判定翻转更硬）"},
		{"assign|errexit-off", "0", "yes", "对照：同一上下文去掉 errexit ⇒ 正常走到末尾"},
		{"condition|errexit-on", "0", "yes", "★ 条件上下文 ⇒ errexit 豁免 ⇒ 只翻转判定、不中止"},
		{"condition|errexit-off", "0", "yes", "对照"},
		{"simple|errexit-on", "141", "no", "★ 简单命令上下文（裸管道）+ errexit ⇒ 当场中止"},
		{"assign-herestring|errexit-on", "0", "yes", "★★ herestring 修法在 errexit 开着时同样成立"},
	}
	byKey := map[string][]string{}
	for _, f := range rows {
		if len(f) < 5 {
			t.Fatalf("errexit 探针读数列数不足（应为 5 列）: %v", f)
		}
		byKey[sc1975ErrKey(f)] = f
	}
	for _, w := range expect {
		f, ok := byKey[w.key]
		if !ok {
			t.Errorf("errexit 探针缺读数 %q ⇒ 当前读数集合：%v", w.key, byKey)
			continue
		}
		if got := sc1975ErrRC(f); got != w.rc {
			t.Errorf("errexit 交互读数变了：%s（%s）⇒ 期望 rc=%s，实得 %s\n"+
				"★ 这是【判据本身】变了（不是某处缺陷变了）⇒ 必须重新实测并更新本门与 §19.20.75",
				w.key, w.why, w.rc, got)
		}
		if got := sc1975ErrReached(f); got != w.reached {
			t.Errorf("errexit 交互读数变了：%s（%s）⇒ 期望 reached_end=%s，实得 %s",
				w.key, w.why, w.reached, got)
		}
	}
	if len(byKey) != len(expect) {
		t.Errorf("errexit 探针读数条数 = %d，期望 %d ⇒ 探针被改过而本门没跟上（或是探针坏了）",
			len(byKey), len(expect))
	}
	t.Logf("errexit 交互读数 %d 条（全部逐条一致）；关键：赋值/简单命令上下文 ⇒ 141 + "+
		"当场中止；条件上下文 ⇒ errexit 豁免；herestring 修法 ⇒ 0", len(rows))
}

// ── 判据⑥的自检：证明判据⑥不是恒绿门 ────────────────────────────────────────

func TestSection1975ErrexitInteractionGateHasTeeth(t *testing.T) {
	root := sc1975RepoRoot(t)
	probe := filepath.Join(root, sc1975ErrexitProbeRel)
	src, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("读 errexit 探针: %v", err)
	}
	// ★ 变异：把探针读数的退出码列改成常量 0 ⇒ 门自己的比较必须判红
	//   （A1/A5 期望 141 ⇒ 必 mismatch ⇒ 证明「两档」这条判据不是恒绿）。
	mutatedSrc := strings.Replace(string(src),
		`emit "$name" "$ctx" "$errexit" "$rc" "$reached"`,
		`emit "$name" "$ctx" "$errexit" "0" "$reached"`, 1)
	if mutatedSrc == string(src) {
		t.Fatal("变异没有发生（探针形态变了？）⇒ 自检无效")
	}
	tmp := t.TempDir()
	mutated := filepath.Join(tmp, "probe-errexit-mutated.sh")
	if err := os.WriteFile(mutated, []byte(mutatedSrc), 0o755); err != nil {
		t.Fatalf("写变异探针: %v", err)
	}
	// ★★ **用例数据文件必须一起拷进临时目录**：探针按 `dirname $0` 找它
	//   ⇒ 只拷探针 ⇒ 探针会「找不到用例文件」而 exit 2 ⇒ ⇒ 自检测不到变异本身。
	//   ⇒ ⇒ 连数据文件一起拷 ⇒ 探针的路径解析照常工作 ⇒ 变异照常生效。
	casesSrc := filepath.Join(root, sc1975ErrexitCasesRel)
	casesRaw, err := os.ReadFile(casesSrc)
	if err != nil {
		t.Fatalf("读用例数据文件: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, filepath.Base(casesSrc)), casesRaw, 0o644); err != nil {
		t.Fatalf("写用例数据文件副本: %v", err)
	}
	_, rows := sc1975RunProbe(t, mutated)
	if len(rows) == 0 {
		t.Fatal("变异探针没有读数 ⇒ 自检无效")
	}
	expect := map[string]string{
		"assign|errexit-on":            "141",
		"simple|errexit-on":            "141",
		"condition|errexit-on":         "0",
		"assign-herestring|errexit-on": "0",
	}
	mismatch := false
	for _, f := range rows {
		if len(f) < 5 {
			continue
		}
		if want, ok := expect[sc1975ErrKey(f)]; ok && sc1975ErrRC(f) != want {
			mismatch = true
		}
	}
	if !mismatch {
		t.Fatal("★ 变异后门自己的比较【没有】判红 ⇒ 判据⑥是恒绿门")
	}
	t.Log("变异（把 errexit 探针的退出码列改成 0）⇒ 门自己的比较判红 ✔ ⇒ 判据⑥有牙")
}
