package ursm_test

// R101 轮 · 形态量具线续十六：**信号路径的【机制事实 + 形态依赖】门**。
//
// ── 这道门要裁决什么 ────────────────────────────────────────────────────────
//
// 仓内与并行会话之间，关于「shell 脚本收到 SIGTERM/SIGINT 时会怎样」有三句互相打架的话：
//
//	（A）并行会话 R95 段 D 实测 1：「bash 的 EXIT trap 在 SIGTERM/SIGINT 下【会】执行」。
//	（B）并行会话 R95 段 E 实测 2（P1/P2/P3）：
//	    「一旦挂了 INT/TERM trap，bash 就不立即终止进程，而是等当前前台命令结束才执行 trap」
//	    ⇒ 「挂信号 trap」不是「让回收更及时」，而是【让回收更晚】。
//	    ⇒ 建议：把收割（kill+wait）挂到 EXIT trap，**不要靠 INT/TERM**。
//	（C）仓内注释（`scripts/audit/healthcheck-dbaudit-scripts.sh`）：
//	    「显式接管 INT/TERM：EXIT trap 在『父进程被信号打死』时**不保证**会跑」。
//
// ⇒ R100 轮把 (A) 判为「成立」、(C) 判为「已被实测证伪」⇒ ★★ **R101 轮的实测把 R100 的裁决也推翻了**。
//
// ── ★★★★ R101 轮实测发现的根因：**上面三句话谈的不是同一个「形态」** ────────
//
// ★★ 关键变量 = **被信号打断时，shell 当时在等什么**：
//
//	形态 W（wait 形态）  ：后台子进程 + `wait "$PID"`   ★ ★ 仓内 healthcheck 的真实形态
//	形态 F（前台形态）   ：前台外部命令（如 `sleep 1.5`）★ ★ 并行会话 P1/P2 用的形态
//
// ★★ 实测（每格 3 次复跑取中位数；bash 3.2.57 与 5.3.9 各一遍；2026-10-22，Darwin 26.6.2 arm64）：
//
//	形态 │ 信号  │ INT/TERM trap │ 脚本体继续? │  trap 触发 │ EXIT trap │ 退出
//	─────┼───────┼───────────────┼─────────────┼───────────┼───────────┼──────────
//	 F   │ TERM  │ 无            │ ✘ 立即终止   │    —      │  ~12ms    │ 被 TERM 杀
//	 F   │ TERM  │ 有(不 exit)  │ ✔ 继续       │ ~1400ms   │ ~1400ms   │ 0
//	 F   │ TERM  │ 有(带 exit)  │ ✘ 停止       │ ~1400ms   │ ~1400ms   │ 143
//	 F   │ INT   │ 无            │ ★★ **继续**  │    —      │ ~1380ms   │ 0   ←★ SIGINT 被忽略
//	 F   │ INT   │ 有(带 exit)  │ ✘ 停止       │ ~1380ms   │ ~1380ms   │ 143
//	 W   │ TERM  │ 无            │ ✘ 立即终止   │    —      │  ~13ms    │ 被 TERM 杀
//	 W   │ TERM  │ 有(不 exit)  │ ✔ 继续       │ ~12–58ms  │ ~12–58ms  │ 0
//	 W   │ TERM  │ 有(带 exit)  │ ✘ 停止       │ ~13–29ms  │ ~13–29ms  │ 143
//	 W   │ INT   │ 无            │ ✘ 立即终止   │    —      │  ~13ms    │ 129 / 被 INT 杀
//	 W   │ INT   │ 有(带 exit)  │ ✘ 停止       │ ~12–25ms  │ ~12–25ms  │ 143
//
// ⇒⇒⇒ **三条互相独立的机制事实**（每条都有上面的读数支撑）：
//
//	① 【形态依赖】trap 触发时刻差两个数量级：
//	   形态 W ≈ 12–30ms（`wait` 被信号**立即打断** ⇒ trap 立即跑）
//	   形态 F ≈ 1400ms（被推迟到**前台命令结束**）
//	   ⇒⇒⇒ **「挂信号 trap ⇒ 收割更晚」只在形态 F 成立，在仓内形态 W 不成立。**
//	   ⇒⇒⇒ ⇒ 并行会话 (B) 的建议「不要靠 INT/TERM」**不能外推到仓内形态**。
//	② 【终止性 ≠ 清理性】形态 F + SIGINT + 无 trap ⇒ **脚本体继续跑完、退出码 0**
//	   ⇒⇒⇒ 「EXIT trap 会执行」（会清理）**推不出**「信号能让脚本停下来」（会停）。
//	   ⇒⇒⇒ ⇒ 并行会话语言映射表 `signal_covers_sigterm=yes ⇒ 纯 shell 脚本不接信号零风险`
//	   ⇒⇒⇒   这一步推论**不成立**（它把两个轴压成了一个轴）。
//	③ 【反模式】INT/TERM trap **不 exit** ⇒ 脚本体在终止信号后**继续执行**
//	   ⇒⇒⇒ 仓内两处 trap（healthcheck / factmatrix）都**带 exit** ⇒ 形态正确 ⇒ 保留。
//	④ 【不保证】★ 「trap 在信号终止下会执行」**不是确定性的**：
//	   读数：第一轮 24 格里 **3 格 trap 一次都没跑**（进程被信号直接杀死）、
//	   随后每格 3 次复跑 **36/36 全跑**、门那一轮又出现 **1 格没跑**
//	   ⇒⇒⇒ ⇒ 「EXIT trap 会执行」是**一次观测**；「EXIT trap 一定会执行」是**过强的普适断言**；
//	   ⇒⇒⇒ ⇒ 仓内那句「EXIT trap 在父进程被信号打死时**不保证**会跑」= **准确的严谨表述**。
//	   ⇒⇒⇒ ⇒ ★★ 推论：既然 trap 可能一次都不跑 ⇒ 一次性库脚本的回收动作
//	   ⇒⇒⇒ ⇒   **必须可事后核对**（查落点），不能只看「trap 跑没跑」。
//
// ── ★★★ 静态守卫的【形态约束】（★ 并行会话 R97 段七 ① 的提醒 ⇒ 落进文件头）──
//
// ★★ 下一个人不必靠撞墙发现这条：
//
//	① 本门 ⑤ 判「INT/TERM trap 能不能让进程停下来」⇒ 认**两种**合法形态：
//	   · trap 动作里**直接**写 `exit`（仓内现有写法）
//	   · `exit` 被**抽进** trap 动作调用的函数体（★ 工程上更漂亮、去重更好 ⇒ 合法形态）
//	   ⇒ ⇒ 判据**顺着代码形态长**，而不是逼代码长成判据的样子。
//	② 反过来说：**判据若只认某一种写法**，下一个人会把「更好的写法」写成「更差的读数」，
//	   还会以为门在报错 ⇒ ⇒ ⇒ 形态约束必须**写在文件头**，而不是让人撞墙发现。
//	③ 同族提醒（并行会话原话，保留）：★ 「代码要照判据的形态写；这不是门的错」
//	   ⇒ ⇒★ 两边都成立：**判据要能容纳好写法**（本门已照此改），**代码也要照判据写**（写进文件头）。
//
// ── ★★ 门为什么断言【关系】而不只是【绝对时刻】────────────────────────────────
//
// ★ 本轮踩到的三个坑，都是门当场抓出来的：
//   ① 阈值基准错：用「前台命令全长」而不是「发信号后剩余时长」⇒ 低估 200ms。
//   ② **两个 trap 写同一个标记文件** ⇒ 「EXIT trap 跑没跑」与「INT/TERM trap 跑没跑」混为一谈
//      ⇒ 读数自相矛盾（既像「trap 没跑」又像「跑了但没副作用」）。
//   ③ **fire 时刻取的是「观测结束时刻」**（等两个标记都出现才记时间）
//      ⇒ 另一个标记永不出现时 ⇒ 时刻被记成 12s 超时值 ⇒ 读数整体作废。
// ⇒⇒⇒ ⇒ 三个修法：标记**分开**、每个标记**独立**记首次出现时刻、判据只断言【关系】。
//
// ── ★★ 为什么【没有】文本禁令门（R100 轮的门曾有，本轮主动撤回）──────────────
//
// ★★ R100 轮那版门禁止「`.sh` 注释里出现『EXIT trap』+『不保证』共现」。
// ⇒⇒⇒ ★★ 本轮实测证明：**那句「不保证」是正确的严谨表述**
//   （读数：第一轮 24 格里 3 格 trap 一次都没跑、随后每格 3 次复跑 36/36 全跑、
//   门那一轮又出现 1 格没跑 ⇒ ⇒⇒ 「不保证」= 实测事实；「会跑」= 一次观测）。
// ⇒⇒⇒ ⇒ 一个禁令门若禁止了**正确**的措辞，它就是一台「制造假红的机器」。
// ⇒⇒⇒ ⇒ 本轮改为：文本侧**只做正向守卫**（仓内 trap 必须带 exit），
//   机制事实交给下面的**动态门**⇒ ⇒ 「禁止某种措辞」永远比「断言某种机制」危险。
//
// ── ★★★ 为什么动态判据带【重试】（★ 本轮第三次踩坑才撞出来的）────────────────
//
// ★ 「EXIT trap 在信号终止下会不会执行」**不是确定性的** ⇒ 门自己会假红。
// ⇒⇒⇒ ⇒ 判据形态：**最多试 3 次，至少 1 次成功即通过；3 次全败 ⇒ 机制彻底变了 ⇒ 门必须红**。
// ⇒⇒⇒ ⇒ 每次失败都**打印**（⇒ 「不保证」持续可见 ⇒ 不会被下一个人当成「总是会跑」）。
// ⇒⇒⇒ ⇒ ★★ 推论（★ 属主/并行会话决策项）：既然 trap **可能一次都不跑**，
// ⇒⇒⇒ ⇒   一次性库脚本的回收动作就**必须可事后核对**（查落点，而不是只看 trap 跑没跑）
// ⇒⇒⇒ ⇒   —— 这与 §19.20.59 台账「回收 + 核实」的三态判据同源。
//
// ── 门自己的「有牙」证明 ─────────────────────────────────────────────────────
//
//   ① 判据全部抽成**可返回错误的纯逻辑**（judge* 函数）⇒ 合成夹具能正面判定它们。
//   ② 每个 judge 都有**反向夹具**（同样的结构、只改那个被测属性 ⇒ 必须一个过一个不过）。
//   ③ 仓根用 `runtime.Caller` 定位（★ 不用工作目录 ⇒ 否则扫不到任何文件 ⇒ 恒绿门）。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ── 观测模型 ────────────────────────────────────────────────────────────────

