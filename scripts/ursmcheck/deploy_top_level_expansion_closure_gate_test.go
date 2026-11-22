package ursm_test

// deploy_top_level_expansion_closure_gate_test.go —— 门：部署脚本的**顶层
// 参数展开**必须全部闭合（`${VAR:-…}` 的右花括号成对）。
//
// 被测对象：scripts/deploy-seamless-docker.sh
//
// ── 为什么这道门存在（2026-11-23 211 部署中断实测）──────────────────────
//
//	症状：部署脚本一启动就炸，错误信息**完全指错方向**：
//	    scripts/deploy-seamless-docker.sh: command substitution: line 1094:
//	      syntax error near unexpected token `|'
//	    scripts/deploy-seamless-docker.sh: line 1094: cd:: command not found
//	    scripts/deploy-seamless-docker.sh: line 1: lock_release_local: command not found
//	  ⇒ 报错行号（1094）与真实病灶（107）**差了 987 行**。
//
//	真因：第 107 行的顶层参数展开**少了一个右花括号**：
//	    错：DD_UPSTREAM_PORT_HELPER="${DD_UPSTREAM_PORT_HELPER:-$(cd …)/ops/x.py"
//	    对：DD_UPSTREAM_PORT_HELPER="${DD_UPSTREAM_PORT_HELPER:-$(cd …)/ops/x.py}"
//	  ⇒ `${…:-…}` 没闭合 ⇒ bash 把**后面所有内容**（包括 1094 行注释里的
//	     反引号）吸入同一个命令替换 ⇒ 在注释里的 `` `|` `` 处炸开。
//
//	★ 为什么 `bash -n` 查不出来：**语法是合法的**——少一个 `}` 之后，
//	  外层双引号仍然配对、整个文件仍能解析通过；只是**运行时的语义**变了
//	  （参数展开吞掉了后文）。⇒ 这类缺陷**只有真跑才能发现**。
//
//	★ 为什么不能只靠「跑一次 --help」：那要求有人在每次改脚本后都记得跑，
//	  而这条门要把它变成**自动**的回归闸。
//
// ── 判据 ────────────────────────────────────────────────────────────────
//
//	① 【行为判据】真跑 `bash -n`（解析层）与真跑脚本 `--help`（运行层）
//	   ⇒ 两者都不得出现 "syntax error" / "command substitution" 类症状。
//	   ★ 运行层是这道门的核心：解析层对「少一个 `}`」是**恒绿**的。
//	② 【对照臂】故意把脚本里的一个顶层 `${…:-…}` 删掉右花括号 ⇒ 必须失败
//	   ⇒ ⇒ 证明这道判据能分辨好坏，不是恒绿。
//	③ 【静态判据】顶层（非注释）行里 `${NAME:-…}` 这种**带默认值**的展开必须
//	   右花括号成对——只盯这一形态（病灶形态），且跳过单引号内的字面量。
import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const deployScriptRel = "../deploy-seamless-docker.sh"

// bashPath 找可用的 bash（优先 bash5，退回系统 bash）。
func bashPath(t *testing.T) string {
	t.Helper()
	for _, p := range []string{
		filepath.Join(os.Getenv("HOME"), "sdk/bash5/bin/bash"),
		"/bin/bash",
		"/usr/bin/bash",
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	t.Skip("no bash found")
	return ""
}

func readTopLevelExpansionScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(deployScriptRel)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", deployScriptRel, err)
	}
	return string(b)
}

// ── 判据 ①：真跑（解析层 + 运行层）都不得出现命令替换/语法错误症状 ──────
func TestDeployScriptTopLevelExpansionClosed(t *testing.T) {
	bash := bashPath(t)

	// 运行层：真跑 --help（脚本顶层参数展开就在这几行求值）。
	out, err := exec.Command(bash, deployScriptRel, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("跑 %s --help 失败: %v\n%s", deployScriptRel, err, out)
	}
	text := string(out)
	for _, sym := range []string{
		"command substitution",
		"syntax error",
		"unbound variable",
		"command not found",
	} {
		if strings.Contains(text, sym) {
			t.Errorf("部署脚本顶层求值出现 %q 症状 —— 顶层参数展开很可能没闭合\n%s\n"+
				"  ★ 这类缺陷 `bash -n` 查不出（语法合法，只是运行语义变了）",
				sym, text)
		}
	}

	// 解析层：`bash -n` 也要干净（作为另一类缺陷的兜底）。
	var stderr strings.Builder
	cmd := exec.Command(bash, "-n", deployScriptRel)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Errorf("bash -n 失败: %v\n%s", err, stderr.String())
	}
}

