package schema

// shell_bash_compat_gate_test.go —— 「仓内 shell 脚本不得依赖 bash 4+ 特性」门
//
// # 这道门补的是什么（2026-10-21）
//
// `deploy/sql/sync-objects.sh` 用 `${obj_type^}` 生成 README 的一级标题。
// `${var^}`（首字母大写展开）是 **bash 4.0+** 的特性；
// **macOS 自带的 /bin/bash 是 3.2**（实测 `GNU bash, version 3.2.57`）
// ⇒ 不支持 ⇒ 脚本在 macOS 上以 `bad substitution` 直接退出（实测复现）。
//
// ★★ 这类缺陷的**危害形态**值得单独记：
//
//	Linux（bash 5）上完全正常 ⇒ 若 CI 只跑 Linux，这条路径**恒绿**；
//	缺陷只在 macOS 上现形 ⇒ 典型的「平台依赖的假绿」：
//	**门是绿的，但脚本在开发者机器上是坏的**。
//
// ⇒ 与其指望「CI 平台刚好会抓」，不如把「不得依赖 bash 4+」写成**静态判据**：
//	静态扫描**跨平台一致**（Linux 上也照样能抓）。
//
// # ★ 这条纪律仓内**早就有**（2026-10-21 取证时才发现）
//
//	`scripts/checks/install-entrypoints-test.sh:7` 写着
//	「install.sh 必须能被 macOS 自带的 bash 3.2 解析（不用 mapfile / ${var,,}）」，
//	`scripts/checks/user-scripts-sync-test.sh:20` 同样口径。
//	⇒ 纪律是对的，**覆盖面不全**：只查了 user scripts 与 install 入口，
//	  没查 `scripts/audit/` 与 `deploy/sql/`。
//	⇒ 本门把同一纪律**扩到全域**，并补上「B 档脚本必须显式声明最低版本」。
//
// # 分档：按「这个脚本会在哪台机器上被执行」
//
//	A 档（本地/CI 必跑）—— macOS 的 bash 3.2 不含 bash 4 特性
//	  ⇒ **不得**使用。判据 = deploy/sql/ + scripts/audit/ + scripts/checks/
//	  + scripts/tests/ + scripts/test* / verify-*。
//
//	B 档（只在 Linux 生产/运维主机上跑）—— 允许 bash 4+ 特性，
//	  但**必须显式声明最低 bash 版本**（`# requires-bash: 4`）。
//	  ⇒ 理由：这类脚本的「运行前提」若只存在于某个人的脑子里，它就是
//	  **隐含前提**；显式声明把它变成可检查的事实。
//
// # 判据口径（bash 4.0+ / 4.3+ 才支持的语法）
//
//	· `${v^}` `${v^^}` `${v,}` `${v,,}`  大小写展开（4.0）
//	· `${!prefix@}` `${!prefix*}`        nameref（4.3）
//	· `mapfile` / `readarray`            读数组（4.0）
//	· `declare -A` / `local -A` / `typeset -A`  关联数组（4.0）
//	· `coproc`                          协程（4.0）
//	· `globstar`（`shopt -s globstar`）  `**` 递归（4.0）
//
// ★ 允许的：`${v:offset}` / `${v#pat}` / `${v//x/y}` / 数组下标 `${a[0]}`
//   / 进程替换 `<(...)` / `[[ ]]` / `$'...'` —— 这些 bash 3.2 都有。
//
// # 为什么这条判据本身要能证明有牙
//
// 静态扫描天然有「扫了个寂寞」的风险（正则写错 ⇒ 全绿）。
// ⇒ 判据自带**自检**：每条规则都**内置一个最小样本**（清单与样本不可能
//   不同步），自检证明「每条规则抓得到自己的样本」、
//   「bash 3.2 支持的写法不会被误判」（过宽的扫描器会把正常写法全变成红
//   —— 那等于逼着人绕过门）、以及「已剔除项不得被误判」。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// bash4OnlyFeatures 是「bash 4.0+/4.3+ 才支持」的语法清单。
//
// 每条 = 特征名 + 匹配正则 + **自检样本**。
//
// ★ 样本必须**内置在清单里**（而不是另写一张表）：否则「加了一条特性」
//
//	「忘了加样本」⇒ 自检与清单分叉 ⇒ 自检变成对另一份表的检查。
//	内置后：清单与样本**不可能**不同步。
//
// ★ 正则必须锚在**结构位**上：`${obj_type^}` 是「标识符 + ^ + }」，
//
//	而 `${obj_type}` 不是。
var bash4OnlyFeatures = []struct {
	name string
	re   *regexp.Regexp
	// sample 是该特性的最小可执行样本（用于自检「这条规则抓得到」）。
	sample string
}{
	{
		"大小写展开 ${v^}/${v^^}/${v,}/${v,,}",
		regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(\^\^|\^|,|,,)\}`),
		"echo ${obj_type^}\necho ${v^^}\necho ${v,}\necho ${v,,}\n",
	},
	{
		"nameref ${!prefix@}/${!prefix*}",
		regexp.MustCompile(`\$\{![A-Za-z_][A-Za-z0-9_]*[@*]`),
		"echo ${!prefix@}\necho ${!prefix*}\n",
	},
	{
		"mapfile/readarray",
		regexp.MustCompile(`(^|[^A-Za-z0-9_])(mapfile|readarray)([^A-Za-z0-9_]|$)`),
		"mapfile -t arr < f\nreadarray -t arr < f\n",
	},
	{
		"关联数组 declare -A / local -A / typeset -A",
		regexp.MustCompile(`(^|[^A-Za-z0-9_])(declare|local|typeset)[[:space:]]+-A`),
		"declare -A m\nlocal -A n\ntypeset -A p\n",
	},
	{
		"coproc",
		regexp.MustCompile(`(^|[^A-Za-z0-9_])coproc([^A-Za-z0-9_]|$)`),
		"coproc NAME { cmd; }\n",
	},
	{
		"globstar（shopt -s globstar）",
		regexp.MustCompile(`shopt[[:space:]]+-s[[:space:]]+globstar`),
		"shopt -s globstar\n",
	},
}

// ★ 已剔除的候选（**实测**证明它们**不是** bash 4+ 特性，留在清单里就是过宽）：
//
//	`&>` / `&>>` 重定向 —— 实测 macOS bash 3.2.57 **支持**
//	  （`bash -c 'echo y &>/tmp/x'` ⇒ rc=0、文件写出）⇒ 它是 bash 3.x 就有的。
//	  ⇒ 若把它算进「bash 4+」，本门会去逼几十个脚本加**毫无必要**的
//	     `requires-bash: 4` 声明 ⇒ 过宽的门比没有门更坏（它训练人绕过门）。
//
//	★ 这一条由自检「已剔除项不得被误判」守着，防止有人日后又加回来。

// shellScript 是被扫描的一个脚本。
type shellScript struct {
	rel  string
	path string
}

// localCIRootDirs 是「本地/CI 必跑」脚本的根目录。
//
// ★ 收窄扫描面是**刻意的**：仓内 300+ 个 shell 脚本里，绝大多数是
//
//	**只在 Linux 生产/运维主机上跑**的部署脚本（deploy-lib.legacy/、
//	rollback-154.sh、loadtest-monitor.sh…）⇒ 让它们也满足 bash 3.2
//	是**范围外的无意义工作量**（本轮实测：这类脚本有 16 个用了 bash 4 特性）。
//	⇒ 只扫「会在开发者机器 / CI 上执行」的那几棵树。
//
// ★ 已知例外（**记录在案**，不强制）：上面那 16 个 Linux-only 脚本用了
//
//	bash 4 特性（mapfile / declare -A / ${v^}）⇒ 它们在 macOS 上跑不了，
//	但它们的运行前提（Linux 主机）本身就是显式的 ⇒ 不构成「平台假绿」。
var localCIRootDirs = []string{"deploy/sql", "scripts/audit", "scripts/checks", "scripts/tests"}

// shellScriptFiles 返回仓内需要扫描的 shell 脚本。
func shellScriptFiles(t *testing.T) []shellScript {
	t.Helper()
	root := repoRootOf(t)
	var out []shellScript
	for _, dir := range localCIRootDirs {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".sh") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			out = append(out, shellScript{rel: rel, path: path})
			return nil
		})
		if err != nil {
			t.Fatalf("遍历 %s 失败：%v", base, err)
		}
	}
	if len(out) == 0 {
		t.Fatalf("一个 shell 脚本都没扫到 —— 扫描面坏了，判据会变成恒绿门")
	}
	return out
}

// repoRootOf 解析仓库根。
func repoRootOf(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败：%v", err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓库根 %q 解析不出来：%v", root, err)
	}
	return root
}

// TestShellScriptsAvoidBash4OnlyFeatures 是本门的主判据。
func TestShellScriptsAvoidBash4OnlyFeatures(t *testing.T) {
	for _, s := range shellScriptFiles(t) {
		raw, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", s.path, err)
		}
		// ★ 剥注释**与单引号字面量**再扫：注释里引用一个 bash 4 特性
		//   （例如「不要用 ${v^}」）不该判红；单引号里的 `mapfile` 是
		//   **字面量**（检查规则本身），也不该判红。
		code := stripShellComments(string(raw))
		for _, feat := range bash4OnlyFeatures {
			loc := feat.re.FindStringIndex(code)
			if loc == nil {
				continue
			}
			line := 1 + strings.Count(code[:loc[0]], "\n")
			t.Fatalf("%s:%d 用了 bash 4+ 特性（%s）—— 本脚本属**本地/CI 必跑**档，"+
				"macOS 自带 bash 3.2 不支持，会以 bad substitution / 语法错误直接退出。"+
				"★ Linux 上不会暴露 ⇒ 这类缺陷必须靠这条静态判据拦，而不是指望 CI 平台",
				s.rel, line, feat.name)
		}
	}
}

// TestBash4FeatureScannerSelfCheck 是扫描器自身的自证（判据必须有牙）。
func TestBash4FeatureScannerSelfCheck(t *testing.T) {
	t.Run("每一条规则都抓得到自己的样本", func(t *testing.T) {
		for _, f := range bash4OnlyFeatures {
			if !f.re.MatchString(f.sample) {
				t.Fatalf("规则 %q 抓不到自己的样本 %q —— 扫描器漏了这条 ⇒ 判据形同虚设",
					f.name, f.sample)
			}
		}
	})

	t.Run("bash 3.2 支持的写法不被误判", func(t *testing.T) {
		ok := []string{
			"echo ${obj_type}\n",
			"echo ${v:1}\n",
			"echo ${v#pre}\n",
			"echo ${v//a/b}\n",
			"echo ${arr[0]}\n",
			"diff <(a) <(b)\n",
			"[[ -n \"$x\" ]] && echo yes\n",
			"echo $'line\\n'\n",
			"grep -oE 'x' f | head -1 | sed 's/^[^ ]*[[:space:]]*//'\n",
			"echo \"${a}\" \"${b}\" > out\n",
			"cmd &> out\n",
			"cmd &>> out\n",
			"declare -a arr=()\n",
			"typeset -i n=1\n",
		}
		for _, s := range ok {
			for _, f := range bash4OnlyFeatures {
				if f.re.MatchString(s) {
					t.Fatalf("样本 %q 被 %q 误判 —— 扫描器过宽会把正常写法全变成红", s, f.name)
				}
			}
		}
	})

	t.Run("已剔除项不得被误判", func(t *testing.T) {
		// `&>` / `&>>` 实测是 bash 3.x 就有的（见文件头注释）。
		// 这条断言防止有人日后把它加回清单 ⇒ 让门去过宽。
		for _, s := range []string{"cmd &> out\n", "cmd &>> out\n"} {
			for _, f := range bash4OnlyFeatures {
				if f.re.MatchString(s) {
					t.Fatalf("已剔除的 `&>` 特性被 %q 重新抓到了 —— 清单过宽了", f.name)
				}
			}
		}
	})

	t.Run("单引号字面量里的特性不被误判", func(t *testing.T) {
		// 真实踩坑：checks/install-entrypoints-test.sh **自己禁止 mapfile**，
		// 而它自己写了 `mapfile` 作为 **grep 的正则字面量** ⇒ 不剔除单引号
		// 就会被自己误伤 ⇒ 单引号内容必须整段剔除。
		s := "hits=\"$(comment_free 'mapfile|readarray|\\$\\{[A-Za-z_]+\\^\\^' \"$f\")\"\n" +
			"grep -nE 'mapfile|readarray' \"$p\"\n"
		for _, f := range bash4OnlyFeatures {
			if f.re.MatchString(stripShellComments(s)) {
				t.Fatalf("单引号字面量里的 %q 被误判 —— 剥字面量没生效", f.name)
			}
		}
	})

	t.Run("双引号内转义的 ${} 不被误判", func(t *testing.T) {
		// 真实踩坑：`say "改回 \${obj_type^}"` 里 `${obj_type^}` 是**字面量**
		// （转义的 $），不是代码 ⇒ 不该判红。
		s := "say \"改回 \\${obj_type^}\"\n"
		for _, f := range bash4OnlyFeatures {
			if f.re.MatchString(stripShellComments(s)) {
				t.Fatalf("双引号内转义的 ${} 被 %q 误判", f.name)
			}
		}
	})

	t.Run("引号定界的 heredoc 体不被扫（字面量）", func(t *testing.T) {
		// 真实踩坑：`python3 - ... <<'PYEOF'` 的体是 **Python 源码**，
		// 里面的 ${...} 只是数据 ⇒ 不该被当成 bash 代码扫。
		s := "python3 - <<'PYEOF'\nprint('${obj_type^}')\nPYEOF\n"
		for _, f := range bash4OnlyFeatures {
			if f.re.MatchString(stripShellComments(s)) {
				t.Fatalf("引号定界的 heredoc 体被 %q 扫到了 —— 字面量不该进扫描面", f.name)
			}
		}
	})

	t.Run("heredoc 体里的特性被抓到（剥注释的盲区）", func(t *testing.T) {
		// ★ 真实盲区：README 模板是**未加引号的 heredoc**，体首行
		//   `# ${obj_type^}` 长得像注释，**但它会被 shell 展开**
		//   ⇒ 它正是缺陷本体 ⇒ 剥注释时必须**保留 heredoc 体**。
		s := "cat > f <<EOF\n# ${obj_type^}\nEOF\n"
		found := false
		for _, f := range bash4OnlyFeatures {
			if f.re.MatchString(stripShellComments(s)) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("heredoc 体里的 bash 4 特性没被抓到 —— 剥注释器把 heredoc 体当注释剥掉了 ⇒ 门有盲区")
		}
	})

	t.Run("注释里的特性引用不被误判", func(t *testing.T) {
		s := "# 不要用 ${obj_type^}，那是 bash 4 才有\n# declare -A 也不行\n# mapfile 也不行\necho ok\n"
		for _, f := range bash4OnlyFeatures {
			if f.re.MatchString(stripShellComments(s)) {
				t.Fatalf("注释里的 %q 被误判 —— 剥注释器没生效", f.name)
			}
		}
	})
}

