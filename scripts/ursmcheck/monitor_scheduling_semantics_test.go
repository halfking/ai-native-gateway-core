package ursm_test

// 巡检「调度语义」门（2026-10-19，§143.14）
//
// # 这道门补的是哪一类缺陷
//
// §143.14 在 252 上查清的根因**不是**「巡检脚本坏了」，而是三条调度层的语义：
//
//	① **重复登记**：crond 同一分钟对同一个作业发起了**两份**逐字相同的 CMD
//	   （10-06 15:54 那次 RELOAD 之后就有了；disk-watch 一度是 ×4）。
//	② **守护进程内存里的 job list 与磁盘上的文件不一致**：
//	   文件后来被修好了，但 crond 从未重载（10-06 15:54 → 10-19 22:53 之间
//	   零次 RELOAD）⇒ **磁盘看起来对、跑起来是旧的**。
//	③ **心跳在说谎**：`pg17-pg-availability-check.sh` 把写心跳的 trap 装在
//	   单实例闸门**之前** ⇒ 「本轮被锁挡掉、根本没采样」也会刷新心跳
//	   ⇒ 「巡检还活着吗」与「巡检在空转」在观感上完全同形。
//	   实测代价：13,043 次 skip 期间心跳一直是新鲜的。
//
// # 为什么这些必须是**行为**门而不是文本门
//
// ①②③ 全部只有「跑起来」才看得见：
//   · 重复登记要在**真实调度**里数「每分钟发了几条」——
//     而仓内没有 crond ⇒ 能钉的形态是「正典文件里每个脚本只被排一次」；
//   · 心跳语义要在**闸门分支**里看「心跳文件有没有被刷新」——
//     文本门（读 trap 装在第几行）测的是**排版**，不是行为。
//
// # 变异验证（每条都实测转红）
//
//	M1 把 availability 的 trap 移回闸门之前      ⇒ ① 红
//	M2 把 onconflict 的 trap 移回闸门之前        ⇒ ② 红
//	M3 去掉 onconflict 的 timeout 包裹            ⇒ ③ 红
//	M4 让 onconflict 缺 timeout 时也去查（不 fail-closed）⇒ ④ 红
//	M5 在正典 cron 里复制一条调度行                ⇒ ⑤ 红

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --------------------------------------------------------------- ① 心跳语义

// TestAvailabilitySkipDoesNotRefreshHeartbeat —— 被锁挡掉的那一轮**不得**刷新心跳。
//
// ★ 为什么这是承重断言：心跳是「巡检还在跑吗」的唯一回答（脚本健康时
//
//	刻意不输出，日志在健康期是不增长的）。
//	若 skip 路径也刷新心跳 ⇒ 巡检被重复调度挤成空转时，心跳照样新鲜
//	⇒ **巡检死了 9 天而心跳一直显示活着**（§143.14 实测形态）。
//	⇒ 心跳的语义必须被钉成「本轮真的采样过」。
func TestAvailabilitySkipDoesNotRefreshHeartbeat(t *testing.T) {
	e := newAvailEnv(t)
	lastrun := filepath.Join(e.state, "pg-availability.lastrun")

	// 先跑一轮正常的（闸门畅通）⇒ 心跳应当被写。
	r := e.run(t, "FAKE_STATE=running", "FAKE_PSQL_OUT=0|off")
	if r.rc != 0 {
		t.Fatalf("正常采样期望 rc=0，实得 %d。\n输出:\n%s", r.rc, r.out)
	}
	first, err := os.Stat(lastrun)
	if err != nil {
		t.Fatalf("正常采样后心跳文件应存在：%v", err)
	}

	// 拉大时间差，确保 mtime 变化可辨（文件系统 mtime 精度可能是 1s）。
	time.Sleep(1100 * time.Millisecond)

	// 现在让锁被占着 ⇒ 这一轮被闸门挡掉。
	r2 := e.run(t, "FAKE_STATE=running", "FAKE_PSQL_OUT=0|off", "FAKE_FLOCK_RC=1")
	if r2.rc != 0 {
		t.Fatalf("被锁挡掉时期望 rc=0（本目录契约：没有新采样，但巡检没失效），实得 %d。\n输出:\n%s",
			r2.rc, r2.out)
	}
	// ★ skip 的说明写在**独立的 skip 日志**里（不是 stdout）——
	//   判据红了先怀疑判据：这里第一次写成读 stdout，于是读到空串。
	skip, err := os.ReadFile(e.skipLog)
	if err != nil {
		t.Fatalf("skip 日志读不到：%v", err)
	}
	if !strings.Contains(string(skip), "锁被占用") {
		t.Errorf("被锁挡掉时应在 skip 日志里说明「锁被占用」，实得:\n%s", skip)
	}

	second, err := os.Stat(lastrun)
	if err != nil {
		t.Fatalf("心跳文件读不到：%v", err)
	}
	if second.ModTime().After(first.ModTime()) {
		t.Errorf("被锁挡掉的那一轮**刷新了心跳**（mtime %s → %s）。\n"+
			"★ 这正是 §143.14 实测到的那个形态：巡检被重复调度挤成空转，\n"+
			"  而心跳一直是新鲜的 ⇒ 「巡检还在跑吗」与「巡检在空转」无法区分。\n"+
			"  ⇒ 心跳的语义必须是「本轮真的采样过 PG」，因此写它的 trap 必须装在\n"+
			"  单实例闸门**之后**（skip 路径在闸门处就 exit 0 了）。",
			first.ModTime(), second.ModTime())
	}
}

