//go:build !integration

// Package main — migration_nonplain_delivery_gate_test.go
//
// migration_nonplain_delivery_gate_test.go —— 「非三位数字前缀」startup 迁移的
// **投递登记册**核对（安装器腿那一半）。
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
//     682b / 683b / 706b / 707b 共 8 条已交付件，其中 706b/707b 是 2026-10 的件）。
//   ⇒ ★★ **实测复现**（2026-10-22，工作树内新建 `912b_zzz_probe_delivery.sql`）：
//     本门 GREEN；投递路径契约门 GREEN
//     ⇒ 该迁移既不在 embeddata/StartupFiles、也不在通道 files=()
//     ⇒ **新装库拿不到、升级库也拿不到，而没有任何一道门会红**。
//
// ── 修法的性质（与 R81 轮 L11-b 门**同款**）──────────────────────────────────
//
// 把「手挑的扫描根清单」换成 ★ **穷举全目录 ＋ 显式清单**：
//   · 穷举：`sql/migrations/startup/` **递归**全量 up 体；
//   · 显式清单：`sql/schema/startup_migration_nonplain.tsv` 逐条写明
//     **非纯数字前缀**那部分（当前 13 条）的投递腿与理由。
//
// 登记册是**两个模块共用**的（bash 侧那份在 scripts/apply-db-revision-sequence_test.sh，
// 本文件读的是同一份 TSV）⇒ **单一事实源**，不是两份各自维护的清单。
//
// 本门只负责**安装器腿**那一半（embeddata + StartupFiles）；通道腿由 bash 侧那道门负责。

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
)

// nonplainDeliveryLegs 是登记册第 2 列的合法取值。
const (
	legChannel   = "channel"   // 只走部署通道腿（files=()）
	legInstaller = "installer" // 只走安装器腿（StartupFiles + embeddata）
	legBoth      = "both"      // 两条腿都走
	legNone      = "none"      // 有意不投递
)

// nonplainRegistryRow 是登记册的一行。
type nonplainRegistryRow struct {
	RelPath string // 相对 sql/migrations/startup/ 的路径（子目录件带前缀，如 "up/x.sql"）
	Leg     string
	Reason  string
}

// readNonplainRegistry 读登记册。**格式错 ⇒ 直接 t.Fatal**（观测手段坏了
// 与被观测对象有问题是两回事，前者必须硬失败）。
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
		if len(fields) < 2 {
			t.Fatalf("投递登记册 %s 第 %d 行字段数 %d（至少要有 文件名<TAB>投递腿）",
				nonplainRegistryPath, i+1, len(fields))
		}
		leg := strings.TrimSpace(fields[1])
		switch leg {
		case legChannel, legInstaller, legBoth, legNone:
		default:
			t.Fatalf("投递登记册 %s 第 %d 行的投递腿 %q 非法（合法值：%s/%s/%s/%s）",
				nonplainRegistryPath, i+1, leg, legChannel, legInstaller, legBoth, legNone)
		}
		rows = append(rows, nonplainRegistryRow{
			RelPath: strings.TrimSpace(fields[0]),
			Leg:     leg,
			Reason:  strings.TrimSpace(fields[2]),
		})
	}
	if len(rows) == 0 {
		t.Fatalf("投递登记册 %s 一条数据行都没有 ⇒ 观测手段失效（读了个空文件？）", nonplainRegistryPath)
	}
	return rows
}

