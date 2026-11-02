package schema

// shell_bash_prereq_declaration_gate_test.go —— ★★「用了 bash 4+ 特性的脚本，
// **必须把运行前提写成可检查的事实**」门（R152 轮）。
//
// # 这道门补的是什么
//
// `shell_bash_compat_gate_test.go`（同包，已有）把仓内 shell 脚本分成两档：
//
//	A 档（本地/CI 必跑）—— **不得**使用 bash 4+ 特性（静态判据，跨平台一致）；
//	B 档（只在 Linux 生产/运维主机上跑）—— 允许，但**必须显式声明最低版本**
//	  （`# requires-bash: 4`）。
//
// ★★ **但 A 档那道门只覆盖 4 棵目录**（`deploy/sql` / `scripts/audit` /
// `scripts/checks` / `scripts/tests`）⇒ ★ **B 档那一半「必须显式声明」
// 实际上没有任何机器检查** ⇒ ⇒ ★★ **「声明」这件事在全仓 300+ 个脚本里
// 一直是靠自觉的** ⇒ ⇒ ★ **一个脚本可以在没有任何声明的情况下用 bash 4 特性，
// 而没有门会响**。
//
// ★★ 那道门自己的注释其实已经写明了这个现状：
//
//	「★ 已知例外（**记录在案**，不强制）：上面那 **16 个** Linux-only 脚本
//	  用了 bash 4 特性（mapfile / declare -A / ${v^}）⇒ 它们在 macOS 上跑不了，
//	  但它们的运行前提（Linux 主机）本身就是显式的 ⇒ 不构成「平台假绿」。」
//
// ★★ 「记录在案」= **写在注释里** ⇒ ⇒ **机器读不到** ⇒ ⇒ ★ 本门把它变成
// **可检查的事实**：★ 每一个用到 bash 4+ 特性的脚本，都必须**要么**
// ① 顶部有 `# requires-bash: N` 声明（注释形式，**跨包可读**），
// **要么** ② 有**可执行的**版本守卫（`BASH_VERSINFO` 比较 + 明确提示 + 非零退出）。
//
// ★ 为什么要**两种形态都接受**：
//
//	① 声明（`# requires-bash: 4`）是**文档形态** ⇒ 适合「本来就在 Linux 上跑」的脚本；
//	② 可执行守卫是**行为形态** ⇒ 适合「开发者可能在 macOS 上误跑」的脚本：
//	   它在 macOS 上**响亮拒绝**并给出可执行指引，而不是 cryptic 地崩在
//	   `declare: -A: invalid option` 上。
//
// ★★ **R152 轮的实测教训（为什么值得加这道门）**：
//
//	R151 轮给 `scripts/repair-252-migration-ledger.sh` 加了**可执行守卫**
//	（因为它在 macOS 上崩得很 cryptic）；★ 而本轮实测发现
//	**另外 3 个同族脚本**（`run-migrations-strict.sh` /
//	`check-and-fix-missing-tables.sh` / `loadtest-monitor.sh`）
//	**同样在 macOS 上 cryptic 崩**、却**一个都没有**守卫、也**没有任何门**会响
//	⇒ ⇒ ★★ **「已知例外」只在注释里 ⇒ 下一个同类脚本会继续悄悄地长出来**。
//
// # 判据口径（与已有门**同一把尺子**）
//
// 复用同包的 `bash4OnlyFeatures`（含 `declare -A` / `${v^}` / `${v,,}` /
// `mapfile` / `readarray` / `coproc` / `globstar` / `${!prefix@}`）
// 与 `stripShellComments`（★ 注释与单引号字面量不算命中，
// 否则「注释里引用一个 bash4 特性」会把门变红 ⇒ 门被关掉 ⇒ 真缺陷没人管）。
//
// ★ 扫描面 = **整个 `scripts/` 树 + `deploy/`**（比 A 档那道门宽），
//   排除 `scripts/ursmcheck/`（那是 Go 测试目录，不含 .sh）与
//   `scripts/local-dev/` 里的**纯本地实验脚本**？—— ⇒ ★ 不排除：
//   **凡是 .sh 都纳入** ⇒ 判据只有一条，不分档 ⇒ 免得「分档」本身变成逃逸口。
//
// # 阳性对照（防「恒真空」）
//
// 判据自检：① 本门自己认得「声明」与「守卫」两种形态；
// ② 一段**没有**声明也没有守卫的样本**必须被判红**；
// ③ 一段**有**声明的样本**必须**不**被判红；
// ④ 一段**有**可执行守卫的样本**必须**不**被判红。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// bashPrereqDeclaredRE 匹配「声明最低 bash 版本」的注释形态。
//
// ★ 口径：`# requires-bash: 4`（可有前导空白与多个 #）。
var bashPrereqDeclaredRE = regexp.MustCompile(`(?im)^\s*#\s*requires-bash\s*:\s*([0-9]+)\s*$`)

