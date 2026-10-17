//go:build !integration

package main

// 2026-10-18：启动迁移「锁等待无守卫」债务的**增量门**。
//
// # 这道门防什么
//
// 生产实测（§44）：一条 `CREATE TABLE IF NOT EXISTS public.proxy_subscriptions`
// 拿 AccessExclusiveLock 堵住了 14 个网关会话，持续约 1.5 分钟。
// 根因不是这条迁移写得差，而是**执行器没有中央收口**：
// `installer/internal/dbinit/runner.go` 的 applySQL 只拼
// `-v ON_ERROR_STOP=1` 与可选 `--single-transaction`，**不设任何 lock_timeout**
// ⇒ 任何一条要拿强锁的迁移，都可能在等锁期间堵住全库。
//
// 普查（canonical 源 `sql/migrations/startup/`）：
//   920 个迁移中仅 17 个含 lock_timeout（1.9%），718 个含强锁 DDL 动词。
//
// # 为什么只卡增量，不卡存量
//
// 给 718 个历史文件补守卫是一次**部署行为变更**（原本能跑通的迁移可能
// 变成 55P03 失败），需要人拍板，不属于这道门能自行决定的范围。
// 本门只拦**新增**：870 之后的新迁移必须自带 lock_timeout，
// 防止债务继续扩大 —— 这是零风险的净收益。
//
// # 三条防止「判据自曝 / 空转」的设计
//
//  1. **阳性对照**：先断言存量里确实存在含 lock_timeout 的文件，
//     证明检测器读到了东西、规则本身会触发。缺这一步的话，
//     一个恒返回空的 detector 也能让本门全绿（判据纪律第 3 条）。
//  2. **非空枚举**：断言读到 870 号文件，否则路径写错 / 目录搬迁时
//     glob 返回空会伪装成「没有违规」。
//  3. **白名单是显式 map**，不是「没有就跳过」——新增一条豁免必须写出理由，
//     且 map 非空时每条都要带 comment 说明。

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// guardCutoffVersion 是存量与增量的分界：≤ 它不管，> 它必须自带守卫。
// 取值理由：870 是 2026-10-18 实测的生产 applied_at 最高版本
// （usage_ledger_recon_cover_idx，2026-10-17 19:10）。
const guardCutoffVersion = 870

// lockTakingDDL 是「会拿强锁」的 DDL 动词集合。
//
// ★ 为什么不能只查 CREATE TABLE：§44 那条是 CREATE TABLE IF NOT EXISTS，
// 但同族里 ALTER TABLE / DROP INDEX / REINDEX 同样拿 AccessExclusiveLock，
// 而 `CREATE INDEX`（非 CONCURRENTLY）拿 ShareLock 会阻塞读写。
// 只写 CREATE TABLE 的门会漏掉其余四类。
var lockTakingDDL = regexp.MustCompile(
	`(?i)\b(CREATE\s+(UNIQUE\s+)?INDEX|CREATE\s+TABLE|ALTER\s+TABLE|DROP\s+TABLE|DROP\s+INDEX|REINDEX|CLUSTER|TRUNCATE)\b`)

// migrationVersionRE 从文件名头部取迁移号。
//
// ★ 必须接受**字母后缀**：`318b_` / `361b_` / `478p2_` / `646b_` 是本仓的
// 正常约定（AGENTS.md 记的 532/392/631/407/536/540/510/513/516/542/361b 就是这一族）。
// 2026-10-18 修正：原正则写成 `^(\d+)_`，**只认纯数字**，
// 于是静默漏掉全部带后缀的迁移（实测 542 个可执行文件里漏掉 12 个）。
// ★ 漏掉的形态恰好是「`871b_xxx.sql` 这类新迁移会整条绕过本门」——
// 这是**假绿**：门看起来在工作，实际留着一个洞。字母后缀不能当噪声。
var migrationVersionRE = regexp.MustCompile(`^([0-9]+[a-z]?[0-9]*)_`)

