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

// reAlterPK 匹配 DROP CONSTRAINT ... pkey 之类会拆掉主键的语句。
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
//   `scripts/pre-commit-check.sh` 强制要求每个迁移都有对应的 .down.sql
//   （check_migration_has_down），删文件会直接破坏这条项目约定；
//   而 `installer/internal/dbinit/runner.go` 里 down 文件数为 0
//   ⇒ 它们从不被自动执行，危险只来自**人工 psql**。
//   ⇒ CI 门唯一能做的是：让「新增一颗地雷」红，让「已知的那颗」有据可查。
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
}

// TestDownMigrationsDoNotBreakTheWritePath 是本门的正身。
//
// 对每个触碰 ursm_node_snapshot_min 的 .down.sql：
// 若它把主键改写成与 writer 的 ON CONFLICT **不同**的列集合，
//   且**不在** knownWritePathLandmines 里 ⇒ 红（新增地雷，硬拦）。
// 若在登记表里 ⇒ 要求登记理由非空且提到后果（否则登记本身在腐烂）。
//
// ★ 判据量的不是「文件内容对不对」，而是「跑完它之后写入还能不能成功」
//   —— 也就是**消费者的可用性**，不是**函数的返回值**。
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
		m := reAddPrimaryKey.FindStringSubmatch(body)
		if m == nil {
			continue // 不动主键的 down
		}
		got := joinCols(m[1])
		if got == want {
			continue
		}
		reason, registered := knownWritePathLandmines[name]
		if !registered {
			offenders = append(offenders, strings.Join([]string{
				name,
				"reverted PK to [" + got + "]",
				"writer needs [" + want + "]",
			}, "\n  "))
			continue
		}
		// 已登记：理由不能空，且必须说清后果，否则「登记」会退化成「静默豁免」。
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s 在 knownWritePathLandmines 里但理由为空 —— "+
				"登记必须写清「跑它会发生什么」，否则下一个读代码的人会以为它已被处理。", name)
		}
		if !strings.Contains(reason, "42P10") {
			t.Errorf("%s 的登记理由没提 42P10（本次 P0 的实际错误签名）—— "+
				"「危险」不写具体后果，就只是一句免责声明。", name)
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
	for name := range knownWritePathLandmines {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("登记表列了 %q 但该文件不存在：%v\n"+
				"★ 删文件会放过静默，留空登记会让人误以为已处理。两个方向都要有人发现。", name, err)
		}
	}
}

// TestSnapshotDownMigrationsCarrySafetyWarning ——
// 对确实危险且必须保留的回滚，强制要求它在文件头写明危险与前置条件。
//
// ★ 为什么这条不是「多此一举」：463.down 原本**已经有**这样的警告，
//   但警告写在注释里，于是「跑之前先确认」这一步在赶时间时最容易跳过。
//   把要求固化成测试，是为了让「有没有写警告」不再靠自觉。
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