// TestOnconflictSkipDoesNotRefreshHeartbeat —— 同一条语义，onconflict 也要有。
//
// ★ 这道巡检在 §143.14 之前**连心跳都没有**：唯一的活体证据是日志，
//
//	而日志在「被重复调度挡掉」或「cron 行把输出丢掉」时都不增长
//	⇒ 「日志陈旧」与「巡检死了」在观感上同形（实测：它的日志停在 10-10，
//	而它其实每天都被调度）。
func TestOnconflictSkipDoesNotRefreshHeartbeat(t *testing.T) {
	// 闸门畅通的一轮：应当写心跳。
	rc, out, state := runOnconflictEnv(t, 0,
		"echo 'ursm_node_snapshot_min_pkey|credential_id,raw_model_name,snapshot_ts,tenant_id|true|1'")
	if rc != 0 {
		t.Fatalf("正常采样期望 rc=0，实得 %d。\n输出:\n%s", rc, out)
	}
	lastrun := filepath.Join(state, "pg-onconflict.lastrun")
	first, err := os.Stat(lastrun)
	if err != nil {
		t.Fatalf("正常采样后心跳文件应存在：%v\n"+
			"★ 没有心跳 ⇒ 「这道巡检还在跑吗」只能问日志，而日志在健康期是增长的，\n"+
			"  在被挡掉/输出被丢时是不增长的 ⇒ 两者在观感上同形（§143.14 实测形态）。", err)
	}

	time.Sleep(1100 * time.Millisecond)

	// 锁被占着 ⇒ 这一轮被闸门挡掉。
	rc2, out2, _ := runOnconflictEnv(t, 0,
		"echo 'ursm_node_snapshot_min_pkey|credential_id,raw_model_name,snapshot_ts,tenant_id|true|1'",
		"FAKE_FLOCK_RC=1")
	if rc2 != 0 {
		t.Fatalf("被锁挡掉时期望 rc=0（巡检没失效，只是本轮没采样），实得 %d。\n输出:\n%s", rc2, out2)
	}
	second, err := os.Stat(lastrun)
	if err != nil {
		t.Fatalf("心跳文件读不到：%v", err)
	}
	if second.ModTime().After(first.ModTime()) {
		t.Errorf("被锁挡掉的那一轮**刷新了心跳**（mtime %s → %s）。\n"+
			"★ 心跳必须表示「真的问过库」，而不是「脚本被启动过」。",
			first.ModTime(), second.ModTime())
	}
}

// --------------------------------------------------------------- ② 查询上限

