//go:build !integration

// Package main — migration_nonplain_delivery_gate_test.go
//
// migration_nonplain_delivery_gate_test.go —— 「非三位数字前缀」startup 迁移的
// **投递登记册**核对（安装器这一半）。
//
// ── 为什么要有这道门（2026-10-22 R82 轮**实测复现**）──────────────────────────
//
// 本包里已有一道「强制 embed 门」（migration_embed_sync_gate_test.go 的
// TestCanonicalStartupMigrationsFrom849AreEmbedded），它决定「一条 canonical
// 迁移必须在 embeddata 里有副本吗」，靠的是**从文件名里解析一个整数**：
//
//	num, ok := migrationNumber(e.Name())   // strconv.Atoi(name[:idx])
//	if !ok || num < minMandatoryEmbedMigration {
//	    continue                            // ← 解析不出来 ⇒ 直接跳过
//	}
//
// 而同名的投递路径契约门（scripts/apply-db-revision-sequence_test.sh）用的是
// 同一个形状：`[[ "$name" =~ ^[0-9]{3}_.*\.sql$ ]] || continue`。
//
// ⇒ ★★ **同一个病的两个实例**：任何**解析不出纯三位数字前缀**的迁移
//   （`NNNb_` / `NNNpN_` / 日期名）对**两道门都完全隐形**。
//   ⇒ 而 `NNNb_` **是本仓正在使用的命名约定**（361b / 487b / 487c / 680b /
//     682b / 683b / 706b / 707b 共 8 条已交付件）。
//   ⇒ ★★ **实测复现**（2026-10-22，工作树内新建 `912b_zzz_probe_delivery.sql`）：
//     本门 GREEN；投递路径契约门 GREEN
//     ⇒ 该迁移既不在 embeddata/StartupFiles、也不在通道 files=()
//     ⇒ **新装库拿不到、升级库也拿不到，而没有任何一道门会红**。
//
// ── R83 轮实测出的**第二宇宙**（R82 轮自己的遗漏）────────────────────────────
//
// R82 轮的登记册**只穷举了 canonical 目录** ⇒ ⇒ **embeddata/startup/ 是另一个
// 独立的文件宇宙**，而它里面有**非前缀名**的文件：
//
//	session_turns_hot_bootstrap.sql
//
// ★★ **canonical 目录里根本没有这个文件** ⇒ ⇒ **R82 轮的门在这个宇宙上仍然是
// 瞎的** ⇒ ⇒ 而它恰好是 `session_turns_hot`（settle worker 读的那张表）及其
// 11 个索引的载体 ⇒ ⇒ **它必须被登记、必须有投递腿**。
//
// ── 修法的性质（与 R81 轮 L11-b 门**同款**）──────────────────────────────────
//
// 把「手挑的扫描根清单」换成 ★ **穷举全目录 ＋ 显式清单**：
//   · 穷举：**两个宇宙各穷举一次**（canonical 递归 / embeddata 平铺）；
//   · 显式清单：`sql/schema/startup_migration_nonplain.tsv`（15 条）。
//
// 登记册是**两个模块共用**的（bash 侧那道门与本文件读的是同一份 TSV）
// ⇒ **单一事实源**，不是两份各自维护的清单。
//
// ── 两道门各管什么（**分工要写明**，否则读者会以为两边都查了）────────────────
//
//   · 本文件（Go 侧）：**穷举与结构** —— 两个宇宙的枚举 ⇄ 册子、键在磁盘上存在、
//     投递腿取值合法、`none` 必须写效果落点、落点 kind 前缀合法、
//     installer 腿 ⇒ embeddata 里必须有副本；
//   · bash 侧：**事实核对** —— 效果落点的**内容**是否成立
//     （基线里有没有那张表/那一列/那个索引、对象在不在**迁移 890 自己列出的**
//     退役清单里、bootstrap 文件在不在）。
//   ⇒ ⇒ 两边是**互补**的：任何一边单独存在都放行不了「声明与事实不符」。

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// nonplainRegistryPath 是「非纯数字前缀迁移」的投递登记册（仓根相对路径）。
	// 它同时被 bash 侧的投递路径契约门读（scripts/apply-db-revision-sequence_test.sh）。
	nonplainRegistryPath = "../../../sql/schema/startup_migration_nonplain.tsv"

	// embeddataStartupDir 是安装器 embed 的 startup 目录（第二个宇宙的根）。
	embeddataStartupDir = "embeddata/startup"
)

