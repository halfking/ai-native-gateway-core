package ursm_test

// PG 可用性心跳巡检的行为门（2026-10-05）
//
// ★ 为什么需要这道门，而 monitor_script_exec_gate_test.go 不够：
//   那道门只断言「无参数无库时不会崩、退出码 ∈ {0,1,3}」。
//   在开发机上它读到的答案是 3（容器没装在这台机器上），于是
//   **0 / 1 / 3 三个分支一个都没被走过**。
//   而这三个分支的区别正是本脚本的全部价值：
//     0 = 「PG 活着」   1 = 「PG 死了」   3 = 「我不知道」
//   把 1 写成 0，事故当天就会被读成「一切正常」；把 3 写成 0 同理。
//   ⇒ 必须有一道门**真的把这三条路径各走一遍**。
//
// 手法是夹具 + 假运行时：把 `podman` / `docker` 换成受环境变量驱动的假命令，
// 状态目录指向 t.TempDir()，notify 换成记录脚本。
// 不连真库 ⇒ 这道门在 CI 和开发机上都能跑，且不依赖 252 在线。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const availabilityScript = "../252-monitor/pg17-pg-availability-check.sh"

const fakeRuntimeBody = `#!/bin/sh
# 假容器运行时。行为完全由 FAKE_* 环境变量驱动。
#  ★ 「存在性探测」（inspect 无 -f）恒成功，而状态查询（inspect -f）可返回空
#    —— 这两件事在真机上本来就可能不一致（刚被删掉的容器、网络文件系统上的
#    短暂不可见），把它们分开才能模拟「找得到运行时但容器没了」。
case "$1" in
  inspect)
    case "$2" in
      -f)
        case "$3" in
          '{{.State.Status}}')
            [ -n "$FAKE_STATE" ] && printf '%s\n' "$FAKE_STATE"
            exit 0 ;;
          *)
            printf 'exit=0 oom=false finished=2026-01-01T00:00:00Z\n'
            exit 0 ;;
        esac ;;
      *) exit 0 ;;
    esac ;;
  exec)
    [ -n "$FAKE_PSQL_SLEEP" ] && sleep "$FAKE_PSQL_SLEEP"
    [ -n "$FAKE_PSQL_OUT" ] && printf '%s\n' "$FAKE_PSQL_OUT"
    exit "${FAKE_PSQL_RC:-0}" ;;
esac
exit 0
`

// 最小 timeout 实现。macOS 自带 BSD date 且**没有** timeout(1)（coreutils 的
// gtimeout 未必装了），所以被测脚本在开发机上会走到「command not found」→
// rc=127 → 被误读成「PG 拒连」。
// ★ 那不是被测脚本的缺陷，是**量具缺件**。这里补上它，
//
//	才能让「超时被正确处理」这条断言在开发机上真的成立，而不是被跳过。
const fakeTimeoutBody = `#!/bin/sh
# 只实现 timeout <秒> <cmd...>，语义取自 GNU coreutils：
#   到点 SIGTERM，被调用方以「被信号杀死」结束 ⇒ 本进程转成 124。
#   124 与真 timeout 一致：被测脚本靠它区分「PG 卡死」和「PG 拒连」。
#   透传实测过：rc=7 原样返回 7，rc=0 原样返回 0。
set -m 2>/dev/null
d=$1; shift
"$@" & pid=$!
# ★ 必须杀**进程组**（负号），且必须 set -m 让后台作业自成一组。
#   只 kill $pid 杀不掉：被调方是个 shell 正阻塞在 wait 子进程上，
#   bash 推迟处理信号直到前台命令结束 ⇒ 「1 秒超时」实际要等满 5 秒。
#   第一版断言「应被 1s 截断」实测耗时 5.6s，正是这个原因；
#   而症状（超时没生效）会直接指向被测脚本，而真因在夹具。
# ★ { } 2>/dev/null 压掉 shell 回收时的 "Terminated: 15" ——
#   真 timeout 不打这行，被测脚本 log 里凭空多一行会让人以为出了别的问题。
( sleep "$d"; kill -TERM -"$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null ) >/dev/null 2>&1 & watcher=$!
{ wait "$pid"; rc=$?; } 2>/dev/null
kill "$watcher" 2>/dev/null
[ "$rc" -ge 128 ] && exit 124
exit "$rc"
`