// TestOnconflictQueryTimeoutAbortsInsteadOfHanging —— psql 挂死时必须**超时退出**。
//
// ★ 这是 §143.14 查出的**真缺陷**：onconflict 原先**没有任何超时** ⇒
//
//	PG 半死（容器还在 listen、事务卡死）时 `docker exec ... psql` 一直挂着
//	⇒ 脚本持锁不放 ⇒ 之后每一轮都被闸门挡掉 ⇒
//	**这道巡检实质死亡，而它的日志只是「陈旧」，没有任何东西说它死了。**
//	⇒ 判据必须钉「它会自己退出」，而不是「它会给出结论」——
//	  后者需要一个能挂死的 psql 替身，那正是量具的活儿。
func TestOnconflictQueryTimeoutAbortsInsteadOfHanging(t *testing.T) {
	// ★★ 2026-10-21 改判据形态：从「**时钟**」改成「**标记**」。
	//
	//   旧判据是 `elapsed > 4s ⇒ 红`。而它自己的**噪声底**是 1.5~2.45s
	//   （宿主 load 39 时连跑 3 次：1.50 / 2.45 / 1.86s）⇒ 阈值与噪声底只差
	//   **1.6~2.6 倍** ⇒ 负载一高就假红（2026-10-21 当天命中两次：
	//   一次 elapsed=15.48s，症状像「被测脚本没设超时」；真因是夹具的击杀延迟）。
	//   ⇒ 按「阈值型判据先量噪声底」纪律：**判别职责改交给标记**。
	//     替身在「真的跑完了」时写一个标记文件 ⇒ 判据要求「标记不存在」
	//     = 查询**确实被截断了**（与时钟无关）。
	//     这与同族 availability 门（`FAKE_PSQL_COMPLETED` 标记）同款。
	//   ⇒ 时钟只保留一条**宽松的兜底**（「没挂死」），不再承担判别职责。
	//
	//   替身跑 60s（不是 5s）：负载极高时夹具的击杀可能晚到十几秒，
	//   替身太短会把「杀晚了」误判成「没截断」。60s 给足余量；
	//   代价只落在**变异验证**那几轮（没有超时包裹 ⇒ 替身跑满 60s ⇒ 门红）。
	dir := t.TempDir()
	completed := filepath.Join(dir, "psql-completed")
	start := time.Now()
	rc, out, _ := runOnconflictEnv(t, 0,
		"sleep 60\n: > \"$ONCONFLICT_PSQL_COMPLETED\"",
		"ONCONFLICT_QUERY_TIMEOUT=1",
		"ONCONFLICT_PSQL_COMPLETED="+completed)
	elapsed := time.Since(start)

	if _, err := os.Stat(completed); err == nil {
		t.Errorf("psql 替身**跑完了**（完成标记已生成，脚本耗时 %s）—— 查询**没有被截断**。\n"+
			"★ 这正是 §143.14 的死因形态：查询没有上限 ⇒ 一直等一个永远不回来的答案\n"+
			"  ⇒ 脚本持锁不放 ⇒ 之后每一轮都被闸门挡掉 ⇒ 巡检实质死亡，而日志只是「陈旧」。\n"+
			"  期望：到 ONCONFLICT_QUERY_TIMEOUT 就退出（exit 3，本次不出结论）。\n"+
			"  ★ 若宿主负载极高，也可能是**夹具的击杀晚于 60s** —— 那是量具的问题，\n"+
			"    但两种情况都必须红（不能拿「负载高」当理由放过）。", elapsed)
	} else if !os.IsNotExist(err) {
		t.Fatalf("完成标记读不到：%v", err)
	}
	if elapsed > 120*time.Second {
		t.Errorf("psql 挂死时脚本耗时 %s —— 它在**等一个永远不回来的答案**。\n"+
			"★ 这正是 §143.14 的死因形态：无上限的查询 ⇒ 永久占锁 ⇒ 巡检实质死亡。\n"+
			"  期望：到 ONCONFLICT_QUERY_TIMEOUT 就退出（exit 3，本次不出结论）。", elapsed)
	}
	if rc != 3 {
		t.Errorf("psql 挂死时期望 exit 3（量具不可用、不出结论），实得 %d。\n输出:\n%s", rc, out)
	}
	if !strings.Contains(out, "没有返回") && !strings.Contains(out, "rc=124") {
		t.Errorf("超时消息应说清「查询在 Ns 内没有返回（PG 卡死，不是拒连）」，实得:\n%s", out)
	}
}

