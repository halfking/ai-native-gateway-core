package ursm_test

// 252 的 **按需工具**（非 cron 调度）部署漂移门（2026-10-25）
//
// # 这道门补的是哪块盲区
//
// 仓里关于 252 的部署台账有**两本**，它们合起来仍然看不见一类东西：
//
//	deployed-252.tsv      记「cron 调度的脚本」线上跑的是哪一版
//	  └ deployed_script_drift_test.go → TestDeployDriftManifestCoversExactlyTheCronInvokedScripts
//	     （台账集合必须 **==** 正典 cron 里被调度的脚本集合：多一行红、少一行红）
//	  └ deployed_cron_drift_test.go  → TestCronCanonicalSchedulesEveryLedgeredScript
//	     （台账里每个脚本必须在正典 cron 副本里**有调度行**）
//	deployed-cron-252.tsv  记「整份 cron 文件」的线上/仓内 md5
//
// ★ 那两道门把 `deployed-252.tsv` 的范围**钉死成「cron 调度集合」** ——
//
//	给任何**不被 cron 调度**的脚本加一行到 deployed-252.tsv
//	⇒ 第一道门判「幽灵条目」红 + 第二道门判「没有调度行」红
//	⇒ ★ 这是**设计如此，不是漏洞**。所以「线上有、仓内 cron 不调」的那些脚本
//	  **不能**塞进 deployed-252.tsv，只能另开一本台账（`deployed-tools-252.tsv`）。
//
// ★ 于是出现一块**结构性盲区**（本门要补的就是它）：
//
//	252 的 /opt/scripts/ 里有一批脚本 —— 仓内**不排 cron**（按需手动跑 / 库函数 /
//	测试夹具 / 长期运行的采样进程），线上**存在**、仓内**有源**，但**没有任何台账
//	记录它们线上跑的是哪一版**。
//	⇒ 「线上有、仓内 cron 不调、无人跟踪」—— 改了仓内脚本也没人知道线上还在跑旧版。
//
// ★ 本门与新台账（`deployed-tools-252.tsv`）合起来，把这一类也变成
//   「仓内可查的事实」：改了脚本不更新台账 ⇒ 提交时就红。
//
// # 候选集怎么算（★ 判据的时点与口径）
//
//	候选集 = scripts/252-monitor/ 下（**含 manual/ 子目录**）所有 .sh / .py 文件
//	       **减去**正典 cron 副本 etc.cron.d.pg17 里被调度的那些
//	       （判定：非注释行里出现 `/opt/scripts/<文件名>`）
//
// ★ **刻意不用 cron 候选集那套前缀约定**（`cronJobName` = `^(pg17|ursm)-.*\.(sh|py)$`）：
//
//	· notify.sh / unused-index-report.sh / index-reader-census.sh / monitor_loadtest.sh
//	  **不占**那个前缀 ⇒ 前缀约定会把它们排除在外，而它们**正是**本门要管的
//	· manual/pg17-start.sh **占**那个前缀 ⇒ 前缀约定会把它算成「cron 任务」，
//	  而它**明令禁止**进 cron（它做的是 stop + rm 一个生产数据库容器，
//	  定时执行等于定时删库）
//
//	⇒ 前缀约定是「命名风格」，**不是**「实际被调度」⇒ 本门按**实际调度**判定。
//	  （`cronJobName` 在本门里只用作**旁证**：用它标出那些「按前缀该被调度、
//	  却没被调度」的文件，供日志阅读。）
//
// # 这道门只能守住「仓内」
//
//	它**不能** ssh 到 252 去 diff（CI 与开发机都没有那台机器的凭据）。
//	⇒ 它钉的是**仓内可判**的事：
//
//	① 台账的「仓内 md5」必须等于仓内文件的实际 md5
//	   ⇒ 改了按需工具却不更新台账 ⇒ **门红**。
//	② 状态与两个 md5 必须自洽
//	   ⇒ `in-sync` ⇒ 线上 md5 == 仓内 md5；
//	     `drift` ⇒ 两者**必须**不同且**必须**写清原因（默默漂着不行）。
//	③ 台账行集合 **==** 候选集（**双向**）：少一行红、多一行红。
//	④ 台账行**不得**是被 cron 调度的脚本（两本台账双记 ⇒ 两本都会漂）。
//
// # 变异验证（本门的五条变异都实测转红）
//
//	M1  改台账里某行的「仓内 md5」              ⇒ ① 红
//	M2  删台账里的一行                            ⇒ ③ 红（候选集里有、台账里没有）
//	M3a 把一个 drift 行改成 in-sync（两个 md5 仍不同） ⇒ ② 红（in-sync 却不相等）
//	M3b 让一个 drift 行的两个 md5 相同（状态仍 drift） ⇒ ② 红（drift 却相等）
//	M4  把一个被 cron 调度的脚本加进新台账        ⇒ ③ 红（幽灵）+ ④ 红（双记）
//
// ⇒ 每个方向都有**能红的路径**，不是恒真门。

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// toolDriftManifestPath 是本门对应的新台账（★ 与 deployed-252.tsv **分工**，不重叠）。
const toolDriftManifestPath = "../252-monitor/deployed-tools-252.tsv"

