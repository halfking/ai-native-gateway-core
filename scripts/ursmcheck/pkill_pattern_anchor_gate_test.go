package ursm_test

// 「按模式清场」的**锚点门**（2026-11-23）
//
// ★★★ **这道门钉的是什么（★ 真实事故）**：★★★
//
//　★ 2026-11-23 23:0x，★ **同机两车道并跑同名同命令的闸门** ⇒ ★★ 一车道用
//　★ `pkill -f "deploy-test-gate.sh"` 清场 ⇒ ★★ **按【命令行文本】匹配**
//　★ ⇒ ★★ **另一车道那轮闸门被同一条 pkill 打死**（★ 我方先后两次：★ 一次是
//　★ **SIGQUIT**（★ Go 运行时收信号 ⇒ 打栈退出 ★）、★ 一次是**日志直接断掉** ★）★★。
//
// ★★★ **仓内现状（★ 逐条核实过、★ 不是「到处都有问题」★★）**：★★★
//
//　★ ① `scripts/deploy-test-gate.sh`（★ **闸门脚本自身** ★）⇒ ★ **零 `pkill`**
//　★ 　　⇒ ★★ **⇒ 闸门脚本没有模式清场、★ 不存在这处缺陷** ★★；
//　★ ② 仓内**被执行**的几处 `pkill -f` **都带锚点**（★ `gateway.*18090` ／
//　★ 　　`server-v2.py` ／`ssh.*<port>:<ip>:5432` ★）⇒ ★ **⇒ 已有的都不误伤** ★；
//　★ ③ ★★ **唯一一处宽模式**（★ `pkill -f gateway` ★）在
//　★ 　　`scripts/quick-start-loadtest.sh` 的 **echo 提示文本里** ⇒ ★
//　★ 　　★★ **它是【给人复制粘贴的】⇒ ★ 复制走就会误杀** ★★
//　★ 　　⇒ ★★ **而且它会命中本仓的 Go 编译进程**（★ 编译进程的命令行含
//　★ 　　`github.com/kaixuan/llm-gateway-go/...` ★）★★
//　★ 　　⇒ ★★ **⇒ 本轮已改成「按记录的 PID 停」**（★ 与仓内先例
//　★ 　　`scripts/verify-fpslot-fix.sh` 的 SAFETY 注释同款 ★）★★。
//
// ★★★ **所以这道门的作用是：★ 把【仓库自己写下的规则】变成【机器强制】★★**
//
//　★ 仓内**已经有**这条规则（★ 写在 `verify-fpslot-fix.sh` 的 SAFETY 注释里：
//　★ 「killed by recorded PID, ★ NEVER `pkill -f pattern`」 ★）⇒ ★★
//　★ ★★ 但它只是**散文** ⇒ ★★ **散文挡不住下一个人的 `pkill -f gateway`**
//　★ ★★ ⇒ ★★ **⇒ 这道门把散文变成判据** ★★。
//
// ★★★ **判据（★ 刻意做得很窄、★ 免得误伤）★★★：
//
//　★ **任何 `pkill -f` / `killall` 的模式若是【裸单词】⇒ 判红** ★
//　★ （★ 裸单词＝**不含任何锚点字符**：`· / : _ * = [ ^ $` ★）
//　★ ⇒ ★★ `pkill -f gateway` **判红**（★ 宽 ★）／★
//　★ 　　`pkill -f server-v2.py` **绿**（★ 带锚点 ★）／★
//　★ 　　`pkill -f 'ssh.*1234:10.0.0.1:5432'` **绿**（★ 带锚点 ★）★★。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pkillAnchorRE 匹配 `pkill` / `killall` 后面的**模式参数**。
//
// ★ 取「`-f` 之后的第一个非空白 token」⇒ ★ 兼容带引号与不带引号两种写法 ★。
// ★ ★ 注意：★ Go 的 RE2 **不支持反向引用**（★ `\1` 会 panic ★）⇒ ★★
// ★★ 所以三种引号形态用**三个独立捕获组**分别取 ★★。
var pkillAnchorRE = regexp.MustCompile(`\b(?:pkill|killall)\b[^|;&\n]*?(?:-f|--full)\s+(?:'([^']+)'|"([^"]+)"|([^\s|;&]+))`)

// pkillBareWordRE 匹配「**裸单词**模式」（★ 不含任何锚点字符 ★）。
//
// ★ ★ 锚点字符集合：★ `.` `/` `:` `_` `*` `=` `[` `^` `$` ★
// ★ ★ **注意**：★ 这里必须是 **ASCII 的 `.`**（★ U+002E ★）★ —— ★
// ★ ★ 写成全角 `·`（★ U+00B7 ★）会让 `server-v2.py` 被误判成裸单词（★ 本轮踩过 ★）★★。
var pkillBareWordRE = regexp.MustCompile(`^[^./:_*=\[\^$]+$`)

// pkillGateSkipDirs 是扫描时跳过的目录（★ 第三方与历史归档 ★）。
var pkillGateSkipDirs = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	".git":         true,
}

// TestPkillPatternsMustBeAnchored —— ★★ 承重断言：★★
//
// ★★ **任何 `pkill -f` 的裸单词模式都会判红**（★ 那是「一车清场、另一车被杀」
// ★★ 与「复制粘贴误杀 Go 编译」两类事故的共同入口 ★★）。
func TestPkillPatternsMustBeAnchored(t *testing.T) {
	root := repoRootForPkill(t)
	var bad []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if pkillGateSkipDirs[base] || strings.HasPrefix(base, ".") && base != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".sh") && !strings.HasSuffix(path, ".bash") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(b), "\n") {
			// ★ ★ 跳过**注释行**（★ `#` 开头 ★）：★★
			// ★ ★ 注释里的 `pkill -f pattern` 是【散文】（★ 例如仓内先例
			// ★ ★ `verify-fpslot-fix.sh` 的 SAFETY 注释：★ 「NEVER `pkill -f pattern`」
			// ★ ★ ——★ 它在讲【不要这么写】、★ 不是一条可执行的命令 ★★）★★
			// ★ ★ ⇒ ★★ 判据只管【可执行形态】★ ★（★ 而 echo 里的提示**要管** ★
			// ★ ★ ——★ 那是给人复制粘贴的 ★★）★★。
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, m := range pkillAnchorRE.FindAllStringSubmatch(line, -1) {
				// ★ 三个捕获组分别对应：★ 单引号／双引号／无引号 ★
				pat := ""
				for _, g := range m[1:] {
					if g != "" {
						pat = g
						break
					}
				}
				if pat == "" {
					continue
				}
				if pkillBareWordRE.MatchString(pat) {
					bad = append(bad, fmt.Sprintf("%s:%d: 模式 %q 是【裸单词】⇒ ★ 它会命中任何命令行里含该词的进程"+
						"（★ 含本仓 Go 编译进程：★ 包路径 github.com/kaixuan/llm-gateway-go/… ★）⇒ "+
						"★ 请改成【带锚点】的模式或【按记录的 PID 停】",
						rel, i+1, pat))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描仓内 shell 脚本失败: %v", err)
	}
	for _, b := range bad {
		t.Errorf("★ %s", b)
	}
	if len(bad) == 0 {
		t.Logf("★ 仓内 `pkill -f` / `killall` 的模式全部带锚点（★ 宽模式已在本轮修掉 ★）")
	}
}

// repoRootForPkill 定位仓根（★ 本门在 `scripts/ursmcheck` 下 ★）。
func repoRootForPkill(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("★ 找不到仓根（★ go.mod ★）")
	return ""
}
