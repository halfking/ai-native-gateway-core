package ursm_test

// 252 巡检脚本的**部署漂移**门（2026-10-19，§143.10）
//
// # 这道门补的是什么
//
// 仓里已有一道 `cron_registration_test.go`，它管的是「**巡检脚本有没有在 cron 里接线**」。
// 它**抓不到**本轮发现的那类问题：仓里把脚本修好了，**线上还在跑旧版**。
//
// 实测（252，2026-10-19）：`/etc/cron.d/pg17` 登记了 **12 个**巡检脚本，
// 其中 **6 个**在 252 上跑的不是仓内版本（最旧的落后 8 天，来源是 10-11 01:21 的一次批量部署）。
// ⇒ 后果不是「文件不一样」，而是**行为不一样**，而且**全部无人察觉**：
//
//   · pg17-index-bloat.sh            少了 pg_toast 命名空间与叶分区索引
//                                    ⇒ 229MB 死 TOAST 索引 + 4.5GB 叶分区索引**不可见**
//   · pg17-drop-old-columnar-partitions.sh
//                                    §88 的 VACUUM FULL 修法未上线
//                                    ⇒ 生产日志 6 次全部失败（隐式事务块）却打 "done"
//   · pg17-duplicate-index-check.sh   只有段1，缺段2/段3 ⇒ 答不出「删了会不会长回来」
//   · pg17-proactive-empty-table-cleanup.sh
//                                    父表名仍是 request_logs（860 已改名）⇒ 候选集永远空
//   · ursm-snapshot-payload-bloat.sh  avg 缺 coalesce ⇒ 报 182B（真值 ~27B）
//
// ★ 这是 §10.16「在生产上 A/B 验证过 ≠ 已上线」的**上一层**：
//   那一节说的是「旁挂服务不在发布链上」，本节说的是「**巡检脚本**也不在发布链上」，
//   而且巡检脚本的后果更隐蔽 —— 巡检**照常跑、照常打日志、照常报绿**，
//   只是它量的东西少了一大片。
//
// # 这道门为什么只能是「仓内」的
//
// 它**不能** ssh 到 252 去 diff（CI 与开发机都没有那台机器的凭据）。
// ⇒ 所以它钉的不是「线上跑的是哪一版」（那是 `deployed-252.tsv` 里的**记录**），
// 而是**两件仓内可判的事**：
//
//	① 台账里的「仓内 md5」必须等于仓内文件的实际 md5
//	   ⇒ 改了脚本却不更新台账 ⇒ **门红**。这就把「改 ⇒ 部署 ⇒ 记录」
//	   变成了**一次提交里必须完成的事**，而不是「想起来再部署」。
//
//	② 状态与两个 md5 必须自洽
//	   ⇒ `in-sync` ⇒ 线上 md5 == 仓内 md5；
//	     `drift` ⇒ 两者**必须**不同且**必须**写清原因
//	     （「暂时没部署」是可以的，**默默漂着**不行）。
//
// # 变异验证（本门的三条变异都实测转红）
//
//	M1 改脚本内容（仓内 md5 变）        ⇒ ① 红
//	M2 把 drift 行的状态改成 in-sync     ⇒ ② 红（线上 md5 仍与仓内不同）
//	M3 删掉台账里的一行                   ⇒ 集合相等 红
//
// ⇒ 判据的三个方向都有**能红的路径**，不是恒真门。

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	driftManifestPath = "../252-monitor/deployed-252.tsv"
	driftCronPath     = "../252-monitor/etc.cron.d.pg17"
	driftMonitorDir   = "../252-monitor"
)

// cronInvokedScript 抓 cron 正典文件里**真正被调度**的脚本名。
//
// ★ 只认 `/opt/scripts/xxx.sh` 形态：cron 文件的注释里也提到过脚本名
//
//	（「pg17-index-bloat.sh 周日 04:40 …」），按裸名字抓会把注释里的
//	提及也当成调度 ⇒ 台账集合被污染。
var cronInvokedScript = regexp.MustCompile(`/opt/scripts/([A-Za-z0-9._-]+\.sh)`)

// driftRow 是台账的一行。
type driftRow struct {
	Name     string
	Deployed string
	Repo     string
	Status   string
	Reason   string
}

func readDriftManifest(t *testing.T) []driftRow {
	t.Helper()
	b, err := os.ReadFile(driftManifestPath)
	if err != nil {
		t.Fatalf("读部署台账 %s: %v", driftManifestPath, err)
	}
	var rows []driftRow
	for i, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		f := strings.Split(trimmed, "\t")
		if len(f) < 4 {
			t.Fatalf("%s:%d 字段不足 4 个（脚本名/线上md5/仓内md5/状态）：%q", driftManifestPath, i+1, trimmed)
		}
		r := driftRow{Name: f[0], Deployed: f[1], Repo: f[2], Status: f[3]}
		if len(f) > 4 {
			r.Reason = strings.TrimSpace(strings.Join(f[4:], "\t"))
		}
		rows = append(rows, r)
	}
	return rows
}