// toolDriftRow 是新台账的一行（列与 deployed-252.tsv 完全一致）。
type toolDriftRow struct {
	Name     string
	Deployed string
	Repo     string
	Status   string
	Reason   string
}

// readToolDriftManifest 读新台账。
//
// ★ 解析规则刻意与 `readDriftManifest`（deployed-252.tsv 的）**逐字一致**：
//
//	跳过空行与 `#` 开头的注释行；字段按 TAB 切；原因字段允许含 TAB（拼回一整段）。
//	⇒ 两本台账的机器可读性一致，diff 两本台账也不会因解析口径不同而误判。
func readToolDriftManifest(t *testing.T) []toolDriftRow {
	t.Helper()
	b, err := os.ReadFile(toolDriftManifestPath)
	if err != nil {
		t.Fatalf("读按需工具部署台账 %s: %v", toolDriftManifestPath, err)
	}
	var rows []toolDriftRow
	for i, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		f := strings.Split(trimmed, "\t")
		if len(f) < 4 {
			t.Fatalf("%s:%d 字段不足 4 个（脚本名/线上md5/仓内md5/状态）：%q",
				toolDriftManifestPath, i+1, trimmed)
		}
		r := toolDriftRow{Name: f[0], Deployed: f[1], Repo: f[2], Status: f[3]}
		if len(f) > 4 {
			r.Reason = strings.TrimSpace(strings.Join(f[4:], "\t"))
		}
		rows = append(rows, r)
	}
	return rows
}

// toolDriftRepoScripts 递归扫 scripts/252-monitor/，返回「文件名 → 仓内相对路径」。
//
// ★ 递归是必须的：`manual/pg17-start.sh` 住在子目录里，而台账按**文件名**记
//
//	（线上部署路径是 /opt/scripts/pg17-start.sh，与仓内子目录无关）。
//
// ★ 文件名冲突 ⇒ 直接判死：台账按文件名记，冲突会让「台账里的 md5 该对哪个文件」
//
//	变成**不可判**的事 ⇒ 与其悄悄取一个，不如让门红。
func toolDriftRepoScripts(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	collisions := map[string][]string{}
	err := filepath.WalkDir(driftMonitorDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".sh") && !strings.HasSuffix(name, ".py") {
			return nil
		}
		rel, relErr := filepath.Rel(driftMonitorDir, path)
		if relErr != nil {
			return relErr
		}
		if prev, ok := out[name]; ok {
			collisions[name] = append(collisions[name], prev, rel)
			return nil
		}
		out[name] = rel
		return nil
	})
	if err != nil {
		t.Fatalf("递归扫 %s: %v", driftMonitorDir, err)
	}
	if len(collisions) > 0 {
		for name, paths := range collisions {
			t.Errorf("scripts/252-monitor/ 下有多个同名文件 %s（%v）⇒ "+
				"台账按文件名记 ⇒ 「该对哪个文件」不可判 ⇒ 请改名后重记。",
				name, paths)
		}
	}
	return out
}

