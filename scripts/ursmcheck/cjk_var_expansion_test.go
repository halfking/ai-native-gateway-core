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
//	② 跳过**注释**（整行注释 ＋ **尾随注释**；★ 尾随注释的判据是
//	   「`#` 在引号外 **且** 处于**词首**」⇒ `a#b` 是字面量、不是注释）。
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
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const cjkVarRegistry = "cjk_var_sites.tsv"

// ★ cjkVarSkipDirs 已删除：★ 2026-10-23（R85 轮收尾）起，扫描范围改为
// 【git 追踪的文件】（见 scanRepoShellScripts）⇒ ⇒ 不再需要「按目录名跳过」。

// cjkVarSite 是一条「裸 $VAR 紧跟非 ASCII」的站点。
type cjkVarSite struct {
	Line int
	Text string
}

// scanCJKVarInText 按判据口径扫描一段文本，返回站点列表。
//
// ★ 抽成独立函数是为了让**扫描器自检**能直接喂合成输入
//
//	（否则「扫描器扫了个寂寞」= 门恒绿）。
func scanCJKVarInText(text string) []cjkVarSite {
	var sites []cjkVarSite
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// ★★ ②+③ 剥掉尾随注释与单引号区段 ⇒ 只留「会参与展开」的字节
		seg := cjkCodeSegment(line)
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

// cjkCodeSegment 返回一行里**会参与变量展开**的字节：★ 去掉尾随注释 ＋ 去掉单引号区段。
//
// ★★ 2026-10-23（R85 轮收尾）：★ 旧实现只剥**整行**注释（`HasPrefix(trimmed, "#")`）
//
//	  ⇒ ⇒ **尾随注释**里的 `$VAR）` 被报成站点 ⇒ ⇒ **假阳性**。
//	  ★ 实测触发的站点（`scripts/mut-nginx-realip-patch.sh:47` / `:56`）：
//
//		run_gate_patch() {   # run_gate_patch  （用 $MUT_PATCHER / $MUT_ENGINE）
//
//	  ⇒ bash **不展开注释** ⇒ ⇒ 不是站点（★ 实测：`set -u` 下尾随注释里的
//	    `$UNDEFINED_VAR` 不报错、退出码 0）⇒ ⇒ 旧口径会把登记册染成「站点数增加」。
//
// ★★ 而判据不能退化成「见 # 就截断」—— bash 的规则是：
//
//	`#` 只有在【引号外】**且**处于【词首】时才是注释起始
//	⇒ `a#b` 是字面量（不是注释）⇒ `echo "x"#y $v中` 里的 `$v中` **仍是站点**。
//
// ⇒ ⇒ 本函数按 bash 的真实语义单遍走一遍行，记住单/双引号状态。
func cjkCodeSegment(line string) string {
	var b strings.Builder
	inSingle, inDouble := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && !inSingle:
			// ★ 反斜杠：下一个字节按字面处理
			//   （`\$` 不展开、 `\"` 不切换引号状态、`\\` 是一个字面反斜杠）。
			b.WriteByte(c)
			if i+1 < len(line) {
				i++
				b.WriteByte(line[i])
			}
			continue
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			continue
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '#' && !inSingle && !inDouble:
			if i == 0 || isCommentWordStart(line[i-1]) {
				// ★ 尾随注释 ⇒ 后面整段不参与展开
				return b.String()
			}
		}
		if inSingle {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// isCommentWordStart 判断某个字节之后出现的 `#` 是否处于**词首**。
//
// ★ bash 的规则：`#` 必须在词首才是注释起始 ⇒ `a#b` 是字面量。
// ⇒ 词边界 = 空白 ＋ 分隔符（`;&|()<>`）。
func isCommentWordStart(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\v', '\f', ';', '&', '|', '(', ')', '<', '>':
		return true
	}
	return false
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
//
//	而 regexp 的 FindAllStringIndex 给的是整个匹配（含名字），拿不到结束偏移。
func bareVarMatches(s string) [][2]int {
	var out [][2]int
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		// ★★★★ 2026-10-23（12h 审计轮）：★ **`\$` 不是变量展开 ⇒ 必须跳过。**
		//
		// 实测触发的站点（scripts/deploy-seamless-docker.sh:2240，★ 由 2026-10-22
		// 的「211 nginx X-Real-IP 修补」引入）：
		//
		//	log "nginx llm.itestu.cn TLS: X-Real-IP 已是 \$remote_addr（无需改动，未 reload）"
		//
		// ⇒ `\$(...)` 在 bash 里是【字面 $】⇒ **`$remote_addr` 根本不会被展开**
		// ⇒ ⇒ 「裸 $VAR 紧跟非 ASCII」这件事【不成立】⇒ ⇒ 原扫描器把它判成站点
		// ⇒ ⇒ ⇒ **假阳性** ⇒ ⇒ 登记册被误判成「被误删」⇒ ⇒ 门红得没有道理
		// （★ 真实的危害是有的：bash 在 UTF-8 locale 下把 `v中` 整个吃成变量名
		//   ⇒ `set -u` 直接报「未绑定的变量」⇒ 但**那说的是没转义的 `$`**）。
		//
		// ★ 判据用【反斜杠奇偶】（★ 不是「前一个字节是不是反斜杠」）：
		//   bash 里 `\\` 是【转义后的一个反斜杠】⇒ 它后面的 `$` **仍然会**被展开；
		//   只有【奇数个连续反斜杠】才把 `$` 变成字面量。
		//   ⇒ 实测（bash 3.2.57 与 5.3.9 一致，locale=zh_CN.UTF-8）：
		//     `echo "$v中"`   ⇒ 变量名被吃成 `v中` ⇒ set -u 报「未绑定的变量」⇒ ★ 真实危害
		//     `echo "\$v中"`  ⇒ 字面 `$v中` ⇒ ★ 不是危害（★ 本次修的就是这一类）
		//     `echo "\\$v中"` ⇒ `\\` ⇒ 一个字面反斜杠 ⇒ `$v中` 仍被展开 ⇒ ★ 真实危害
		if i > 0 && s[i-1] == '\\' {
			n := 0
			for k := i - 1; k >= 0 && s[k] == '\\'; k-- {
				n++
			}
			if n%2 == 1 {
				continue // ★ 转义过的 $ ⇒ 字面量 ⇒ 不是展开点
			}
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
//
// ★★ 2026-10-23（R85 轮收尾）：★ 扫描范围从【文件系统遍历】改成
//
//	【git 追踪的文件】（`git ls-files`）⇒ ⇒ **读数只取决于【提交的树】**，
//	不取决于本机工作树里有什么 ⇒ ⇒ **同一个提交在干净 checkout 与脏工作树上读数相同**。
//
// ★★ 为什么要改：★ 旧实现走 `filepath.Walk` ⇒ ⇒ 把 **gitignored 的本机临时目录**
//
//	也扫了 ⇒ 实测 `.audit148/run.sh`（`.gitignore:78` 的 `.audit*/` 命中）
//	有 1 处站点 ⇒ ⇒ **同一个提交在别人机器上绿、在本机红** ⇒ ⇒ 读数不可复现。
//
// ⇒ ⇒ 这是本线纪律「判据读自己的输入时，先核对【读出来的形状】与【写进去的形状】一致」
//
//	的一次实例：★ 判据的输入集合必须是**确定的**。
func scanRepoShellScripts(root string) (map[string]int, map[string][]cjkVarSite, error) {
	counts := map[string]int{}
	details := map[string][]cjkVarSite{}

	top, err := repoTopLevel(root)
	if err != nil {
		return nil, nil, fmt.Errorf("解析仓根失败: %w", err)
	}
	rels, err := trackedShellScripts(top)
	if err != nil {
		return nil, nil, fmt.Errorf("取 git 追踪的 shell 脚本失败: %w", err)
	}
	for _, rel := range rels {
		data, err := os.ReadFile(filepath.Join(top, rel))
		if err != nil {
			continue // 读不到就当没有（不因权限问题让门红）
		}
		sites := scanCJKVarInText(string(data))
		if len(sites) == 0 {
			continue
		}
		counts[rel] = len(sites)
		details[rel] = sites
	}
	return counts, details, nil
}

// repoTopLevel 返回 git 仓根（绝对路径）。
func repoTopLevel(root string) (string, error) {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// trackedShellScripts 返回 git 追踪的 `.sh` / `.bash` 文件（相对仓根，已排序）。
func trackedShellScripts(top string) ([]string, error) {
	out, err := exec.Command("git", "-C", top, "ls-files", "-z").Output()
	if err != nil {
		return nil, err
	}
	var rels []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		if strings.HasSuffix(p, ".sh") || strings.HasSuffix(p, ".bash") {
			rels = append(rels, p)
		}
	}
	sort.Strings(rels)
	return rels, nil
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
//
//	⇒ 用 `-cjk-var-update` 免得 init 阶段直接 panic（同包旗标重复定义）。
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
		// ★★★ 2026-10-22 12h 审计轮：**「登记册为空」不再等于「没有门」**。
		//
		// 背景：本轮把全仓存量的 **52 处**站点全部修掉（裸 `$VAR` ⇒ `${VAR}`）
		//   ⇒ 登记册被清空 ⇒ 而旧判据是「登记册为空 ⇒ 门恒绿 ⇒ 直接红」。
		//   ⇒ 那条判据当初是**对的**（那时登记册就是事实源，清空＝事实源丢失），
		//   但在「存量归零」之后它变成了**错误的阻碍**。
		//
		// ⇒ 所以现在必须把两种含义**区分开**：
		//   · 登记册空 **且** 全仓零站点 ⇒ 零容忍生效 ⇒ 绿（任何新增站点都会红）；
		//   · 登记册空 **但** 全仓仍扫到站点 ⇒ 登记册被误删 ⇒ 红（事实源丢了）。
		if len(counts) == 0 {
			t.Logf("✔ 登记册已清空且全仓零站点 ⇒ 零容忍生效（任何新增站点都会红）")
			return
		}
		t.Fatalf("登记册是空的，但全仓仍扫到 %d 个文件 / %d 处站点 ⇒ 登记册被误删 ⇒ 门不能退化成「什么都不查就绿」\n"+
			"★ 修法：go test ./scripts/ursmcheck/ -run TestCJKVarExpansionNoNewSites -cjk-var-update",
			len(counts), totalSites(counts))
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
//
//	  用合成输入断言：
//		① 裸 `$VAR` 紧跟汉字 ⇒ 命中；
//		② `${VAR}` 紧跟汉字 ⇒ **不**命中（花括号终止变量名）；
//		③ 整行注释里的 `$VAR中` ⇒ **不**命中（注释不执行）；
//		③' **尾随注释**里的 `$VAR中` ⇒ **不**命中（★ R85 轮收尾补的用例：
//		     实测触发的假阳性 `run_gate_patch() {   # … （用 $MUT_ENGINE）`）；
//		③'' `#` **不在词首** ⇒ 不是注释 ⇒ 其后的 `$VAR中` **仍**命中
//		     （bash 规则：`a#b` 是字面量）；
//		④ 单引号里的 `$VAR中` ⇒ **不**命中（单引号不展开）；
//		⑤ ASCII 紧跟（`$VAR)`）⇒ **不**命中。
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
		// ★★★★ 2026-10-23（12h 审计轮）：★ 转义过的 `$` **不是展开点** ⇒ 不是站点。
		//   ★ 这不是「放宽判据」，而是把判据对齐到 bash 的真实语义：
		//   `\$` ⇒ 字面 $（不展开）⇒ 「紧跟非 ASCII」不成立 ⇒ 不该报。
		{"转义美元不展开", `echo "值=\$v中"`, 0},
		// ★★ 偶数个反斜杠 ⇒ `\\` 是一个字面反斜杠 ⇒ 它后面的 `$` **仍然会**被展开
		//   ⇒ ★ 仍是真站点（★ 判据用奇偶，不是「前一个字节是不是反斜杠」）。
		{"偶数反斜杠仍展开", `echo "值=\\$v中"`, 1},
		// ★★★★ 2026-10-23（R85 轮收尾）：★★ 尾随注释 ⇒ 旧口径是**假阳性**。
		//   ★ 实测：`set -uo pipefail; echo "ok"  # （用 $UNDEFINED_VAR）` ⇒ 退出码 0
		//   ⇒ ⇒ bash 不展开注释 ⇒ ⇒ 不是站点。
		{"尾随注释", `echo "ok"  # （用 $v中）`, 0},
		{"尾随注释在函数定义后", `run_gate() {  # 说明（用 $MUT_ENGINE）`, 0},
		// ★★ 双引号里的 `#` **不是**注释起始 ⇒ 不能在那里截断
		{"双引号内的井号", `echo "a # b"  # 注释 $v中`, 0},
		// ★★ `#` 不在词首 ⇒ bash 当字面量 ⇒ 其后的站点**仍然**是站点
		{"井号非词首不是注释", `echo "x"#y $v中`, 1},
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