// ── 判据 ②：对照臂——故意删掉一个右花括号 ⇒ 必须失败 ────────────────────
func TestDeployScriptTopLevelExpansionClosureGuardIsNotAlwaysGreen(t *testing.T) {
	bash := bashPath(t)
	orig := readTopLevelExpansionScript(t)

	// 找一行顶层的 ${…:-…} 展开（用 DD_UPSTREAM_PORT_HELPER 作锚），
	// 删掉它的最后一个右花括号 ⇒ 制造与 211 实测同型的缺陷。
	const anchor = `DD_UPSTREAM_PORT_HELPER="${DD_UPSTREAM_PORT_HELPER:-`
	idx := strings.Index(orig, anchor)
	if idx < 0 {
		t.Skip("脚本里没有 DD_UPSTREAM_PORT_HELPER 锚点（形态已变），跳过对照臂")
	}
	end := strings.Index(orig[idx:], `}"`)
	if end < 0 {
		t.Skip("锚点形态已变，跳过对照臂")
	}
	broken := orig[:idx] + orig[idx:idx+end] + orig[idx+end+1:]

	// 对照臂要放在 scripts/ 旁边（它按自身目录找同目录脚本）。
	repoRoot, _ := filepath.Abs("..")
	brokenPath := filepath.Join(repoRoot, ".ursm-broken-deploy.sh")
	if err := os.WriteFile(brokenPath, []byte(broken), 0o755); err != nil {
		t.Fatalf("写对照臂失败: %v", err)
	}
	defer os.Remove(brokenPath)

	out, err := exec.Command(bash, brokenPath, "--help").CombinedOutput()
	if err == nil {
		t.Errorf("对照臂（删掉顶层展开的右花括号）竟然通过了 —— 这道门是恒绿的\n%s", out)
	}
}

// ── 判据 ③：静态——顶层非注释行的 ${NAME:-…} 展开右花括号必须成对 ───────
func TestDeployScriptTopLevelDefaultExpansionBracesBalanced(t *testing.T) {
	src := readTopLevelExpansionScript(t)

	lines := strings.Split(src, "\n")
	bad := 0
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// 只看**带默认值**的展开 `${NAME:-`（病灶形态），且跳过单引号段。
		scan := stripSingleQuoted(ln)
		for pos := 0; pos < len(scan); {
			j := strings.Index(scan[pos:], "${")
			if j < 0 {
				break
			}
			j += pos
			// 确认这是 `${NAME:-` 形态（而不是别的展开）。
			k := j + 2
			for k < len(scan) && isNameChar(scan[k]) {
				k++
			}
			if k+1 >= len(scan) || scan[k] != ':' || scan[k+1] != '-' {
				pos = j + 2
				continue
			}
			// 从 j 起做括号计数，找配平的 `}`。
			depth := 0
			closed := -1
			for x := j; x < len(scan); x++ {
				switch scan[x] {
				case '{':
					depth++
				case '}':
					depth--
					if depth == 0 {
						closed = x
					}
				}
				if closed >= 0 {
					break
				}
			}
			if closed < 0 {
				bad++
				t.Errorf("第 %d 行的顶层参数展开没有闭合：%s", i+1, truncate(ln, 120))
				pos = j + 2
				continue
			}
			pos = closed + 1
		}
	}
	if bad > 0 {
		t.Errorf("共有 %d 处顶层参数展开未闭合", bad)
	}
}

// stripSingleQuoted 把单引号段替换成等长空格（保留列号，避免把字面量当展开）。
func stripSingleQuoted(s string) string {
	b := []byte(s)
	inQuote := false
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '\'':
			inQuote = !inQuote
			b[i] = ' '
		case '\\':
			if inQuote && i+1 < len(b) {
				i++
				b[i] = ' '
			}
		default:
			if inQuote {
				b[i] = ' '
			}
		}
	}
	return string(b)
}

func isNameChar(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
