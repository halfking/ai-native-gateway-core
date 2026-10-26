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
    if [ -n "$FAKE_PSQL_SLEEP" ]; then
      # The marker lives in the delayed child too: terminating only this
      # runtime shell must not hide a query child that ran to completion.
      sh -c 'sleep "$FAKE_PSQL_SLEEP"; [ -n "$FAKE_PSQL_COMPLETED" ] && : > "$FAKE_PSQL_COMPLETED"'
    fi
    [ -n "$FAKE_PSQL_COMPLETED" ] && : > "$FAKE_PSQL_COMPLETED"
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
#
# ★★★ 2026-10-21 修（宿主负载型假红，形态二）：**父进程的 stderr 必须与子进程的分开**。
#   bash 的 job control（set -m）在 fork 之后要调 setpgid，把后台作业放进新进程组；
#   极端负载下这一步会失败，bash 把失败写成一行
#     「bin/timeout: child setpgid (NNN to NNN): Operation not permitted」
#   打到**它自己的 stderr**。而被测脚本正是用 "timeout ... 2>&1" 捕获这一路的
#   ⇒ 那行噪声混进「psql 的输出」⇒ 字段数从 4 变成 1
#   ⇒ 判据报「期望 4 个字段，实得 1」—— **症状指向被测脚本，真因在夹具**。
#   （2026-10-21 实测命中一次：scripts/ursmcheck 整包红在
#     TestOnconflictMatchExitsZero，失败消息里就带着这行 setpgid。）
#   ⇒ 修法：**先把真 stderr 存进 fd 9，再把父的 stderr 静音**，
#     子进程 exec 之前用 "exec 2>&9" 把 stderr 还原回真管道。
#     ⇒ bash 自己的抱怨（父的、watcher 的）全进 /dev/null；
#       而**被调用方的 stderr 一行不少**地回到被测脚本的捕获面。
#     ⇒ 这不是「放宽断言」，是**把噪声从量具里拿掉**：判据本身没错。
[ $# -ge 2 ] || exit 125
exec 9>&2
exec 2>/dev/null
set -m 2>/dev/null
d=$1; shift
# ★ 子进程要**自己**是进程组长：set -m 之后 bash 给**后台作业**新进程组，
#   而这里的后台作业是下面那个子 shell ⇒ 它 exec 成被调方后 pid/pgid 不变
#   ⇒ 「kill -TERM -$pid」仍然能打到整个组（含被调方的孙进程）。
( exec 2>&9; exec "$@" ) & pid=$!
# ★ 必须杀**进程组**（负号），且必须 set -m 让后台作业自成一组。
#   只 kill $pid 杀不掉：被调方是个 shell 正阻塞在 wait 子进程上，
#   bash 推迟处理信号直到前台命令结束 ⇒ 「1 秒超时」实际要等满 5 秒。
#   第一版断言「应被 1s 截断」实测耗时 5.6s，正是这个原因；
#   而症状（超时没生效）会直接指向被测脚本，而真因在夹具。
# ★ { } 2>/dev/null 压掉 shell 回收时的 "Terminated: 15" ——
#   真 timeout 不打这行，被测脚本 log 里凭空多一行会让人以为出了别的问题。
# ★★ 2026-10-20 订正（§143.14）：watcher 必须在被杀时**立刻退出**，
#   绝不能「等 sleep 结束才执行那行 kill」。
#   旧写法的坑是 bash 的信号语义：**shell 等待前台子命令时收到信号，
#   会推迟到该子命令结束才处理** ⇒
#     「kill "$watcher"」 只杀了 subshell 本身，**没杀它的 sleep 子进程**；
#     而 subshell 正在等 sleep ⇒ 信号被推迟 ⇒ sleep 结束后 subshell
#     **仍然执行** 「kill -TERM -"$pid"」 ⇒ 那个 pid 早已被系统回收复用
#     ⇒ **误杀后续运行里的无关进程**。
#   症状完全不像「夹具坏了」：表现为「间歇性少一条告警」这种与判据无关的现象
#   （2026-10-20 实测：-count=1/2 绿、-count=3 红；pristine HEAD 同样 -count=6 里红 1 次）。
#   ⇒ 修法两条同时上：① 「trap 'exit 0' TERM」 让 watcher 收到信号就退出，
#     绝不执行那行 kill；② 先杀**整个进程组**（set -m 下 watcher 自成一组，
#     连它的 sleep 一起收），杀不掉再退回杀子 shell。
( trap 'exit 0' TERM; sleep "$d"; kill -TERM -"$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null ) >/dev/null 2>&1 & watcher=$!
{ wait "$pid"; rc=$?; } 2>/dev/null
kill -TERM -"$watcher" 2>/dev/null || kill -TERM "$watcher" 2>/dev/null
[ "$rc" -ge 128 ] && exit 124
exit "$rc"
`

const fakeNotifyBody = `#!/bin/sh
[ -n "$FAKE_NOTIFY_SLEEP" ] && sleep "$FAKE_NOTIFY_SLEEP"
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

// 假 flock（2026-10-19 新增，§143.14）。
//
// ★ 为什么需要它：被测脚本的「单实例闸门」用 `flock -n 9` 判断
//
//	「锁是不是被占着」，而 **macOS 没有 flock(1)**（util-linux 的东西），
//	⇒ 开发机上 `command -v flock` 失败 ⇒ 整道闸门被跳过（fail-open）
//	⇒ **闸门那条分支在开发机上从来没被执行过**，于是
//	「被锁挡掉的那一轮到底做了什么」无人验证。
//	§143.14 的教训正是：没被走过的分支里藏着真缺陷 ——
//	skip 路径当时会刷新心跳文件（心跳在说谎）。
//
// 语义：只实现「拿不到锁 ⇒ 退出非 0」这一件事。
//
//	FAKE_FLOCK_RC=1 ⇒ 模拟「锁被别的实例占着」。
//
// ★ 被测脚本调的是 "flock -n 9"（fd 形式），真 flock 会去锁那个 fd；
//
//	这里不碰 fd —— 本门要验的是**闸门分支的行为**，不是 flock 的实现。
const fakeFlockBody = `#!/bin/sh
# 假 flock：不实现真锁，只按 FAKE_FLOCK_RC 决定「拿到 / 拿不到」。
# ★ 被测脚本调的是 "flock -n 9"（fd 形式），真 flock 会去锁那个 fd；
#   这里不碰 fd —— 本门要验的是**闸门分支的行为**，不是 flock 的实现。
exit "${FAKE_FLOCK_RC:-0}"
`

type availEnv struct {
	bin       string
	state     string
	notifyLog string
	lockDir   string
	skipLog   string
}

type availRun struct {
	rc     int
	out    string
	notify []string
}

// newAvailEnv 造一套夹具。必须带四个假命令：podman、docker（脚本挑运行时用）、
// timeout（开发机可能没有）、flock（开发机没有 ⇒ 闸门整条被跳过）。
func newAvailEnv(t *testing.T) *availEnv {
	t.Helper()
	dir := t.TempDir()
	e := &availEnv{
		bin:       filepath.Join(dir, "bin"),
		state:     filepath.Join(dir, "state"),
		notifyLog: filepath.Join(dir, "notify.log"),
		lockDir:   filepath.Join(dir, "lock"),
		skipLog:   filepath.Join(dir, "skip.log"),
	}
	if err := os.MkdirAll(e.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"podman":  fakeRuntimeBody,
		"docker":  fakeRuntimeBody,
		"timeout": fakeTimeoutBody,
		"flock":   fakeFlockBody,
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
		// ★ 单实例闸门必须被**真正执行**（否则 macOS 上没有 flock ⇒ 闸门整条
		//   被 fail-open 跳过 ⇒ 闸门分支永远没被验证过）。
		//   锁目录与 skip 日志都指到临时目录：默认值在开发机上不可写，
		//   mkdir 失败同样会让闸门被跳过。
		"LLMGW_LOCK_DIR="+e.lockDir,
		"LLMGW_LOCK_SKIP_LOG="+e.skipLog,
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
	completed := filepath.Join(e.bin, "psql-completed")
	// 「容器 running」本身**不是**证据 —— 必须真有一条查询成功。
	// FAKE_STATE=running 配上查询输出 0|off，才是完整的健康证据。
	r := e.run(t, "FAKE_STATE=running", "FAKE_PSQL_OUT=0|off", "FAKE_PSQL_COMPLETED="+completed)
	if r.rc != 0 {
		t.Fatalf("期望 rc=0（PG 活着），实得 %d。\n输出:\n%s", r.rc, r.out)
	}
	if _, err := os.Stat(completed); err != nil {
		t.Fatalf("自然完成的查询必须写入对照标记：%v", err)
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
	completed := filepath.Join(e.bin, "psql-completed")
	// A slow notification is outside the query timeout. Observe whether the
	// query completed, so scheduler/notification time cannot mimic a lost timer.
	//
	// ★ 2026-10-21：替身跑 **60s**（原 5s）。这道门用「完成标记」判别
	//   （标记不存在 = 查询确实被截断），而替身**太短**会把「夹具的击杀晚到了」
	//   误判成「查询没被截断」⇒ 负载一高就假红。
	//   2026-10-21 实测：同族 onconflict 门的替身跑 5s 时，宿主 load 39 下
	//   出现过 15.48s 的慢跑 ⇒ 击杀能晚到十几秒 ⇒ 替身必须比它长得多。
	//   代价只落在变异验证（没有超时包裹 ⇒ 替身跑满 60s ⇒ 门红）。
	r := e.run(t, "FAKE_STATE=running", "FAKE_PSQL_SLEEP=60", "PG17_QUERY_TIMEOUT=1",
		"FAKE_PSQL_COMPLETED="+completed, "FAKE_NOTIFY_SLEEP=4")
	if r.rc != 1 {
		t.Fatalf("查询超时期望 rc=1，实得 %d。\n输出:\n%s", r.rc, r.out)
	}
	if _, err := os.Stat(completed); !os.IsNotExist(err) {
		t.Errorf("超时查询不应自然完成，完成标记检查结果=%v；PG17_QUERY_TIMEOUT=1 未截断延迟查询", err)
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