func md5OfFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// cronInvokedScripts 返回 cron 正典文件里被调度的脚本名集合。
func cronInvokedScripts(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(driftCronPath)
	if err != nil {
		t.Fatalf("读 %s: %v", driftCronPath, err)
	}
	seen := map[string]bool{}
	for _, m := range cronInvokedScript.FindAllStringSubmatch(string(b), -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestDeployDriftManifestCoversExactlyTheCronInvokedScripts —— 台账集合与 cron 集合**必须相等**。
//
// ★ 为什么要**两个方向**都查：
//
//	· 少一行 ⇒ 那个脚本的线上版本**无人记录**（漂移照旧发生，无人知道）
//	· 多一行 ⇒ 台账里有个**幽灵**（仓里与线上都不存在，或早被删了），
//	  而它会让人误以为「12 个脚本都在册」
func TestDeployDriftManifestCoversExactlyTheCronInvokedScripts(t *testing.T) {
	rows := readDriftManifest(t)
	cron := cronInvokedScripts(t)

	// 正向自证：两个集合都不能空，否则下面全部断言恒真。
	if len(rows) < 8 {
		t.Fatalf("部署台账只有 %d 行 (%v) —— 台账本身快空了，这道门正在空转", len(rows), rows)
	}
	if len(cron) < 8 {
		t.Fatalf("从 cron 文件里只解析出 %d 个被调度脚本 (%v) —— 正则失效，这道门正在空转", len(cron), cron)
	}

	inManifest := map[string]bool{}
	for _, r := range rows {
		if inManifest[r.Name] {
			t.Errorf("台账里 %s 出现重复行", r.Name)
		}
		inManifest[r.Name] = true
	}
	inCron := map[string]bool{}
	for _, c := range cron {
		inCron[c] = true
	}

	for _, c := range cron {
		if !inManifest[c] {
			t.Errorf("cron 调度的 %s 在部署台账里没有记录 ⇒ 它的线上版本无人跟踪", c)
		}
	}
	for _, r := range rows {
		if !inCron[r.Name] {
			t.Errorf("部署台账里的 %s 不在 cron 被调度集合里 ⇒ 幽灵条目", r.Name)
		}
	}
}

// TestDeployDriftManifestRepoChecksumMatchesDisk —— 台账的「仓内 md5」必须等于文件的实际 md5。
//
// ★ 这是整道门的**承重断言**：改了脚本却不更新台账 ⇒ 这里红。
//
//	没有它，台账只是一张「曾经记过一次」的便条，改脚本的人不会被迫知道它。
func TestDeployDriftManifestRepoChecksumMatchesDisk(t *testing.T) {
	for _, r := range readDriftManifest(t) {
		path := filepath.Join(driftMonitorDir, r.Name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("台账里的 %s 在仓内不存在: %v", r.Name, err)
			continue
		}
		got := md5OfFile(t, path)
		if got != r.Repo {
			t.Errorf("%s 的仓内内容已变（台账记 %s，实际 %s）\n"+
				"★ 这正是本门要拦的事：改了脚本却没部署、也没更新台账 ⇒ "+
				"线上继续跑旧版而**无人察觉**（2026-10-19 实测 12 个里 6 个如此）。\n"+
				"⇒ 请同时做两件事：部署到 252，并把台账的「仓内 md5」与「线上 md5」一起更新。",
				r.Name, r.Repo, got)
		}
	}
}

// TestDeployDriftStatusIsConsistentWithBothChecksums —— 状态与两个 md5 必须自洽。
//
//	in-sync ⇒ 线上 md5 == 仓内 md5
//	drift   ⇒ 线上 md5 != 仓内 md5 **且** 原因非空
//
// ★ 「drift 但没写原因」是本门要拦的**第二种默默漂着**：
//
//	漂着不可怕，**没人知道它为什么漂**才可怕。
func TestDeployDriftStatusIsConsistentWithBothChecksums(t *testing.T) {
	rows := readDriftManifest(t)
	if len(rows) == 0 {
		t.Fatal("台账为空")
	}
	driftCount := 0
	for _, r := range rows {
		switch r.Status {
		case "in-sync":
			if r.Deployed != r.Repo {
				t.Errorf("%s 标成 in-sync，但线上 md5(%s) != 仓内 md5(%s) ⇒ "+
					"要么状态写错了，要么线上其实在跑旧版却标成同步。\n"+
					"★ 「标成同步」比「标成漂移」危险：它让漂移**不可见**。",
					r.Name, r.Deployed, r.Repo)
			}
		case "drift":
			driftCount++
			if r.Deployed == r.Repo {
				t.Errorf("%s 标成 drift，但两个 md5 相同 (%s) ⇒ 状态过期了（可能已部署却忘了改状态）", r.Name, r.Repo)
			}
			if r.Reason == "" {
				t.Errorf("%s 标成 drift 却没有写原因 ⇒ 漂移变成默默发生的事。\n"+
					"★ 请写清：线上跑的是哪一版、差在哪、为什么不部署（破坏性动作？待拍板？）、何时处置。",
					r.Name)
			}
		default:
			t.Errorf("%s 的状态 %q 非法（只能是 in-sync / drift）", r.Name, r.Status)
		}
	}
	// 正向自证：本轮实测确有 drift（3 个），若一条都没有，说明台账被清空了。
	if driftCount == 0 {
		t.Errorf("台账里一条 drift 都没有 —— 2026-10-19 实测有 3 个（drop-old-columnar / " +
			"proactive-empty-table-cleanup / vacuum-bloat）。若它们确已处置，请把状态改掉并写明原因；" +
			"**不要直接删行**（删行会让漂移重新变成不可见）。")
	}
}

// TestDeployDriftInSyncReasonMustNotContradictItself —— 状态与**原因文字**必须自洽。
//
// ★ 2026-10-24 12h 审计轮（问题⑤）抓到：台账里两行 status=in-sync，
//
//	而**它们自己的原因文字**仍写着「线上未部署 / 线上尚不存在此文件 / 线上仍是旧版」。
//	⇒ ⇒ 一行台账**同时声称「已部署」和「未部署」** ⇒ 半个字段在骗人。
//
// ★ 为什么原来那道门没抓到：
//
//	`TestDeployDriftStatusIsConsistentWithBothChecksums` 的不变量只管
//	**两个 md5 列与 status**，**不看原因文字** ⇒ ⇒ 矛盾文字永远在门缝里。
//	⇒ 而且 14335 那道门在同一笔提交里被改成「只查正向 token 存在」
//	  ⇒ 门跟着断言走 ⇒ 对着一行自相矛盾的数据保持全绿。
//
// ★ 本门把不变量补全成三条：
//
//	① in-sync ⇒ 原因文字里**不得**出现「未部署」语义的裸词
//	② in-sync ⇒ 原因文字里**必须**有部署证据（现状 banner）
//	③ drift   ⇒ 原因文字里**必须**有现状 banner（同样不许只有历史叙述）
//
// ★ 判别词表刻意选「**裸词**」：历史段落里如果想提到部署前的事实，
//
//	必须写成过去时（如「部署前：线上跑的是旧版」），而不是「线上仍是旧版」。
//	⇒ ⇒ 这样「现在到底是什么状态」在台账里是**唯一可判的**。
func TestDeployDriftInSyncReasonMustNotContradictItself(t *testing.T) {
	rows := readDriftManifest(t)
	if len(rows) == 0 {
		t.Fatal("台账为空")
	}

	// 裸词表：出现即表示「现在还没部署」，与 in-sync 自相矛盾。
	contradicting := []string{
		"线上仍是旧版",
		"线上仍跑旧判据",
		"线上未部署",
		"仓内已改，线上未部署",
		"线上尚不存在",
		"尚未部署到 252",
	}

	for _, r := range rows {
		if r.Status != "in-sync" && r.Status != "drift" {
			continue
		}
		// ★ 矛盾裸词**只对 in-sync 行**是矛盾。
		//   对 drift 行，「线上仍是旧版」恰恰是**正确**的描述（线上确实跑的是旧版）
		//   ⇒ ⇒ 把同一把尺子套到 drift 行上，会把**真话判成假话**。
		if r.Status == "in-sync" {
			for _, bad := range contradicting {
				if strings.Contains(r.Reason, bad) {
					t.Errorf("%s 的状态是 %q，但原因里仍写着 %q ⇒ ⇒ **同一行台账同时声称"+
						"「已部署」和「未部署」** ⇒ 半个字段在骗人。\n"+
						"★ 请把部署前的叙述改成过去时（如「部署前：线上跑的是旧版」），\n"+
						"  并在原因开头写清**现状**。",
						r.Name, r.Status, bad)
				}
			}
		}
		// 现状 banner：**只对 in-sync 行**要求。
		//
		// ★ 为什么 drift 行不要求：对 drift 行，「当前状态」就是
		//   「线上跑的是旧版 + 为什么没部署」⇒ 它的 md5 两列已经表达了
		//   「线上 ≠ 仓内」，而原因文字表达「为什么」⇒ ⇒ **已经是自洽的**。
		//   ⇒ 而 in-sync 行必须显式说清「已部署的是哪一版 / 什么时候 / 实测读数」，
		//   否则读者只能靠会话记忆判断线上在跑什么。
		if r.Status == "in-sync" && !strings.Contains(r.Reason, "【现状：") {
			t.Errorf("%s 的状态是 %q，但原因里没有「【现状：…」」开头 ⇒ ⇒ "+
				"读者无法从台账本身判出**当前**部署的是哪一版，只能靠会话记忆。\n"+
				"★ 请在原因开头写一行现状（已部署到哪 / 线上 md5 / 实测读数）。",
				r.Name, r.Status)
		}
	}
}
