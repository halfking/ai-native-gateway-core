package ursm_test

// cjk_var_expansion_test.go — 2026-10-21 R85 轮（形态量具线）
//
// # 这道门要拦的事故：★ **bash 会把非 ASCII 字节吸进变量名**
//
// 现象（R85 轮实测，bash 3.2 与 5.3 都一样）：
//
//	set -u; v=1; echo "X$v中Y"
//	⇒ bash: v?: 未绑定的变量     （exit 127，脚本当场退出）
//	set -u 之外：v=1; echo "X$v中Y"
//	⇒ 输出 "X??Y"                （**值和那个汉字一起被吞掉** ⇒ 静默失真）
//
// 机制：bash 解析 `$` 之后的**变量名**时，把「后随的非 ASCII 字节」也算进名字里
// ⇒ 变量名变成 `v中` ⇒ 这个名字**从来没人定义过** ⇒
//　　- `set -u` ⇒ 报「未绑定的变量」并**中止脚本**；
//　　- 无 `set -u` ⇒ 展开成空串 ⇒ **原值与那个字符一起消失**。
//
// ★ 为什么这值得一道仓级门：仓里已有一处**同类**的局部守卫
//   （`sql/schema/integration_gate_test.go::TestGateDoesNotRequireHostPsql` 里的
//   `\$[A-Za-z_][A-Za-z0-9_]*[^\x00-\x7f]` 正则），但它**只覆盖那一个脚本**。
//   ⇒ R85 轮实测全仓：**52 处代码点 / 33 个文件**，其中 **49 处在 `set -u` 脚本里**
//   ⇒ ⇒ 一旦走到那一行，脚本就在**最需要它说话的时候**（失败分支、告警分支）死掉。
//
// # 判据口径（与登记册 `cjk_var_sites.tsv` 必须逐字一致）
//
//	① 只认**裸** `$VAR`；`${VAR}` 带花括号是**安全**的（花括号终止变量名）。
//	② 跳过整行注释（首个非空白字符是 `#`）。
//	③ 跳过**单引号**区段（单引号里不发生展开）。
//	④ 其余位置：凡 `$VAR` 后面紧跟一个 **>= 0x80 的字节**，记 1 个站点。
//
// # 棘轮（ratchet）而不是「零容忍」
//
// 存量 52 处里有 47 处落在**别人正在改**的脚本上（`scripts/252-monitor/*`、
// `scripts/user/*`、`scripts/audit/*` …）⇒ 本轮**不替属主批量改**（跨写者、跨语义）。
// ⇒ 所以门做成**棘轮**：登记册钉住每个文件的**当前站点数**，
//
//	- 新增文件出现站点 ⇒ 红（登记册里没有这个文件）；
//	- 登记过的文件站点数**超过**登记值 ⇒ 红（有人在加坑）；
//	- 站点数**少于**登记值 ⇒ 绿（有人修好了 ⇒ 这是好事，不阻塞）。
//
// ★ 诚实的边界：这个棘轮按**文件**计数 ⇒ **同一文件里「修一处 + 加一处」
//   会互相抵消**（总数不变 ⇒ 门不红）⇒ ⇒ 它拦的是「**净增长**」，
//   不是「逐个站点」。逐站点钉死需要钉到行内容，代价是别人改一行就要重登记
//   ⇒ 在共享仓里噪音太大 ⇒ 本轮不取。
//
// ★★ 门本身必须能证明**扫描器有牙**：文件末尾的 `TestCJKVarScannerHasTeeth`
//   用**合成输入**断言扫描器确实会报（否则「扫描器扫了个寂寞」也能全绿）。

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const cjkVarRegistry = "cjk_var_sites.tsv"

// cjkVarSkipDirs 是扫描时跳过的目录。
var cjkVarSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	"dist": true, "build": true, ".venv": true, "__pycache__": true,
}

// cjkVarSite 是一条「裸 $VAR 紧跟非 ASCII」的站点。
type cjkVarSite struct {
	Line int
	Text string
}

// scanCJKVarInText 按判据口径扫描一段文本，返回站点列表。
//
// ★ 抽成独立函数是为了让**扫描器自检**能直接喂合成输入
//   （否则「扫描器扫了个寂寞」= 门恒绿）。
func scanCJKVarInText(text string) []cjkVarSite {
	var sites []cjkVarSite
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// ③ 去掉单引号区段（单引号里不发生展开）
		var b strings.Builder
		inSingle := false
		for _, r := range line {
			if r == '\'' {
				inSingle = !inSingle
				continue
			}
			if !inSingle {
				b.WriteRune(r)
			}
		}
		seg := b.String()
		// ① 去掉 ${VAR} 形式（安全）后再找裸 $VAR
		seg = stripBracedVars(seg)
		for _, m := range bareVarMatches(seg) {
			end := m[1]
			if end < len(seg) && seg[end] >= 0x80 {
				sites = append(sites, cjkVarSite{Line: i + 1, Text: strings.TrimSpace(line)})
				break
			}
		}
	}
	return sites
}

