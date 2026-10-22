package ursm_test

// pg17-disk-watch.sh 的 IM 去噪判据（2026-10-19，§143.8）
//
// # 这道门要修的缺陷
//
// 252 只读实测（原实现的实况）：
//
//	cooldown 文件 906 行（= 指纹种类数），其中 767 行已超 24h 未复现
//	alert log 12,371 条推送记录（2026-07-15 → 2026-10-19，3 个月）
//	最近 12 个指纹的时间戳间隔 = 584~615 秒 ≈ 脚本的 10 分钟周期
//	⇒ 冷却一次都没生效过：每一轮都在推 IM。
//
// 根因：**指纹里含告警正文**，而正文全是会抖的取值
// （`🔴磁盘 critical: 72% (avail=54GB), 阈值 90%`）。
// 磁盘 71↔72、avail 53↔54 都产生新指纹 ⇒ 冷却永远命中不到上一条。
//
// ★ 更要紧的是量：3 个月 12,371 次推送里**只有 56 次**是真的电平跳变，
//   其余 12,315 次（99.5%）在重复推同一个没变过的状态。
//
// # 改法（三条触发，缺一条就有盲区）
//
//	① 电平跳变（ok↔warning↔critical↔gauge_unavailable）—— 永远推，**不受冷却约束**
//	② 告警种类集变化（同电平但冒出新问题）—— 受冷却约束
//	③ 心跳：状态持续未复述，至多 PG17_HEARTBEAT_MIN（默认 1440）后必推一次
//
// # ⚠ 这道门最重要的一条不是「少推了」，是「不许静默」
//
// 把「电平判定」改成「跳变判定」时最危险的写法是让一个**持续存在**的故障
// 在第一次推送后永远沉默 —— 告警就从「吵」变成「瞎」。
// 2026-10-10 那 8 小时盲区正是后者造成的（且当时还叠加了去重坏掉）。
// ⇒ 所以本文件里 ③/④/⑤ 三道门**全部是「必须推」的方向**，
//   「必须不推」只有一道（①），且它必须能被「同一条告警的值抖动」打中。
//
// # 为什么用假 docker / 假 df
//
// 本脚本的电平直接由 `df -P /` 的 used% 决定；开发机的真实读数会随磁盘占用变化
// ⇒ 门禁结果不可复现。故脚本新增 DOCKER_BIN / DF_BIN / PG17_NOTIFY_BIN 三个注入点
// （与 emergency-cleanup / vacuum-bloat 同款）。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const diskWatchScript = "../252-monitor/pg17-disk-watch.sh"

// dwFakeDocker 按 SQL 内容应答，并**记录每一次调用**。
// ⚠ 本桩必须复刻脚本实际发出的查询，而不是笼统返回一个数：
//
//	漏掉任何一个分支，脚本就会走「量不出来 → 0」的路径，
//	那样 level 会退化成 ok，本文件所有断言 SKIP/PUSH 的门全部假绿。
func dwFakeDocker(t *testing.T, dir string) string {
	t.Helper()
	// ⚠⚠ 模式**不能带括号**：`case` 的字面模式里 `\(` 不按转义处理
	//   （实测：`*pg_database_size\('llm_gateway'\)*` 不匹配同名字符串，
	//    而同样的字符串作为变量展开后放进 case 就匹配 —— 两种写法行为不同）。
	//   ⇒ 一律用不含括号的子串，并**按特异性从高到低**排列
	//     （`pg_database_size` 出现在每条 -U llm_gateway 的查询里，只能放最后）。
	// ⚠ 漏掉任何一个分支 ⇒ 该查询返回空 ⇒ 脚本走「量不出来 → 0」路径
	//   ⇒ level 可能退化成 ok ⇒ 本文件所有断言 PUSH/SUPPRESS 的门全部假绿。
	script := `#!/bin/sh
all="$*"
{
  echo "--- call ---"
  for a in "$@"; do echo "$a"; done
} >> "$TRACE_FILE"

case "$all" in
  *datname*)
      printf '%s\n' "${FAKE_DB_ALL_GB:-40.00}" ;;
  *session_turns_hot*)
      printf '%s\n' "${FAKE_REQ_LOGS_HOT_MB:-100}" ;;
  *model_probe_runs_hot*)
      printf '%s\n' "${FAKE_MP_HOT_MB:-100}" ;;
  *handoff_logs*)
      printf '%s\n' "${FAKE_HANDOFF_MB:-10}" ;;
  *columnar_internal.chunk*)
      awk -v g="${FAKE_CHUNK_GB:-1.00}" 'BEGIN{printf "%d\n", g*1024*1024*1024}' ;;
  *pg_stat_file*)
      awk -v g="${FAKE_WAL_GB:-1.00}" 'BEGIN{printf "%d\n", g*1024*1024*1024}' ;;
  *sz/1024/1024*)
      printf '%s\n' "t1=100MB, t2=90MB" ;;
  *pct::numeric*)
      printf '%s\n' "t=1.0%(100)" ;;
  *pg_database_size*)
      awk -v g="${FAKE_DB_GB:-29.00}" 'BEGIN{printf "%d\n", g*1024*1024*1024}' ;;
  *)
      printf '\n' ;;
esac
exit 0
`
	return dwWriteFake(t, dir, "fake-docker", script)
}

