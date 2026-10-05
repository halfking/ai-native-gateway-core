package ursm_test

// 252 cron 正典文件与巡检脚本的一致性门（2026-10-04）
//
// 背景：scripts/252-monitor/etc.cron.d.pg17 的头部写着同步契约 ——
// 「本文件必须与服务器 /etc/cron.d/pg17 逐行一致。部署方式是**整文件覆盖**，
//   不是合并 —— 少一条就会在覆盖时把生产任务删掉。」
//
// 这条契约已经在 2026-10-03 与 2026-10-04 各违反过一次。第二次（本次）
// 是：服务器上 `17 * * * * ursm-snapshot-health.sh` 一直在跑，仓库模板里
// 却没有 —— **下一次整文件覆盖就会把这条每小时巡检从生产删掉**。
//
// 这道门只能守住仓库侧：模板与脚本目录的一致。它**抓不到**"模板有、
// 服务器没有"（那需要 ssh diff，只能靠部署前人工 diff）。两者的分工：
//   · 本门：新增巡检脚本忘了在模板里注册（最常见、且必然漏）
//   · 人工：部署前 `diff <(ssh 252 cat /etc/cron.d/pg17) etc.cron.d.pg17`

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const cronFile = "../252-monitor/etc.cron.d.pg17"

// 候选集按**仓库既有的命名前缀约定**划定：cron 任务一律叫 pg17-* 或 ursm-*。
//
// ★ 初版把该目录下所有 .sh/.py 都当候选，结果把 browser-{cors,ui,www}-test.py
//
//	报成"没注册" —— 那是浏览器测试夹具，本来就不是 cron 任务。
//	教训同「判据红了先怀疑判据」：门红了，先问门的候选集取对没有。
//	为什么不改成显式白名单：白名单本身就是"靠人维护就会漂"的那类东西
//	（writer.go 注释原话），而前缀约定已经在用了。
//
// 明确排除：notify.sh 是 webhook 库函数（被调用而非被调度）；
//
//	etc.cron.d.pg17 是正典文件本身。
var cronJobName = regexp.MustCompile(`^(pg17|ursm)-.*\.(sh|py)$`)

// cronTaskLine 匹配一条真正的 cron 任务行（去掉注释与空行后，
// 首字段是分钟数，且行里含 /opt/scripts/）。
var cronTaskLine = regexp.MustCompile(`^\s*\S+\s+\S+\s+\S+\s+\S+\s+\S+\s+root\s+`)

func readCron(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(cronFile)
	if err != nil {
		t.Fatalf("read cron file: %v", err)
	}
	return strings.Split(string(b), "\n")
}

// TestEveryMonitorScriptIsRegisteredInCron —— 核心门。
// 本目录里每个可执行巡检脚本，都必须在正典 cron 文件里有一行调度它。
func TestEveryMonitorScriptIsRegisteredInCron(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "252-monitor"))
	if err != nil {
		t.Fatalf("read monitor dir: %v", err)
	}
	lines := readCron(t)
	body := strings.Join(lines, "\n")

	var scripts []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !cronJobName.MatchString(name) {
			continue
		}
		scripts = append(scripts, name)
	}
	// 正向自证：候选集不能为空。空候选会让下面所有断言恒真。
	if len(scripts) < 8 {
		t.Fatalf("only %d cron-job candidates found (%v) — if the naming convention moved, "+
			"this gate silently stops guarding anything", len(scripts), scripts)
	}

	for _, s := range scripts {
		if !strings.Contains(body, "/opt/scripts/"+s) {
			t.Errorf("monitor script %s has no cron entry in %s.\n"+
				"★ 这不是「忘了更新文档」这么轻：该文件是**整文件覆盖**部署的，"+
				"漏一条就等于下次部署把这条生产任务删掉（header 里已记两次同类事故）。",
				s, cronFile)
		}
	}
}

