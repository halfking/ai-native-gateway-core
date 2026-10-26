package ursm_test

// 252 的 **cron 调度**漂移门（2026-10-19，§143.12）
//
// # 这道门补的是「第三种漂移」
//
// 仓里已有两道门：
//
//	deployed_script_drift_test.go   盯「**脚本文件**线上是不是仓内那一版」
//	cron_registration_test.go       盯「**仓内正典 cron 副本**里有没有这一行」
//
// 两道门合起来仍然看不见：**仓内有、线上没有**。
//
// 2026-10-19 实测（就在 §143.12 这一轮）：
// `pg17-duplicate-index-check.sh` 早已部署到 252 —— 文件在 /opt/scripts/ 里、
// md5 与仓内逐字一致 ⇒ `deployed-252.tsv` 记的是 in-sync、
// 脚本登记门也全绿 —— 但 /etc/cron.d/pg17 上**没有它的调度行**。
// ⇒ 它**从未被运行过**：重复索引这一整类（最贵一对是
//   session_summaries.pkey / session_summaries.session_key_uidx，
//   定义**逐字相同**、各 71MB、而该表持续写入）长期不可见，
//   而巡检照常跑、照常打日志、照常报绿，**无人察觉**。
//
// # 这道门为什么能挡住它
//
// 本台账（`deployed-cron-252.tsv`）记的是**整文件 md5**，不是「有效行 md5」：
// 仓内正典副本 `scripts/252-monitor/etc.cron.d.pg17` 是 252 上 cron 文件的**来源**，
// 注释头也一并对齐 ⇒ 「整文件一致」是可达成的状态。
//
// 关键性质：只要
//
//	「线上 md5」 == 「仓内正典 md5」 == 「仓内文件实际 md5」
//
// 三者成立 ⇒ 线上文件与仓内正典**逐字相同** ⇒ 仓内有的每一条调度，
// 线上必然也有 ⇒ 「仓内有、线上没有」被**结构性排除**，
// 而不是靠人记得去更新。
//
// ★ 选「整文件 md5」而不是「有效行 md5」的理由：
//   两者都能挡住上面那个形态（有效行集合相等即足够），
//   但整文件更严 —— 它连「线上跑的是旧版注释/旧版调度顺序」都不放过，
//   而 252 的 cron 文件正是**仓内正典的一个旧修订**（2026-10-06 之前的版本），
//   只比有效行会漏掉这个事实。
//
// # 变异验证（本门的三条变异都实测转红）
//
//	M1 改仓内正典副本的内容（仓内 md5 变） ⇒ ① 红
//	M2 把 drift 行的状态改成 in-sync            ⇒ ② 红
//	M3 删掉台账行 / 整个台账变空              ⇒ ③ 红
//
// ⇒ 三个方向都有**能红的路径**，不是恒真门。

import (
	"os"
	"strings"
	"testing"
)

const (
	cronDriftManifestPath = "../252-monitor/deployed-cron-252.tsv"
	cronDriftCanonical    = "../252-monitor/etc.cron.d.pg17"
)

// cronDriftRow 是 cron 调度台账的一行。
type cronDriftRow struct {
	Path     string
	Deployed string
	Repo     string
	Status   string
	Reason   string
}

func readCronDriftManifest(t *testing.T) []cronDriftRow {
	t.Helper()
	b, err := os.ReadFile(cronDriftManifestPath)
	if err != nil {
		t.Fatalf("读 cron 调度台账 %s: %v", cronDriftManifestPath, err)
	}
	var rows []cronDriftRow
	for i, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		f := strings.Split(trimmed, "\t")
		if len(f) < 4 {
			t.Fatalf("%s:%d 字段不足 4 个（cron 路径/线上md5/仓内md5/状态）：%q",
				cronDriftManifestPath, i+1, trimmed)
		}
		r := cronDriftRow{Path: f[0], Deployed: f[1], Repo: f[2], Status: f[3]}
		if len(f) > 4 {
			r.Reason = strings.TrimSpace(strings.Join(f[4:], "\t"))
		}
		rows = append(rows, r)
	}
	return rows
}

// TestCronDriftManifestRepoChecksumMatchesDisk —— 台账的「仓内正典 md5」必须等于文件的实际 md5。
//
// ★ 这是承重断言：改了仓内正典 cron 副本却不更新台账 ⇒ 这里红。
func TestCronDriftManifestRepoChecksumMatchesDisk(t *testing.T) {
	rows := readCronDriftManifest(t)
	if len(rows) == 0 {
		t.Fatal("cron 调度台账为空")
	}
	for _, r := range rows {
		got := md5OfFile(t, cronDriftCanonical)
		if got != r.Repo {
			t.Errorf("仓内正典 cron 副本已变（台账记 %s，实际 %s）\n"+
				"★ 这正是本门要拦的事：改了调度却不部署、也不更新台账 ⇒ "+
				"线上继续跑旧调度（可能少了某条 ⇒ 那条巡检**从未运行过**）而**无人察觉**。\n"+
				"⇒ 请同时做两件事：部署到 252，并把台账的「仓内正典 md5」与「线上 md5」一起更新。",
				r.Repo, got)
		}
	}
}