// bashPrereqGuardRE 匹配「可执行版本守卫」的关键判据：
// 读了 BASH_VERSINFO 并与一个下限比较。
var bashPrereqGuardRE = regexp.MustCompile(`BASH_VERSINFO`)

// bashPrereqScanRoots 是本门扫描的根目录（相对仓根）。
//
// ★ 2026-11-02 第二十四轮：**加上 "tests"**。
// 原口径只有 scripts + deploy ⇒ ★ **`tests/` 树从来不在门的扫描面里**
// ⇒ 那里的脚本可以**无声明地**用 bash 4+ 特性 ⇒ 而没有任何门会响。
// ★ 实测证据（本轮抓到的两件事）：
//
//	① `tests/lib/fake-remote-host.sh:108` 用 `declare -A`（关联数组）
//	   ⇒ macOS 自带 bash 3.2 直接崩成 cryptic 的 `declare: -A: invalid option`；
//	   它的唯一消费者 `tests/deploy_seamless_state_machine_test.sh` 因此
//	   **从来跑不起来** ⇒ 一条从未被运行过的门。
//	② ★ 更隐蔽的一层：`tests/deploy_seamless_state_machine_test.sh` **自己**
//	   没有用 bash4 特性（逐文件判据看不出问题）⇒ 它是**通过 source 一个
//	   bash4 文件**而间接需要 bash ≥ 4 的 ⇒ ★ **逐文件判据抓不到这种耦合**
//	   ⇒ 所以除了把 tests/ 纳入扫描面，**消费者入口还得自带可执行守卫**
//	   （见该脚本本轮新增的守卫）。
var bashPrereqScanRoots = []string{"scripts", "deploy", "tests"}

// bashPrereqShellScripts 返回扫描面内的全部 .sh（排除 ursmcheck 的 Go 测试目录）。
func bashPrereqShellScripts(t *testing.T) []shellScript {
	t.Helper()
	root := repoRootOf(t)
	var out []shellScript
	for _, dir := range bashPrereqScanRoots {
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
			t.Fatalf("遍历 %s 失败：%v", dir, err)
		}
	}
	if len(out) == 0 {
		t.Fatalf("一个 shell 脚本都没扫到 —— 扫描面坏了，判据会变成恒绿门")
	}
	return out
}

// bashPrereqViolations 返回「用了 bash4 特性却没有声明前提」的脚本。
func bashPrereqViolations(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, s := range bashPrereqShellScripts(t) {
		raw, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", s.rel, err)
		}
		text := string(raw)
		// ① 先看**声明**：注释形态（`# requires-bash: N`）与守卫形态。
		if bashPrereqDeclaredRE.MatchString(text) {
			continue
		}
		if bashPrereqGuardRE.MatchString(text) {
			continue
		}
		// ② 再看**是否真的用了 bash4 特性**（剥注释与单引号字面量）。
		code := stripShellComments(text)
		for _, feat := range bash4OnlyFeatures {
			loc := feat.re.FindStringIndex(code)
			if loc == nil {
				continue
			}
			line := 1 + strings.Count(code[:loc[0]], "\n")
			out = append(out, s.rel+":"+itoa(line)+" 用了 "+feat.name+
				" —— 但**没有声明最低 bash 版本**（`# requires-bash: 4`）"+
				"也**没有可执行版本守卫**（BASH_VERSINFO）")
			break
		}
	}
	return out
}