// TestOnconflictRefusesUnboundedQueryWhenTimeoutMissing —— 没有 timeout(1) 时
// **放弃本轮**，而不是无上限地查。
//
// ★ 为什么必须 fail-closed：无上限地等一个可能永远不回来的答案，
//
//	等价于把自己的心跳也一起停掉（而心跳正是「我还在」的证据）。
//	⇒ 宁可这一轮不出结论（exit 3），也不要悄悄挂死。
func TestOnconflictRefusesUnboundedQueryWhenTimeoutMissing(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-psql")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'should-not-run'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 造一个「肯定找不到 timeout」的 PATH。
	// ★ 只把 PATH 换成临时目录**不够**：开发机/容器里可能装了 coreutils
	//   或 gtimeout ⇒ 这道门会**因机器而异**（一台绿一台红）⇒ 那是假门。
	//   ⇒ 显式只放白名单命令，且白名单里没有 timeout。
	sandbox := filepath.Join(dir, "sandbox")
	if err := os.MkdirAll(sandbox, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"date", "mkdir", "basename", "tr", "sed", "sort", "paste", "awk", "cut"} {
		src, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("构造沙箱时找不到 %s: %v", name, err)
		}
		if err := os.Symlink(src, filepath.Join(sandbox, name)); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command("bash", onconflictScriptPath(t))
	cmd.Env = append(os.Environ(),
		"PSQL_CMD="+fake,
		"PATH="+sandbox,
		"ONCONFLICT_STATE_DIR="+filepath.Join(dir, "state"),
		"LLMGW_LOCK_DIR="+filepath.Join(dir, "lock"),
		"LLMGW_LOCK_SKIP_LOG="+filepath.Join(dir, "skip.log"),
	)
	out, _ := cmd.CombinedOutput()
	rc := cmd.ProcessState.ExitCode()

	if rc != 3 {
		t.Errorf("缺 timeout(1) 时期望 exit 3（放弃本轮、不出结论），实得 %d。\n输出:\n%s", rc, out)
	}
	if strings.Contains(string(out), "should-not-run") {
		t.Errorf("缺 timeout(1) 时仍然去查了库 —— 那就是**无上限地等**。\n输出:\n%s", out)
	}
	if !strings.Contains(string(out), "timeout") {
		t.Errorf("应明说「没有 timeout(1) ⇒ 无法给查询设上限」，实得:\n%s", out)
	}
}

// --------------------------------------------------------------- ③ 调度去重

// TestCanonicalCronSchedulesEachScriptExactlyOnce —— 仓内正典 cron 副本里，
// **每个 /opt/scripts/xxx.sh 只出现在一条非注释行里**。
//
// ★ 这道门针对的是 §143.14 查清的根因形态 ①（重复登记）：
//
//	252 实测 crond 在 10-06 15:54 之后，每分钟对 availability 发**两份**
//	逐字相同的 CMD；同一版文件里 disk-watch 是 **×4**。
//	⇒ 那 9 天里每分钟有一份实例被单实例闸门挡掉（13,043 次 skip），
//	而巡检本体一直健康 ⇒ 表面像「巡检死了」，实为「巡检被跑了两次」。
//
// ★ 为什么是「每个脚本恰好一次」而不是「命令行去重」：
//
//	同一个脚本被排两次且**时刻不同**（如 `* * * * *` 与 `*/1 * * * *`）
//	同样会重复执行，而按命令行文本去重抓不到它。
//
// ★ 为什么必须**剥注释**：正典文件的头部注释里逐个点名了所有脚本
//
//	（「pg17-pg-availability-check.sh 每分钟 可用性心跳」等），
//	不剥就会把它们算成调度 ⇒ 门恒红。
func TestCanonicalCronSchedulesEachScriptExactlyOnce(t *testing.T) {
	b, err := os.ReadFile(cronDriftCanonical)
	if err != nil {
		t.Fatalf("读仓内正典 cron 副本: %v", err)
	}
	count := map[string]int{}
	for i, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue // 注释不是调度
		}
		for _, name := range cronScheduledScriptNames(trimmed) {
			count[name]++
			if count[name] > 1 {
				t.Errorf("脚本 %s 在正典 cron 副本里被排了 %d 次（第 %d 行又出现一次）\n"+
					"★ §143.14 实测（252）：crond 对同一个作业发两份逐字相同的 CMD，\n"+
					"  每分钟有一份被单实例闸门挡掉（9 天累计 13,043 次 skip），\n"+
					"  表面看像「巡检死了」，实为「巡检被跑了两次」——\n"+
					"  而其中一份真的在采样，所以心跳一直是新鲜的 ⇒ 无人察觉。\n"+
					"  ⇒ 正典里每个脚本只允许有一条调度行。",
					name, count[name], i+1)
			}
		}
	}
	if len(count) == 0 {
		t.Fatal("正典 cron 副本里一条调度行都没解析出来 —— 解析形态变了，请同步本判据")
	}
}

