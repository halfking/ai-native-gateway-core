package startup

// 回滚脚本的「写路径可破坏性」门（2026-10-06，审计 §10.65）
//
// 起因是一次 P0 静默数据丢失的真实复盘：
//   有人执行了 `463_ursm_v2_snapshot_tenant_identity.down.sql`，
//   它把 ursm_node_snapshot_min 的主键从 4 列退回 3 列
//   （同时 DROP NOT NULL 了 tenant_id）。
//   writer 的 `ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name)`
//   因此找不到匹配的唯一索引，**每一次写入**抛 SQLSTATE 42P10。
//   后果：间歇失败 51 小时 + 全丢 12.4 小时，768 次失败只记 WARN、零告警。
//
// ★ 关键不在于「有人跑错了」，而在于**没有任何东西拦着再跑一次**：
//   - 该 .down.sql 仍在仓里，内容与故障态**逐字相同**；
//   - 部署通道的迁移 runner **只跑 up**，所以 CI/部署门对它完全失明；
//   - 现有那道 `writer_on_conflict_contract_test.go` 钉的是
//     「基线 schema ↔ writer」，**从不看 .down.sql**。
//
// ⇒ 本门把「回滚会打断写路径」这件事从注释里搬到测试里。
//   注释不会让任何人停下，红门会。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// writer 路径：从本包出发的相对路径。
const snapshotWriterGo = "../../../domains/ursm/v2/persist/writer.go"

const snapshotTable = "ursm_node_snapshot_min"

// reONConflictFromWriter 提取 writer 的 ON CONFLICT 推断列。
// 形态与 domains/ursm/v2/persist 那道门同源，但这里必须自己读一次：
// 跨包的导出测试助手会把两处判据绑在一起，改一处就一起漂。
var reONConflictFromWriter = regexp.MustCompile(`(?is)ON\s+CONFLICT\s*\(([^)]*)\)`)

// reAddPrimaryKey 匹配 .down.sql 里的 ADD PRIMARY KEY / ADD CONSTRAINT ... PRIMARY KEY。
// 两种写法都要覆盖，否则「换个写法就绕过」会成为常态。
var reAddPrimaryKey = regexp.MustCompile(
	`(?is)ADD\s+(?:CONSTRAINT\s+\S+\s+)?PRIMARY\s+KEY\s*\(([^)]*)\)`)

// reRebindCanonicalName 匹配「把另一个表改名成 ursm_node_snapshot_min」。
//
// ★ 这条腿是补洞，不是锦上添花（2026-10-06 自曝）：
//
//	830.down 里 `ADD PRIMARY KEY` 出现 **0 次** —— 它不重写主键，
//	而是用 RENAME 交换：
//	  ursm_node_snapshot_min       -> ursm_node_snapshot_min_post825
//	  ursm_node_snapshot_min_legacy -> ursm_node_snapshot_min
//	于是规范名被**重新绑定到 453 时代那张 3 列主键的旧表**，
//	写入随即开始抛 42P10 —— 与 `463.down` 产出**完全相同的可观测状态**。
//
//	★ 教训与本 runbook 的 M25 同族：**「门绿」只说明门覆盖的那条路径是安全的**。
//	  变异/风险分析必须问「还有别的写法能造成同样的后果吗」。
var reRebindCanonicalName = regexp.MustCompile(
	`(?is)ALTER\s+TABLE\s+(?:ONLY\s+)?(?:public\.)?(\S+)\s+RENAME\s+TO\s+(?:public\.)?` + snapshotTable + `\b`)

// reDropPK 匹配 DROP CONSTRAINT ... pkey 之类会拆掉主键的语句。
var reDropPK = regexp.MustCompile(`(?is)DROP\s+CONSTRAINT\s+(?:IF\s+EXISTS\s+)?\S*` + snapshotTable + `_pkey`)

func normColSet(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func joinCols(s string) string { return strings.Join(normColSet(s), ",") }

// writerONConflictCols 返回 writer 的 ON CONFLICT 列集合（排序后逗号串）。
func writerONConflictCols(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.FromSlash(snapshotWriterGo))
	if err != nil {
		t.Fatalf("read %s: %v", snapshotWriterGo, err)
	}
	m := reONConflictFromWriter.FindSubmatch(b)
	if m == nil {
		t.Fatalf("writer.go 里没解析到 ON CONFLICT —— writer 形态变了，请同步本判据")
	}
	return joinCols(string(m[1]))
}