// 投递腿取值（与登记册第 2 列一致）。
const (
	legChannel   = "channel"
	legInstaller = "installer"
	legBoth      = "both"
	legNone      = "none"
)

// 效果落点的 kind 前缀（登记册第 3 列）。**内容**由 bash 侧核对（基线 / 890 / bootstrap）。
const (
	spotBaseline      = "baseline:"
	spotBaselineIndex = "baseline-index:"
	spotRetired       = "retired:"
	spotBootstrap     = "bootstrap:"
)

// nonplainRegistryRow 是登记册的一行。
type nonplainRegistryRow struct {
	Key     string // 宇宙前缀 + 相对路径，如 "embeddata/session_turns_hot_bootstrap.sql"
	Leg     string
	Landing string
	Reason  string
}

// readNonplainRegistry 读登记册。
//
// ⚠ 两类「坏」要分开：
//
//	· **观测手段坏了**（读不到文件 / 一条数据行都没有）⇒ `t.Fatalf`（硬失败，
//	  与「被观测对象有问题」不是一回事）；
//	· **登记册某一行格式不对** ⇒ `t.Errorf`（**不中断**）⇒ ⇒ 其它行的问题
//	  仍然会被报出来 ⇒ **一次报全**（沿用本仓的收集器风格）。
func readNonplainRegistry(t *testing.T) []nonplainRegistryRow {
	t.Helper()
	raw, err := os.ReadFile(nonplainRegistryPath)
	if err != nil {
		t.Fatalf("读投递登记册 %s: %v", nonplainRegistryPath, err)
	}
	var rows []nonplainRegistryRow
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		// ★ 列数必须**恰好 4 列** —— 「效果落点为空、理由非空」这种写法在
		//   Go 的 strings.Split 下是**合法的**（Split 不折叠空字段）⇒ 判据能看见它；
		//   而 bash 侧用 IFS=TAB 的 read 会**折叠**空字段 ⇒ 两边语义不对齐
		//   （bash 侧已改用 \x1f 分隔，见 scripts/apply-db-revision-sequence_test.sh）。
		//   ⇒ 这里用**恰好 4 列**把「列数不对」这件事本身变成一条判据。
		if len(fields) != 4 {
			t.Errorf("投递登记册 %s 第 %d 行的列数是 %d（必须恰好 4 列：键<TAB>投递腿<TAB>效果落点<TAB>理由）",
				nonplainRegistryPath, i+1, len(fields))
			continue
		}
		row := nonplainRegistryRow{
			Key:     strings.TrimSpace(fields[0]),
			Leg:     strings.TrimSpace(fields[1]),
			Landing: strings.TrimSpace(fields[2]),
			Reason:  strings.TrimSpace(fields[3]),
		}
		switch row.Leg {
		case legChannel, legInstaller, legBoth, legNone:
		default:
			t.Fatalf("投递登记册 %s 第 %d 行的投递腿 %q 非法（合法值：%s/%s/%s/%s）",
				nonplainRegistryPath, i+1, row.Leg, legChannel, legInstaller, legBoth, legNone)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		t.Fatalf("投递登记册 %s 一条数据行都没有 ⇒ 观测手段失效（读了个空文件？）", nonplainRegistryPath)
	}
	return rows
}

// isPlainNumberName 判定「文件名（不是路径）是否以纯三位数字前缀开头」。
func isPlainNumberName(name string) bool {
	if len(name) < 4 || name[3] != '_' {
		return false
	}
	for i := 0; i < 3; i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}

// nonPlainKeysOf 穷举一个目录里的非前缀 up 体，返回带宇宙前缀的键集合。
//
// ⚠ 两处细节都不是可有可无的：
//
//	· canonical 宇宙**递归**（含 `up/` 子目录）—— 否则子目录里的件对这道门隐形；
//	· 按 **basename** 判定前缀（不是相对路径）—— `up/376_x.sql` 的 basename
//	  是纯数字前缀名 ⇒ 它**不是**非前缀迁移。
func nonPlainKeysOf(t *testing.T, root, universe string, recursive bool) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	walk := func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			return nil
		}
		if isPlainNumberName(name) {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out[universe+"/"+filepath.ToSlash(rel)] = true
		return nil
	}
	if recursive {
		if err := filepath.WalkDir(root, walk); err != nil {
			t.Fatalf("遍历 %s: %v", root, err)
		}
		return out
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读 %s: %v", root, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := walk(filepath.Join(root, e.Name()), e, nil); err != nil {
			t.Fatalf("遍历 %s: %v", root, err)
		}
	}
	return out
}

