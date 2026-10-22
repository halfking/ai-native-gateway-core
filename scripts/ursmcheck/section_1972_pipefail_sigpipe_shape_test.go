package ursm_test

// R99 轮 · 段十三（§19.20.72）的**机制门**：「变量写端 → 管道 → 早退消费者」这一族，
// **在什么形态下**才会真的让 `set -o pipefail` 下的管道退出码变成 141（假非零）。
//
// ── 为什么要有这道门 ────────────────────────────────────────────────────────
//
// ★ 这道门**不判定任何一处是不是缺陷**（那是逐处裁决的事）⇒ 它只钉住**判据本身**。
// ★ 起因：段十一（§19.20.67）结论「这一族是缺陷」是**对的**（go test 输出＝多行）；
//   但同族里也有**写端恒为单行**的站点（如 `printf '%s' "$msg"` 取的是一条 PG 日志行）
//   ⇒ **那一批没有危害** ⇒ ★⇒ 「这一族＝缺陷」是**过宽的表述**，必须按形态分流。
// ★ 而分流依据**只能靠实测**（读代码推机制会推错）：
//   实测（bash 5.3.9 / darwin-arm64）⇒ **决定性变量是【写端输出的行数】，不是链形态**：
//     · 写端输出【单行】⇒ grep 必须读到行尾才敢判 ⇒ 写端也写完了 ⇒ 退出码 0 ⇒ **无危害**；
//     · 写端输出【多行】且【超管道缓冲 64 KiB】⇒ grep 读完首行就命中并早退
//       ⇒ 写端还堵在管道里 ⇒ EPIPE/SIGPIPE ⇒ **141** ⇒ `pipefail` ⇒ **假非零**
//       ⇒ 判定点 `if <管道>; then` 恒假 ⇒ 结论塌成「没匹配」；
//     · 消费者是 `head -c` ⇒ 无论行数都早退 ⇒ **有害**（这正是段十 §19.20.66 那条真缺陷）。
// ★ ⇒ **判据（可操作形式）**：这一族里，**只有「写端可能多行且无上界」或「消费者是 head」**
//   才是真缺陷面；写端是单条记录 ⇒ 无害（改了也无收益）。
//
// ── 三条判据 ────────────────────────────────────────────────────────────────
//
// ① **机制读数被钉住**：跑 `scripts/audit/probe-pipefail-sigpipe-shape.sh`（仓内可重跑），
//    断言 8 条读数逐条一致 ⇒ ★ 若 bash/OS 行为变了 ⇒ **红**（而不是让下一个人重新推错）。
// ② **量具自检（防恒绿门）**：把探针拷到临时目录、注入一条**错的读数** ⇒
//    门自己的比较逻辑必须能判红 ⇒ ★ 证明判据①不是恒绿。
// ③ **清单进度基线**：候选清单 `docs/audit/evidence/2026-10-21-pipefail-scope-drop-candidates.tsv`
//    的行数必须等于基线（120）⇒ ★ 新增站点 ⇒ **红**（防止清单与仓内形态脱节）。
//    ★★ 本判据只挡【行数变化】⇒ **内容过期（行数不变）挡不住** ⇒ 那一层由
//    §19.20.75 门的判据⑤（清单 ↔ 生成器**逐字一致**）保证。
//
// ★ 对生产的写入 = 0（只读仓内文件 + 只跑纯 shell 探针）。

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	sc1972ProbeRel = "scripts/audit/probe-pipefail-sigpipe-shape.sh"
	sc1972ListRel  = "docs/audit/evidence/2026-10-21-pipefail-scope-drop-candidates.tsv"
	// ★ 基线：清单行数（剥注释 + 顶层覆盖口径收紧 + 量具自身排除）
	//   ★ 四变体单变量对照（同一棵树、逐项开关）：A 段十一口径 = 122 ⇒ B 只剥注释 = 120
	//     ⇒ **剥注释 = −2**（2 条幽灵注释行，站点已定位到
	//      scripts/audit/verify-session-family-p0-realdb.sh:116/124）｜C 只收紧顶层 = 122
	//     ⇒ **顶层收紧 = ±0**
	//   ★ ⇒ 段十一清单的 120 ⇒ **多报 2 条**；引用计数必须带口径
	//   ★★ **118 → 110（段十四 / §19.20.75）**：段十四把「危害成立条件」补到五条（新增
	//     「退出码真被消费」与「写端上界」两维）⇒ 逐处人工回读 ⇒ **修掉 8 处真缺陷**
	//     （verify-g1-route-contract.sh:127、verify-l11b-cost-view-rowset.sh:175、
	//      deploy-seamless.sh:1401/1403/1405/1407、verify-ursm-v2-rollout.sh:112、
	//      verify-local-deployment.sh:333 ⇒ 全部改为 herestring 形态）⇒ 那 8 处不再是候选
	//     ⇒ ⇒ **基线随之下降是【预期的】，不是清单脱节** ⇒ 回落即真脱节（要红）
	//   ★★★ **订正（段十四定稿时）**：110 是【跨一次 merge 未重跑】的过期值 ⇒ 实际 120。
	//     差 10 处的来源 = 对方的 R82 `459d80a3c`（9 处 verify-nonplain-migration-delivery.sh）
	//     ＋ R75 `dea4eda4d`（1 处 ursm-snapshot-slot-refresh.sh）⇒ **口径的差值不变、树变了**。
	//   ★★ **基线不得【从清单抄】**（= 自证）⇒ 真正的牙是 §19.20.75 门的判据⑤
	//     （清单 ↔ 生成器**逐字一致**）⇒ 本门只负责「行数」这一层。
	sc1972ListRowsBaseline = 82
	// ★★ 段十五（§19.20.76）：120 ⇒ 82（53 处写端改 herestring ⇒ 38 行消失 + 15 行内容变）
	//   ★ 这是【弱判据】的例行更新（只提醒“清单变过”）⇒ 真正的守门是【逐字一致】判据。
)