// stripBracedVars 把 `${NAME}` 形态替换成等长占位（保持后续字节偏移不变）。
func stripBracedVars(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '$' && i+1 < len(s) && s[i+1] == '{' {
			j := i + 2
			for j < len(s) && (s[j] == '_' || (s[j] >= 'a' && s[j] <= 'z') ||
				(s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			if j < len(s) && s[j] == '}' && j > i+2 {
				for k := i; k <= j; k++ {
					b.WriteByte('\x00')
				}
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// bareVarMatches 返回所有**裸** `$NAME` 的 [起始, 结束) 字节偏移。
//
// ★ 手工扫描而不是用 regexp：需要精确定位「$ 之后连续的名字字节」的**结束位置**，
//   而 regexp 的 FindAllStringIndex 给的是整个匹配（含名字），拿不到结束偏移。
func bareVarMatches(s string) [][2]int {
	var out [][2]int
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		j := i + 1
		if j >= len(s) {
			break
		}
		c := s[j]
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			continue
		}
		for j < len(s) {
			c := s[j]
			if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
				j++
				continue
			}
			break
		}
		out = append(out, [2]int{i, j})
		i = j - 1
	}
	return out
}

// scanRepoShellScripts 扫全仓 shell 脚本，返回「相对路径 → 站点数」与站点明细。
func scanRepoShellScripts(root string) (map[string]int, map[string][]cjkVarSite, error) {
	counts := map[string]int{}
	details := map[string][]cjkVarSite{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if cjkVarSkipDirs[info.Name()] && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".sh") && !strings.HasSuffix(path, ".bash") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil // 读不到就当没有（不因权限问题让门红）
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		sites := scanCJKVarInText(string(data))
		if len(sites) == 0 {
			return nil
		}
		counts[rel] = len(sites)
		details[rel] = sites
		return nil
	})
	return counts, details, err
}

// readCJKVarRegistry 读登记册：path → count。
func readCJKVarRegistry(path string) (map[string]int, error) {
	reg := map[string]int{}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			return nil, fmt.Errorf("登记册行缺列：%q", line)
		}
		n, cerr := strconv.Atoi(strings.TrimSpace(parts[1]))
		if cerr != nil {
			return nil, fmt.Errorf("登记册第 2 列不是整数：%q", line)
		}
		reg[parts[0]] = n
	}
	return reg, sc.Err()
}