// dwFakeDf 造一行 df -P 输出，让 used% 与 avail 由环境变量控制。
// 字段对应：$3 Used / $4 Available / $5 Capacity / $6 Mounted。
func dwFakeDf(t *testing.T, dir string) string {
	t.Helper()
	script := `#!/bin/sh
echo "Filesystem 1024-blocks Used Available Capacity Mounted on"
used_blocks=$(( ${FAKE_USED_PCT:-72} * 1048576 ))
avail_blocks=$(( ${FAKE_AVAIL_GB:-54} * 1048576 ))
echo "fakefs 104857600 $used_blocks $avail_blocks ${FAKE_USED_PCT:-72}% /"
exit 0
`
	return dwWriteFake(t, dir, "fake-df", script)
}

// dwFakeNotify 记录被推送了几次。
func dwFakeNotify(t *testing.T, dir string) string {
	t.Helper()
	script := `#!/bin/sh
echo "NOTIFIED $(date +%s) $*" >> "$NOTIFY_TRACE"
exit 0
`
	return dwWriteFake(t, dir, "fake-notify", script)
}

func dwWriteFake(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("写 %s: %v", name, err)
	}
	return p
}

// diskWatchRun 跑一轮脚本。state 可为 nil（首次运行）。
// 返回：notify 轨迹、alert log 内容、采样 log 内容、退出码。
func diskWatchRun(t *testing.T, state string, env ...string) (string, string, string, int) {
	t.Helper()
	dir := t.TempDir()
	dockerBin := dwFakeDocker(t, dir)
	dfBin := dwFakeDf(t, dir)
	notifyBin := dwFakeNotify(t, dir)

	trace := filepath.Join(dir, "trace.txt")
	notifyTrace := filepath.Join(dir, "notify.txt")
	logPath := filepath.Join(dir, "disk.log")
	alertPath := filepath.Join(dir, "disk.alert.log")
	statePath := filepath.Join(dir, "alertstate")
	if state != "" {
		if err := os.WriteFile(statePath, []byte(state), 0o644); err != nil {
			t.Fatalf("写状态文件: %v", err)
		}
	}

	cmd := exec.Command(bashForScriptFor(t, diskWatchScript), diskWatchScript)
	cmd.Env = append(os.Environ(),
		"DOCKER_BIN="+dockerBin,
		"DF_BIN="+dfBin,
		"PG17_NOTIFY_BIN="+notifyBin,
		"TRACE_FILE="+trace,
		"NOTIFY_TRACE="+notifyTrace,
		"LLMGW_DISK_WATCH_LOG="+logPath,
		"LLMGW_DISK_WATCH_ALERT_LOG="+alertPath,
		"PG17_DISK_WATCH_STATE="+statePath,
		"CONTAINER=fake-pg", "PG_USER=u", "PG_DB=llm_gateway",
		"LLMGW_PG17_CONF=/nonexistent/pg17.conf",
		"LLMGW_NO_SINGLE_INSTANCE_LOCK=1",
		"LLMGW_LOCK_DIR="+dir,
	)
	cmd.Env = append(cmd.Env, env...)
	err := cmd.Run()
	exitCode := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exitCode = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("启动脚本失败: %v", err)
	}

	notify, _ := os.ReadFile(notifyTrace)
	alert, _ := os.ReadFile(alertPath)
	logBody, _ := os.ReadFile(logPath)
	// 把状态文件带回给下一轮用（状态文件是判据之间唯一的耦合点）
	st, _ := os.ReadFile(statePath)
	_ = st
	return string(notify), string(alert), string(logBody), exitCode
}

