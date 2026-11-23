package dbinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// =====================================================================
// TestFreshDbBuilderReplicatesInstallerSeed
//
// 它钉的是什么
//
// 2026-11-23 L29 轮上游把 956 的前置守卫（`models_canonical` 里存在 **active**
// 的 `minimax-m3.1-flash`）从**迁移**挪到了**安装器代码**（runner.go 的
// modelsCanonicalSeedSQL，只在新装路径执行）。
//
// 于是出现了两种「新装」：
//   ① 真安装器新装（走 InitSchema）⇒ 补种执行 ⇒ 956 守卫通过；
//   ② SQL 文件链复刻（scripts/audit/build-fresh-install-db.sh）⇒ ★ 补种不
//      在文件链里 ⇒ ★ 956 必然中止。
//
// ★ 本门的作用：★ 保证「建造器」与「安装器」在这一点上**不漂移**——
//   · 建造器必须真的从 runner.go 里**抽取**补种 SQL（★ 不是内联一份副本）；
//   · 抽取出来的那份，必须与 runner.go 的常量**逐字相等**；
//   · 抽取失败必须**中止**（★ 不许「跳过这一步」⇒ 跳过＝造出一份假底账）。
//
// ★ 为什么用「执行脚本里的 python 块」而不是「在 Go 里重写一遍正则」：
//   ★ 重写一遍＝两份解析逻辑 ⇒ 建造器抽错了、门却照自己那份算 ⇒ 恒绿
//   ⇒ ⇒ 所以门**直接执行建造器自己那段抽取代码**、再与 Go 侧解析结果比对。
// =====================================================================

const freshDbBuilderPath = "../../../scripts/audit/build-fresh-install-db.sh"

// seedConstRe 从 runner.go 解析 Go 常量（★ 门自己的那份解析）。
var seedConstRe = regexp.MustCompile("(?s)const\\s+modelsCanonicalSeedSQL\\s*=\\s*`([^`]*)`")

func readFreshDbBuilder(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(freshDbBuilderPath)
	if err != nil {
		t.Fatalf("read %s: %v\n"+
			"★ 建造器脚本不在仓内 ⇒ 「新装底账」这条链无法复跑\n", freshDbBuilderPath, err)
	}
	return string(b)
}

// builderSeedPyBlock 从建造器脚本里切出它自己的 python 抽取块
// （★ 形态：`extract_seed_sql() { python3 - "$RUNNER_GO" <<'PY' … PY }`）。
//
// ★ 切不出来 ⇒ 门红（★ 因为那意味着建造器**不再**从 runner.go 抽取 ⇒
//
//	要么它内联了副本（漂移），要么它删了这步（造出假底账））。
func builderSeedPyBlock(t *testing.T, script string) string {
	t.Helper()
	const open = "<<'PY'\n"
	i := strings.Index(script, open)
	if i < 0 {
		t.Fatalf("建造器脚本里找不到 python heredoc 块（标记 %q）\n"+
			"★ ⇒ 它不再从 runner.go 抽取补种 SQL ⇒ ★ 要么内联了副本（会漂移）\n"+
			"★ ⇒ 要么删掉了这一步（会造出假底账）\n", open)
	}
	rest := script[i+len(open):]
	j := strings.Index(rest, "\nPY\n")
	if j < 0 {
		t.Fatalf("建造器脚本里的 python heredoc 块没有闭合标记（\\nPY\\n）\n")
	}
	return rest[:j+1]
}

func TestFreshDbBuilderReplicatesInstallerSeed(t *testing.T) {
	script := readFreshDbBuilder(t)

	// ── 判据 ①：建造器声明了抽取函数、且失败即中止（不许跳过）────────
	for _, needle := range []string{
		"extract_seed_sql",
		"modelsCanonicalSeedSQL",
		"bad \"抽取 runner.go 的 modelsCanonicalSeedSQL 失败",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("建造器脚本里缺少片段 %q\n"+
				"★ ⇒ 建造器与安装器在【基线补种】这一步上漂移了\n"+
				"★ ⇒ 新装底账会缺那一行 ⇒ 956 守卫中止 ⇒ census 不可用\n", needle)
		}
	}

	// ── 判据 ②：抽取出来的内容 == runner.go 的常量（逐字）────────────
	goSrc, err := os.ReadFile("runner.go")
	if err != nil {
		t.Fatalf("read runner.go: %v", err)
	}
	m := seedConstRe.FindSubmatch(goSrc)
	if m == nil {
		t.Fatalf("runner.go 里解析不出 modelsCanonicalSeedSQL 常量\n")
	}
	goConst := string(m[1])

	py := builderSeedPyBlock(t, script)
	tmp, err := os.CreateTemp("", "seed-extract-*.py")
	if err != nil {
		t.Fatalf("create temp py: %v", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(py); err != nil {
		t.Fatalf("write temp py: %v", err)
	}
	tmp.Close()

	runnerGo, err := filepath.Abs("runner.go")
	if err != nil {
		t.Fatalf("abs runner.go: %v", err)
	}
	cmd := exec.Command("python3", tmp.Name(), runnerGo)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("★ 建造器自己的抽取块执行失败（★ python3=%v）：%v\n"+
			"★ ⇒ 建造器跑起来会在这一步中止 ⇒ 新装底账造不出来\n",
			exec.Command("python3", "--version").String(), err)
	}
	if got := string(out); got != goConst {
		t.Errorf("★ 建造器抽出来的补种 SQL 与 runner.go 的常量**不一致**\n"+
			"★ ⇒ 建造器用的是副本而不是真源 ⇒ 上游一改常量这里就静默漂移\n"+
			"★ --- 建造器抽出来的（%d 字节） ---\n%s\n"+
			"★ --- runner.go 的常量（%d 字节） ---\n%s\n",
			len(got), got, len(goConst), goConst)
	}
}