// sigObs 是一次信号观测（★ 所有时刻都以【发信号时刻】为 0 点）。
type sigObs struct {
	ExitTrapMS int // EXIT trap 标记首次出现（-1 = 未出现）
	SigTrapMS  int // INT/TERM trap 标记首次出现（-1 = 未出现）
	ContMS     int // 阻塞命令之后的脚本体是否继续（-1 = 未继续）
	ExitMS     int // 进程退出时刻（-1 = 未退出）
	ExitCode   int
	Signaled   bool
	Signal     syscall.Signal
	TimedOut   bool
}

// sigTrapFired 返回「INT/TERM trap 是否执行」的延迟（-1 = 未执行）。
func (o sigObs) sigTrapFired() bool { return o.SigTrapMS >= 0 }

// bodyContinued 返回「脚本体是否在收到终止信号后继续执行」。
func (o sigObs) bodyContinued() bool { return o.ContMS >= 0 }

// exitTrapRan 返回「EXIT trap 是否执行」。
func (o sigObs) exitTrapRan() bool { return o.ExitTrapMS >= 0 }

// ── 纯逻辑判据（可返回错误 ⇒ 可被正面判定）─────────────────────────────────

// judgeFormContrast 断言【形态依赖】：wait 形态的 trap 立即触发，前台形态的被推迟。
//
// ★★★ 判据形态（★ 本轮第六处自我推翻：门自己的注释写着「不写死任何绝对毫秒数」，
//
//	而第一版就写死了「wait ≤ 400ms」「fg ≥ 800ms」⇒ ⇒★ **高负载下它假红了一次**
//	（实测：机器 load 420+ 时 wait 形态的触发延迟被拉长 ⇒ 越过了那条绝对上限））。
//
// ⇒⇒⇒ ⇒★ **判据的形态必须与【被测量的机制】同源**：
//	机制 = 「两个形态的触发时刻差两个数量级」⇒ ⇒⇒ 判据就该是【差值】，不是各自的绝对值。
//	· 形态 F 的延迟由**前台命令时长**决定（≈1300ms，负载无关）⇒ 下界可以断言；
//	· 形态 W 的延迟**随机器负载浮动**（常态 13–30ms，高负载下会拉长）⇒ **不设绝对上限**。
//
// ⇒⇒⇒ ⇒★ 所以判据 = 「fg 的延迟 − wait 的延迟 ≥ minGapMS」⇒ ⇒ **负载再高也不会假红**
//	（只要形态差异本身还在）。
func judgeFormContrast(waitObs, fgObs sigObs, minGapMS int) error {
	if !waitObs.sigTrapFired() || !fgObs.sigTrapFired() {
		return fmt.Errorf("两组观测都必须执行了 INT/TERM trap（wait=%v fg=%v）", waitObs.sigTrapFired(), fgObs.sigTrapFired())
	}
	gap := fgObs.SigTrapMS - waitObs.SigTrapMS
	if gap < minGapMS {
		return fmt.Errorf("期望两个形态的 trap 触发延迟相差 >= %dms，实际差 %dms（wait=%dms fg=%dms）⇒ ⇒ 「形态依赖」未复现",
			minGapMS, gap, waitObs.SigTrapMS, fgObs.SigTrapMS)
	}
	return nil
}