// diskWatchTwoRuns 在**同一个状态文件**上连跑两轮，返回第二轮的 notify 次数。
// ⚠ 必须共享状态文件：判据量的正是「第二轮读到第一轮写下的状态」，
//
//	两轮各用独立临时目录的话，第二轮永远看不到状态 ⇒ 永远推送 ⇒ 门恒绿。
func diskWatchTwoRuns(t *testing.T, env1, env2 []string) (firstNotify, secondNotify string, secondAlert string, sampleLog string) {
	t.Helper()
	dir := t.TempDir()
	dockerBin := dwFakeDocker(t, dir)
	dfBin := dwFakeDf(t, dir)
	notifyBin := dwFakeNotify(t, dir)
	trace := filepath.Join(dir, "trace.txt")
	notifyTrace := filepath.Join(dir, "notify.txt")
	logPath := filepath.Join(dir, "disk.log")
	alertPath := filepath.Join(dir, "disk.alert.log")
	statePath := filepath.Join(dir, "alertstate")

	run := func(env []string) {
		cmd := exec.Command(bashForScriptFor(t, diskWatchScript), diskWatchScript)
		cmd.Env = append(os.Environ(),
			"DOCKER_BIN="+dockerBin, "DF_BIN="+dfBin, "PG17_NOTIFY_BIN="+notifyBin,
			"TRACE_FILE="+trace, "NOTIFY_TRACE="+notifyTrace,
			"LLMGW_DISK_WATCH_LOG="+logPath, "LLMGW_DISK_WATCH_ALERT_LOG="+alertPath,
			"PG17_DISK_WATCH_STATE="+statePath,
			"CONTAINER=fake-pg", "PG_USER=u", "PG_DB=llm_gateway",
			"LLMGW_PG17_CONF=/nonexistent/pg17.conf",
			"LLMGW_NO_SINGLE_INSTANCE_LOCK=1", "LLMGW_LOCK_DIR="+dir)
		cmd.Env = append(cmd.Env, env...)
		_ = cmd.Run()
	}

	run(env1)
	mid, _ := os.ReadFile(notifyTrace)
	run(env2)
	end, _ := os.ReadFile(notifyTrace)
	alert, _ := os.ReadFile(alertPath)
	sample, _ := os.ReadFile(logPath)

	// 只统计第二轮新增的行
	firstCount := strings.Count(string(mid), "NOTIFIED")
	all := strings.Split(strings.TrimRight(string(end), "\n"), "\n")
	second := strings.Join(all[firstCount:], "\n")
	return string(mid), second, string(alert), string(sample)
}

func skipDiskWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 基线（既有，非回归）：守卫脚本与 docker/df 桩是 bash 轨迹")
	}
}

func notifyCount(s string) int { return strings.Count(s, "NOTIFIED") }

// TestDiskWatchSuppressesRepeatedAlertWhenOnlyValuesJitter 是本组的正身。
//
// 形态：磁盘 91% → 92%，其余读数不变。
// ⇒ 告警**种类**没变（还是「🔴磁盘 critical」），只是取值抖了。
// ⇒ 第二轮**不得**再推 IM —— 这正是原实现每 10 分钟推一次的形态。
//
// ⚠⚠ 这道门要**隔离「指纹去抖」这一个性质**，所以做了两件事：
//
//	① 直接比较两轮算出的指纹（采样日志里的 `fp=`）：去抖正确 ⇒ 两个 fp 相同。
//	   变异 M3（把去抖的 sed 去掉）⇒ 两个 fp 不同 ⇒ 这条立即红。
//	② 行为面：把冷却下限调到 0（PG17_CRIT_COOLDOWN_MIN=0），
//	   让「告警种类集变化」分支**变成可达**。
//	   ⚠ 不这么做的话，这道门会**因为错的原因变绿**：去抖失效 ⇒ 指纹变了 ⇒
//	     落入「种类集变化」分支，但该分支被冷却下限（30/60 分钟）挡住
//	     ⇒ 第二轮照样抑制 ⇒ 门照样绿 ⇒ **去抖坏了没人知道**。
//	     （这正是 §143.7 那条「因为错的原因变绿比红更危险」的同族形态。）
func TestDiskWatchSuppressesRepeatedAlertWhenOnlyValuesJitter(t *testing.T) {
	skipDiskWindows(t)

	first, second, _, _ := diskWatchTwoRuns(t,
		[]string{"FAKE_USED_PCT=91", "FAKE_AVAIL_GB=54", "PG17_CRIT_COOLDOWN_MIN=0"},
		[]string{"FAKE_USED_PCT=92", "FAKE_AVAIL_GB=53", "PG17_CRIT_COOLDOWN_MIN=0"})

	if notifyCount(first) != 1 {
		t.Fatalf("第一轮应当推送一次（状态文件为空 ⇒ 视为跳变），实际 %d 次\n%s", notifyCount(first), first)
	}
	if notifyCount(second) != 0 {
		t.Fatalf("★ 告警种类未变、只是取值从 91%% 抖到 92%%，第二轮仍推了 IM —— "+
			"这正是 252 上 3 个月推了 12,371 次的形态（指纹含取值 ⇒ 冷却从不命中）\n%s", second)
	}
}

