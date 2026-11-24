package upgrader

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// ============================================================================
// migration_order_suffix_test.go
//
// 「离线升级执行序」守卫 —— 2026-11-24 12h 修订审计轮·第三轮新增。
//
// ── 要钉住什么（★ 本轮抓到的头号缺陷）─────────────────────────────────
//
// ★★★ 离线升级通道（installer/internal/upgrader/offline_apply.go）按
//      **migrationPathLess** 排序执行，而它用 **versionPrefix** 解析文件名的数字前缀：
//
//	func versionPrefix(name string) (int, bool) {
//	    idx := strings.Index(name, "_")
//	    if idx <= 0 { return 0, false }
//	    v, err := strconv.Atoi(name[:idx])   // ★★ "983b" ⇒ Atoi 失败 ⇒ (0,false)
//	    if err != nil { return 0, false }
//	    return v, true
//	}
//
// ★★★ 而 migrationPathLess 的「一方无数字前缀」分支是：
//
//	case okA: return false // a 带前缀、b 无前缀：无前缀（bootstrap）在前
//
// ★★★ ⇒ ★★ **字母后缀记法（983b / 983c / 989b …）被解析成「无数字前缀」**
//      ⇒ ⇒ 它们被排到 **所有纯数字迁移之前**
//      ⇒ ⇒ ★★ `983b_add_raw_model_name.sql`（ALTER TABLE request_metrics ADD COLUMN）
//      **排在 `983_request_metrics.sql`（建表）之前** ⇒ 表还不存在 ⇒ ALTER 落空
//      ⇒ ⇒ 而 executeSQLFile 用 **ON_ERROR_STOP=1** ⇒ ★★ **离线升级整体中止**。
//
// ★★★ ★★ **为什么上一轮那道门没抓到**：★
//      `installer/internal/dbinit/migration_auto_enum_order_test.go` 自己实现了
//      `autoEnumParseNumber`（★ **剥掉尾部字母** ⇒ "983b" ⇒ (983,"b")）
//      并在注释里断言「与仓内 versionPrefix 的**排序**行为一致」。
//      ★★ 而生产代码的 versionPrefix **不做折叠** ⇒ ★★ **门的口径与被测物不一致**
//      ⇒ ★★ 门一直绿着，生产一直乱序（★ 这就是「量具与被测物口径不一致 ⇒ 假绿」）。
//
// ── 这道门断言什么 ───────────────────────────────────────────────────────
//
// ★ ① **主臂（行为门）**：用**生产排序函数** `migrationPathLess` 给真实 startup
//   迁移文件名排序 ⇒ 断言 **字母后缀件排在其主号迁移之后**
//   （983b/983c 在 983 之后；989b 在 989 之后）。
// ★ ② **对照臂**：纯数字迁移之间的数值序仍正确（900 < 983）⇒ 证明门不是恒真。
// ★ ③ **对照臂**：bootstrap 类（无数字前缀）仍排在带前缀之前（既有语义不变）。
//
// ── 与 dbinit 侧 `migrationVersionOf` 的语义区别（★ 必须区分，否则两边都错）──
//
// ★ dbinit 的 migrationVersionOf 用来判「这条迁移是否已应用」（查台账 version）。
// ★ 它**必须不折叠**：★ 折叠后 989b 会去查台账 '989' ⇒ 而 '989' 行是
// ★ ledger repair 写的 ⇒ ⇒ 989b 被**假跳过** ⇒ public.supplier_contract_rates
// ★ 在新装实例上建不出来（★ 详见 installer/internal/dbinit/migration_version_identity_test.go）。
// ★★ ⇒ 两个函数**必须保持语义不同**：
// ★★   upgrader.versionPrefix ＝ **排序**口径 ⇒ 折叠（字母后缀归主号组）
// ★★   dbinit.migrationVersionOf ＝ **台账跳过**口径 ⇒ 不折叠（字母后缀不参与版本键）
// =============================================================================

const startupDirForOrder = "../../../sql/migrations/startup"

func startupNamesForOrder(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(startupDirForOrder)
	if err != nil {
		t.Fatalf("读 startup 迁移目录失败（%s）：%v", startupDirForOrder, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") || strings.HasSuffix(e.Name(), ".down.sql") {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// orderWithProductionSort 用**生产排序函数**给文件名排序（★ 不自造排序口径）。
func orderWithProductionSort(names []string) []string {
	out := append([]string(nil), names...)
	sort.Slice(out, func(i, j int) bool { return migrationPathLess(out[i], out[j]) })
	return out
}

func indexOf(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i
		}
	}
	return -1
}

// ── 门 ①（★ 主臂）：字母后缀件必须排在其主号迁移之后 ─────────────────────
func TestMigrationOrder_LetterSuffixSortsAfterItsMainMigration(t *testing.T) {
	ordered := orderWithProductionSort(startupNamesForOrder(t))

	cases := []struct{ main, suffix string }{
		{"983_request_metrics.sql", "983b_add_raw_model_name.sql"},
		{"983_request_metrics.sql", "983c_add_occurred_cursor_index.sql"},
	}
	for _, c := range cases {
		iMain, iSuf := indexOf(ordered, c.main), indexOf(ordered, c.suffix)
		if iMain < 0 || iSuf < 0 {
			t.Fatalf("★ startup 目录里找不到 %s 或 %s ⇒ 本门的前提（迁移已改名/登记）需重新评估。", c.main, c.suffix)
		}
		if iSuf < iMain {
			t.Errorf("★ ★★ 离线升级执行序错误：%s（第 %d 位）排在了 %s（第 %d 位）**之前**。\n"+
				"★ ★★ 字母后缀件被 versionPrefix 的 Atoi 失败判成「无数字前缀」⇒ 被排到所有纯数字迁移之前\n"+
				"★ ★★ ⇒ 补列件先跑、建表件后跑 ⇒ ALTER 落空 ⇒ ON_ERROR_STOP=1 ⇒ **离线升级整体中止**。\n"+
				"★ 修法：versionPrefix 对字母后缀**折叠回主号**（排序口径），\n"+
				"★     而 dbinit.migrationVersionOf（台账跳过口径）**保持不折叠**。",
				c.suffix, iSuf+1, c.main, iMain+1)
		}
	}
}

// ── 门 ②（对照臂）：纯数字迁移之间的数值序仍正确 ⇒ 门不是恒真 ──────────
func TestMigrationOrder_PureNumericOrderStillAscending(t *testing.T) {
	ordered := orderWithProductionSort(startupNamesForOrder(t))
	i900, i983 := indexOf(ordered, "900_freediscovery_schema_reassert.sql"), indexOf(ordered, "983_request_metrics.sql")
	if i900 < 0 || i983 < 0 {
		t.Fatalf("★ 找不到基准迁移 ⇒ 本门前提需重新评估。")
	}
	if i900 > i983 {
		t.Errorf("★ 对照臂失效：900 排在了 983 之后（%d > %d）⇒ 纯数字数值序坏了 ⇒ 本门的对照臂需要重新评估。",
			i900+1, i983+1)
	}
}

// ── 门 ③（对照臂）：bootstrap 类（无数字前缀）仍排在带前缀之前 ───────────
func TestMigrationOrder_NoPrefixFilesStillComeFirst(t *testing.T) {
	names := []string{"983b_add_raw_model_name.sql", "00-prerequisites.sql", "983_request_metrics.sql"}
	ordered := orderWithProductionSort(names)
	if ordered[0] != "00-prerequisites.sql" {
		t.Errorf("★ 对照臂失效：无数字前缀的 bootstrap 件应排在最前，实际第一个是 %s。", ordered[0])
	}
}
