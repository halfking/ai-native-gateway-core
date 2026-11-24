package dbinit

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/upgrader"
)

// sortWithProductionSort 用**生产排序函数**（upgrader.MigrationPathLess）排序。
// ★ 为什么要跨包借生产函数：★ 本轮实测教训＝「门自造一份排序口径」⇒ 门绿而生产乱序。
func sortWithProductionSort(names []string) []string {
	out := append([]string(nil), names...)
	sort.SliceStable(out, func(i, j int) bool { return upgrader.MigrationPathLess(out[i], out[j]) })
	return out
}

// ============================================================================
// migration_version_identity_test.go
//
// 「迁移身份」守卫 —— 2026-11-24 12h 修订审计轮·第三轮新增。
//
// ── 这道门钉三件事（三件都是**实测坐实**的，不是推测）────────────────────
//
// ★★★ ① **台账跳过口径**：安装器腿的「已应用」判据是
//      **文件名版本号 → 查 public.schema_migrations.version**
//      （startupSkipReason → startupPending → migrationVersionOf）。
//      而 version 列是 **text** ⇒ **一行只能属于一个 version**
//      ⇒ 若两条迁移共用同一个**文件名版本号**，先执行的那条会把那一行占住
//      ⇒ 后执行的那条被判「已应用」⇒ **被跳过** ⇒ 它的产物永不落地。
//
// ★★★ ② **排序口径与跳过口径必须不同**（★ 本轮实测坐实的「假绿」闭环）：
//      · upgrader.versionPrefix（**执行序**）⇒ 字母后缀**折叠**回主号
//        ⇒ 983b/983c 排在 983 之后（★ 本轮已修：折叠前实测 983b 排第 14 位、
//          983 排第 658 位 ⇒ 离线升级里补列件先于建表件 ⇒ ON_ERROR_STOP=1 整体中止）。
//      · dbinit.migrationVersionOf（**台账跳过**）⇒ 字母后缀**不折叠**
//        ⇒ 不参与版本键 ⇒ 永不被同号行假跳过（★ 折叠会重新引入 989b 被假跳过）。
//      ⇒ ★ 两个函数的语义**必须保持不同** ⇒ 本门分别钉住两侧。
//
// ★★★ ③ **自登记版本号 == 文件名版本号**（★ 台本「谁拥有这一行」的唯一性）
//      ⇒ 不满足时，**同一号段的多条迁移会抢同一行台本**
//      ⇒ ⇒ 运维读台本会被误导，★ 且产物到位探针（ledger_artifact_probe）若按
//        version 判据 ⇒ **假通过**（★ 本轮已把 5 条探针判据改成「version + description」双条件）。
//
// ── 门 ① 的号段口径（★ 与既有唯一性门对齐，不重复造轮子）───────────────
//
// ★ tests/migration_version_prefix_unique_test.sh **只管 ≥500 号段**
//   （★ 实测：500 号以下的 15 组同号是**既有基线**，被该门放过）。
// ★★ 而安装器腿的跳过判据**不分类号段** ⇒ ★★ 但本门也**不重复造轮子**：
// ★★ 门 ① 与唯一性门**同口径（≥500 段）**⇒ 两门在同一批号段上互补：
// ★★   唯一性门 ⇒ 判「同号是否算撞号（无关身份）」
// ★★   本门 ①   ⇒ 判「同号会不会让安装器腿假跳过（功能后果）」
// ★★ 500 号以下的既有同号 ⇒ 由门 ①'（自登记一致）+ 生产账本核对覆盖。
// =============================================================================

// ── 门 ①：纯数字版本键不得重复（≥500 号段，与唯一性门同口径）────────────
func TestMigrationIdentity_PureNumericVersionKeyIsUnique(t *testing.T) {
	owner := map[int]string{}
	var dupes []string
	for _, name := range startupMigrationFiles(t) {
		v, ok := migrationVersionOf(name)
		if !ok {
			// ★ 非纯数字前缀 ⇒ **不参与版本键** ⇒ 不在本门判据面内
			// （★ 仓内同族后缀记法的既有约定，见文件头说明）。
			continue
		}
		if v < 500 {
			// ★ 与 tests/migration_version_prefix_unique_test.sh **同口径**
			// （★ 它只管 ≥500 号段）⇒ 500 以下的既有同号不重复报。
			continue
		}
		if prev, seen := owner[v]; seen {
			dupes = append(dupes, versionKeyDupMessage(v, name, prev))
		} else {
			owner[v] = name
		}
	}
	if len(dupes) > 0 {
		t.Fatalf("★ ≥500 号段的 startup 迁移**纯数字版本键重复** ⇒ 安装器腿按「文件名版本号查台账」判定时，\n"+
			"★ 先执行的那条会占住该行 ⇒ **后执行的那条被假跳过** ⇒ 它的产物永不落地。\n"+
			"★ 重复项：\n  %s\n"+
			"★ 修法：把后一条改成**同族字母后缀**（如 989b_…）⇒ 非纯数字前缀不参与版本键 ⇒ 不再被假跳过。",
			strings.Join(dupes, "\n  "))
	}
}