// cronScheduledScriptNames 抽出「一条调度行里被排的脚本名」。
// 形态：/opt/scripts/<name>。只认 /opt/scripts/ 前缀，
// 免得把日志路径（/var/log/xxx.log）误当成脚本。
func cronScheduledScriptNames(line string) []string {
	var out []string
	for _, tok := range strings.Fields(line) {
		if !strings.HasPrefix(tok, "/opt/scripts/") {
			continue
		}
		name := strings.TrimPrefix(tok, "/opt/scripts/")
		// 去掉尾随的 shell 元字符与参数分隔符。
		name = strings.Trim(name, "\"'`,;|&")
		if name == "" || !strings.HasSuffix(name, ".sh") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// --------------------------------------------------------------- ④ stderr 失声

// TestSingleInstanceGateMustNotSwallowStderr —— 单实例闸门**不得**把脚本的
// stderr 一起重定向掉。
//
// ★★ 这是本轮（§143.14）查出的**最贵的一个缺陷**，而且它**藏了 9 天**：
//
//	原形态：`&& exec 9>"$_llmgw_lock_file" 2>/dev/null; then`
//	exec **不带命令**时，重定向施加到**当前 shell** ⇒ 从那一刻起
//	脚本的 fd 2 永久指向 /dev/null ⇒ 脚本此后所有写 stderr 的 log()
//	输出**全部消失**。
//	⇒ 252 上自 2026-10-10 部署这道闸门起，**12 个巡检脚本全部失声**：
//	  靠 stderr + `| tee -a <log>` 落盘的那几个，日志从此不再增长
//	  （pg17-pg-availability.log 停在 10-10 16:47、
//	   pg17-onconflict-constraint.log 停在 10-10 16:14）。
//	⇒ 而告警推送还在（notify 是主动调用，不走 stderr）⇒
//	  **「出事时能收到推送、但事后查不到任何证据」** —— 这比没有告警更坏：
//	  它让人以为「已经处理过了」。
//
// ★ 为什么既有门没抓住（这本身是本轮最值得记的一条）：
//
//	既有门**有牙**（它们断言的是「输出里必须有『卡死』二字」这类内容），
//	但 macOS **没有 flock(1)** ⇒ `command -v flock` 失败 ⇒ 整道闸门被
//	fail-open 跳过 ⇒ **闸门分支在开发机上从来没被执行过** ⇒ 那段
//	`exec` 也就从来没在开发机上执行过 ⇒ 缺陷活着。
//	⇒ 本轮补上「假 flock」后，**既有门立刻把这条缺陷抓了出来**
//	  （6 条门当场变红）。⇒ **「补齐量具」比「新写门」更能挖出真缺陷。**
//
// ★ 判据为什么是「读源码」而不是「跑脚本」：
//
//	这 12 个脚本的运行形态各不相同（参数、运行时、判据都不一样），
//	要逐个跑起来造夹具的成本远高于读一行重定向；
//	而这条缺陷的**语义是纯 shell 规则**（exec 无命令时的重定向作用域），
//	由上面那条最小实验（exec 吞 stderr / { } 组不吞）直接坐实。
//	⇒ 行为面由 availability 与 onconflict 两道**行为门**覆盖（它们真的跑脚本）。
func TestSingleInstanceGateMustNotSwallowStderr(t *testing.T) {
	dir := filepath.Join("..", "252-monitor")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 %s: %v", dir, err)
	}
	guarded := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("读 %s: %v", e.Name(), err)
		}
		for _, seg := range scanShell(string(b)).Code {
			trimmed := strings.TrimSpace(seg.Text)
			if trimmed == "" {
				continue // 引号里/注释里的文本不是代码
			}
			if !strings.Contains(trimmed, "exec 9>") {
				continue
			}
			guarded++
			if execFd9SwallowsStderr(trimmed) {
				t.Errorf("%s:%d 单实例闸门把 stderr 一起重定向掉了：%s\n"+
					"★ exec 不带命令时，重定向施加到**当前 shell** ⇒ 脚本此后的\n"+
					"  stderr 永久指向 /dev/null ⇒ 全部 log() 输出消失 ⇒\n"+
					"  「出事时收得到推送、事后查不到任何证据」。\n"+
					"  ⇒ 改成 { exec 9>\"$lock\"; } 2>/dev/null：组的重定向只作用于组本身。",
					e.Name(), seg.Line, trimmed)
			}
		}
	}
	if guarded < 12 {
		t.Errorf("只找到 %d 处单实例闸门 —— 形态变了？本门覆盖的脚本族可能已改名/新增，"+
			"请同步本判据（不要因为「找不到」就以为没有缺陷）。", guarded)
	}
}