// sc1972RepoRoot 从测试工作目录（scripts/ursmcheck）回到仓根。
func sc1972RepoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

// sc1972RunProbe 跑探针，返回 (表头后的数据行, 原始输出)。
func sc1972RunProbe(t *testing.T, scriptPath string) [][]string {
	t.Helper()
	cmd := exec.Command("bash", scriptPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("探针 %s 执行失败: %v\n%s", scriptPath, err, out)
	}
	var rows [][]string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	first := true
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if first {
			first = false
			if len(fields) > 0 && fields[0] == "header" {
				continue
			}
		}
		rows = append(rows, fields)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("扫探针输出: %v", err)
	}
	return rows
}

// sc1972Key 把一行压成可比对的键。
func sc1972Key(f []string) string {
	if len(f) < 5 {
		return strings.Join(f, "|")
	}
	return strings.Join(f[:5], "|")
}

// sc1972RC 取退出码列（第 6 列；前面是 写端|消费者|body形态|尺寸|命中位置）。
func sc1972RC(f []string) string {
	if len(f) < 6 {
		return ""
	}
	return f[5]
}

// ── 判据①：机制读数被钉住 ───────────────────────────────────────────────────

func TestSection1972SigpipeShapeReadingsArePinned(t *testing.T) {
	root := sc1972RepoRoot(t)
	probe := filepath.Join(root, sc1972ProbeRel)
	if _, err := os.Stat(probe); err != nil {
		t.Fatalf("探针不存在: %v", err)
	}
	rows := sc1972RunProbe(t, probe)
	if len(rows) == 0 {
		t.Fatal("探针没有任何读数 ⇒ 量具坏了")
	}

	byKey := map[string]string{}
	for _, f := range rows {
		if len(f) < 6 {
			t.Fatalf("探针读数列数不足（应为 6 列：写端|消费者|body形态|尺寸|命中位置|退出码）: %v", f)
		}
		byKey[sc1972Key(f)] = sc1972RC(f)
	}

	// ★ 期望表（用「写端|消费者|body|尺寸|位置」五元组；位置在探针里没单独列 ⇒
	//   探针的「命中在末尾」那一行与「命中在开头」的行 key 相同 ⇒ 探针必须把位置也列出来）
	//   ⇒ 若探针缺位置列 ⇒ 直接判红（这正是本轮踩过的坑：位置不列 ⇒ 读数不可判）。
	type wantRow struct {
		key string
		rc  string
	}
	expect := []wantRow{
		{"printf|grep-q|long|200000|yes", "0"},    // 单行 ⇒ grep 读到行尾 ⇒ 写端写完 ⇒ 无危害
		{"echo|grep-q|long|200000|yes", "0"},      // 同上
		{"printf|grep-q|lines|1000|yes", "0"},     // 多行但未超缓冲 ⇒ 无危害
		{"printf|grep-q|lines|200000|yes", "141"}, // ★ 多行 + 超缓冲 + 命中靠前 ⇒ 危害成立
		{"echo|grep-q|lines|200000|yes", "141"},   // ★ 同上
		{"printf|grep-q|lines|200000|no", "0"},    // ★ 命中在末尾 ⇒ grep 也要读到末尾 ⇒ 写端写完 ⇒ 无危害
		{"printf|head-c|long|200000|yes", "141"},  // ★ head -c ⇒ 无论行数都早退 ⇒ 有害
		{"printf|head-c|lines|200000|yes", "141"},
	}
	for _, w := range expect {
		got, ok := byKey[w.key]
		if !ok {
			t.Errorf("探针缺读数 %q ⇒ 当前读数：%v", w.key, byKey)
			continue
		}
		if got != w.rc {
			t.Errorf("机制读数变了：%s ⇒ 期望 %s，实得 %s\n"+
				"★ 这是【判据本身】变了（不是某处缺陷变了）⇒ 必须重新实测并更新本门与 §19.20.72",
				w.key, w.rc, got)
		}
	}
	t.Logf("探针读数 %d 条；关键读数：grep-q+多行+200000 ⇒ 141（危害）；grep-q+单行+200000 ⇒ 0（无害）", len(rows))
}