// canonicalNonPlainMigrations 穷举 canonical 目录里**解析不出纯三位数字前缀**的
// up 体，返回相对路径集合。
//
// ⚠ 两处细节都不是可有可无的：
//
//	· **递归**（含 `up/` 子目录）—— 否则子目录里的件对这道门隐形；
//	· 按 **basename** 判定前缀（不是相对路径）—— `up/376_x.sql` 的 basename
//	  是纯数字前缀名 ⇒ 它**不是**非前缀迁移。
func canonicalNonPlainMigrations(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	err := filepath.WalkDir(canonicalStartupDir, func(path string, d os.DirEntry, err error) error {
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
		rel, relErr := filepath.Rel(canonicalStartupDir, path)
		if relErr != nil {
			return relErr
		}
		if isPlainNumberName(name) {
			return nil
		}
		out[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s: %v", canonicalStartupDir, err)
	}
	return out
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

// TestCanonicalNonPlainMigrationsHaveDeclaredDeliveryLeg 是本门的主判据：
// ① 登记册**恰好**覆盖 canonical 目录里全部非纯数字前缀的 up 体（双向）；
// ② 声明走安装器腿的，必须真的在 embeddata 里；
// ③ 声明「有意不投递」的，必须**不在** embeddata 里。
func TestCanonicalNonPlainMigrationsHaveDeclaredDeliveryLeg(t *testing.T) {
	rows := readNonplainRegistry(t)

	nonplain := canonicalNonPlainMigrations(t)
	// 量具自检：穷举必须真的扫到东西，否则 ① 会恒绿（门看上去正常、实际扫了个寂寞）。
	if len(nonplain) == 0 {
		t.Fatalf("穷举没有扫到任何非纯数字前缀的 canonical 迁移 ⇒ 枚举面失效 ⇒ 本门会恒绿")
	}

	declared := map[string]nonplainRegistryRow{}
	for _, r := range rows {
		if _, dup := declared[r.RelPath]; dup {
			t.Errorf("投递登记册里 %s 登记了两次", r.RelPath)
			continue
		}
		declared[r.RelPath] = r
	}

	// ①-a 目录里有、册子里没有 ⇒ 漏登记 ⇒ 这条迁移对所有门隐形
	for rel := range nonplain {
		if _, ok := declared[rel]; !ok {
			t.Errorf("canonical 迁移 %s 的文件名解析不出纯三位数字前缀 ⇒ 它对**强制 embed 门**"+
				"与**投递路径契约门**都完全隐形（两道门都靠文件名解析判视野）⇒ "+
				"它既不在 embeddata/StartupFiles、也不在通道 files=() ⇒ 新装库与升级库都拿不到 ⇒ "+
				"必须先在 %s 登记投递腿，再登记进对应通道", rel, nonplainRegistryPath)
		}
	}
	// ①-b 册子里有、目录里没有 ⇒ 陈旧条目
	for _, r := range rows {
		if _, ok := nonplain[r.RelPath]; !ok {
			t.Errorf("投递登记册 %s 登记了 %s，但 %s 下没有这个文件 ⇒ 登记册陈旧",
				nonplainRegistryPath, r.RelPath, canonicalStartupDir)
			continue
		}
		// ② 声明走安装器腿的，必须真的在 embeddata 里
		if r.Leg == legInstaller || r.Leg == legBoth {
			embedPath := filepath.Join(embedStartupDir, filepath.Base(r.RelPath))
			if _, err := os.Stat(embedPath); err != nil {
				t.Errorf("投递登记册声明 %s 走安装器腿，但它不在 %s 里 ⇒ 新装库拿不到：%v",
					r.RelPath, embedStartupDir, err)
			}
		}
		// ③ 声明「有意不投递」的，必须不在 embeddata 里
		if r.Leg == legNone {
			embedPath := filepath.Join(embedStartupDir, filepath.Base(r.RelPath))
			if _, err := os.Stat(embedPath); err == nil {
				t.Errorf("投递登记册声明 %s 有意不投递，但它**在** %s 里 ⇒ 声明与事实不符",
					r.RelPath, embedStartupDir)
			}
		}
	}
	t.Logf("非纯数字前缀 canonical 迁移 %d 条；登记册 %d 条", len(nonplain), len(rows))
}

// TestNonPlainRegistryLegValuesAreKnown 把「投递腿」取值收在四个已知值内。
// 单列一条是因为它读的是**登记册本身**（不是文件系统）⇒ 它的失败原因与主判据不同。
func TestNonPlainRegistryLegValuesAreKnown(t *testing.T) {
	for _, r := range readNonplainRegistry(t) {
		switch r.Leg {
		case legChannel, legInstaller, legBoth, legNone:
		default:
			t.Errorf("登记册 %s 的投递腿 %q 非法", r.RelPath, r.Leg)
		}
		if r.Reason == "" {
			t.Errorf("登记册 %s 没有写理由 ⇒ 「有意不投递」这类决定必须留下可审计的理由", r.RelPath)
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