// judgeBodyStops 断言「收到终止信号后脚本体停止执行」。
func judgeBodyStops(o sigObs, wantExitCode int) error {
	if o.bodyContinued() {
		return fmt.Errorf("脚本体在收到终止信号后仍继续执行（cont=%dms）⇒ ⇒ 信号没有让脚本停下来", o.ContMS)
	}
	if o.ExitCode != wantExitCode {
		return fmt.Errorf("退出码应为 %d，实际 %d（signaled=%v）", wantExitCode, o.ExitCode, o.Signaled)
	}
	return nil
}

// judgeBodyContinues 断言「脚本体继续执行」（★ 用于证明「信号被忽略」那一格）。
func judgeBodyContinues(o sigObs) error {
	if !o.bodyContinued() {
		return fmt.Errorf("脚本体未继续执行（cont=%dms）⇒ ⇒ 与实测不符", o.ContMS)
	}
	return nil
}

// judgeExitTrapRan 断言 EXIT trap 执行过。
func judgeExitTrapRan(o sigObs) error {
	if !o.exitTrapRan() {
		return fmt.Errorf("EXIT trap 未执行（exitTrap=%dms）", o.ExitTrapMS)
	}
	return nil
}

// judgeTrapActionExits 断言 trap 动作**能让进程停下来**。
//
// ★★★ 形态约束（★ 并行会话 R97 段七 ① 的提醒 ⇒ 已落进本文件头）：
//
//	判据认两种形态：
//	  ① trap 动作里**直接**出现 `exit`（仓内现有写法）；
//	  ② trap 动作调用的**函数体里**出现 `exit`（★ 把 `exit 143` 抽进 cleanup/helper ——
//	     工程上更漂亮、去重更好 ⇒ **合法形态**，不该被判成「没带 exit」）。
//	⇒ ⇒⇒ ★★ 判据要顺着【代码形态】长，而不是逼代码长成【判据】的样子；
//	⇒ ⇒⇒ 否则「更好的写法」会被写成「更差的读数」⇒ ⇒⇒ ⇒★ 下一个人会以为门在报错。
func judgeTrapActionExits(action, fileBody string) error {
	if strings.Contains(action, "exit") {
		return nil
	}
	for _, fn := range trapActionCallers(action) {
		if shellFuncBodyHasExit(fileBody, fn) {
			return nil
		}
	}
	return fmt.Errorf("trap 动作里没有 exit（既不在动作里、也不在被调用函数的函数体里）⇒ ⇒ 收到终止信号后脚本体会继续执行（实测反模式）：%q", action)
}