// TestDiskWatchFingerprintIsStableAcrossValueJitter 直接钉住「指纹只认种类、不认取值」。
//
// 这是上面那条的行为面孪生，但**更精确**：它不看推送行为，只看两个 fp 是否相同。
// ⇒ 指纹里混进取值（M3）时，它会比行为面更早、更准地红。
func TestDiskWatchFingerprintIsStableAcrossValueJitter(t *testing.T) {
	skipDiskWindows(t)

	_, _, _, logBody := diskWatchTwoRuns(t,
		[]string{"FAKE_USED_PCT=91", "FAKE_AVAIL_GB=54"},
		[]string{"FAKE_USED_PCT=92", "FAKE_AVAIL_GB=53"})

	var fps []string
	for _, line := range strings.Split(logBody, "\n") {
		if i := strings.Index(line, " fp="); i >= 0 {
			rest := line[i+4:]
			fp := rest
			if j := strings.Index(rest, " "); j >= 0 {
				fp = rest[:j]
			}
			fps = append(fps, fp)
		}
	}
	if len(fps) < 2 {
		t.Fatalf("采样日志里没找到两轮的 fp（im-gate 行），日志:\n%s", logBody)
	}
	if fps[0] != fps[1] {
		t.Fatalf("★ 两轮只有取值不同（91%% vs 92%%）却算出两个不同指纹（%s / %s）—— "+
			"指纹里混进了取值，这正是原实现每 10 分钟推一次的根因\nlog:\n%s", fps[0], fps[1], logBody)
	}
}

// TestDiskWatchStillWritesLocalAlertLogWhenSuppressed 钉住「去噪只作用于 IM」。
//
// ⚠ 这是本组最容易被顺手做掉的一条：把 IM 去重顺手扩到本地 alert log，
//
//	告警是会安静了，但事后**再也翻不到**当时到底报了什么。
//	本仓 §143.8 的教训正是这个方向：按「让错误更难被翻到」的方向优化。
func TestDiskWatchStillWritesLocalAlertLogWhenSuppressed(t *testing.T) {
	skipDiskWindows(t)

	_, _, alertLog, _ := diskWatchTwoRuns(t,
		[]string{"FAKE_USED_PCT=91"},
		[]string{"FAKE_USED_PCT=92"})

	if len(alertLog) == 0 {
		t.Fatal("本地 alert log 一条都没有 —— 取证链被切断了")
	}
	if notifyCount(alertLog) != 0 {
		t.Fatalf("alert log 里不该出现 notify 痕迹\n%s", alertLog)
	}
	if strings.Count(alertLog, "🔴磁盘 critical") != 2 {
		t.Fatalf("★ 两轮都超阈值，本地 alert log 必须**各记一条**（两轮），实际 %d 条 —— "+
			"IM 去重不得蔓延到本地日志\n%s", strings.Count(alertLog, "🔴磁盘 critical"), alertLog)
	}
}