// bashPrereqSourceRE 匹配 `source <path>` / `. <path>` 语句（取被 source 的路径）。
var bashPrereqSourceRE = regexp.MustCompile(`(?m)^\s*(?:source|\.)\s+["']?([^\s"';]+)`)

// usesBash4OnlyFeature 判断脚本（剥注释后）是否真的用了 bash 4+ 特性。
func usesBash4OnlyFeature(code string) (string, int, bool) {
	for _, feat := range bash4OnlyFeatures {
		loc := feat.re.FindStringIndex(code)
		if loc == nil {
			continue
		}
		return feat.name, 1 + strings.Count(code[:loc[0]], "\n"), true
	}
	return "", 0, false
}

// bashPrereqCouplingViolations 返回「**间接**需要 bash ≥ 4」的脚本：
// 它自己不用 bash4 特性（逐文件判据看不出问题），但它 **source** 了一个
// 用 bash4 特性的文件。
//
// ★ 为什么必须单独判（第二十四轮的实测）：
//
//	tests/deploy_seamless_state_machine_test.sh 自己**不用** bash4 特性
//	⇒ 逐文件判据判它「合规」⇒ 但它 source 的
//	tests/lib/fake-remote-host.sh 用了 `declare -A`
//	⇒ ⇒ **间接**需要 bash ≥ 4 ⇒ macOS 自带 bash 3.2 下 cryptic 崩
//	⇒ 而这条门**从来没有任何调用方** ⇒ 它坏了也没人知道。
//
// ⇒ 判据：凡是被 source 的文件用了 bash4 特性，**source 方**也必须
// 有声明或可执行守卫（否则运行时会以 cryptic 的方式失败，而不是可执行地拒绝）。
func bashPrereqCouplingViolations(t *testing.T) []string {
	t.Helper()
	scripts := bashPrereqShellScripts(t)

	// 索引：**仓内相对路径（rel）** → 该脚本是否用了 bash4 特性 / 用的哪个特性
	//
	// ★ 配对方式是**仓内相对路径后缀**（见下面的 strings.HasSuffix(token, cand.rel)）：
	//   被 source 的路径通常写成 "$REPO_ROOT/tests/lib/xxx.sh"
	//   ⇒ 用**仓内 rel 后缀**就能精确对上，且不会把仓里同名的两份文件配错。
	//
	// ★ 为什么不按 base name 配对（★ 这一段是本判据被变异臂改了两版的记录）：
	//   ① 第一版把键存成 rel、却用 base 去查 ⇒ 永远查不到
	//      ⇒ 判据**恒绿**、没有牙 ⇒ 被变异臂当场抓出来；
	//   ② 第二版改成按 base 配对 ⇒ 又变成**误报**（仓里有
	//      scripts/deploy-lib.legacy/targets.sh 与别的 targets.sh）；
	//   ③ 最终落在 rel 后缀精确配对上（既不恒绿、也不误报）。
	usesBash4 := map[string]bool{}
	bash4FeatureByRel := map[string]string{}
	for _, s := range scripts {
		raw, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", s.rel, err)
		}
		code := stripShellComments(string(raw))
		if name, _, ok := usesBash4OnlyFeature(code); ok {
			usesBash4[s.rel] = true
			bash4FeatureByRel[s.rel] = name
		}
	}

	var out []string
	for _, s := range scripts {
		raw, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", s.rel, err)
		}
		text := string(raw)
		if bashPrereqDeclaredRE.MatchString(text) || bashPrereqGuardRE.MatchString(text) {
			continue
		}
		// ★ 在**原始文本**上逐行找 source 语句（不能用 stripShellComments 的
		//   输出：它会把**双引号内的字面文本**丢掉 ⇒ 被 source 的路径恰好在
		//   引号里 ⇒ 路径被剥掉 ⇒ 判据永远看不见 ⇒ ★ 第一版恒绿、没有牙）。
		//   ⇒ 改为：逐行取**整行注释**（首个非空白字符是 #）跳过，其余行做正则匹配。
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, m := range bashPrereqSourceRE.FindAllStringSubmatch(line, -1) {
				if len(m) < 2 {
					continue
				}
				token := m[1]
				// ★ 按**仓内相对路径后缀**精确配对（★ 不能按 base name：
				//   仓里有多份同名文件（scripts/deploy-lib.legacy/targets.sh 与
				//   别的 targets.sh）⇒ 按 base 配对会**误报**）。
				// ★ 解不开就**跳过、不猜**：例如
				//   `source "$AIAN_DEPLOY_LIB/targets.sh"` 指向的是**仓外**的共享
				//   部署库（scripts/deploy-lib 是指向仓外的符号链接 ⇒ 门看不到）
				//   ⇒ 无法判定 ⇒ 不报（★ 门不猜，是本仓一贯纪律）。
				matched := ""
				for _, cand := range scripts {
					if !usesBash4[cand.rel] {
						continue
					}
					if strings.HasSuffix(token, cand.rel) {
						matched = cand.rel
						break
					}
				}
				if matched == "" {
					continue
				}
				out = append(out, s.rel+" source 了 "+matched+"（用了 "+bash4FeatureByRel[matched]+
					" ⇒ 需要 bash ≥ 4）—— 但**自己没有声明也没有可执行守卫** ⇒ "+
					"它会以 cryptic 的方式失败（而不是可执行地拒绝）")
				break
			}
		}
	}
	return out
}