func versionKeyDupMessage(v int, a, b string) string {
	return "版本键 " + strconv.Itoa(v) + " ⇒ " + a + "  与  " + b
}

// ── 门 ①'（★ 棘轮）：台账自登记版本号必须等于文件名版本号 ───────────────
//
// ★ 存量豁免清单（★ 8 条，每条都有分类；★ 豁免集合必须**恰好**等于扫描结果
//   ⇒ 新增错位 ⇒ 红；修好一条 ⇒ 也要从清单里删掉 ⇒ 红 ⇒ ★⇒ **只许变好**）。
var selfRegistrationKnownExceptions = map[string]struct {
	written string
	kind    string
}{
	// ★ 非数字版本文本（★ 不参与版本键 ⇒ 不是错位；但台本里会有一行非数字 version）。
	"445_routing_persistence_hardening.sql": {"2026-07-14-routing-persistence-hardening", "非数字版本文本"},
	// ★ 已作废迁移（★ 被 681 回退；台本描述为 SUPERSEDED）⇒ 它的自登记值是历史残留。
	"665_provider_error_details_fingerprint_index_repair.sql": {"663", "已作废迁移（被 681 回退）"},
	// ★ 台账回填迁移（★ 职责内：它给**别的**迁移补记账行）。
	"923_schema_migrations_ledger_backfill.sql": {"900", "台账回填迁移（职责内）"},
	// ★★ 错位一格（★ 真缺陷：★ 生产台本上这五行被前一条覆盖 ⇒ 运维读台本被误导。
	//   ★★ 彻底修复需要**改这 5 条已 applied 迁移的字节** ⇒ 触 L23 字节冻结
	//   ★★ ⇒ 属 owner 决策项（★ 主档 §4-L1 已登记，★ 本轮不擅自拍板）。
	"985_request_reconciliation_links.sql":  {"984", "错位一格（L1，owner 决策项）"},
	"986_provider_attempt_media_usage.sql":   {"985", "错位一格（L1，owner 决策项）"},
	"987_provider_invoice_evidence.sql":      {"986", "错位一格（L1，owner 决策项）"},
	"988_provider_invoice_usage_buckets.sql": {"987", "错位一格（L1，owner 决策项）"},
	"989b_supplier_contract_rates.sql":       {"988", "错位一格（L1，owner 决策项）"},
}

var selfRegistrationInsertPattern = regexp.MustCompile(
	`(?s)INSERT INTO public\.schema_migrations\s*\([^)]*\)\s*VALUES\s*\(\s*'([^']+)'`)

