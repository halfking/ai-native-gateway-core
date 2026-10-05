package ursm_test

// 「能删掉生产数据库容器的脚本」的两道门（2026-10-05 新增）
//
// 存在的原因还是那一次事故：2026-10-05 15:46 我在一条复合 ssh 命令里
// 顺手写了容器删除子命令，把生产的 pg-252-pg17 删了，停机约 3~4 分钟。
//
// ★ 注意这两道门**并不能**防止那次事故本身：它发生在我自己的命令行里，
//   不在任何仓库文件里。仓库里没有的东西，门看不见。
//   那这两道门防的是什么？防的是**同一类错误被写进仓库并部署**：
//   把 `podman rm` 顺手写进某个巡检脚本的清理分支，然后被 cron 每 10 分钟
//   执行一次。那比一次手误严重得多，而且会持续发生。
//
// ★ 也说明为什么门要钉「这个脚本会被调度」这个**结构事实**，
//   而不是去猜「这个脚本危不危险」：危险是主观判断，会随人变化；
//   「它在不在 cron 里」是可验证的事实，且一旦错了后果不可逆。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	manualDir       = "../252-monitor/manual"
	manualStartName = "pg17-start.sh"
	monitorDirName  = "../252-monitor"
)

// containerDestroyer 匹配「能让一个容器消失或停摆」的动作。
//
// 词边与后续空白都钉住：`grep -E "docker rm"` 会把 `docker rmi`、
// 以及注释里的 "docker rm" 一并算上，误报会让人养成无视这道门的习惯。
var containerDestroyer = regexp.MustCompile(`\b(podman|docker)\s+(rm|rmi|stop|kill)\b`)

// TestManualStartScriptExistsInRepo —— 它曾经只活在 252 上，没有任何版本控制。
// 没有正典就没有 diff，一次静默失败可以无限期地不被 review。
func TestManualStartScriptExistsInRepo(t *testing.T) {
	p := filepath.Join(manualDir, manualStartName)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 %s 失败: %v\n"+
			"★ 恢复生产数据库的脚本必须进仓库。2026-10-05 它的 pgvector 恢复步骤"+
			"静默失败，而它不在任何版本控制里 ⇒ 无人 review、无人 diff。", p, err)
	}
	if !strings.Contains(string(b), "pg-252-pg17") {
		t.Errorf("%s 里看不到目标容器名 —— 确认这份正典确实是那台机器的脚本", p)
	}
	if out, err := exec.Command("bash", "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("bash -n 失败: %v\n%s", err, out)
	}
}

// TestManualStartScriptIsNeverScheduled —— 它做的事是 stop + rm 一个生产库容器。
// 一旦被写进 etc.cron.d.pg17，就是每周期删一次生产数据库。
func TestManualStartScriptIsNeverScheduled(t *testing.T) {
	raw, err := os.ReadFile(cronFile)
	if err != nil {
		t.Fatalf("读 %s: %v", cronFile, err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, manualStartName) {
			t.Errorf("%s 第 %d 行调度了 %s：\n%s\n"+
				"★ 它会 `podman stop` + `podman rm` 一个生产数据库容器。"+
				"这是**手动**脚本，进 cron 等于定时删库。",
				cronFile, i+1, manualStartName, line)
		}
	}
}

// TestNoScheduledMonitorCanDestroyAContainer 是上面那道门的推广形态：
// 与其只盯着这一个文件名，不如断言整个「会被调度的那一层」都不含删除动词。
// manual/ 子目录不在候选集内 —— 按目录划界，而不是靠逐个文件名豁免。
func TestNoScheduledMonitorCanDestroyAContainer(t *testing.T) {
	entries, err := os.ReadDir(monitorDirName)
	if err != nil {
		t.Fatalf("读 %s: %v", monitorDirName, err)
	}
	// 正向自证：候选集不能空。目录读错 / 筛选条件失效会让下面全部恒真。
	var scripts []string
	for _, e := range entries {
		if e.IsDir() || !cronJobName.MatchString(e.Name()) {
			continue
		}
		scripts = append(scripts, e.Name())
	}
	if len(scripts) < 8 {
		t.Fatalf("被调度的脚本候选只有 %d 个 (%v) —— 筛选条件失效，这道门正在空转",
			len(scripts), scripts)
	}
	for _, name := range scripts {
		b, err := os.ReadFile(filepath.Join(monitorDirName, name))
		if err != nil {
			t.Fatalf("读 %s: %v", name, err)
		}
		if m := containerDestroyer.Find(b); len(m) > 0 {
			t.Errorf("被 cron 调度的 %s 里出现容器删除动作 %q。\n"+
				"★ 巡检脚本会被周期执行；一个顺手写进去的 `podman rm` 会在无人值守时"+
				"反复停掉/删掉生产数据库容器。破坏性动作请只放在 manual/ 下的手动脚本里。",
				name, m)
		}
	}
}