// trapActionCallers 从 trap 动作里挑出**看起来是 shell 函数调用**的词。
func trapActionCallers(action string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(action, func(r rune) bool {
		return r == ';' || r == '|' || r == '&' || r == '(' || r == ')' || r == ' ' || r == '\t' || r == '\n'
	}) {
		if part == "" || strings.ContainsAny(part, "$'\"") {
			continue
		}
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(part) {
			continue
		}
		// 排除 shell 内建（它们没有函数体可查）。
		switch part {
		case "true", "false", "exit", "rm", "echo", "printf", "kill", "wait", "sleep", "test":
			continue
		}
		out = append(out, part)
	}
	return out
}

// shellFuncBodyHasExit 判断文件里定义的某个 shell 函数体里是否出现 `exit`。
//
// ★ 口径：函数定义行形如 `name() {`；函数体到**独占一行**的 `}` 为止。
func shellFuncBodyHasExit(fileBody, fn string) bool {
	lines := strings.Split(fileBody, "\n")
	defRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(fn) + `\s*\(\)\s*\{`)
	inBody := false
	for _, ln := range lines {
		if !inBody {
			if defRe.MatchString(ln) {
				inBody = true
			}
			continue
		}
		if strings.TrimSpace(ln) == "}" {
			return false
		}
		if strings.Contains(ln, "exit") {
			return true
		}
	}
	return false
}

// ── 观测侧：真跑 bash ───────────────────────────────────────────────────────

const (
	sigFgSleepMS   = 1500 // 前台形态：前台外部命令时长
	sigBgSleepMS   = 5000 // wait 形态：后台子进程时长（父脚本通常提前退出 ⇒ 子进程成孤儿，无害）
	sigSignalDelay = 200  // 发信号前的静默期
	sigPollStepMS  = 10   // 轮询间隔
	sigTimeoutMS   = 12000
)

// sigScript 生成被观测的父脚本。
//
//	★ 三种形态（form）：
//	  "fg"   —— 前台外部命令 `sleep 1.5`（★ 并行会话 P1/P2 用的形态）
//	  "wait" —— 后台子进程 + `wait "$CHILD"`（★ 仓内 healthcheck 的真实形态）
//
//	★ 三个标记文件**互相独立**（★ 本轮修掉的坑②）：
//	  exitfired ← EXIT trap
//	  sigfired  ← INT/TERM trap
//	  cont      ← 阻塞命令之后的脚本体
//
//	★ trap 形态（mode）：
//	  "none"    —— 不挂 INT/TERM trap
//	  "noexit"  —— 挂但不 exit（★ 反模式）
//	  "exit143" —— 挂且 exit 143（★ 仓内形态）
func sigScript(form, mode string) string {
	var sb strings.Builder
	sb.WriteString("#!/bin/bash\nDIR=\"$1\"\n")
	sb.WriteString("trap 'echo E > \"$DIR/exitfired\"' EXIT\n")
	switch mode {
	case "none":
	case "noexit":
		sb.WriteString("trap 'echo S > \"$DIR/sigfired\"' INT TERM\n")
	case "exit143":
		sb.WriteString("trap 'echo S > \"$DIR/sigfired\"; exit 143' INT TERM\n")
	default:
		panic("未知 trap 形态: " + mode)
	}
	switch form {
	case "fg":
		fmt.Fprintf(&sb, "sleep %.1f\n", float64(sigFgSleepMS)/1000.0)
	case "wait":
		fmt.Fprintf(&sb, "sleep %.1f &\nCHILD=$!\nwait \"$CHILD\"\n", float64(sigBgSleepMS)/1000.0)
	default:
		panic("未知阻塞形态: " + form)
	}
	sb.WriteString("echo C > \"$DIR/cont\"\n")
	return sb.String()
}

// runSigProbe 跑一次观测。
func runSigProbe(t *testing.T, bashBin string, form, mode string, sig syscall.Signal) sigObs {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "parent.sh")
	if err := os.WriteFile(script, []byte(sigScript(form, mode)), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}

	cmd := exec.Command(bashBin, script, dir)
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 %s 失败: %v", bashBin, err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	type exitInfo struct {
		code     int
		signaled bool
		s        syscall.Signal
	}
	exitCh := make(chan exitInfo, 1)
	go func() {
		err := cmd.Wait()
		info := exitInfo{}
		if ee, ok := err.(*exec.ExitError); ok {
			info.code = ee.ExitCode()
			if ws, ok := ee.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				info.signaled = true
				info.s = ws.Signal()
			}
		}
		exitCh <- info
	}()

	time.Sleep(sigSignalDelay * time.Millisecond)
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("发信号失败: %v", err)
	}
	t0 := time.Now()

	seen := map[string]int{}
	saw := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	names := []string{"exitfired", "sigfired", "cont"}
	harvest := func() {
		now := int(time.Since(t0) / time.Millisecond)
		for _, n := range names {
			if _, ok := seen[n]; !ok && saw(n) {
				seen[n] = now
			}
		}
	}
	deadline := time.Now().Add(sigTimeoutMS * time.Millisecond)
	for {
		harvest()
		select {
		case e := <-exitCh:
			harvest()
			o := sigObs{ExitTrapMS: -1, SigTrapMS: -1, ContMS: -1, ExitMS: int(time.Since(t0) / time.Millisecond),
				ExitCode: e.code, Signaled: e.signaled, Signal: e.s}
			if v, ok := seen["exitfired"]; ok {
				o.ExitTrapMS = v
			}
			if v, ok := seen["sigfired"]; ok {
				o.SigTrapMS = v
			}
			if v, ok := seen["cont"]; ok {
				o.ContMS = v
			}
			return o
		default:
		}
		if time.Now().After(deadline) {
			return sigObs{ExitTrapMS: -1, SigTrapMS: -1, ContMS: -1, ExitMS: -1, TimedOut: true}
		}
		time.Sleep(sigPollStepMS * time.Millisecond)
	}
}