// ── 判据②：量具自检（证明判据①不是恒绿） ───────────────────────────────────

func TestSection1972SigpipeShapeGateHasTeeth(t *testing.T) {
	root := sc1972RepoRoot(t)
	probe := filepath.Join(root, sc1972ProbeRel)
	src, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("读探针: %v", err)
	}

	// ★ 自检（阳性）：把探针里的「141」读数改成一个错的值 ⇒ 门自己的比较必须判红。
	//   ⇒ 做法：把探针拷到临时目录、注入错的读数、跑它、断言「读数与期望不符」能被检出。
	tmp := t.TempDir()
	mutated := filepath.Join(tmp, "probe-mutated.sh")
	mutatedSrc := strings.Replace(string(src), "emit printf grep-q lines  200000 yes",
		"emit printf grep-q lines  200000 yes\n#MUT\n", 1)
	// ★ 上面的替换只是插注释 ⇒ 不够；真正要改的是**读数** ⇒ 改探针里的比较基准行。
	mutatedSrc = strings.Replace(mutatedSrc, "emit \"$we\" \"$cons\" \"$form\" \"$size\" \"$at_start\" \"$rc\"",
		"emit \"$we\" \"$cons\" \"$form\" \"$size\" \"$at_start\" \"999\"", 1)
	if mutatedSrc == string(src) {
		t.Fatalf("变异没有发生（探针形态变了？）⇒ 自检无效")
	}
	if err := os.WriteFile(mutated, []byte(mutatedSrc), 0o755); err != nil {
		t.Fatalf("写变异探针: %v", err)
	}
	rows := sc1972RunProbe(t, mutated)
	if len(rows) == 0 {
		t.Fatal("变异探针没有读数 ⇒ 自检无效")
	}
	// ★ 变异后读数必须与期望不符（否则门判据①就是恒绿）
	mutatedOK := false
	for _, f := range rows {
		if len(f) >= 6 && sc1972RC(f) == "999" {
			mutatedOK = true
			break
		}
	}
	if !mutatedOK {
		t.Fatalf("变异后读数里没有 999 ⇒ 变异没生效 ⇒ 自检无效")
	}
	// ★ 门自己的判据必须能把它判红：逐条与期望比 ⇒ 必有 mismatch
	//   ★ 键必须与判据①【同一个口径】（5 字段：写端|消费者|body|尺寸|命中位置）
	//     ⇒ 否则键永不匹配 ⇒ mismatch 恒 false ⇒ 本自检会恒红（第一版就栽在这里）。
	expect := map[string]string{
		"printf|grep-q|lines|200000|yes": "141",
	}
	mismatch := false
	for _, f := range rows {
		if len(f) < 6 {
			continue
		}
		key := sc1972Key(f)
		if wantRC, ok := expect[key]; ok && sc1972RC(f) != wantRC {
			mismatch = true
		}
	}
	if !mismatch {
		t.Fatal("★ 变异后门自己的比较【没有】判红 ⇒ 判据①是恒绿门")
	}
	t.Log("变异（把探针读数改成 999）⇒ 门自己的比较判红 ✔ ⇒ 判据①有牙")
}

// ── 判据③：清单进度基线 ───────────────────────────────────────────────────

func TestSection1972CandidateListRowCountBaseline(t *testing.T) {
	root := sc1972RepoRoot(t)
	list := filepath.Join(root, sc1972ListRel)
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
	if rows != sc1972ListRowsBaseline {
		t.Errorf("清单行数 = %d，基线 = %d\n"+
			"★ 清单与仓内形态脱节（要么有人改了脚本、要么清单没重生成）⇒ 请重跑生成器",
			rows, sc1972ListRowsBaseline)
	}
	t.Logf("清单 %s：%d 行（基线 %d）", sc1972ListRel, rows, sc1972ListRowsBaseline)
}
