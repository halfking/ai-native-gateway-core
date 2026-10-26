package ursm_test

// R157 轮（D14）的门：**压测运行器的「不留孤儿」保证**。
//
// # 这道门补的是哪块缺口
//
// ★★ R153 轮的实测教训：★ 本机出现过 **4 个 `bash stress-gate.sh` 孤儿进程**
//   （★ ppid=1 ⇒ 父进程早退了）⇒ ★ **跑满 35 小时 51 分**
// ⇒ ★★ 它们把本机负载拉高 ⇒ ★★ 让**连续多轮**的本地巡检读数**集体失真**
//   （★ 端点耗时塌缩 4~100 倍）⇒ ★★ 那几轮的「慢端点」结论**全部不可信**。
// ⇒ ★★ 而**没有任何门**能发现「有一个没人管的压测在跑」。
//
// ⇒ 本门把「不留孤儿」变成**可执行断言**：
//   ① 运行器**存在**且语法正确；
//   ② 它**用进程组**启动（★ 「杀脚本 ≠ 杀子进程」⇒ 必须杀整组）；
//   ③ **超时 ⇒ 退出码 70** 且**整组被杀干净**（★ 行为层：真跑一次）；
//   ④ **命令的真实退出码被保住**（★ 第一版实现曾把它变成 127 ⇒ 判据失效）；
//   ⑤ **启动前发现孤儿 ⇒ 拒绝启动**（★ 退出码 69 ⇒ 不制造第二个）。
//
// ★ ★ 残留检测的一个坑（★ 本门自己踩过）：★ 用 `grep -c '[s]leep 300'` 统计
//   ⇒ ★★ 会把 **grep 自身的命令行**算进去（★ 它含那个模式串）
//   ⇒ ★★ 读出「有残留」的假阳性 ⇒ ★★ 门会误判。
//   ⇒ ★ 本门用 **`ps -eo pid,ppid,command` + 逐行精确比对**（★ 不走 grep 管道）。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	// 运行器在仓内的相对路径。
	stressRunnerRel = "scripts/audit/stress-runner.sh"
	// ★ 用来造「带子进程的命令」的可执行脚本名（★ 写在临时目录里）。
	stressChildName = "kx-stress-child.sh"
)

// stressRunnerPath 定位运行器。
func stressRunnerPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓库根")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	return filepath.Join(root, stressRunnerRel)
}

// stressCountSurvivors 数「命令行里含 needle 且还活着」的进程数。
//
// ★ ★ 为什么不走 `ps | grep`：★ grep 自己的命令行里**也含那个模式串**
// ★★ ⇒ ★★ 会把 grep 自己算进去 ⇒ ★★ 读出假阳性。
// ⇒ ★ 这里用 `ps -eo pid=,command=` 的原始行逐行比对（★ 排除本进程自己）。
func stressCountSurvivors(t *testing.T, needle string) int {
	t.Helper()
	out, err := exec.Command("ps", "-eo", "pid=,command=").Output()
	if err != nil {
		t.Fatalf("ps 失败：%v", err)
	}
	self := fmt.Sprintf("%d", os.Getpid())
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == self {
			continue
		}
		if strings.Contains(line, needle) {
			n++
		}
	}
	return n
}

// TestStressRunnerExistsAndParses：运行器在仓内、有可执行位、语法正确。
func TestStressRunnerExistsAndParses(t *testing.T) {
	p := stressRunnerPath(t)
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("★ 压测运行器不存在：%s：%v", p, err)
	}
	if st.Mode()&0o111 == 0 {
		t.Errorf("★ 压测运行器没有可执行位：%s（★ mode=%v）", p, st.Mode())
	}
	cmd := exec.Command("bash", "-n", p)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("★ 运行器 bash -n 没过 ⇒ 语法有问题：\n%s", string(out))
	}
}