// stripShellComments 去掉 shell 脚本里的注释**与单引号字面量**，
// 但**保留 heredoc 体**（heredoc 里的内容会被 shell 展开 ⇒ 那是可执行面）。
//
// ★ 三层处理，每一层都是**实测踩出来的**：
//
//	① 注释：注释里**引用**一个 bash 4 特性（例如在注释里解释
//	   「不要用 ${v^}」）不该被判红。
//	   ⚠ 漏掉「前一个是换行」这一支，会让**除第一行外**的行首注释
//	   全部留在扫描面上（自检当场抓到了这个坑）。
//
//	② 单引号字面量：单引号里的内容**永远不参与展开** ⇒ 里面的
//	   `mapfile` 是**字面量**，不是调用。
//	   ⚠ 真实踩坑：`scripts/checks/install-entrypoints-test.sh` 里
//	   `hits="$(comment_free 'mapfile|readarray|\$\{...\}' "$ROOT/install.sh")"`
//	   —— 这道门**自己禁止 mapfile**，而它自己写了 `mapfile` 作为
//	   **正则字面量** ⇒ 不剔除单引号就会被自己误伤。
//	   ⇒ 单引号段的内容整段剔除（**保留换行**，行号不能漂）。
//
//	③ ★ heredoc 体**必须区分两种定界**（2026-10-21 变异验证当场暴露的盲区）：
//	   · **未加引号**的 heredoc（`<<EOF`）⇒ 体**会被 shell 展开** ⇒ 体是
//	     **可执行面** ⇒ 体**原样保留**（不剥注释、不剔单引号）。
//	     真实踩坑：`deploy/sql/sync-objects.sh` 的 README 模板是未加引号的
//	     heredoc，体首行就是 `# ${obj_type^}` ——它在文本上长得像注释，
//	     **但它会被 shell 展开**（所以它正是缺陷本体）。
//	     ⇒ 若按注释剥掉 ⇒ 门**恒绿** ⇒ 门没牙。
//	   · **加了引号**的 heredoc（`<<'EOF'` / `<<"EOF"`）⇒ 体**永远是字面量**
//	     ⇒ 体**整段剔除**（只保留换行）。
//	     真实踩坑：审计脚本里 `python3 - ... <<'PYEOF'` 的体是 Python 源码，
//	     里面出现的 `${...}` 只是数据 ⇒ 不该被当成 bash 代码扫。
//
// ★ 双引号里的内容**保留**：双引号内变量会展开 ⇒ `echo "${v^}"` 是真代码。
func stripShellComments(src string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))

	inSingle := false
	inDouble := false
	// heredocDelim 非空 = 当前处于某个 heredoc 体内（体从下一行开始）。
	heredocDelim := ""
	// heredocLiteral 为 true 表示该 heredoc 是**引号定界**（`<<'EOF'`）⇒
	// 体是**字面量** ⇒ 整段剔除（只保留换行）。
	// 为 false 表示**未加引号**（`<<EOF`）⇒ 体**会被展开** ⇒ 原样保留。
	heredocLiteral := false

	for _, line := range lines {
		// ── heredoc 体 ────────────────────────────────────────
		if heredocDelim != "" {
			if heredocLiteral {
				out = append(out, "") // 字面量体：整段剔除（保留行号）
			} else {
				out = append(out, line) // 可执行体：原样保留
			}
			if strings.TrimSpace(line) == heredocDelim {
				heredocDelim = ""
				heredocLiteral = false
			}
			continue
		}

		var b strings.Builder
		for i := 0; i < len(line); i++ {
			c := line[i]
			switch {
			// ── 单引号：内部全是字面量 ──────────────────────────
			case c == '\'' && !inDouble && (i == 0 || line[i-1] != '\\'):
				inSingle = !inSingle
				// 注释里的撇号不算：注释已在下面被丢弃。
			case inSingle:
				// 单引号内的内容不写（闭合引号由上面的 case 处理）。
			// ── 双引号：只有 ${...} 与 $(...) 是可执行面 ─────────
			case c == '"' && !inSingle && (i == 0 || line[i-1] != '\\'):
				inDouble = !inDouble
			case inDouble:
				switch {
				case c == '\\' && i+2 < len(line) && line[i+1] == '$' && line[i+2] == '{':
					// 转义的 ${...}：展开成**字面量** ⇒ 整段剔除。
					depth := 0
					j := i + 2
					for ; j < len(line); j++ {
						if line[j] == '{' {
							depth++
						} else if line[j] == '}' {
							depth--
							if depth == 0 {
								break
							}
						}
					}
					i = j
				case c == '$' && i+1 < len(line) && line[i+1] == '{':
					// 真正的展开 ⇒ **保留**该片段（它是可执行面）。
					depth := 0
					j := i + 1
					for ; j < len(line); j++ {
						if line[j] == '{' {
							depth++
						} else if line[j] == '}' {
							depth--
							if depth == 0 {
								break
							}
						}
					}
					if j >= len(line) {
						j = len(line) - 1 // 未配平（截断的输入）⇒ 夹住
					}
					b.WriteString(line[i : j+1])
					i = j
				case c == '$' && i+1 < len(line) && line[i+1] == '(':
					// 命令替换 ⇒ 其内容是**真代码** ⇒ 递归走一遍状态机。
					//
					// ★ 为什么必须递归而不是「原样保留」（2026-10-21 实测踩坑）：
					//   `hits="$(comment_free 'mapfile|...' "$f")"` 里，命令替换
					//   **内部还有单引号** ⇒ 原样保留 ⇒ 单引号状态不参与状态机
					//   ⇒ 其后的 `mapfile` 字面量被当成代码 ⇒ 误伤。
					depth := 0
					j := i + 1
					for ; j < len(line); j++ {
						if line[j] == '(' {
							depth++
						} else if line[j] == ')' {
							depth--
							if depth == 0 {
								break
							}
						}
					}
					if j >= len(line) {
						j = len(line) - 1 // 未配平（截断的输入）⇒ 夹住
					}
					b.WriteString("$(")
					// ★★ 2026-10-25（R152 轮）补的**下界夹持**：
					//
					//	`$(` 出现在**行尾**时（多行命令替换的常规写法：
					//	`GIT_TAG=$(` / 换行 / `git describe …` / `)`）
					//	⇒ i+2 已经越过行尾 ⇒ `line[i+2 : j]` **越界** ⇒ **panic**。
					//
					//	★ 这个 panic 是**真实存在的**：R152 轮把扫描面从 4 棵目录扩到
					//	全仓 `scripts/`+`deploy/` 时立刻踩到（仓内 9+ 个脚本用多行
					//	命令替换）。★ 它之前没炸，是因为**没有任何一道门扫到那些脚本**
					//	⇒ ⇒ ★★ **「没炸」是「没被扫到」，不是「没问题」**。
					//
					//	⇒ 下面把切片两端都夹住：start ≤ j ≤ len(line)。
					start := i + 2
					if start > len(line) {
						start = len(line)
					}
					if j < start {
						j = start
					}
					if j > len(line) {
						j = len(line)
					}
					b.WriteString(stripShellComments(line[start:j]))
					b.WriteString(")")
					i = j
				}
				// 其它双引号内字符是**字面量** ⇒ 不写。
			// ── 注释 ────────────────────────────────────────────
			case c == '#':
				// 「行首的 #」也算注释：行首 = 串首，或前一个字符是空白。
				if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
					i = len(line) // 注释到行尾
					continue
				}
				b.WriteByte(c)
			// ── heredoc 开始 ────────────────────────────────────
			case c == '<' && i+1 < len(line) && line[i+1] == '<' &&
				(i+2 >= len(line) || line[i+2] != '<'):
				//
				// ★★ 解析定界符时**必须把引号算进去**（2026-10-21 实测踩坑）：
				//   早先版本没算引号 ⇒ 推进量少 1 ⇒ 闭合引号落在下一轮里
				//   **被当成普通单引号**翻转了 inSingle ⇒ 后面**整段**解析全错。
				rest := line[i+2:]
				end := i + 2
				if strings.HasPrefix(rest, "-") {
					rest = rest[1:]
					end++
				}
				quote := byte(0)
				if len(rest) > 0 && (rest[0] == '\'' || rest[0] == '"') {
					quote = rest[0]
					rest = rest[1:]
					end++
				}
				if len(rest) == 0 || !(rest[0] == '_' || rest[0] == '/' ||
					(rest[0] >= 'A' && rest[0] <= 'Z') || (rest[0] >= 'a' && rest[0] <= 'z')) {
					b.WriteString("<<")
					i++
					continue
				}
				j := 0
				for j < len(rest) && rest[j] != ' ' && rest[j] != '\t' && rest[j] != '"' && rest[j] != '\'' {
					j++
				}
				heredocDelim = rest[:j]
				heredocLiteral = quote != 0
				end += j
				if quote != 0 {
					end++ // 跳过闭合引号（它**不是**单引号字符串）
				}
				i = end - 1 // 定界符是**数据** ⇒ 不写进扫描面
				continue
			default:
				b.WriteByte(c)
			}
		}
		out = append(out, b.String())
	}
	return strings.Join(out, "\n")
}