// TestCronDriftStatusIsConsistentWithBothChecksums —— 状态与两个 md5 必须自洽。
//
//	in-sync ⇒ 线上 md5 == 仓内正典 md5
//	drift   ⇒ 两者**必须**不同且**必须**写清原因
func TestCronDriftStatusIsConsistentWithBothChecksums(t *testing.T) {
	rows := readCronDriftManifest(t)
	if len(rows) == 0 {
		t.Fatal("cron 调度台账为空")
	}
	for _, r := range rows {
		switch r.Status {
		case "in-sync":
			if r.Deployed != r.Repo {
				t.Errorf("%s 标成 in-sync，但线上 md5(%s) != 仓内正典 md5(%s) ⇒ "+
					"要么状态写错了，要么线上其实是一份旧修订（**可能少了某条调度**）。\n"+
					"★ 「标成同步」比「标成漂移」危险：它让漂移**不可见**。",
					r.Path, r.Deployed, r.Repo)
			}
		case "drift":
			if r.Deployed == r.Repo {
				t.Errorf("%s 标成 drift，但两个 md5 相同 (%s) ⇒ 状态过期了（可能已部署却忘了改状态）", r.Path, r.Repo)
			}
			if r.Reason == "" {
				t.Errorf("%s 标成 drift 却没有写原因 ⇒ 漂移变成默默发生的事。\n"+
					"★ 请写清：线上与仓内差在哪（**逐条列出少/多的调度**）、为什么不部署、何时处置。",
					r.Path)
			}
		default:
			t.Errorf("%s 的状态 %q 非法（只能是 in-sync / drift）", r.Path, r.Status)
		}
	}
}

// TestCronDriftManifestIsNotEmpty —— 台账不能被清空。
//
// ★ 为什么这条不是废话：一道门如果台账空了就会**全部跳过**，
//   变成恒真门。删行（而不是把状态改掉并写明原因）会让漂移重新变成不可见。
func TestCronDriftManifestIsNotEmpty(t *testing.T) {
	rows := readCronDriftManifest(t)
	if len(rows) == 0 {
		t.Fatalf("cron 调度台账 %s 里一条记录都没有 —— "+
			"这道门正在空转（台账空了，所有断言都会跳过）。\n"+
			"★ 不要直接删行：请把状态改成 in-sync/drift 并写清原因。",
			cronDriftManifestPath)
	}
	for _, r := range rows {
		if r.Path == "" {
			t.Errorf("台账里有一行的路径为空")
		}
		if r.Reason == "" {
			t.Errorf("%s 没有写原因（哪怕是 in-sync 也请写一句「何时部署/为何同步」）", r.Path)
		}
	}
}

// TestCronCanonicalSchedulesEveryLedgeredScript —— 仓内正典必须为**每个**已登记脚本排一条调度。
//
// ★ 这条与 `deployed_script_drift_test.go` 的集合相等门**方向相反**、互为补充：
//   那条比的是「仓内 cron 里有的 == 台账里有的」（防幽灵）；
//   这条比的是「台账里的每个脚本 == 仓内 cron 里都有它的调度行」（防漏排）。
//
//   为什么还要有它：脚本台账能记「文件已部署且与仓内一致」，
//   但**文件在 /opt/scripts/ 里 ≠ 它被调度**。
//   §143.12 实测：pg17-duplicate-index-check.sh 正是这种形态
//   （文件在、md5 一致、台账 in-sync，却从没被调度过）。
//   ⇒ 本门在**仓内**就能把「已登记却没排调度」的点出来。
func TestCronCanonicalSchedulesEveryLedgeredScript(t *testing.T) {
	scriptRows := readDriftManifest(t)
	canonical := md5OfFile(t, cronDriftCanonical) // 顺带确保文件可读
	_ = canonical

	b, err := os.ReadFile(cronDriftCanonical)
	if err != nil {
		t.Fatalf("读仓内正典 cron 副本: %v", err)
	}
	content := string(b)

	for _, r := range scriptRows {
		// 只认「真正被调度」的形态：/opt/scripts/xxx.sh 出现在一条**非注释**行里。
		// 注释里提到脚本名不算调度。
		scheduled := false
		for _, line := range strings.Split(content, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if strings.Contains(trimmed, "/opt/scripts/"+r.Name) {
				scheduled = true
				break
			}
		}
		if !scheduled {
			t.Errorf("已登记的 %s 在仓内正典 cron 副本里**没有调度行** ⇒ "+
				"部署到 252 之后它也不会被运行（§143.12 实测：pg17-duplicate-index-check.sh "+
				"正是这个形态：文件在、md5 一致、台账 in-sync，却从没被调度过）。", r.Name)
		}
	}
}