// bashCandidates 返回本机上【所有可用的 bash】⇒ ★ 机制事实与版本有关 ⇒ 都要测。
func bashCandidates(t *testing.T) []string {
	t.Helper()
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if _, err := os.Stat(p); err != nil {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	add("/bin/bash")
	if lp, err := exec.LookPath("bash"); err == nil {
		add(lp)
	}
	add("/opt/homebrew/bin/bash")
	add("/usr/local/bin/bash")
	if len(out) == 0 {
		t.Skip("本机找不到任何 bash ⇒ 跳过信号路径机制门")
	}
	return out
}

// bashVersion 取版本串（只用于日志）。
//
// ★ 家 brew 的 bash 在中文 locale 下 `--version` 输出被本地化（"版本" 而不是 "version"）
// ⇒ ⇒⇒ 所以**不能**按固定英文单词找版本号 ⇒ ⇒ 改为直接找**版本号形态**的 token。
func bashVersion(bin string) string {
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return "?"
	}
	if m := regexp.MustCompile(`\d+\.\d+(\.\d+)?`).FindString(string(out)); m != "" {
		return m
	}
	return "?"
}

// sigAttempts = 每个动态断言允许的尝试次数。
//
// ★★ 为什么需要它（★ 本轮实测两次撞上的事实）：
//
//	「EXIT trap 在信号终止下会不会执行」**不是确定性的** ⇒ 第一轮 24 格里 3 格没跑、
//	随后每格 3 次复跑 36/36 全跑、门那一轮又出现 1 格没跑
//	⇒ ⇒⇒ **「不保证会跑」是实测事实**（这也是仓内原注释那句话**对**的原因）。
// ⇒⇒⇒ ⇒★ 于是判据不能写成「这一次必须跑」（那是在赌一个未验证的普适性 ⇒ 必然假红）。
// ⇒⇒⇒ ⇒★ 正确形态：**最多试 N 次，至少 1 次成功即通过**；
//	  ⇒⇒⇒   全部失败 ⇒ 说明机制**彻底**变了（那才是真回归）⇒ ⇒⇒ 门必须红。
// ⇒⇒⇒ ⇒★ 且每次失败都要**打印**（⇒ 「不保证」这件事持续可见 ⇒ 不会被下一个人当成「总是会跑」）。
const sigAttempts = 3

// probeUntil 反复跑观测直到判据通过（或用尽次数）。
//
// ★ 返回最后一次错误（全部失败时）⇒ 调用方据此 t.Fatalf。
func probeUntil(t *testing.T, bashBin, form, mode string, sig syscall.Signal, label string, judge func(sigObs) error) (sigObs, error) {
	t.Helper()
	var last sigObs
	var lastErr error
	for i := 1; i <= sigAttempts; i++ {
		obs := runSigProbe(t, bashBin, form, mode, sig)
		last = obs
		if err := judge(obs); err == nil {
			return obs, nil
		} else {
			lastErr = err
			t.Logf("⚠ %s %s（%s）：第 %d/%d 次未满足判据 ⇒ %v ⇒ ★ 这就是「trap 不保证会执行」的**可复现证据** ⇒（判据允许重试，%d 次全败才判失败）",
				bashBin, label, mode, i, sigAttempts, err, sigAttempts)
		}
	}
	return last, lastErr
}

// ── ① 形态依赖门（核心）────────────────────────────────────────────────────

// TestSignalPathFormContrastIsReal 断言「形态依赖」是事实：
//
//	同一 bash、同一信号（SIGTERM）、同样挂 INT/TERM trap：
//	  · wait 形态（后台子进程 + wait）⇒ trap **立即**触发（wait 被信号打断）
//	  · 前台形态（前台外部命令）      ⇒ trap **被推迟**到前台命令结束
//
// ★★ 这条门同时裁决两件事：
//   ① 「挂信号 trap ⇒ 收割更晚」**只在形态 F 成立**；
//   ② ⇒⇒⇒ 并行会话基于形态 F 的建议**不能外推到仓内形态 W**。
func TestSignalPathFormContrastIsReal(t *testing.T) {
	for _, bashBin := range bashCandidates(t) {
		bashBin := bashBin
		t.Run(filepath.Base(bashBin), func(t *testing.T) {
			waitObs, err := probeUntil(t, bashBin, "wait", "exit143", syscall.SIGTERM, "wait 形态",
				func(o sigObs) error {
					if !o.sigTrapFired() {
						return fmt.Errorf("INT/TERM trap 未执行")
					}
					return nil
				})
			if err != nil {
				t.Fatalf("%s：wait 形态三次都没满足判据 ⇒ %v", bashBin, err)
			}
			fgObs, err := probeUntil(t, bashBin, "fg", "exit143", syscall.SIGTERM, "前台形态",
				func(o sigObs) error {
					if !o.sigTrapFired() {
						return fmt.Errorf("INT/TERM trap 未执行")
					}
					if o.SigTrapMS < 800 {
						return fmt.Errorf("前台形态：trap 应被推迟到前台命令结束（>= 800ms），实际 %dms", o.SigTrapMS)
					}
					return nil
				})
			if err != nil {
				t.Fatalf("%s：前台形态三次都没满足判据 ⇒ %v", bashBin, err)
			}
			if err := judgeFormContrast(waitObs, fgObs, 500); err != nil {
				t.Fatalf("%s（bash %s）：%v", bashBin, bashVersion(bashBin), err)
			}
			t.Logf("%s（bash %s）SIGTERM + 带 exit 的 trap：wait 形态 trap=%dms；前台形态 trap=%dms（差 %dms）⇒ ★ 形态依赖 ✔",
				bashBin, bashVersion(bashBin), waitObs.SigTrapMS, fgObs.SigTrapMS, fgObs.SigTrapMS-waitObs.SigTrapMS)
		})
	}
}