// TestDiskWatchPushesOnLevelTransition 钉住①电平跳变必推。
//
// 状态文件写着上一轮是 warning，本轮 used_pct 越过 DISK_CRIT_PCT ⇒ critical。
// ⇒ 必须推，且**不受冷却约束**（warning→critical 若被吞掉，正好在最该让人看见时静默）。
func TestDiskWatchPushesOnLevelTransition(t *testing.T) {
	skipDiskWindows(t)

	// 上一轮：warning（60~74%），本轮：critical（>=75%）
	state := fmt.Sprintf("%s:warning:%d", "deadbeefdeadbeefdeadbeefdeadbeef", nowUnix()-300)
	notify, alert, logBody, exitCode := diskWatchRun(t, state, "FAKE_USED_PCT=91")

	if notifyCount(notify) != 1 {
		t.Fatalf("★ 电平从 warning 跳到 critical 却没有推送 —— 降级/升级都必须在 IM 上可见\n"+
			"notify:\n%s\nalert:\n%s\nlog:\n%s", notify, alert, logBody)
	}
	if !strings.Contains(logBody, "电平跳变") {
		t.Fatalf("采样日志必须写明是「电平跳变」触发的，否则事后无法解释为什么这次推了\nlog:\n%s", logBody)
	}
	if exitCode != 1 {
		t.Fatalf("critical/warning 时的退出码契约是 1，实际 %d\nlog:\n%s", exitCode, logBody)
	}
}

// TestDiskWatchPushesOnHeartbeatAfterSilence 钉住③心跳 —— **本组最关键的一条**。
//
// 形态：电平没变（还是 critical）、告警种类也没变、但距离上次推送已超过心跳窗口。
// ⇒ 必须推。
//
// ★ 这条缺了，改动就从「降噪」变成「让一个持续故障永远沉默」。
//
//	删掉心跳分支（变异 M3）后，下面这道门就是唯一会红的地方。
func TestDiskWatchPushesOnHeartbeatAfterSilence(t *testing.T) {
	skipDiskWindows(t)

	// 心跳默认 1440 分钟（1 天）。构造「恰好 2 天前推过一次」的状态。
	state := fmt.Sprintf("%s:critical:%d", "deadbeefdeadbeefdeadbeefdeadbeef", nowUnix()-2*24*3600)
	notify, alert, logBody, _ := diskWatchRun(t, state, "FAKE_USED_PCT=91")

	if notifyCount(notify) != 1 {
		t.Fatalf("★ 状态持续 2 天未复述却没有心跳推送 —— 这就是「告警从吵变成瞎」\n"+
			"notify:\n%s\nalert:\n%s\nlog:\n%s", notify, alert, logBody)
	}
	if !strings.Contains(logBody, "心跳") {
		t.Fatalf("必须写明是心跳触发的（否则与「电平跳变」分不开，事后无法解释重复推送）\nlog:\n%s", logBody)
	}
}

// TestDiskWatchSuppressesWithinHeartbeatWindow 是心跳的**另一侧**：
// 电平未变、种类未变、且距上次推送只有 10 分钟 ⇒ 必须不推。
// 没有这条，上面那条可以靠「永远推」变绿，而那正是原实现的缺陷。
func TestDiskWatchSuppressesWithinHeartbeatWindow(t *testing.T) {
	skipDiskWindows(t)

	// ★ 指纹必须与本轮实际算出的**完全一致**，否则会走「种类集变化」分支而推送。
	//   跑一次拿到真实指纹，再用它构造状态文件。
	dir := t.TempDir()
	statePath := filepath.Join(dir, "alertstate")
	runOnce := func() {
		cmd := exec.Command(bashForScriptFor(t, diskWatchScript), diskWatchScript)
		cmd.Env = append(os.Environ(),
			"DOCKER_BIN="+dwFakeDocker(t, dir), "DF_BIN="+dwFakeDf(t, dir),
			"PG17_NOTIFY_BIN="+dwFakeNotify(t, dir),
			"TRACE_FILE="+filepath.Join(dir, "trace.txt"),
			"NOTIFY_TRACE="+filepath.Join(dir, "n1.txt"),
			"LLMGW_DISK_WATCH_LOG="+filepath.Join(dir, "d.log"),
			"LLMGW_DISK_WATCH_ALERT_LOG="+filepath.Join(dir, "d.alert.log"),
			"PG17_DISK_WATCH_STATE="+statePath,
			"CONTAINER=fake-pg", "PG_USER=u", "PG_DB=llm_gateway",
			"LLMGW_PG17_CONF=/nonexistent/pg17.conf",
			"LLMGW_NO_SINGLE_INSTANCE_LOCK=1", "LLMGW_LOCK_DIR="+dir,
			"FAKE_USED_PCT=91", "FAKE_AVAIL_GB=54")
		_ = cmd.Run()
	}
	runOnce()
	st, err := os.ReadFile(statePath)
	if err != nil || len(st) == 0 {
		t.Fatalf("第一轮没写出状态文件：%v", err)
	}
	fp := strings.SplitN(string(st), ":", 2)[0]
	if len(fp) != 32 {
		t.Fatalf("状态文件里的指纹不是 32 位 md5：%q", string(st))
	}

	// 第二轮：同电平同种类，但把「上次推送」改到 10 分钟前 ⇒ 应抑制
	if err := os.WriteFile(statePath, []byte(fmt.Sprintf("%s:critical:%d", fp, nowUnix()-600)), 0o644); err != nil {
		t.Fatalf("写状态: %v", err)
	}
	notify2 := filepath.Join(dir, "n2.txt")
	cmd := exec.Command(bashForScriptFor(t, diskWatchScript), diskWatchScript)
	cmd.Env = append(os.Environ(),
		"DOCKER_BIN="+dwFakeDocker(t, dir), "DF_BIN="+dwFakeDf(t, dir),
		"PG17_NOTIFY_BIN="+dwFakeNotify(t, dir),
		"TRACE_FILE="+filepath.Join(dir, "trace.txt"),
		"NOTIFY_TRACE="+notify2,
		"LLMGW_DISK_WATCH_LOG="+filepath.Join(dir, "d.log"),
		"LLMGW_DISK_WATCH_ALERT_LOG="+filepath.Join(dir, "d.alert.log"),
		"PG17_DISK_WATCH_STATE="+statePath,
		"CONTAINER=fake-pg", "PG_USER=u", "PG_DB=llm_gateway",
		"LLMGW_PG17_CONF=/nonexistent/pg17.conf",
		"LLMGW_NO_SINGLE_INSTANCE_LOCK=1", "LLMGW_LOCK_DIR="+dir,
		"FAKE_USED_PCT=91", "FAKE_AVAIL_GB=54")
	_ = cmd.Run()
	got, _ := os.ReadFile(notify2)
	if notifyCount(string(got)) != 0 {
		t.Fatalf("★ 同电平同种类且距上次 10 分钟（< 心跳 1440 分钟）仍推了 IM —— "+
			"降噪没生效，就是原实现那个每 10 分钟一次的形态\nnotify:\n%s", string(got))
	}
}