// execFd9SwallowsStderr 判断「打开 fd 9 的那条重定向是否把 fd 2 也带走了」。
//
// ★ 关键细节：组形态 `{ exec 9>file; } 2>/dev/null` 里，`2>/dev/null` 在
//
//	`}` **之后** ⇒ 它属于组的外层 redirection，不是 exec 的 ⇒ 合法。
//	所以必须只检查**大括号内部**的那一段。
func execFd9SwallowsStderr(line string) bool {
	idx := strings.Index(line, "exec 9>")
	if idx < 0 {
		return false
	}
	rest := line[idx+len("exec 9>"):]
	if b := strings.Index(rest, "}"); b >= 0 {
		rest = rest[:b]
	}
	return strings.Contains(rest, "2>")
}

// TestSingleInstanceGateScanIgnoresQuotedAndCommentedText —— 「代码位」口径的自证。
//
// ★ 这道门补的是**判据自身的假阳性**（§143.14 实测踩到）：
//
//	本轮在 `ursm-snapshot-health.sh` 的 SQL 注释里写下过一句说明
//	「exec 9>file 2>/dev/null」—— 那段文字位于**双引号字符串**内部
//	⇒ 在 shell 语义里是**惰性文本**（不会被执行），
//	但「原始文本 grep」的门照样报红 ⇒ 门被自己的注释挡住 ⇒ 门失去意义。
//
//	⇒ 口径必须是「**只有引号外的文本才可能被执行**」。
//	⇒ 本门用三段形态钉住这个口径：
//
//	① `#` 行注释里的该模式        ⇒ **不得**进代码位
//	② 双引号字符串里的该模式      ⇒ **不得**进代码位
//	③ 真正的代码行（引号外）      ⇒ **必须**进代码位
//
//	★ 第③条是「量具自证」：只有①②而没有③，扫描器可能整体失效（恒空）。
func TestSingleInstanceGateScanIgnoresQuotedAndCommentedText(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantInCode bool
	}{
		{"行注释里的模式", "# 以前写的是 exec 9>file 2>/dev/null\n", false},
		{"双引号 SQL 注释里的模式", "SQL=\"\n-- 旧写法 exec 9>file 2>/dev/null 会吞 stderr\n\"\n", false},
		{"真正的闸门代码行", "  && exec 9>\"$lock\" 2>/dev/null; then\n", true},
		{"修好的组形态", "  && { exec 9>\"$lock\"; } 2>/dev/null; then\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code := ""
			for _, seg := range scanShell(tc.content).Code {
				code += seg.Text
			}
			got := strings.Contains(code, "exec 9>")
			if got != tc.wantInCode {
				t.Errorf("%s：代码位里%s「exec 9>」（期望 contains=%v）。代码位=<<<%s>>>",
					tc.name, map[bool]string{true: "含有", false: "不含"}[got], tc.wantInCode, code)
			}
		})
	}
	// ★ 再钉一次「真缺陷仍被抓」：把闸门退回旧形态，代码位里必须能检出
	//   「exec 9> 之后到 } 之前有 2>」。
	bad := scanShell("  && exec 9>\"$lock\" 2>/dev/null; then\n").Code
	if len(bad) == 0 {
		t.Fatal("扫描器没有产出代码位 —— 量具坏了")
	}
	if !execFd9SwallowsStderr(bad[0].Text) {
		t.Errorf("旧形态闸门没有被判定为「吞 stderr」，判据失效。代码位=<<<%s>>>", bad[0].Text)
	}
	good := scanShell("  && { exec 9>\"$lock\"; } 2>/dev/null; then\n").Code
	if len(good) == 0 {
		t.Fatal("扫描器没有产出代码位 —— 量具坏了")
	}
	if execFd9SwallowsStderr(good[0].Text) {
		t.Errorf("组形态被误判为「吞 stderr」。代码位=<<<%s>>>", good[0].Text)
	}
}