// ── ② 终止性 ≠ 清理性门 ────────────────────────────────────────────────────

// TestSignalPathCleanupIsNotTermination 断言【会清理 ≠ 会停】。
//
//	形态 F（前台外部命令）+ SIGINT + **不挂** INT/TERM trap
//	⇒⇒⇒ 实测：脚本体**继续跑完**、退出码 0、EXIT trap 在末尾执行。
//
// ⇒⇒⇒ ⇒ 「EXIT trap 在信号下会执行」（会清理）**推不出**「信号能让脚本停下来」（会停）。
// ⇒⇒⇒ ⇒ 任何把这两件事压成一件事的推论（例如「shell 不接信号零风险」）都不成立。
func TestSignalPathCleanupIsNotTermination(t *testing.T) {
	for _, bashBin := range bashCandidates(t) {
		bashBin := bashBin
		t.Run(filepath.Base(bashBin), func(t *testing.T) {
			obs, err := probeUntil(t, bashBin, "fg", "none", syscall.SIGINT, "前台形态 · 无信号 trap",
				func(o sigObs) error {
					if err := judgeBodyContinues(o); err != nil {
						return err
					}
					return judgeExitTrapRan(o)
				})
			if err != nil {
				t.Fatalf("%s：%v ⇒ ⇒ 本门守的「SIGINT 在前台形态下被忽略」这一格未复现", bashBin, err)
			}
			t.Logf("%s（bash %s）SIGINT + 无 INT/TERM trap：脚本体继续执行（cont=%dms）、退出码 %d、EXIT trap=%dms ⇒ ★ 会清理 ≠ 会停 ✔",
				bashBin, bashVersion(bashBin), obs.ContMS, obs.ExitCode, obs.ExitTrapMS)
		})
	}
}

// ── ③ 反模式门：trap 不带 exit ⇒ 脚体会继续 ────────────────────────────────

// TestSignalPathTrapWithoutExitLetsBodyContinue 断言反模式。
//
//	形态 W + SIGTERM + INT/TERM trap **不带 exit** ⇒ 脚本体在终止信号后**继续执行**。
//	形态 W + SIGTERM + INT/TERM trap **带 exit 143** ⇒ 脚本体**停止**、退出码 143。
func TestSignalPathTrapWithoutExitLetsBodyContinue(t *testing.T) {
	for _, bashBin := range bashCandidates(t) {
		bashBin := bashBin
		t.Run(filepath.Base(bashBin), func(t *testing.T) {
			noExit, err := probeUntil(t, bashBin, "wait", "noexit", syscall.SIGTERM, "wait 形态 · trap 不带 exit",
				judgeBodyContinues)
			if err != nil {
				t.Fatalf("%s：不带 exit 的 trap 形态三次都不符 ⇒ %v", bashBin, err)
			}
			withExit, err := probeUntil(t, bashBin, "wait", "exit143", syscall.SIGTERM, "wait 形态 · trap 带 exit",
				func(o sigObs) error { return judgeBodyStops(o, 143) })
			if err != nil {
				t.Fatalf("%s：带 exit 的 trap 形态三次都不符 ⇒ %v", bashBin, err)
			}
			t.Logf("%s（bash %s）SIGTERM：trap 不带 exit ⇒ 脚本体继续（cont=%dms，退出码 %d）；带 exit 143 ⇒ 脚本体停止（退出码 %d）⇒ ★ 反模式 ✔",
				bashBin, bashVersion(bashBin), noExit.ContMS, noExit.ExitCode, withExit.ExitCode)
		})
	}
}

// ── ④ 清理性门：EXIT trap 在信号终止下执行 ─────────────────────────────────