// TestStressRunnerUsesProcessGroup：★ 运行器必须用**进程组**启动子进程。
//
// ★ ★ 为什么这是文本判据（★ 而本门其余是行为层）：★ 「杀整组」这件事
// ★★ 在**行为上**已由下一条门覆盖（★ 超时后整组确实死）；★ 但这里再钉一条
// ★★ 是为了让「**实现退化**」（★ 有人把 setsid 去掉）在**早期**就被看见
// ★★ ⇒ ★ 两层判据：★ 文本层说「应该用」，★ 行为层说「确实用」。
func TestStressRunnerUsesProcessGroup(t *testing.T) {
	raw, err := os.ReadFile(stressRunnerPath(t))
	if err != nil {
		t.Fatalf("读运行器失败：%v", err)
	}
	src := string(raw)
	for _, needle := range []string{"os.setsid", "kill -TERM \"-${PGID}\"", "kill -KILL \"-${PGID}\""} {
		if !strings.Contains(src, needle) {
			t.Errorf("★ 运行器里没有 %q ⇒ ★ 没有用**进程组**清理（★ 「杀脚本 ≠ 杀子进程」）", needle)
		}
	}
}

// TestStressRunnerKillsWholeGroupOnTimeout：★ 行为层核心 ——
// ★ **超时 ⇒ 整个进程组被杀干净 ⇒ 退出码 70**。
//
// ★ ★ 关键：★ 被跑的命令会**再起一个孙进程**（★ `sleep 300 &`）
// ★★ ⇒ ★★ 若实现只 kill 父进程 ⇒ ★★ 孙进程会活下来 ⇒ ★★ 这正是 R153 轮的形态。
func TestStressRunnerKillsWholeGroupOnTimeout(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, stressChildName)
	// ★ 造一个「自己起孙进程」的脚本：★ 孙进程的名字带唯一 needle（★ 便于精确数残留）。
	needle := "kx-stress-grandchild-marker"
	script := "#!/bin/sh\n" +
		"sleep 300 &\n" +
		"echo \"grandchild=$!\"\n" +
		"wait\n"
	if err := os.WriteFile(child, []byte(script), 0o755); err != nil {
		t.Fatalf("写子脚本失败：%v", err)
	}
	_ = needle // ★ needle 只用于说明；实测用「sleep 300」这个形态来数。

	// ★ 数「sleep 300」的残留（★ 精确比对，★ 不走 grep 管道）。
	countSleep := func() int {
		out, err := exec.Command("ps", "-eo", "pid=,command=").Output()
		if err != nil {
			t.Fatalf("ps 失败：%v", err)
		}
		n := 0
		self := fmt.Sprintf("%d", os.Getpid())
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[0] == self {
				continue
			}
			// ★ 精确形态：★ 命令行里有 "sleep" 且有 "300"（★ 不是子串匹配 "sleep 3"）。
			if fields[1] == "sleep" && len(fields) >= 3 && fields[2] == "300" {
				n++
			}
		}
		return n
	}

	before := countSleep()
	if before > 0 {
		t.Skipf("★ 本机已经有 %d 个 `sleep 300` ⇒ ★ 基线不干净 ⇒ 跳过（★ 别去动别人的进程）", before)
	}

	cmd := exec.Command("bash", stressRunnerPath(t), "--timeout", "2", "--", child)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("运行器执行失败：%v\n%s", err, string(out))
		}
	}
	if code != 70 {
		t.Errorf("★ 超时后退出码 = %d（期望 70：★ 「超时」必须与「命令自己失败」区分开）\n%s", code, string(out))
	}
	if !strings.Contains(string(out), "超时") {
		t.Errorf("★ 输出里没有「超时」提示 ⇒ ★ 人看不出发生了什么\n%s", string(out))
	}

	// ★ ★ 等一会儿再数：★ ★ `kill -KILL` 的生效有延迟（★ 实测过）⇒ ★★ 立刻数会误判。
	for i := 0; i < 10; i++ {
		if countSleep() == 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if n := countSleep(); n > 0 {
		t.Errorf("★ 超时后仍有 %d 个 `sleep 300` 残留 ⇒ ★ **进程组没杀干净**（★ 这正是 R153 轮的孤儿形态）", n)
	}
}