// migrationLeadingDigitsRE 单独取迁移号的**前导数字**。
//
// ★ 不能靠 TrimRight 剥字母：`478p2_` 的字母在**中间**，
// 剥尾部字母会留下 "478p2" 导致 Atoi 失败（2026-10-18 实测踩到）。
var migrationLeadingDigitsRE = regexp.MustCompile(`^[0-9]+`)

// downMigrationSuffix 是回滚脚本；它们不会被 runner 执行，必须排除。
var downMigrationSuffix = ".down.sql"

// migration 是启动迁移文件的一条记录。
type migration struct {
	name    string // 文件名，用于报错定位
	version int    // 数值迁移号（`478p2_` 取 478）
	body    string
}

// readMigrationFiles 枚举 canonical 目录下**会被 runner 执行**的编号迁移。
//
// ★ 复用 migration_embed_sync_gate_test.go 里既有的 canonicalStartupDir 常量
// 而不是自己再定义一份：两份路径常量一旦漂移，本门就会静默扫错目录，
// 而「扫错目录」的表现恰好是「没有违规」⇒ 恒绿。
//
// ★ 用切片而不是 map[version]string：`318_` 与 `318b_` 的数值号都是 318，
// 用 int 作键会**静默丢掉其中一个文件**，而丢文件的表现同样是「少了一条违规」。
func readMigrationFiles(t *testing.T) []migration {
	t.Helper()
	if _, err := os.Stat(canonicalStartupDir); err != nil {
		t.Fatalf("读不到 canonical 启动迁移目录 %s: %v", canonicalStartupDir, err)
	}
	entries, err := filepath.Glob(filepath.Join(canonicalStartupDir, "*.sql"))
	if err != nil {
		t.Fatalf("glob 失败: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("枚举到 0 个迁移文件 —— glob 路径写错或目录被搬走，" +
			"此时本门会以「没有违规」的形式空转全绿")
	}
	out := make([]migration, 0, len(entries))
	for _, p := range entries {
		base := filepath.Base(p)
		// ★ 排除 .down.sql：回滚脚本不会被 runner 执行，
		// 把它算进来会让「含强锁 DDL 的数量」这个口径虚高。
		if strings.HasSuffix(base, downMigrationSuffix) {
			continue
		}
		m := migrationVersionRE.FindStringSubmatch(base)
		if m == nil {
			continue // 非编号文件（如 2026-07-13-*.sql）不在本门范围
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读 %s: %v", p, err)
		}
		digits := migrationLeadingDigitsRE.FindString(base)
		if digits == "" {
			t.Fatalf("文件 %s 匹配了迁移号正则却取不到前导数字（形态正则已漂移）", base)
		}
		version, err := strconv.Atoi(digits)
		if err != nil {
			t.Fatalf("文件 %s 的前导数字不是整数: %v", base, err)
		}
		out = append(out, migration{name: base, version: version, body: string(b)})
	}
	return out
}