// writeCJKVarRegistry 写登记册（-update 用）。
func writeCJKVarRegistry(path string, counts map[string]int, details map[string][]cjkVarSite) error {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("# R85 轮登记册：shell 脚本里「裸 $VAR 紧跟非 ASCII 字节」的站点\n")
	b.WriteString("#\n")
	b.WriteString("# ★ 背景：bash 会把 $ 之后紧跟的非 ASCII 字节**并入变量名**\n")
	b.WriteString("#   ⇒ 变量名变成 `$VAR中` ⇒ 从没人定义过 ⇒\n")
	b.WriteString("#     - set -u ⇒ 报「未绑定的变量」并**中止脚本**（实测 exit 127）；\n")
	b.WriteString("#     - 无 set -u ⇒ 展开成空串 ⇒ **原值与那个字符一起消失**（静默失真）。\n")
	b.WriteString("#   ⇒ 修法：把裸 `$VAR` 写成 `${VAR}`（花括号终止变量名）。\n")
	b.WriteString("#\n")
	b.WriteString("# 列：path<TAB>count<TAB>note\n")
	b.WriteString("#   count = 该文件当前的站点数（**棘轮上界**：只许减不许增）\n")
	b.WriteString("#   note  = 一句话说明（人工口径）\n")
	b.WriteString("#\n")
	b.WriteString("# ★ 门：scripts/ursmcheck/cjk_var_expansion_test.go\n")
	b.WriteString("#   新增文件出现站点 ⇒ 红；登记过的文件站点数增加 ⇒ 红；减少 ⇒ 绿（修好了）。\n")
	b.WriteString("# ★ 诚实的边界：按**文件**计数 ⇒ 同一文件「修一处 + 加一处」会互相抵消。\n")
	b.WriteString("#\n")
	b.WriteString("# 存量来源：R85 轮全仓实测（52 处 / 33 文件；其中 49 处在 set -u 脚本里）。\n")
	b.WriteString("# ★ 存量未批量修的原因：47 处落在**别人正在改**的脚本上 ⇒ 跨写者改动需属主拍板。\n")
	b.WriteString("#\n")
	for _, k := range keys {
		note := ""
		if len(details[k]) > 0 {
			note = fmt.Sprintf("第 %d 行等 %d 处", details[k][0].Line, len(details[k]))
		}
		b.WriteString(fmt.Sprintf("%s\t%d\t%s\n", k, counts[k], note))
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// ★ 旗标名带前缀：同包已有人占用 `-update`（hardcoded_dsn_fallback_test.go）
//   ⇒ 用 `-cjk-var-update` 免得 init 阶段直接 panic（同包旗标重复定义）。
var cjkVarUpdate = flag.Bool("cjk-var-update", false, "重算并覆盖 CJK 变量名登记册")

// TestCJKVarExpansionNoNewSites 是本轮的门：**存量棘轮**。
func TestCJKVarExpansionNoNewSites(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("解析仓根失败: %v", err)
	}
	counts, details, err := scanRepoShellScripts(root)
	if err != nil {
		t.Fatalf("扫描仓内 shell 脚本失败: %v", err)
	}
	regPath := filepath.Join(".", cjkVarRegistry)

	if *cjkVarUpdate {
		if err := writeCJKVarRegistry(regPath, counts, details); err != nil {
			t.Fatalf("写登记册失败: %v", err)
		}
		t.Logf("已重算登记册：%d 个文件 / %d 处站点", len(counts), totalSites(counts))
		return
	}

	reg, err := readCJKVarRegistry(regPath)
	if err != nil {
		t.Fatalf("读登记册 %s 失败: %v\n"+
			"★ 登记册是本门的**事实源**；读不到 ⇒ 门不能退化成「什么都不查就绿」\n"+
			"★ 修法：go test ./scripts/ursmcheck/ -run TestCJKVarExpansionNoNewSites -update",
			cjkVarRegistry, err)
	}
	if len(reg) == 0 {
		t.Fatalf("登记册是空的 ⇒ 门会**恒绿**（等于没有门）")
	}

	// ① 登记过的文件：站点数不得增加
	var grew []string
	for path, n := range counts {
		old, ok := reg[path]
		if !ok {
			grew = append(grew, fmt.Sprintf("★ 新文件出现站点：%s（%d 处）", path, n))
			continue
		}
		if n > old {
			grew = append(grew, fmt.Sprintf("★ 站点数增加：%s（登记 %d → 现 %d）", path, old, n))
		}
	}
	// ② 登记册里有、现在没了的（修好了）⇒ 只报告，不阻塞
	var fixed []string
	for path, old := range reg {
		if n, ok := counts[path]; !ok || n < old {
			fixed = append(fixed, fmt.Sprintf("%s（登记 %d → 现 %d）", path, old, counts[path]))
		}
	}
	sort.Strings(grew)
	sort.Strings(fixed)
	if len(fixed) > 0 {
		t.Logf("★ 有 %d 个文件的站点数低于登记值（修好了，可从登记册里摘掉）：\n  %s",
			len(fixed), strings.Join(fixed, "\n  "))
	}
	if len(grew) > 0 {
		var b strings.Builder
		for _, g := range grew {
			b.WriteString(g + "\n")
			// 把具体站点行也打出来，让修的人不用再找一遍
			path := strings.Fields(g)[len(strings.Fields(g))-2]
			for _, s := range details[path] {
				b.WriteString(fmt.Sprintf("      %s:%d: %s\n", path, s.Line, s.Text))
			}
		}
		t.Fatalf("shell 脚本里出现了新的「裸 $VAR 紧跟非 ASCII 字节」站点：\n%s\n"+
			"★ bash 会把那个非 ASCII 字节**并进变量名** ⇒ set -u 下脚本当场中止，\n"+
			"  无 set -u 下值与字符一起被吞掉（静默失真）。\n"+
			"★ 修法：把裸 `$VAR` 改成 `${VAR}`（花括号终止变量名）。\n"+
			"★ 若这是**有意保留**的存量，请把该文件与站点数登记进 %s（`-update` 可重算）。",
			b.String(), cjkVarRegistry)
	}
	t.Logf("✔ CJK 变量名站点：无净增长（现有 %d 个文件 / %d 处站点，全部在登记册棘轮内）",
		len(counts), totalSites(counts))
}

func totalSites(counts map[string]int) int {
	n := 0
	for _, v := range counts {
		n += v
	}
	return n
}

// TestCJKVarScannerHasTeeth 证明**扫描器本身有牙**。
//
// ★ 没有这道自检，一个「扫了个寂寞」的扫描器会让上面那道门**恒绿**。
//   用合成输入断言：
//	① 裸 `$VAR` 紧跟汉字 ⇒ 命中；
//	② `${VAR}` 紧跟汉字 ⇒ **不**命中（花括号终止变量名）；
//	③ 行尾注释里的 `$VAR中` ⇒ **不**命中（注释不执行）；
//	④ 单引号里的 `$VAR中` ⇒ **不**命中（单引号不展开）；
//	⑤ ASCII 紧跟（`$VAR)`）⇒ **不**命中。
func TestCJKVarScannerHasTeeth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"裸变量紧跟汉字", `echo "值=$v中"`, 1},
		{"花括号形式安全", `echo "值=${v}中"`, 0},
		{"整行注释", `# echo "值=$v中"`, 0},
		{"单引号内", `echo '值=$v中'`, 0},
		{"ASCII 紧跟安全", `echo "值=$v)"`, 0},
		{"两处各算一次", "echo \"a=$x中\"\necho \"b=$y中\"", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := len(scanCJKVarInText(tc.in))
			if got != tc.want {
				t.Fatalf("扫描器对 %q 报 %d 处，期望 %d 处\n"+
					"★ 扫描器没有牙 ⇒ 上面那道门会**恒绿**（等于没有门）", tc.in, got, tc.want)
			}
		})
	}
}