const fakeNotifyBody = `#!/bin/sh
# 一次调用记一**行**：把 body 里的换行折成字面 \n。
# ★ 告警 body 是多行的（"本次没有关于 PG 好坏的信息 / <消息> / ★ …"），
#   而读取侧按行切分 ⇒ 不折叠的话，一条告警会被数成三条，
#   于是「只推了 1 次」这个断言会去数多行内容然后失败 ——
#   症状指向「去重坏了」，真因在夹具的记录面。判据红了先怀疑判据。
#
# ★ 必须分两步写。若写成「printf 补换行之后再 tr 掉换行」，tr 会把 printf
#   刚补的那个换行一起吃掉 ⇒ 文件里**一个换行都没有**，读取侧数出来是 1 条
#   而不是 N 条。症状是「明明推了 3 条，却数出 0 条」。
printf '%s' "$*" | tr '\n' '\134\156' >> "$FAKE_NOTIFY_LOG"
printf '\n' >> "$FAKE_NOTIFY_LOG"
exit 0
`

type availEnv struct {
	bin       string
	state     string
	notifyLog string
}

type availRun struct {
	rc     int
	out    string
	notify []string
}

// newAvailEnv 造一套夹具。必须带三个假命令：podman、docker（脚本挑运行时用）、
// timeout（开发机可能没有）。
func newAvailEnv(t *testing.T) *availEnv {
	t.Helper()
	dir := t.TempDir()
	e := &availEnv{
		bin:       filepath.Join(dir, "bin"),
		state:     filepath.Join(dir, "state"),
		notifyLog: filepath.Join(dir, "notify.log"),
	}
	if err := os.MkdirAll(e.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"podman":  fakeRuntimeBody,
		"docker":  fakeRuntimeBody,
		"timeout": fakeTimeoutBody,
	} {
		if err := os.WriteFile(filepath.Join(e.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.bin, "notify"), []byte(fakeNotifyBody), 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *availEnv) run(t *testing.T, extra ...string) availRun {
	t.Helper()
	cmd := exec.Command("bash", availabilityScript)
	cmd.Env = append(os.Environ(),
		// 假命令必须排在最前：脚本按 podman→docker 顺序挑运行时，
		// 本机真装了 podman 的话会抢在假命令前面被选中（阳性对照为 0 的坑）。
		"PATH="+e.bin+":/usr/bin:/bin:/usr/sbin:/sbin",
		"LC_ALL=C",
		"LANG=C",
		"PG17_STATE_DIR="+e.state,
		"PG17_NOTIFY="+filepath.Join(e.bin, "notify"),
		"FAKE_NOTIFY_LOG="+e.notifyLog,
	)
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()

	r := availRun{out: string(out)}
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("脚本不是以退出码结束（%v），说明崩在解释器层。输出:\n%s", err, out)
		}
		r.rc = ee.ExitCode()
	}

	// ★ notify 记录必须**无条件**读，不能放在「rc≠0 才读」的分支里。
	//   「恢复」通知恰恰是由退出码 0 的那一轮发出的（脚本在 verdict_ok 里
	//   先推送再 exit 0），而第一版把读日志放在 err==nil 的 early return 之后
	//   ⇒ 恢复通知被系统性漏读，而症状表现为「一条通知都没有」，
	//   会把人指向去重逻辑，而真因是量具的读取面。
	if b, err := os.ReadFile(e.notifyLog); err == nil {
		for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			if l != "" {
				r.notify = append(r.notify, l)
			}
		}
	}
	return r
}