func TestMigrationIdentity_SelfRegisteredVersionMatchesFileName(t *testing.T) {
	found := map[string]string{}
	for _, name := range startupMigrationFiles(t) {
		raw, err := os.ReadFile(autoEnumStartupDir + "/" + name)
		if err != nil {
			t.Fatalf("读迁移文件失败（%s）：%v", name, err)
		}
		body := string(raw)
		if !strings.Contains(body, "INSERT INTO public.schema_migrations") {
			continue // ★ 无自登记段 ⇒ 不参与台本 ⇒ 不在本门判据面内
		}
		idx := strings.Index(name, "_")
		if idx <= 0 {
			continue
		}
		fileVersion := name[:idx]
		matches := selfRegistrationInsertPattern.FindAllStringSubmatch(body, -1)
		if len(matches) == 0 {
			continue
		}
		hit := false
		for _, m := range matches {
			if m[1] == fileVersion {
				hit = true
				break
			}
		}
		if !hit {
			written := make([]string, 0, len(matches))
			for _, m := range matches {
				written = append(written, m[1])
			}
			found[name] = strings.Join(written, ",")
		}
	}

	// ★ 豁免集合必须**恰好**等于扫描结果 ⇒ 新增错位 ⇒ 红；修好一条 ⇒ 也要删豁免 ⇒ 红。
	var unexpected, stale []string
	for name := range found {
		if _, ok := selfRegistrationKnownExceptions[name]; !ok {
			unexpected = append(unexpected, name+" ⇒ 自登记 "+found[name])
		}
	}
	for name := range selfRegistrationKnownExceptions {
		if _, ok := found[name]; !ok {
			stale = append(stale, name+" ⇒ 已不再错位（★ 请从豁免清单里删掉它）")
		}
	}
	sort.Strings(unexpected)
	sort.Strings(stale)
	if len(unexpected) > 0 {
		t.Errorf("★ ★★ 新增**台账自登记版本号 ≠ 文件名版本号**的迁移：\n  %s\n"+
			"★ ⇒ 它与同号迁移**抢同一行台本** ⇒ 运维读台本被误导，\n"+
			"★ ⇒ 且产物到位探针若只按 version 判据 ⇒ **假通过**（★ 巡检失明）。\n"+
			"★ 修法：把自登记值改成文件名版本号（★ 若该迁移已 applied ⇒ 改字节触 L23 ⇒ 需 owner 拍板）。",
			strings.Join(unexpected, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("★ 豁免清单里的条目**已不再错位**（★ 棘轮只许变好 ⇒ 请从清单里删掉）：\n  %s",
			strings.Join(stale, "\n  "))
	}
}

// ── 门 ②：字母后缀记法不参与版本键 ⇒ 永不被假跳过 ──────────────────────
func TestMigrationIdentity_LetterSuffixNeverParticipatesInVersionKey(t *testing.T) {
	cases := []string{
		"983b_add_raw_model_name.sql",
		"983c_add_occurred_cursor_index.sql",
		"989b_supplier_contract_rates.sql",
		"361b_xxx.sql",
		"478p2_affinity.sql",
	}
	for _, name := range cases {
		if v, ok := migrationVersionOf(name); ok {
			t.Errorf("★ %s 的文件名前缀**不是纯数字**，却解析出版本键 %d ⇒ "+
				"★ 它会参与安装器腿的「按台账跳过」判定 ⇒ 一旦同号行存在就被假跳过。", name, v)
		}
		if !startupPending(name, productionLedgerShape()) {
			t.Errorf("★ %s 应当**恒为 pending**（非纯数字前缀 ⇒ 不参与版本键）⇒ "+
				"★ 但 startupPending 返回 false ⇒ 它会被台账里的同号行假跳过。", name)
		}
	}
}

// ── 门 ③（★ 核心行为门）：供应商合同费率表不得被假跳过 ──────────────────
func TestMigrationIdentity_SupplierContractRatesIsNeverFalselySkipped(t *testing.T) {
	applied := productionLedgerShape()

	// ★ 主臂：改用同族后缀后，该迁移**不得**被台账里的 '989' 行假跳过。
	const newName = "989b_supplier_contract_rates.sql"
	if !startupPending(newName, applied) {
		t.Errorf("★ %s 在生产账本形态（'989' 行已被 ledger repair 占用）下被判「已应用」⇒ "+
			"★ 会被**假跳过** ⇒ public.supplier_contract_rates 在新装实例上建不出来。\n"+
			"★ 修法：文件名用**同族字母后缀**（非纯数字前缀 ⇒ 不参与版本键 ⇒ 恒 pending）。", newName)
	}

	// ★ 对照臂（★ 固化已坐实的事实，★ 防「有人以为这是新缺陷」）：
	//   旧名带纯数字前缀 ⇒ 查 '989' ⇒ 命中 ledger repair 写的行 ⇒ 跳过。
	const oldName = "989_supplier_contract_rates.sql"
	if startupPending(oldName, applied) {
		t.Errorf("★ 对照臂失效：%s 在生产账本形态下**居然**不被跳过 ⇒ "+
			"★ 说明安装器腿的跳过判据变了 ⇒ 本门的前提（按文件名版本号查台账）需要重新评估。", oldName)
	}

	// ★ 对照臂 2：纯数字前缀**确实**参与版本键判定（★ 证明门 ② 不是恒真）。
	if startupPending("985_request_reconciliation_links.sql", applied) {
		t.Errorf("★ 对照臂失效：纯数字前缀的迁移居然不参与版本键判定 ⇒ 门 ②/门 ③ 的前提需要重新评估。")
	}

	// ★★★ 磁盘绑定（★ 把上面的**纯函数性质**绑到**磁盘上真实存在的那个文件**）：
	// ★ 若磁盘上还是纯数字前缀的 `989_supplier_contract_rates.sql`，
	// ★ 则上面主臂只是一句「假如叫这个名字就不会被跳过」的假设 ⇒ **测不到真实产物**。
	for _, name := range startupMigrationFiles(t) {
		if !strings.Contains(name, "supplier_contract_rates") {
			continue
		}
		if _, ok := migrationVersionOf(name); ok {
			t.Errorf("★ 磁盘上的供应商合同费率迁移仍叫 %s（**纯数字前缀**）⇒ \n"+
				"★ ★★ 它参与安装器腿的版本键判定 ⇒ 台账里 '989' 行（ledger repair 写的）\n"+
				"★ ★★ 会把它假跳过 ⇒ **新装实例上 public.supplier_contract_rates 建不出来**。\n"+
				"★ 修法：改名为同族字母后缀 989b_supplier_contract_rates.sql（★ 文件内容/字节不变）。", name)
		}
	}
}

// ── 门 ④：部署通道按「序列名:文件名 + 内容指纹」记账（★ 两条通道口径不同的前提）──
func TestMigrationIdentity_DeployChannelKeysByFileNameAndFingerprint(t *testing.T) {
	const script = "../../../scripts/apply-db-revision-sequence.sh"
	raw, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("读部署通道脚本失败（%s）：%v", script, err)
	}
	body := string(raw)
	if !strings.Contains(body, `marker="${sequence_name}:${base}"`) {
		t.Errorf("★ 部署通道脚本不再按「序列名:文件名」构造 marker ⇒ \n"+
			"★ 它可能改成了按 schema_migrations.version 记账 ⇒ \n"+
			"★ ★★ 那么「安装器腿按文件名版本号查台账」与「部署通道」将变成同一套口径 ⇒ \n"+
			"★ ★★ 本文件的门 ①/门 ③ 的前提（两条通道口径不一致）需要重新评估。")
	}
	if !strings.Contains(body, "content_sha256") {
		t.Errorf("★ 部署通道脚本不再记录 content_sha256 ⇒ ★ 内容指纹通道缺失 ⇒ 需要重新评估跳过判据。")
	}
}

// ── 门 ⑤（★ 跨口径对照）：排序口径（折叠）与跳过口径（不折叠）必须不同 ──
//
// ★★★ 这是本轮最容易被「顺手统一」掉的地方：★
//	· 有人看到两个函数都叫「解析文件名前缀」⇒ 可能会把它们**统一**★
//	· 但统一到「折叠」⇒ 989b 会被台账里 '989' 行假跳过 ⇒ 供应商合同费率表建不出来
//	· 统一到「不折叠」⇒ 离线升级里 983b 排到建表件之前 ⇒ 离线升级整体中止
// ★★ ⇒ 两个方向都会出事 ⇒ ⇒ 必须**分别**钉住 ★★。
func TestMigrationIdentity_SortScopeFoldsButSkipScopeDoesNot(t *testing.T) {
	// ★ 跳过口径（dbinit）：**不折叠** ⇒ 字母后缀不参与版本键。
	if v, ok := migrationVersionOf("983b_add_raw_model_name.sql"); ok {
		t.Errorf("★ dbinit.migrationVersionOf 把 983b 解析成 %d ⇒ ★ 它参与了台账版本键判定 ⇒ "+
			"★ 一旦台账里有 '983' 行，983b 就会被假跳过。", v)
	}
	// ★ 排序口径（upgrader）：**折叠** ⇒ 字母后缀归主号组 ⇒ 排在主迁移之后。
	// ★★ 这里用**生产排序函数**的行为间接验证（★ 直接断言跨包的 upgrader.versionPrefix
	//    不可导入，故通过它导出的排序函数来证）★★。
	ordered := sortWithProductionSort([]string{
		"983b_add_raw_model_name.sql",
		"983_request_metrics.sql",
	})
	if ordered[0] != "983_request_metrics.sql" {
		t.Errorf("★ 排序口径未折叠字母后缀 ⇒ %s 排在了 %s 之前 ⇒ "+
			"★ 离线升级里补列件会先于建表件执行 ⇒ ALTER 落空 ⇒ ON_ERROR_STOP=1 ⇒ 整体中止。",
			ordered[0], ordered[1])
	}
}

// startupMigrationFiles 列出 canonical startup 目录里的正向迁移文件（★ 不含 .down.sql）。
func startupMigrationFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(autoEnumStartupDir)
	if err != nil {
		t.Fatalf("读 startup 迁移目录失败（%s）：%v", autoEnumStartupDir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// productionLedgerShape 生产账本形态（211 只读实测，2026-11-24）：
// ★ '984'…'988' 由 985/986/987/988/989_supplier 依次错位写入，'989' 由 ledger repair 写入。
func productionLedgerShape() map[int]bool {
	return map[int]bool{984: true, 985: true, 986: true, 987: true, 988: true, 989: true}
}