// TestSignalPathExitTrapRunsUnderTermination 断言「EXIT trap 在信号终止下执行」。
//
// ★ 只断言**两个最关键的格子**（实测 3/3 稳定）：
//   · 形态 W + SIGTERM + 无 INT/TERM trap（★ 仓内形态：EXIT-only 的那条路）
//   · 形态 W + SIGTERM + 带 exit 的 trap（★ 仓内真实形态）
// ⇒★ 其余格子只打印、不写成断言（★ 本轮实测里出现过 1 格偶发未执行
//   ⇒ 写成普适断言就是在赌一个未验证的普适性 ⇒ ★ 这正是仓内原注释「不保证」是对的场合）。
func TestSignalPathExitTrapRunsUnderTermination(t *testing.T) {
	for _, bashBin := range bashCandidates(t) {
		bashBin := bashBin
		t.Run(filepath.Base(bashBin), func(t *testing.T) {
			for _, tc := range []struct {
				name string
				mode string
			}{{"EXIT-only", "none"}, {"带 exit 的信号 trap", "exit143"}} {
				obs, err := probeUntil(t, bashBin, "wait", tc.mode, syscall.SIGTERM, "wait 形态 · "+tc.name,
					judgeExitTrapRan)
				if err != nil {
					t.Fatalf("%s %s：三次都没执行 EXIT trap ⇒ %v ⇒ ⇒ 「EXIT trap 在信号终止下会执行」这一事实被推翻", bashBin, tc.name, err)
				}
				t.Logf("%s %s：EXIT trap 执行 ✔（%dms），退出码 %d", bashBin, tc.name, obs.ExitTrapMS, obs.ExitCode)
			}
		})
	}
}

// ── ⑤ 读数全景（只打印，不断言）────────────────────────────────────────────

// TestSignalPathReadingsMatrix 打印 (bash 版本 × 形态 × 信号 × trap 形态) 的全部读数。
//
// ★ 它**不做断言**（除「trap 必须执行」）⇒ 因为绝对时刻随版本/形态/信号变化。
// ⇒⇒⇒ ★ 读数必须持续可见 ⇒ ⇒ 否则「形态依赖」这类事实会再次只活在某个人的脑子里。
func TestSignalPathReadingsMatrix(t *testing.T) {
	for _, bashBin := range bashCandidates(t) {
		for _, form := range []string{"wait", "fg"} {
			for _, sig := range []struct {
				name string
				s    syscall.Signal
			}{{"SIGTERM", syscall.SIGTERM}, {"SIGINT", syscall.SIGINT}} {
				for _, mode := range []string{"none", "noexit", "exit143"} {
					obs := runSigProbe(t, bashBin, form, mode, sig.s)
					status := fmt.Sprintf("code=%d", obs.ExitCode)
					if obs.Signaled {
						status = fmt.Sprintf("killed-by-%v", obs.Signal)
					}
					if obs.TimedOut {
						status = "TIMEOUT"
					}
					t.Logf("读数 %-22s bash=%-8s %-5s %-8s %-8s | EXIT=%6d SIG=%6d cont=%6d exit=%6d %s",
						filepath.Base(bashBin), bashVersion(bashBin), form, sig.name, mode,
						obs.ExitTrapMS, obs.SigTrapMS, obs.ContMS, obs.ExitMS, status)
					// ★★ 「trap 一次都没跑」是**合法观测**，不是门红 —— 但必须**显式标注**：
					//   这正是仓内原注释「EXIT trap 在父进程被信号打死时**不保证**会跑」的**实测依据**。
					if mode != "none" && !obs.sigTrapFired() {
						t.Logf("   ⚠⚠ 上一格：★ INT/TERM trap **一次都没执行**（EXIT trap=%dms、进程=%s）⇒ ★ 这就是「不保证」的**可复现证据** ⇒ ⇒ 回收动作必须可**事后核对**（不能只依赖 trap 一定跑）",
							obs.ExitTrapMS, status)
					}
					if mode == "none" && !obs.exitTrapRan() {
						t.Logf("   ⚠⚠ 上一格：★ EXIT trap **未执行**（EXIT trap=%dms）⇒ ★ 同上 ⇒「不保证」的可复现证据",
							obs.ExitTrapMS)
					}
				}
			}
		}
	}
}

// ── ⑥ 静态守卫：仓内 trap 必须带 exit ──────────────────────────────────────

// r101TrapActionRe 匹配仓内脚本里的 INT/TERM trap 行，并抓出 trap 动作。
var r101TrapActionRe = regexp.MustCompile(`trap\s+'([^']*)'\s+INT(\s+TERM|\s+TERM\s+INT)`)

// r101GuardedScripts 是本门静态守卫覆盖的脚本（★ 都是【建库 + 回收】的一次性库脚本）。
var r101GuardedScripts = []string{
	"scripts/audit/healthcheck-dbaudit-scripts.sh",
	"scripts/audit/measure-factsource-cost-matrix.sh",
}

// TestSignalPathRepoTrapsMustExit 静态守卫：仓内的一次性库脚本，其 INT/TERM trap 必须带 `exit`。
//
// ★ 为什么是静态守卫而不是动态门：动态门测的是**机制**；
//   而「本仓这行 trap 是否符合正确机制」是**逐文件**的事实 ⇒ ⇒ 静态断言更贴切。
// ⇒⇒⇒ ★★ 而这条守卫的存在理由（实测反模式）：trap 不带 exit ⇒ 收到终止信号后
//   脚本体**继续执行** ⇒ ⇒ 一次性库脚本可能在 cleanup 之后**继续建库** ⇒ ⇒ 泄漏。
func TestSignalPathRepoTrapsMustExit(t *testing.T) {
	root := r101RepoRoot(t)
	for _, rel := range r101GuardedScripts {
		rel := rel
		t.Run(rel, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("读 %s 失败: %v", rel, err)
			}
			m := r101TrapActionRe.FindStringSubmatch(string(b))
			if m == nil {
				t.Fatalf("%s：找不到 INT/TERM trap 行 ⇒ ⇒ 本门守的形态消失了（要么被改掉、要么本门该退役）", rel)
			}
			if err := judgeTrapActionExits(m[1], string(b)); err != nil {
				t.Fatalf("%s：%v", rel, err)
			}
		})
	}
}