// TestCanonicalNonPlainMigrationsHaveDeclaredDeliveryLeg 是本门的主判据：
// ① **两个宇宙**的登记册都**恰好**覆盖各自目录里全部非纯数字前缀的 up 体（双向）；
// ② 声明走安装器腿的，必须真的在 embeddata 里；
// ③ 声明「有意不投递」的，必须**不在** embeddata 里、且**必须写效果落点**。
func TestCanonicalNonPlainMigrationsHaveDeclaredDeliveryLeg(t *testing.T) {
	rows := readNonplainRegistry(t)

	// ① 两个宇宙的穷举。
	canonicalKeys := nonPlainKeysOf(t, canonicalStartupDir, "canonical", true)
	embedKeys := nonPlainKeysOf(t, embeddataStartupDir, "embeddata", false)
	// 量具自检：穷举必须真的扫到东西，否则 ① 会恒绿（门看上去正常、实际扫了个寂寞）。
	if len(canonicalKeys) == 0 {
		t.Fatalf("canonical 宇宙的穷举没有扫到任何非纯数字前缀的迁移 ⇒ 枚举面失效 ⇒ 本门会恒绿")
	}
	if len(embedKeys) == 0 {
		t.Fatalf("embeddata 宇宙的穷举没有扫到任何非纯数字前缀的迁移 ⇒ 枚举面失效 ⇒ 本门会恒绿")
	}
	// ★ 第二个宇宙的**代表件**必须是那个「只在 embeddata 里存在」的文件 ——
	//   它是 R83 轮实测出的那个盲点 ⇒ 少了它 ⇒ 说明枚举面又退化了。
	if !embedKeys["embeddata/session_turns_hot_bootstrap.sql"] {
		t.Fatalf("embeddata 宇宙的穷举没有扫到 session_turns_hot_bootstrap.sql ⇒ " +
			"★ 它是 R83 轮实测出的盲点代表件（只在 embeddata 里存在、canonical 目录没有）⇒ " +
			"枚举面退化 ⇒ 本门会恒绿")
	}

	declared := map[string]nonplainRegistryRow{}
	for _, r := range rows {
		if _, dup := declared[r.Key]; dup {
			t.Errorf("投递登记册里 %s 登记了两次", r.Key)
			continue
		}
		declared[r.Key] = r
	}

	actual := map[string]bool{}
	for k := range canonicalKeys {
		actual[k] = true
	}
	for k := range embedKeys {
		actual[k] = true
	}

	// ①-a 目录里有、册子里没有 ⇒ 漏登记 ⇒ 这条迁移对所有门隐形
	for key := range actual {
		if _, ok := declared[key]; !ok {
			t.Errorf("startup 迁移 %s 的文件名解析不出纯三位数字前缀 ⇒ 它对**强制 embed 门**"+
				"与**投递路径契约门**都完全隐形（两道门都靠文件名解析判视野）⇒ "+
				"它既不在 embeddata/StartupFiles、也不在通道 files=() ⇒ 新装库与升级库都拿不到 ⇒ "+
				"必须先在 %s 登记投递腿，再登记进对应通道", key, nonplainRegistryPath)
		}
	}
	// ①-b 册子里有、目录里没有 ⇒ 陈旧条目
	for _, r := range rows {
		if _, ok := actual[r.Key]; !ok {
			t.Errorf("投递登记册 %s 登记了 %s，但对应目录里没有这个文件 ⇒ 登记册陈旧",
				nonplainRegistryPath, r.Key)
			continue
		}
		base := r.Key[strings.LastIndex(r.Key, "/")+1:]
		// ② 声明走安装器腿的，必须真的在 embeddata 里
		if r.Leg == legInstaller || r.Leg == legBoth {
			embedPath := filepath.Join(embeddataStartupDir, base)
			if _, err := os.Stat(embedPath); err != nil {
				t.Errorf("投递登记册声明 %s 走安装器腿，但它不在 %s 里 ⇒ 新装库拿不到：%v",
					r.Key, embeddataStartupDir, err)
			}
		}
		// ③ 声明「有意不投递」的，必须不在 embeddata 里、且必须写效果落点
		if r.Leg == legNone {
			embedPath := filepath.Join(embeddataStartupDir, base)
			if _, err := os.Stat(embedPath); err == nil {
				t.Errorf("投递登记册声明 %s 有意不投递，但它**在** %s 里 ⇒ 声明与事实不符",
					r.Key, embeddataStartupDir)
			}
			if r.Landing == "" || r.Landing == "-" {
				t.Errorf("投递登记册声明 %s 有意不投递，却没有写效果落点 ⇒ "+
					"这条决定没有任何可核对的依据（散文不算依据）", r.Key)
			}
		}
	}
	t.Logf("canonical 宇宙非前缀迁移 %d 条；embeddata 宇宙 %d 条；登记册 %d 条",
		len(canonicalKeys), len(embedKeys), len(rows))
}