// TestLockGuardDetectorCanFire 是阳性对照：本门自己也可能是坏的。
//
// 它在存量里找一条「既含强锁 DDL、又不含 lock_timeout」的文件作为样本
// 来验证规则会触发。若将来有人把 lockTakingDDL 改成永不匹配，
// 本用例会先红，而不是让 TestStartupMigrationsAboveCutoffHaveLockGuard
// 变成一盏永远绿的灯。
func TestLockGuardDetectorCanFire(t *testing.T) {
	files := readMigrationFiles(t)

	// 对照 1：规则确实能识别强锁 DDL（存量里应大量命中）。
	// 对照 2：规则确实能识别 lock_timeout（存量里应有命中）。
	ddlHits, toHits := 0, 0
	for _, m := range files {
		if lockTakingDDL.MatchString(m.body) {
			ddlHits++
		}
		if strings.Contains(strings.ToLower(m.body), "lock_timeout") {
			toHits++
		}
	}
	if ddlHits == 0 {
		t.Error("阳性对照失败：lockTakingDDL 在存量迁移里一个都没匹配上 —— " +
			"规则本身坏了，任何基于它的门都会恒绿")
	}
	if toHits == 0 {
		t.Error("阳性对照失败：存量里找不到含 lock_timeout 的迁移 —— " +
			"要么文件全被改坏，要么本门读错了目录")
	}

	// 对照 3：**枚举完整性**。凡「以数字开头且不是 .down.sql」的 .sql 文件
	// 都必须被计入 files。
	//
	// ★ 为什么必须写成与计数比较，而不是「拿另一条正则去匹一遍」：
	// 2026-10-18 第一版对照在测试里**硬编码了** `^[0-9]+[a-z]?[0-9]*_`，
	// 与被测的 migrationVersionRE **不是同一条规则** ⇒ 把被测正则退回
	// `^([0-9]+)_` 时对照照样全绿，而真带后缀的 `478p2_` 已被整条漏掉。
	// **断言与被测对象必须共用同一把尺子**，否则它测的是自己。
	entries, err := filepath.Glob(filepath.Join(canonicalStartupDir, "*.sql"))
	if err != nil {
		t.Fatalf("glob 失败: %v", err)
	}
	numeric := 0
	for _, p := range entries {
		base := filepath.Base(p)
		if strings.HasSuffix(base, downMigrationSuffix) {
			continue
		}
		// 「编号迁移」的独立判定：**第一个 `_` 之前的那一段里不能有连字符**。
		// `646_x.sql`/`478p2_x.sql`/`646b_x.sql` 满足；
		// `2026-07-13-multimodal-token-fields-hot.sql` 因含 `-` 被排除。
		// ★ 实现刻意用 strings.Index 而不是复用 migrationVersionRE：
		// 复制同一条正则等于把尺子复制一份，两边一起漂移时对照察觉不到。
		cut := strings.Index(base, "_")
		if cut <= 0 {
			continue
		}
		if !strings.Contains(base[:cut], "-") {
			numeric++
		}
	}
	if numeric != len(files) {
		t.Errorf("枚举完整性失败：编号迁移 %d 个，但只解析出 %d 个 —— "+
			"migrationVersionRE 与「编号迁移」的口径不一致，"+
			"被漏掉的迁移会整条绕过本门（假绿）", numeric, len(files))
	}
	t.Logf("阳性对照：读到 %d 个可执行迁移，强锁 DDL 命中 %d 个，lock_timeout 命中 %d 个；"+
		"独立判定的编号迁移 %d 个（枚举完整性已核对）",
		len(files), ddlHits, toHits, numeric)
}

// lockGuardAllowlist 是显式豁免：迁移号 → 豁免理由。
// ⚠️ 当前为空。往里加条目必须同时写清「为什么它不需要 lock_timeout」，
// 且该迁移仍需自己用 CONCURRENTLY 或 REPEATABLE READ 等手段处理并发。
var lockGuardAllowlist = map[int]string{}

// TestStartupMigrationsAboveCutoffHaveLockGuard 是本门的正题。
func TestStartupMigrationsAboveCutoffHaveLockGuard(t *testing.T) {
	files := readMigrationFiles(t)

	var violations []string
	checked := 0
	for _, m := range files {
		if m.version <= guardCutoffVersion {
			continue
		}
		if why, ok := lockGuardAllowlist[m.version]; ok {
			if strings.TrimSpace(why) == "" {
				violations = append(violations, describe(m,
					"在 lockGuardAllowlist 里但豁免理由是空的"))
			}
			continue
		}
		if !lockTakingDDL.MatchString(m.body) {
			continue // 纯 DML / 纯视图迁移，不拿强锁
		}
		checked++
		if !strings.Contains(strings.ToLower(m.body), "lock_timeout") {
			violations = append(violations, describe(m,
				"含强锁 DDL 但没有 lock_timeout —— 它可能在等锁期间堵住全库（参见 §44）"))
		}
	}

	if checked == 0 && len(violations) == 0 {
		t.Logf("当前没有 >%d 的强锁 DDL 迁移，本轮空转（正常：门等的是下一条新增）", guardCutoffVersion)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

func describe(m migration, msg string) string {
	return "迁移 " + m.name + "（#" + strconv.Itoa(m.version) + "）：" + msg
}