// knownWritePathLandmines = 已知会打断写路径、但因项目约定必须保留的 .down.sql。
//
// ★ 为什么是「登记表」而不是「直接删掉」：
//
//	`scripts/pre-commit-check.sh` 强制要求每个迁移都有对应的 .down.sql
//	（check_migration_has_down），删文件会直接破坏这条项目约定；
//	而 `installer/internal/dbinit/runner.go` 里 down 文件数为 0
//	⇒ 它们从不被自动执行，危险只来自**人工 psql**。
//	⇒ CI 门唯一能做的是：让「新增一颗地雷」红，让「已知的那颗」有据可查。
//
// ★ 登记 ≠ 免责。跑它的人必须自己承担 —— 因此每条都要写清后果。
var knownWritePathLandmines = map[string]string{
	"463_ursm_v2_snapshot_tenant_identity.down.sql": `
2026-10-06 真实 P0：有人执行了它，主键退回 3 列并 DROP NOT NULL 了 tenant_id，
writer 的 ON CONFLICT (4 列) 因此每次抛 42P10 —— 间歇失败 51 小时 + 全丢 12.4 小时。
★ 463 **没有安全回滚**：writer 是该表唯一写方且硬依赖 4 列主键，
  任何把它退回 3 列的脚本都会打断全部写入。
  真要回滚必须同时停掉 persist writer（URSM_V2_SHADOW_PERSIST=0），
  并接受该表将停止接收数据。
  保留原因：pre-commit-check 强制要求 .down.sql 存在，且 dbinit 从不执行它。
  检测侧已由 §10.60 的真库巡检覆盖（每小时比对已部署唯一约束 vs writer 列集合）。`,

	"830_ursm_node_snapshot_min_partitioned.down.sql": `
★ 第二颗地雷，与 463.down 产出**完全相同的可观测状态**，但走的是另一条路径：
  它不写任何 ADD PRIMARY KEY，而是 RENAME 交换
    ursm_node_snapshot_min       -> ursm_node_snapshot_min_post825
    ursm_node_snapshot_min_legacy -> ursm_node_snapshot_min
  规范名因此被重新绑定到 453 时代那张**3 列主键**的旧表，写入随即抛 42P10。
  ⚠ 正因为它不重写主键，本门最初的实现（只扫 ADD PRIMARY KEY）对它**完全失明**。
  ⚠ 库内状态无法区分 463.down 与 830.down：两者都留下「3 列主键 + tenant_id 可空」，
  且 _legacy / _post825 现均已不在，无残留可比对。
  830 是手工迁移（不在 installer 序列），本次线上重建窗口内它**已被改标 .sql.skip**，
  但仓内 down 仍在、且 partguard / partition_manager / db.go 的接线仍指向它。
  回滚前提（该文件自述）：_legacy 必须仍在，否则直接 RAISE。`,
}

// TestDownMigrationsDoNotBreakTheWritePath 是本门的正身。
//
// 对每个触碰 ursm_node_snapshot_min 的 .down.sql：
// 若它把主键改写成与 writer 的 ON CONFLICT **不同**的列集合，
//
//	且**不在** knownWritePathLandmines 里 ⇒ 红（新增地雷，硬拦）。
//
// 若在登记表里 ⇒ 要求登记理由非空且提到后果（否则登记本身在腐烂）。
//
// ★ 判据量的不是「文件内容对不对」，而是「跑完它之后写入还能不能成功」
//
//	—— 也就是**消费者的可用性**，不是**函数的返回值**。
func TestDownMigrationsDoNotBreakTheWritePath(t *testing.T) {
	want := writerONConflictCols(t)
	entries, err := filepath.Glob("*.down.sql")
	if err != nil {
		t.Fatalf("glob down.sql: %v", err)
	}
	if len(entries) < 50 {
		t.Fatalf("只找到 %d 个 .down.sql —— 目录约定若变了，这道门会安静地不再守护任何东西", len(entries))
	}

	var offenders []string
	var checked int
	for _, name := range entries {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(b)
		if !strings.Contains(body, snapshotTable) {
			continue
		}
		checked++
		_, registered := knownWritePathLandmines[name]
		m := reAddPrimaryKey.FindStringSubmatch(body)
		if m != nil {
			got := joinCols(m[1])
			if got != want && !registered {
				offenders = append(offenders, strings.Join([]string{
					name,
					"reverted PK to [" + got + "]",
					"writer needs [" + want + "]",
				}, "\n  "))
			}
		}
		// ★ 改名型：规范名被重新绑定到另一张物理表。
		//   静态分析无法知道那张表带什么主键（它可能来自任意历史版本），
		//   所以按「危险」处理并要求登记 —— 而不是因为「没写主键」就放过。
		if rm := reRebindCanonicalName.FindStringSubmatch(body); rm != nil {
			source := strings.TrimSpace(rm[1])
			if _, registered := knownWritePathLandmines[name]; !registered {
				offenders = append(offenders, strings.Join([]string{
					name,
					"rebinds " + snapshotTable + " to a different physical table (" + source + ")",
					"writer needs a unique index exactly on [" + want + "]",
					"★ 静态分析无法知道 " + source + " 带什么主键，故按危险处理；" +
						"若确知一致，请在登记里写明依据",
				}, "\n  "))
			}
		}
	}

	// 反向自证：这道门必须真的扫到了东西，否则「0 违规」是恒真。
	if checked < 3 {
		t.Fatalf("只检查了 %d 个涉及 %s 的 down 迁移 —— 预期至少 3 个（453/463/818/830）。"+
			"★ 没有覆盖就等于没有判据。", checked, snapshotTable)
	}

	if len(offenders) > 0 {
		t.Errorf("以下回滚脚本会把主键改成与 writer 的 ON CONFLICT 不一致的列集合，"+
			"跑一次就会让全部写入抛 42P10（2026-10-06 已真实发生过）：\n  %s\n\n"+
			"★ 处置二选一：\n"+
			"  1) 确实不需要回滚 → 登记进 knownWritePathLandmines 并写明后果；\n"+
			"  2) 确实需要 → 先让 writer 不再依赖这 4 列，否则回滚必然打断写路径。\n"+
			"  ★ 不要用「在注释里写危险」代替处置：部署通道只跑 up，"+
			"对 .down.sql 完全失明，唯一拦得住人的是把它登记在册并让后果显式化。",
			strings.Join(offenders, "\n  "))
	}
}