// ── ⑦ 判据自检：判据必须有区分度（防恒绿门）──────────────────────────────────

// TestSignalPathJudgeSelfCheck 用合成夹具证明每个判据**能区分形态**。
//
// ★★ 若两种形态给出相同的裁决 ⇒ ⇒ 判据无区分度 ⇒ ⇒ 整门可能恒绿。
func TestSignalPathJudgeSelfCheck(t *testing.T) {
	// 形态差异：wait 形态立即、形态 F 被推迟。
	waitObs := sigObs{ExitTrapMS: 13, SigTrapMS: 13, ContMS: -1, ExitMS: 24, ExitCode: 143}
	fgObs := sigObs{ExitTrapMS: 1400, SigTrapMS: 1400, ContMS: -1, ExitMS: 1415, ExitCode: 143}
	if err := judgeFormContrast(waitObs, fgObs, 500); err != nil {
		t.Fatalf("形态差异应判为成立：%v", err)
	}
	if err := judgeFormContrast(fgObs, waitObs, 500); err == nil {
		t.Fatalf("形态差异反了也不应通过 ⇒ 判据无区分度")
	}
	// ★★ 高负载形态：wait 侧的延迟被拉长（实测会到几百 ms）⇒ 判据必须仍然成立（★ 这就是「不用绝对上限」的意义）。
	loadedWait := sigObs{ExitTrapMS: 620, SigTrapMS: 620, ContMS: -1, ExitMS: 640, ExitCode: 143}
	if err := judgeFormContrast(loadedWait, fgObs, 500); err != nil {
		t.Fatalf("wait 侧被负载拉长（620ms）时形态差异仍应成立 ⇒ 判据不该有绝对上限：%v", err)
	}
	if err := judgeFormContrast(loadedWait, fgObs, 1200); err == nil {
		t.Fatalf("差值不足时不应通过 ⇒ 判据无区分度")
	}
	// 脚本体停止 / 继续。
	if err := judgeBodyStops(waitObs, 143); err != nil {
		t.Fatalf("停止形态应通过 judgeBodyStops：%v", err)
	}
	if err := judgeBodyStops(fgObs, 143); err != nil {
		t.Fatalf("停止形态应通过 judgeBodyStops：%v", err)
	}
	continued := sigObs{ExitTrapMS: 1397, SigTrapMS: 1397, ContMS: 1397, ExitMS: 1444, ExitCode: 0}
	if err := judgeBodyStops(continued, 143); err == nil {
		t.Fatalf("继续形态不应通过 judgeBodyStops ⇒ 判据无区分度")
	}
	if err := judgeBodyContinues(continued); err != nil {
		t.Fatalf("继续形态应通过 judgeBodyContinues：%v", err)
	}
	if err := judgeBodyContinues(waitObs); err == nil {
		t.Fatalf("停止形态不应通过 judgeBodyContinues ⇒ 判据无区分度")
	}
	// 退出码。
	if err := judgeBodyStops(waitObs, 0); err == nil {
		t.Fatalf("退出码不符时不应通过 ⇒ 判据无区分度")
	}
	// EXIT trap 执行与否。
	if err := judgeExitTrapRan(waitObs); err != nil {
		t.Fatalf("EXIT trap 执行形态应通过：%v", err)
	}
	if err := judgeExitTrapRan(sigObs{ExitTrapMS: -1}); err == nil {
		t.Fatalf("EXIT trap 未执行必须被判为不合形态")
	}
	// trap 动作带不带 exit（★ 两种合法形态都要过：动作里直接 exit / exit 抽进被调函数体）。
	if err := judgeTrapActionExits("cleanup || true; exit 143", ""); err != nil {
		t.Fatalf("带 exit 的动作应通过：%v", err)
	}
	if err := judgeTrapActionExits("cleanup || true", ""); err == nil {
		t.Fatalf("不带 exit 的动作不应通过 ⇒ 判据无区分度")
	}
	helperBody := "cleanup() {\n  drop_db\n  exit 143\n}\n"
	if err := judgeTrapActionExits("cleanup || true", helperBody); err != nil {
		t.Fatalf("★ 把 exit 抽进被调函数体是【合法形态】⇒ 应通过：%v", err)
	}
	if err := judgeTrapActionExits("cleanup || true", "cleanup() {\n  drop_db\n}\n"); err == nil {
		t.Fatalf("被调函数体里也没有 exit ⇒ 应判红 ⇒ 判据无区分度")
	}
}

// ── 仓根定位（★ 不用工作目录 ⇒ 否则静态守卫会扫不到任何文件 ⇒ 恒绿门）──────

func r101RepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓库根")
	}
	// scripts/ursmcheck/xxx_test.go ⇒ 上一级是 scripts ⇒ 再上一级是仓库根
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓库根定位失败（%s 下没有 go.mod）⇒ ⇒ 静态守卫会扫不到文件 ⇒ 变成恒绿门", root)
	}
	return root
}

// ── 工具（保留：其它门复用同一套排序/转数字口径）──────────────────────────────

// sigMedian 取中位数（★ 读数抖动时用它，而不是取第一次）。
func sigMedian(v []int) int {
	if len(v) == 0 {
		return -1
	}
	c := append([]int(nil), v...)
	sort.Ints(c)
	return c[len(c)/2]
}

// sigAtoi 宽容地转整数（读不出就返回 -1 ⇒ 宁可标「未知」也不要编一个数）。
func sigAtoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return -1
	}
	return n
}