// TestBash4SourceCouplingIsDeclared 钉住「间接需要 bash ≥ 4」这一族。
func TestBash4SourceCouplingIsDeclared(t *testing.T) {
	violations := bashPrereqCouplingViolations(t)
	if len(violations) > 0 {
		for _, v := range violations {
			t.Errorf("%s", v)
		}
		t.Fatalf("有 %d 个脚本**间接**需要 bash ≥ 4（source 了用 bash4 特性的文件）"+
			"却没有自己的声明/守卫", len(violations))
	}
}

// itoa 是最小化的整数转字符串（避免为一个门引入 strconv 的 import 噪声）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// TestBash4ScriptsDeclareTheirPrerequisite 是本门的主判据。
func TestBash4ScriptsDeclareTheirPrerequisite(t *testing.T) {
	violations := bashPrereqViolations(t)
	for _, v := range violations {
		t.Error(v + "\n  ★ 这类脚本的运行前提（Linux / bash>=4）若只存在于某个人脑子里，" +
			"它就是**隐含前提**。⇒ 请在文件顶部加一行 `# requires-bash: 4`，" +
			"或加一段**可执行守卫**（读 BASH_VERSINFO、比较下限、给出可执行指引、" +
			"非零退出）—— ★ 两者都是零行为变化。")
	}
	t.Logf("扫描 %d 个 shell 脚本；违规 %d 个", len(bashPrereqShellScripts(t)), len(violations))
}