// TestLandmineRegistryHasNoStaleEntries —— 登记表本身会腐。
// 删了 .down.sql 却留着登记，或理由被清空，这道门都会安静地失去意义。
func TestLandmineRegistryHasNoStaleEntries(t *testing.T) {
	if len(knownWritePathLandmines) == 0 {
		t.Fatal("knownWritePathLandmines 为空 —— 上面的门已经不再拦任何东西，却仍显示绿色")
	}
	for name, reason := range knownWritePathLandmines {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("登记表列了 %q 但该文件不存在：%v\n"+
				"★ 删文件会放过静默，留空登记会让人误以为已处理。两个方向都要有人发现。", name, err)
		}
		// 登记 ≠ 免责：理由必须说清「跑它会发生什么」，否则它退化成一句免责声明。
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s 在 knownWritePathLandmines 里但理由为空 —— "+
				"登记必须写清后果，否则下一个读代码的人会以为它已被处理。", name)
		}
		if !strings.Contains(reason, "42P10") {
			t.Errorf("%s 的登记理由没提 42P10（本次 P0 的实际错误签名）—— "+
				"「危险」不写具体后果，就只是免责声明。", name)
		}
	}
}

// TestRenameStyleLandmineIsActuallyDetected —— 补洞的自证。
//
// ★ 这条不是「再测一遍」，而是钉住一个**曾经真实存在的漏网**：
//
//	门的第一版只扫 `ADD PRIMARY KEY`，而 830.down 用 RENAME 打断写路径
//	⇒ 门对它完全失明，却因为「没检出违规」而显示绿色。
//	绿色来自「没扫到」，不是来自「安全」。
//
// 因此：删掉登记里对 830.down 的那条，本门必须立刻红。
// 若将来有人「优化」掉改名这条腿，本门同样会红。
func TestRenameStyleLandmineIsActuallyDetected(t *testing.T) {
	const down = "830_ursm_node_snapshot_min_partitioned.down.sql"
	b, err := os.ReadFile(down)
	if err != nil {
		t.Fatalf("read %s: %v", down, err)
	}
	if !reRebindCanonicalName.Match(b) {
		t.Fatalf("%s 里没解析到「把别的表改名成 %s」——\n"+
			"★ 两种可能必须分开查：① 该回滚已改成别的实现 ⇒ 本门要重新学它的形态；\n"+
			"   ② 本门的 reRebindCanonicalName 失效 ⇒ 改名型地雷会重新漏网。\n"+
			"   不论哪种，现在都不能假设这条腿还工作。", down, snapshotTable)
	}
	// 该文件不含 ADD PRIMARY KEY —— 正是这个事实当初让门失明，把它钉住。
	if reAddPrimaryKey.Match(b) {
		t.Logf("注意：%s 现在也含 ADD PRIMARY KEY，两条检测路径同时生效；"+
			"本自证仍成立（改名那条仍必须被解析到）。", down)
	}
}

// TestSnapshotDownMigrationsCarrySafetyWarning ——
// 对确实危险且必须保留的回滚，强制要求它在文件头写明危险与前置条件。
//
// ★ 为什么这条不是「多此一举」：463.down 原本**已经有**这样的警告，
//
//	但警告写在注释里，于是「跑之前先确认」这一步在赶时间时最容易跳过。
//	把要求固化成测试，是为了让「有没有写警告」不再靠自觉。
func TestSnapshotDownMigrationsCarrySafetyWarning(t *testing.T) {
	const down = "463_ursm_v2_snapshot_tenant_identity.down.sql"
	b, err := os.ReadFile(down)
	if err != nil {
		t.Fatalf("read %s: %v", down, err)
	}
	head := string(b)
	if len(head) > 4000 {
		head = head[:4000] // 只看头部，别让文件后面的说明蒙混过关
	}
	low := strings.ToLower(head)
	for _, kw := range []string{"cannot be safely", "only after confirming"} {
		if !strings.Contains(low, kw) {
			t.Errorf("%s 头部缺少安全前置说明 %q。\n"+
				"★ 这条回滚会把主键退回 3 列并让全部写入失败；"+
				"「跑之前必须先确认」这句话必须在文件**开头**，不能只存在于历史 runbook 里。",
				down, kw)
		}
	}
}