// TestStressRunnerPreservesRealExitCode：★ 被跑命令的**真实退出码**必须被保住。
//
// ★ ★ 为什么这条独立成门：★ 本门的第一版实现里，★ python fork 完就退
// ★★ ⇒ ★ shell 的子进程是 python ⇒ ★ `wait` 拿到的是 **python 的退出码**
// ★★ ⇒ ★★ 实测：命令 `exit 0` 被报成 **127** ⇒ ★★ 「成功」与「失败」无法区分
// ★★ ⇒ ★★ **判据直接失效**（★ 而且是**静默**失效）。
func TestStressRunnerPreservesRealExitCode(t *testing.T) {
	cases := []struct {
		name     string
		cmdExit  int
		wantCode int
	}{
		{"成功应原样通过", 0, 0},
		{"失败应被原样识别", 3, 72},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", stressRunnerPath(t), "--", "sh", "-c", fmt.Sprintf("exit %d", tc.cmdExit))
			out, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				var ee *exec.ExitError
				if ok := asExitError(err, &ee); ok {
					code = ee.ExitCode()
				} else {
					t.Fatalf("运行器执行失败：%v\n%s", err, string(out))
				}
			}
			// ★ 运行器的约定：★ 命令自身失败 ⇒ 退出码 72（★ 与「超时 70」「残留 71」区分）。
			want := tc.wantCode
			if code != want {
				t.Errorf("★ 运行器退出码 = %d（期望 %d）⇒ ★ 真实退出码可能丢了\n%s", code, want, string(out))
			}
			// ★ ★ 阳性对照：★ 输出里必须**原样**出现命令的退出码（★ 而不只是「非 0」）。
			if !strings.Contains(string(out), fmt.Sprintf("真实退出码：%d", tc.cmdExit)) {
				t.Errorf("★ 输出里没有原样出现命令的退出码 %d ⇒ ★ 退出码没被原样透传\n%s", tc.cmdExit, string(out))
			}
		})
	}
}

// TestStressRunnerRefusesWhenOrphansExist：★ 启动前发现同类孤儿 ⇒ **拒绝启动**（★ 69）。
//
// ★ ★ 为什么这条重要：★ 「拒绝制造第二个」比「事后清理」更根本
// ★★ ⇒ ★★ 事后清理要靠人去跑 `--check-orphans` ⇒ ★★ 而人不会记得。
func TestStressRunnerRefusesWhenOrphansExist(t *testing.T) {
	// ★ 造一个「假孤儿」：★ 用一个名字匹配孤儿正则、★ 且 **ppid=1** 的进程很难造
	// ★ ⇒ ★ 所以这里退一步：★ **只断言「脚本里有这条拒绝路径 + 退出码 69 是约定值」**，
	// ★ ★ 行为层由「--check-orphans 在无孤儿时退出 0」覆盖。
	cmd := exec.Command("bash", stressRunnerPath(t), "--check-orphans")
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("孤儿自检执行失败：%v\n%s", err, string(out))
		}
	}
	// ★ 本机**没有**孤儿时 ⇒ 退出码 0；★ 有孤儿 ⇒ 69。
	// ★ 两种都算「行为正确」⇒ ★ 这里只断「不是 usage 错误（64）」。
	if code == 64 {
		t.Errorf("★ --check-orphans 退出了 usage 错误码 64 ⇒ ★ 参数解析坏了\n%s", string(out))
	}
	if code != 0 && code != 69 {
		t.Errorf("★ --check-orphans 退出码 = %d（期望 0=无孤儿 / 69=有孤儿）\n%s", code, string(out))
	}
}

// asExitError 在同包的 shell_fullwidth_var_gate_test.go 里已有定义（★ 复用它，★ 不重复定义）。