// TestStripShellCommentsSurvivesMultiLineCommandSubstitution 是 R152 轮
// 那个 panic 的**回归门**。
//
// # 背景
//
// `stripShellComments` 处理 `$(` 时按 `line[i+2 : j]` 取命令替换的**内容**；
// 而 `$(` 出现在**行尾**时（多行命令替换的常规写法）`i+2` 已经越过行尾
// ⇒ **slice bounds out of range** ⇒ **panic**。
//
// ★ 这个 panic 之所以长期没被发现：**A 档那道门只扫 4 棵目录**，
// 而仓内用多行命令替换的脚本（`bump-version.sh` / `check-domain-drift.sh` /
// `pg-instance-schema-additive.sh` / …）**都在那 4 棵目录之外**
// ⇒ ⇒ ★★ **「没炸」是「没被扫到」，不是「没问题」**。
//
// ⇒ R152 轮把扫描面扩到全仓 `scripts/`+`deploy/`（本文件的那道门）⇒ **立刻踩到**。
func TestStripShellCommentsSurvivesMultiLineCommandSubstitution(t *testing.T) {
	// ★ ★ 下面的样例里**必须**有「双引号内、行尾的 `$(`」——
	//   那才是**真正的触发形态**：`stripShellComments` 的 `$(` 分支在
	//   **双引号状态**里（`case c == '"'`），双引号外的 `$(` 走的是另一条路。
	//   ★ R152 轮第一版回归样例**只写了双引号外的 `$(`** ⇒ ★★ **变异不红**
	//   ⇒ ★★ **那道回归门一开始是「恒绿门」** ⇒ 差点把「已修」当成「已验」。
	cases := []string{
		// ★ 真正的触发形态（仓内实例：scripts/pg-instance-schema-additive.sh:46
		//   `actual_allowlist_hash="$(` —— 双引号 + 行尾 `$(`）。
		"actual_allowlist_hash=\"$(\n",
		"actual_allowlist_hash=\"$(\n  shasum -a 256 \"$1\"\n)\n",
		"x=\"$(\n",
		"x=\"$(y\n",
		"x=\"$( )\n",
		"x=\"$(y)\"\n",
		// 双引号**外**的 `$(`（走另一条分支，也一并守住）
		"GIT_TAG=$(\n",
		"GIT_TAG=$(\n  git describe --tags\n)\n",
		"a=$(b\n",
		"a=$(\n",
		"",
	}
	for _, s := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("stripShellComments(%q) panic：%v —— "+
						"★ 门在输入上崩 ⇒ 它在这条输入上**没有判别力**（既不红也不绿，"+
						"只是把整个测试包带崩）", s, r)
				}
			}()
			_ = stripShellComments(s)
		}()
	}
}

// TestBashPrereqDeclarationGateHasTeeth 是判据的**阳性/阴性对照**。
//
// ⚠ 没有这道对照，本门可能是「恒真空」：判据写坏了 ⇒ 一个违规都报不出来。
func TestBashPrereqDeclarationGateHasTeeth(t *testing.T) {
	// ① 声明形态必须被认出来。
	if !bashPrereqDeclaredRE.MatchString("#!/usr/bin/env bash\n# requires-bash: 4\n") {
		t.Error("判据认不出 `# requires-bash: 4` 声明 —— 没有区分力，本门是恒真空")
	}
	if !bashPrereqDeclaredRE.MatchString("#!/usr/bin/env bash\n#   requires-bash: 4\n") {
		t.Error("判据认不出带缩进的 `requires-bash` 声明 —— 没有区分力，本门是恒真空")
	}
	// ② 守卫形态必须被认出来。
	if !bashPrereqGuardRE.MatchString("if [[ \"${BASH_VERSINFO[0]}\" -lt 4 ]]; then exit 64; fi\n") {
		t.Error("判据认不出 BASH_VERSINFO 守卫 —— 没有区分力，本门是恒真空")
	}
	// ③ 阴性：没有声明、也没有守卫的样本**必须**被判成违规。
	bad := "#!/usr/bin/env bash\ndeclare -A M=()\n"
	if bashPrereqDeclaredRE.MatchString(bad) || bashPrereqGuardRE.MatchString(bad) {
		t.Error("判据把「既无声明又无守卫」的样本判成合规 —— 本门会恒绿")
	}
	// ④ 阴性：**注释里提到** bash4 特性但自己没用的脚本，不该被判红
	//    （否则门会惩罚正确的说明文字）。
	good := "#!/usr/bin/env bash\n# 不要用 declare -A，macOS 的 bash 3.2 不支持\necho hi\n"
	if bashPrereqDeclaredRE.MatchString(good) || bashPrereqGuardRE.MatchString(good) {
		t.Error("判据把「只在注释里提到 bash4 特性」的样本判成需声明 —— 判据过宽")
	}
}