// toolDriftCronScheduled 返回**正典 cron 里被调度**的文件名集合。
//
// ★ 判定：非注释行里出现 `/opt/scripts/<文件名>`。
//
//	· 只看**非注释**行 ⇒ 注释里提到的脚本名不算调度
//	· 按**文件名的完整路径**匹配 ⇒ 不会出现「pg17-index-bloat.sh 被
//	  pg17-index-bloat-selftest.sh 的调度行误命中」这类子串误判
//
// ★ 与 `cronInvokedScripts`（deployed_script_drift_test.go 里的）的差别：
//
//	那条用正则 `/opt/scripts/([A-Za-z0-9._-]+\.sh)` ⇒ **只认 .sh**；
//	本门要判的是**本台账里的行**（含 .py 夹具）⇒ 必须能判 .py ⇒ 这里按文件名逐个查。
func toolDriftCronScheduled(t *testing.T, repoScripts map[string]string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(cronDriftCanonical)
	if err != nil {
		t.Fatalf("读仓内正典 cron 副本 %s: %v", cronDriftCanonical, err)
	}
	scheduled := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		for name := range repoScripts {
			if strings.Contains(trimmed, "/opt/scripts/"+name) {
				scheduled[name] = true
			}
		}
	}
	return scheduled
}

// toolDriftCandidates 推导「按需工具候选集」。
func toolDriftCandidates(t *testing.T) (candidates []string, scheduled []string) {
	t.Helper()
	repo := toolDriftRepoScripts(t)
	sched := toolDriftCronScheduled(t, repo)
	for name := range repo {
		if sched[name] {
			scheduled = append(scheduled, name)
			continue
		}
		candidates = append(candidates, name)
	}
	sort.Strings(candidates)
	sort.Strings(scheduled)
	return candidates, scheduled
}

// TestDeployToolDriftCandidateSetDerivation —— 候选集推导本身必须是**非空**的。
//
// ★ 这条是**正向自证**：候选集空了 ⇒ 下面所有断言（集合相等、md5 对拍、状态自洽）
//
//	都会**恒真** ⇒ 门变成形同虚设。
//
//	同时自证「正典 cron 集合非空」：cron 集合空了 ⇒ 减法没有减掉任何东西
//	⇒ 候选集 == 全部脚本 ⇒ 推导口径失效（判据本身坏了）。
func TestDeployToolDriftCandidateSetDerivation(t *testing.T) {
	candidates, scheduled := toolDriftCandidates(t)

	if len(scheduled) < 8 {
		t.Fatalf("从正典 cron %s 里只推导出 %d 个被调度脚本 (%v) —— "+
			"cron 侧解析失效 ⇒ 本门的「减去被调度集合」这一步是空转。",
			cronDriftCanonical, len(scheduled), scheduled)
	}
	if len(candidates) < 3 {
		t.Fatalf("按需工具候选集只有 %d 个 (%v) —— 候选集推导失效，"+
			"下面所有断言都会恒真（门形同虚设）。", len(candidates), candidates)
	}

	// 候选集与 cron 集合**必须**互斥：推导口径本身就是「减掉被调度的」。
	schedSet := map[string]bool{}
	for _, s := range scheduled {
		schedSet[s] = true
	}
	for _, c := range candidates {
		if schedSet[c] {
			t.Errorf("%s 同时出现在候选集和 cron 被调度集合里 ⇒ 推导口径坏了", c)
		}
	}

	// ★ 旁证：按仓里既有的前缀约定（cronJobName）本该是 cron 任务、却没被调度的文件。
	//   它们是**故意**留在「按需工具」这一侧的（如 manual/pg17-start.sh 明令禁止进 cron）。
	//   记进日志，是为了让人以为「前缀约定没生效」时能立刻看出这是**有意为之**。
	repo := toolDriftRepoScripts(t)
	var prefixButUnscheduled []string
	for name := range repo {
		if cronJobName.MatchString(name) && !schedSet[name] {
			prefixButUnscheduled = append(prefixButUnscheduled, name)
		}
	}
	sort.Strings(prefixButUnscheduled)

	t.Logf("仓内 .sh/.py 共 %d 个；正典 cron 被调度 %d 个；按需工具候选集 %d 个：%v",
		len(repo), len(scheduled), len(candidates), candidates)
	t.Logf("按前缀约定本该进 cron 却没被调度（有意留在按需侧）：%v", prefixButUnscheduled)
}