// 反向自证：门不能因为"什么都匹配"而恒真。确认它真的读到了多条任务行，
// 且这些行不是注释。
func TestCronFileActuallyHasTaskLines(t *testing.T) {
	lines := readCron(t)
	var tasks []string
	for _, l := range lines {
		if cronTaskLine.MatchString(l) {
			tasks = append(tasks, l)
		}
	}
	if len(tasks) < 7 {
		t.Fatalf("parsed only %d task lines:\n%v\n"+
			"the gate is reading a file it doesn't understand — fix the gate before trusting it",
			len(tasks), tasks)
	}
	// 分级退出的巡检不能被 >/dev/null 2>&1 吞掉：那等于没巡检。
	//
	// ★ 为什么是显式清单而不是像上面那样用命名前缀约定：
	//   前缀约定（"所有 pg17-*/ursm-*"）在这里会**误报**。
	//   pg17-disk-watch.sh 与 pg17-emergency-cleanup.sh 确实被 >/dev/null 静默，
	//   但它们是**正确的** —— 告警走脚本内部的 notify.sh 推送，退出码本来就没
	//   承载信息。一条会误报的规则比没有规则更糟：它会训练人无视这道门，
	//   等它真的抓到问题的那天也没人看了。
	//   ⇒ 分界线是「这个巡检的结论是否**只**靠退出码 + stdout 传递」。
	//
	// ★ 事故背景：2026-10-05 PG 停机 3~4 分钟零告警。告警通道退化成
	//   「什么都收得到但什么都不说」和「完全没有通道」在事后无法区分 ——
	//   所以每一道新巡检上线时，都要顺手确认它自己的通道没被静默。
	for _, task := range tasks {
		for _, name := range gradedExitMonitors {
			if strings.Contains(task, "/opt/scripts/"+name) && strings.Contains(task, "/dev/null 2>&1") {
				t.Errorf("graded-exit monitor %s is silenced by /dev/null:\n%s\n"+
					"退出码 0/1/3 分级全被丢弃，等于没巡检", name, task)
			}
		}
	}
}

// gradedExitMonitors = 结论只经由「退出码 + stdout」传递的巡检。
// 这几个脚本被 >/dev/null 2>&1 静默即等于从生产上把它摘掉。
var gradedExitMonitors = []string{
	"ursm-snapshot-health.sh",
	"ursm-snapshot-payload-bloat.sh",
	"pg-table-bloat-check.sh",
	"pg17-pg-availability-check.sh",
}

// TestGradedExitMonitorListStillMatchesDisk —— 上面那份清单本身会腐。
// 判据的门也可能是判据红的理由：清单空了、或某个成员已经改名不存在，
// 断言会安静地变成「什么都没有要查」。所以自证它非空且成员都还在。
func TestGradedExitMonitorListStillMatchesDisk(t *testing.T) {
	if len(gradedExitMonitors) < 4 {
		t.Fatalf("gradedExitMonitors only has %d entries (%v) — the silencing check above "+
			"has quietly stopped guarding anything", len(gradedExitMonitors), gradedExitMonitors)
	}
	for _, name := range gradedExitMonitors {
		if _, err := os.Stat(filepath.Join("..", "252-monitor", name)); err != nil {
			t.Errorf("gradedExitMonitors lists %q but it is not in scripts/252-monitor/: %v\n"+
				"★ 删名字会放过静默，删脚本会放过没接线。两个方向都要有人发现。", name, err)
		}
	}
}