// TestNonPlainRegistryLandingKindsAreKnown 把「效果落点」的 kind 前缀收在四个已知值内。
// 单列一条是因为它读的是**登记册本身**（不是文件系统）⇒ 它的失败原因与主判据不同。
func TestNonPlainRegistryLandingKindsAreKnown(t *testing.T) {
	for _, r := range readNonplainRegistry(t) {
		if r.Reason == "" {
			t.Errorf("登记册 %s 没有写理由 ⇒ 决定必须留下可审计的理由", r.Key)
		}
		if r.Landing == "" || r.Landing == "-" {
			if r.Leg == legNone {
				t.Errorf("登记册 %s 声明「有意不投递」却没有效果落点", r.Key)
			}
			continue
		}
		for _, spot := range strings.Split(r.Landing, ",") {
			spot = strings.TrimSpace(spot)
			if spot == "" {
				continue
			}
			switch {
			case strings.HasPrefix(spot, spotBaseline),
				strings.HasPrefix(spot, spotBaselineIndex),
				strings.HasPrefix(spot, spotRetired),
				strings.HasPrefix(spot, spotBootstrap):
			default:
				t.Errorf("登记册 %s 的效果落点 %q 的 kind 非法（合法：%s… / %s… / %s… / %s…）",
					r.Key, spot, spotBaseline, spotBaselineIndex, spotRetired, spotBootstrap)
			}
		}
	}
}

// TestMigrationNumberRejectsNonPlainNames 把「纯数字前缀」这个判定函数本身钉住：
// 解析不出来的名字必须返回 ok=false —— 正是这条 false 让上层 `continue` ⇒ 隐形。
//
// ⚠ 口径说明：本门（登记册）用的「纯数字前缀」是**严格三位数字**（`^[0-9]{3}_`），
//
//	与投递路径契约门的正则同口径；而既有强制 embed 门用的 migrationNumber 是
//	「第一个 `_` 之前能解析成整数即可」（`1234_x.sql` 在它眼里是版本 1234）。
//	⇒ 两者对**四位数**前缀的可见性不同：这样的文件会**同时**被两道门要求
//	（登记册要求显式登记 ＋ embed 门要求有副本）⇒ **要求更多，不是互相矛盾**。
func TestMigrationNumberRejectsNonPlainNames(t *testing.T) {
	for _, name := range []string{
		"912b_zzz_probe_delivery.sql",
		"478p2_affinity_indexes.sql",
		"2026-07-13-multimodal-token-fields-hot.sql",
		"328a_request_logs_bodies_table.sql",
		"session_turns_hot_bootstrap.sql",
	} {
		if _, ok := migrationNumber(name); ok {
			t.Errorf("%s 不是纯数字前缀名，migrationNumber 却解析出了整数 ⇒ 上层会把它算进门内", name)
		}
	}
	for _, name := range []string{"700_request_logs_view_raw_model_name.sql", "893_fresh_install_partition_prereq.sql"} {
		if _, ok := migrationNumber(name); !ok {
			t.Errorf("%s 是纯数字前缀名，migrationNumber 却解析失败 ⇒ 门会漏掉它", name)
		}
	}
}