// TestDeployToolDriftManifestCoversExactlyTheCandidateSet —— 台账行集合 **==** 候选集。
//
// ★ 为什么要**两个方向**都查：
//
//	· 少一行 ⇒ 那个按需工具的线上版本**无人记录**（漂移照旧发生，无人知道）
//	  —— 这正是本门要补的那个盲区本身
//	· 多一行 ⇒ 台账里有个**幽灵**（仓内根本没有这个文件），
//	  而它会让人误以为「按需工具都在册」
//
// ★ 为什么不能靠人记得登记：加一个 `.sh`/`.py` 文件（哪怕只是改个名）不会触发
//
//	任何既有门 —— `cron_registration_test.go` 只管**占 pg17-/ursm- 前缀**的那批，
//	而按需工具大多**不占**那个前缀 ⇒ ⇒ 「忘了登记」是**默认发生**的事，不是例外。
func TestDeployToolDriftManifestCoversExactlyTheCandidateSet(t *testing.T) {
	rows := readToolDriftManifest(t)
	candidates, _ := toolDriftCandidates(t)

	if len(rows) == 0 {
		t.Fatalf("%s 里一条记录都没有 —— 这道门正在空转（台账空了，所有断言都会跳过）。\n"+
			"★ 不要直接删行：请把状态改成 in-sync/drift 并写清原因。", toolDriftManifestPath)
	}

	inManifest := map[string]bool{}
	for _, r := range rows {
		if inManifest[r.Name] {
			t.Errorf("台账里 %s 出现重复行", r.Name)
		}
		inManifest[r.Name] = true
	}
	inCandidates := map[string]bool{}
	for _, c := range candidates {
		inCandidates[c] = true
	}

	for _, c := range candidates {
		if !inManifest[c] {
			t.Errorf("按需工具 %s 在 %s 里没有记录 ⇒ 它的线上版本无人跟踪\n"+
				"★ 这正是本门要补的盲区：线上有、仓内 cron 不调、没有任何台账记它。\n"+
				"⇒ 请加一行（线上 md5 / 仓内 md5 / 状态 / 原因），或确认它是否该删。",
				c, toolDriftManifestPath)
		}
	}
	for _, r := range rows {
		if !inCandidates[r.Name] {
			t.Errorf("台账里的 %s 不在按需工具候选集里 ⇒ 幽灵条目\n"+
				"★ 候选集 = 仓内 scripts/252-monitor/ 下的 .sh/.py 减去正典 cron 被调度的那些。\n"+
				"⇒ 若它确实存在，请确认它是不是被 cron 调度了（那就该回 deployed-252.tsv 记）；"+
				"若仓内已不存在，请删这一行。",
				r.Name)
		}
	}
}

// TestDeployToolDriftManifestRepoChecksumMatchesDisk —— 台账的「仓内 md5」必须等于文件的实际 md5。
//
// ★ 这是整道门的**承重断言**：改了按需工具却不更新台账 ⇒ 这里红。
//
//	没有它，台账只是一张「曾经记过一次」的便条，改脚本的人不会被迫知道它。
//
// ★ 路径解析走**递归按文件名**（manual/pg17-start.sh → scripts/252-monitor/manual/pg17-start.sh）。
func TestDeployToolDriftManifestRepoChecksumMatchesDisk(t *testing.T) {
	rows := readToolDriftManifest(t)
	repo := toolDriftRepoScripts(t)
	for _, r := range rows {
		rel, ok := repo[r.Name]
		if !ok {
			t.Errorf("台账里的 %s 在仓内 scripts/252-monitor/ 下不存在（任何子目录都没有）",
				r.Name)
			continue
		}
		path := filepath.Join(driftMonitorDir, rel)
		got := md5OfFile(t, path)
		if got != r.Repo {
			t.Errorf("%s 的仓内内容已变（台账记 %s，实际 %s，路径 %s）\n"+
				"★ 这正是本门要拦的事：改了按需工具却没部署、也没更新台账 ⇒ "+
				"线上继续跑旧版而**无人察觉**。\n"+
				"⇒ 请同时做两件事：部署到 252，并把台账的「仓内 md5」与「线上 md5」一起更新。",
				r.Name, r.Repo, got, path)
		}
	}
}