func (e *availEnv) stateLine(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.state, "pg-availability.state"))
	if err != nil {
		t.Fatalf("读 state 文件失败: %v", err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\t")
}

// -------------------------------------------------------------- 0 活着

func TestAvailabilityReportsHealthyOnlyWhenQueryProvesIt(t *testing.T) {
	e := newAvailEnv(t)
	// 「容器 running」本身**不是**证据 —— 必须真有一条查询成功。
	// FAKE_STATE=running 配上查询输出 0|off，才是完整的健康证据。
	r := e.run(t, "FAKE_STATE=running", "FAKE_PSQL_OUT=0|off")
	if r.rc != 0 {
		t.Fatalf("期望 rc=0（PG 活着），实得 %d。\n输出:\n%s", r.rc, r.out)
	}
	if len(r.notify) != 0 {
		t.Errorf("健康时不该推任何告警，实推 %d 条:\n%v", len(r.notify), r.notify)
	}
	if got := e.stateLine(t)[0]; got != "up" {
		t.Errorf("state 第一列期望 up，实得 %q", got)
	}
	// 稳态静默是有意的设计（每分钟一条 OK 会把真事件淹掉），
	// 但心跳文件必须每次都写 —— 「巡检还在跑吗」只能问它。
	if _, err := os.Stat(filepath.Join(e.state, "pg-availability.lastrun")); err != nil {
		t.Errorf("心跳文件没写：%v\n★ 于是「巡检还活着吗」与「一切正常」变得无法区分。", err)
	}
}

// -------------------------------------------------------------- 1 死了

func TestAvailabilityReportsDownAndAlertsOnce(t *testing.T) {
	e := newAvailEnv(t)
	// FAKE_STATE 为空 = 状态查询返回空 = 容器在运行时里找不到了。
	r := e.run(t, "FAKE_STATE=")
	if r.rc != 1 {
		t.Fatalf("容器不存在时期望 rc=1，实得 %d。\n输出:\n%s", r.rc, r.out)
	}
	if len(r.notify) != 1 || !strings.Contains(r.notify[0], "critical") {
		t.Fatalf("容器不存在应推且只推 1 条 critical，实得 %d 条:\n%v", len(r.notify), r.notify)
	}
	if got := e.stateLine(t)[0]; got != "down" {
		t.Errorf("state 第一列期望 down，实得 %q", got)
	}
}

// TestAvailabilityDoesNotReAlertEveryMinute 是「告警去重」这条设计的存在理由。
// 没有它，挂 30 分钟 = 30 条告警，而告警一旦变成噪音就等于没有告警。
func TestAvailabilityDoesNotReAlertEveryMinute(t *testing.T) {
	e := newAvailEnv(t)
	first := e.run(t, "FAKE_STATE=")
	if first.rc != 1 || len(first.notify) != 1 {
		t.Fatalf("首轮期望 rc=1 + 1 条告警，实得 rc=%d / %d 条", first.rc, len(first.notify))
	}
	for i := 0; i < 3; i++ {
		r := e.run(t, "FAKE_STATE=")
		if r.rc != 1 {
			t.Fatalf("持续态第 %d 轮期望 rc=1，实得 %d", i+2, r.rc)
		}
	}
	// 判据落在 notify 日志上（跨全部 4 轮累计），而不是落在某一轮的返回值上 ——
	// 每轮返回值只说明「那一轮没推」，累计计数才能说明「一次都没多推」。
	b, err := os.ReadFile(e.notifyLog)
	if err != nil {
		t.Fatalf("notify 日志读不到: %v", err)
	}
	if n := strings.Count(string(b), "--level critical"); n != 1 {
		t.Errorf("4 轮持续不可用共推了 %d 条 critical，期望恰好 1 条:\n%s", n, b)
	}
}

// 半开：容器在跑，但事务卡死。处置动作与「拒连」不同，消息必须能分开。
func TestAvailabilitySeparatesTimeoutFromRefusal(t *testing.T) {
	e := newAvailEnv(t)
	start := time.Now()
	r := e.run(t, "FAKE_STATE=running", "FAKE_PSQL_SLEEP=5", "PG17_QUERY_TIMEOUT=1")
	if r.rc != 1 {
		t.Fatalf("查询超时期望 rc=1，实得 %d。\n输出:\n%s", r.rc, r.out)
	}
	if el := time.Since(start); el > 4*time.Second {
		t.Errorf("脚本耗时 %v，未被 PG17_QUERY_TIMEOUT=1 截断。\n★ 超时没接线的话，"+
			"PG 半死不活时 psql 会一直挂着，cron 下一轮再叠一个。", el)
	}
	if !strings.Contains(r.out, "卡死") {
		t.Errorf("超时消息应说明是「卡死」而不是「拒连」，实得:\n%s", r.out)
	}
}

func TestAvailabilityRejectsReadOnlyAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
	}{
		{"一直在 recovery", "1|off"},
		{"只读", "0|on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAvailEnv(t)
			r := e.run(t, "FAKE_STATE=running", "FAKE_PSQL_OUT="+tc.out)
			if r.rc != 1 {
				t.Fatalf("%s 期望 rc=1（能连上但不能服务写入），实得 %d。\n输出:\n%s", tc.name, r.rc, r.out)
			}
		})
	}
}