// TestHeartbeatCronRunsEveryMinute —— 这道门是被一次**部署后才暴露**的错误逼出来的。
//
// 我把心跳写成 `59 * * * *`，本意是「每分钟但错开整点」。
// ★ `59 * * * *` 的真实语义是「**每小时**的第 59 分钟跑一次」——
//   cron 的分钟字段无法既表达「每分钟」又表达「错开 :00」；
//   想要错开就只能牺牲频率，于是它从每分钟退化成了每小时。
//   而它**看起来完全正常**：上线后确实在 00:59 跑过一次，
//   log 建好了、心跳文件更新了，我据此写下「cron 已验证触发」。
//   ⇒ 「跑过一次」不等于「按预期频率在跑」。第 11 分钟才发现它没再跑。
//
// 所以这道门钉的不是「这一行存在」（那由上面的注册门负责），
// 而是**它的分钟字段**——频率是这道巡检的全部意义所在，
// 而频率恰恰是「跑一次」看不出来的那部分。
func TestHeartbeatCronRunsEveryMinute(t *testing.T) {
	const script = "pg17-pg-availability-check.sh"
	var found int
	for _, line := range readCron(t) {
		if !cronTaskLine.MatchString(line) || !strings.Contains(line, script) {
			continue
		}
		found++
		fields := strings.Fields(line)
		if len(fields) < 6 {
			t.Errorf("调度 %s 的那行字段不足 6 个，解析不了频率：\n%s", script, line)
			continue
		}
		if minute := fields[0]; minute != "*" {
			t.Errorf("%s 的 cron 分钟字段是 %q，期望 `*`（每分钟）。\n"+
				"★ `59 * * * *` 的真实语义是**每小时**跑一次，不是「每分钟但错开整点」——"+
				"cron 的分钟字段无法同时表达这两件事。而这种错误上线后仍然"+
				"「看起来正常」：它会真的跑一次，只是再也不会跑第二次。\n"+
				"频率是这道巡检的全部意义，且恰恰是只看「跑过没有」看不出来的那部分。\n%s",
				script, minute, line)
		}
	}
	// 正向自证：候选行必须存在，否则上面全部断言空转。
	if found != 1 {
		t.Fatalf("在 %s 里找到 %d 行调度 %s，期望恰好 1 行", cronFile, found, script)
	}
}

// 同步契约的另一半：模板头部必须留着这条警示。
// 它是被违反过两次才写下来的，删掉它等于删掉"为什么会出事"的记录。
func TestCronFileKeepsTheSyncContractWarning(t *testing.T) {
	lines := readCron(t)
	head := strings.Join(lines[:min(20, len(lines))], "\n")
	// 三句都要在：缺任何一句，这条警示就不再完整。
	// ★ 初版只查后两句，于是"删掉『同步契约』那一行"这条变异是绿的 ——
	//   门没在查我以为它查的那句。判据的门也可能是判据红的理由。
	for _, phrase := range []string{"同步契约", "整文件覆盖", "少一条"} {
		if !strings.Contains(head, phrase) {
			t.Fatalf("cron header no longer says %q — that warning block is the only "+
				"record of why a missing line deletes a production job (it was violated twice)", phrase)
		}
	}
}

// TestAvailabilityHeartbeatRunsEveryMinute —— R48-C1（2026-10-06）。
//
// 心跳行曾被写成 `59 * * * *`：标准 5 字段 cron 里那是「每小时 HH:59 一跳」，
// 而全文件头部、runbook §10.31.2、提交信息都按「每分钟」设计——三方一致地错。
// 3~4 分钟的停机（那正是这条心跳存在的理由）有 ~95% 概率一跳都赶不上。
// cron 调度频率语义此前不在任何防线上（见 monitor_script_exec_gate 的盲区注记），
// 这道门把「心跳行分钟字段必须是 *」钉死。
func TestAvailabilityHeartbeatRunsEveryMinute(t *testing.T) {
	lines := readCron(t)
	var heartbeat []string
	for _, l := range lines {
		if !cronTaskLine.MatchString(l) || !strings.Contains(l, "/opt/scripts/pg17-pg-availability-check.sh") {
			continue
		}
		heartbeat = append(heartbeat, l)
	}
	if len(heartbeat) != 1 {
		t.Fatalf("expected exactly 1 cron line for pg17-pg-availability-check.sh, got %d:\n%v",
			len(heartbeat), heartbeat)
	}
	fields := strings.Fields(heartbeat[0])
	if fields[0] != "*" {
		t.Errorf("availability heartbeat minute field is %q, want \"*\".\n"+
			"★ `59 * * * *` 一类写法是「每小时一跳」——心跳失去意义（R48-C1 事故原型）。", fields[0])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