// TestDeployToolDriftStatusIsConsistentWithBothChecksums —— 状态与两个 md5 必须自洽。
//
//	in-sync ⇒ 线上 md5 == 仓内 md5
//	drift   ⇒ 线上 md5 != 仓内 md5 **且** 原因非空
//
// ★ 「drift 但没写原因」是本门要拦的**第二种默默漂着**：
//
//	漂着不可怕，**没人知道它为什么漂**才可怕。
//
// ★ 额外要求：**每一行**都要有原因（in-sync 也要写一句「何时部署/为何同步」）——
//
//	与 `deployed-252.tsv` 的既有惯例一致（那边 in-sync 行同样写现状 banner）。
func TestDeployToolDriftStatusIsConsistentWithBothChecksums(t *testing.T) {
	rows := readToolDriftManifest(t)
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
				t.Errorf("%s 标成 drift，但两个 md5 相同 (%s) ⇒ 状态过期了（可能已部署却忘了改状态）",
					r.Name, r.Repo)
			}
			if r.Reason == "" {
				t.Errorf("%s 标成 drift 却没有写原因 ⇒ 漂移变成默默发生的事。\n"+
					"★ 请写清：线上跑的是哪一版、差在哪、为什么不部署（破坏性动作？待拍板？）、何时处置。",
					r.Name)
			}
		default:
			t.Errorf("%s 的状态 %q 非法（只能是 in-sync / drift）", r.Name, r.Status)
		}
		// drift 行的「原因非空」已在 drift 分支里报过 ⇒ 这里只管**非 drift 行**
		// （否则同一行会报两条几乎一样的错，噪音掩盖真正的信号）。
		if r.Status != "drift" && r.Reason == "" {
			t.Errorf("%s 没有写原因（哪怕是 in-sync 也请写一句「何时部署/为何同步」）", r.Name)
		}
	}
	// 正向自证：本轮实测确有 drift（6 个），若一条都没有，说明台账被清空了。
	if driftCount == 0 {
		t.Errorf("台账里一条 drift 都没有 —— 2026-10-25 实测确有（浏览器三夹具未部署 / " +
			"notify.sh / pg17-start.sh 与仓内分叉 / unused-index-report.sh 未测量）。" +
			"若它们确已处置，请把状态改掉并写明原因；**不要直接删行**" +
			"（删行会让漂移重新变成不可见）。")
	}
}

// TestDeployToolDriftManifestDoesNotOverlapCronLedger —— 两本台账**不得**双记同一个脚本。
//
// ★ 为什么要这条：同一个脚本同时出现在两本台账里 ⇒ **两本都会漂**（各自记各自的 md5，
//
//	改一处只更新一处 ⇒ 另一本立刻过期）⇒ ⇒ 漂移从「一处可见」变成「两处互相打架」。
//
// ★ 判据取**两个方向**：
//
//	① 不在正典 cron 的被调度集合里（cron 侧）
//	② 不在 deployed-252.tsv 的行集合里（台账侧 ⇒ 复用 readDriftManifest）
//
// ★ 也就是说：**cron 调度的脚本一律回 deployed-252.tsv 记**，
//
//	按需工具才进本台账 ⇒ 两本台账**分工、不重叠**。
func TestDeployToolDriftManifestDoesNotOverlapCronLedger(t *testing.T) {
	rows := readToolDriftManifest(t)
	cronRows := readDriftManifest(t)

	inCronLedger := map[string]bool{}
	for _, r := range cronRows {
		inCronLedger[r.Name] = true
	}
	repo := toolDriftRepoScripts(t)
	scheduled := toolDriftCronScheduled(t, repo)

	for _, r := range rows {
		if scheduled[r.Name] {
			t.Errorf("%s 是被正典 cron 调度的脚本，却登记在按需工具台账 %s 里\n"+
				"★ 它应该记在 deployed-252.tsv（那本台账的范围就是 cron 调度集合）⇒ "+
				"双记会让**两本台账各自漂移**（各自记各自的 md5，改一处只更新一处）。",
				r.Name, toolDriftManifestPath)
		}
		if inCronLedger[r.Name] {
			t.Errorf("%s 同时出现在 deployed-252.tsv 与 %s 里 ⇒ 双记 ⇒ 两本都会漂。\n"+
				"★ cron 调度的脚本一律回 deployed-252.tsv 记；本台账只记按需工具。",
				r.Name, toolDriftManifestPath)
		}
	}
}