// -------------------------------------------------------------- 3 我不知道

// 查成功但输出认不出来 —— 这既不是「活着」也不是「死了」。
// 把 3 写成 0，就是「巡检坏了」被读成「PG 正常」，与事故当天同形。
func TestAvailabilityReportsGaugeDownOnFormatDrift(t *testing.T) {
	e := newAvailEnv(t)
	r := e.run(t, "FAKE_STATE=running", "FAKE_PSQL_OUT=0|ON")
	if r.rc != 3 {
		t.Fatalf("输出格式漂移期望 rc=3，实得 %d。\n输出:\n%s", r.rc, r.out)
	}
	if got := e.stateLine(t)[0]; got != "unknown" {
		t.Errorf("state 第一列期望 unknown，实得 %q", got)
	}
	// ★ 巡检自己坏了也必须推一次告警：告警通道静默失效正是 2026-10-05
	//   事故的形态（「什么都收得到但什么都不说」事后与「没有通道」无法区分）。
	if len(r.notify) != 1 || !strings.Contains(r.notify[0], "critical") {
		t.Errorf("量具失效应推 1 条 critical，实得 %d 条:\n%v", len(r.notify), r.notify)
	}
}

func TestAvailabilityDoesNotClaimOutageLengthItNeverObserved(t *testing.T) {
	e := newAvailEnv(t)
	e.run(t, "FAKE_STATE=") // 真的看见了 down

	// 量具坏掉的一段时间：我们不知道 PG 好不好。
	if r := e.run(t, "FAKE_STATE=running", "FAKE_PSQL_OUT=?"); r.rc != 3 {
		t.Fatalf("期望 rc=3，实得 %d", r.rc)
	}
	r := e.run(t, "FAKE_STATE=running", "FAKE_PSQL_OUT=0|off") // 恢复

	if len(r.notify) != 3 {
		t.Fatalf("期望 3 条通知（down / 巡检失效 / 恢复），实得 %d:\n%v", len(r.notify), r.notify)
	}
	rec := r.notify[2]
	if strings.Contains(rec, "中断") {
		t.Errorf("恢复通知里出现「中断」，但这段时间巡检处于失效状态，从未观测到 PG 不可用：\n%s\n"+
			"★ 这是把没测过的事说成测过了。措辞必须是「状态不明」而非「中断」。", rec)
	}
	if !strings.Contains(rec, "unknown") && !strings.Contains(rec, "不明") {
		t.Errorf("恢复通知应说明此前状态不明，实得:\n%s", rec)
	}
}

// TestAvailabilitySaysSoWhenThereIsNoAlertChannel —— 事故的根因不是「PG 挂了」，
// 是「挂了但没有任何通道说」。没有推送通道时，脚本必须**在日志里说这件事**，
// 而不是安静地退 1。
func TestAvailabilitySaysSoWhenThereIsNoAlertChannel(t *testing.T) {
	e := newAvailEnv(t)
	r := e.run(t, "FAKE_STATE=", "PG17_NOTIFY=/nonexistent/notify.sh")
	if r.rc != 1 {
		t.Fatalf("期望 rc=1，实得 %d", r.rc)
	}
	if !strings.Contains(r.out, "没有推送出去") {
		t.Errorf("notify 不可执行时应明说告警没推出去，实得:\n%s", r.out)
	}
}
