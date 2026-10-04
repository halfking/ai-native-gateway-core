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
	for _, task := range tasks {
		if strings.Contains(task, "ursm-snapshot-") && strings.Contains(task, "/dev/null 2>&1") {
			t.Errorf("graded-exit monitor is silenced by /dev/null:\n%s\n"+
				"退出码 0/1/3 分级全被丢弃，等于没巡检", task)
		}
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