// TestDiskWatchFailsTowardAlertingOnBrokenState 钉住「状态文件坏了要往多推的方向坏」。
//
// 形态：状态文件是垃圾 / 空 / 截断。
// ⇒ 解析出的 prev_level 为空 ⇒ 与本轮 level 不同 ⇒ 推送。
//
// ⚠ 方向很重要：2026-10-10 的教训是「监控在自己瞎掉时还报健康」。
//
//	这里若把坏状态解析成「与上轮相同」，就会把一次磁盘故障静默掉。
func TestDiskWatchFailsTowardAlertingOnBrokenState(t *testing.T) {
	skipDiskWindows(t)

	for _, bad := range []string{"", "garbage", ":::::", "abc"} {
		t.Run(fmt.Sprintf("state=%q", bad), func(t *testing.T) {
			notify, _, logBody, _ := diskWatchRun(t, bad, "FAKE_USED_PCT=91")
			if notifyCount(notify) != 1 {
				t.Fatalf("★ 状态文件是 %q（不可解析）却没有推送 —— "+
					"状态坏掉必须往**多推**的方向坏，不能往静默的方向坏\nnotify:\n%s\nlog:\n%s",
					bad, notify, logBody)
			}
		})
	}
}

// TestDiskWatchEscalationIgnoresCooldown 钉住①**不受冷却约束**。
//
// 形态：上一轮 1 秒前刚推过 warning，本轮升到 critical。
// ⇒ 必须推。若把冷却套在电平跳变上，最该被看见的升级会被吞掉。
func TestDiskWatchEscalationIgnoresCooldown(t *testing.T) {
	skipDiskWindows(t)

	state := fmt.Sprintf("%s:warning:%d", "deadbeefdeadbeefdeadbeefdeadbeef", nowUnix()-1)
	notify, _, logBody, _ := diskWatchRun(t, state, "FAKE_USED_PCT=91")

	if notifyCount(notify) != 1 {
		t.Fatalf("★ 1 秒前刚推过 warning 就升到 critical，仍未推送 —— "+
			"冷却若套在电平跳变上，降级/升级会被静默掉\nnotify:\n%s\nlog:\n%s", notify, logBody)
	}
}

func nowUnix() int64 {
	b, _ := exec.Command("date", "+%s").Output()
	var n int64
	fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &n)
	return n
}
