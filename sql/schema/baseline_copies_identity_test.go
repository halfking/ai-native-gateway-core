package schema

// baseline_copies_identity_test.go —— **基线副本三×三字节一致性门**（2026-11-09）
//
// # 这道门防什么
//
// 仓内有三份**基线 SQL**，每个文件各存三处：
//
//	sql/schema/                             ← ★ **SSOT（正典）**
//	installer/cmd/llm-gw-installer/embeddata/   ← 新装库实际用的那份（//go:embed）
//	deploy/sql/schemas/baseline/                ← 随包分发的部署基线
//
// 文件共 3 个 ⇒ 3 × 3 = **9 个实体**（实测大小：00-prereqs 3,629 B /
// 01-schema 1,057,486 B / 02-seed 163,111 B）。
//
// # 为什么要有这道门（不是「看着挺整齐」的那种理由）
//
// ★ **它们是三份**（不是一份 + 两个软链）⇒ ⇒ 任何一次「只改了一处」的提交
//   都会让**新装出来的库**与**存量库**的形状悄悄分叉。
//   ⇒ 而这正是本仓**已经踩过**的坑：docs/全面审计v3/2026-11-05/02-审计发现与缺陷台账.md
//   **B1** 条记的就是「`3c9954acb` 只更新 SSOT 01-schema.sql，两份基线副本停在
//   5be6b2f0e ⇒ `TestSessionBodiesBaselineCopiesAreByteIdentical` 红」。
//   ⇒ ⇒ **检测**能力当时是有的，但**只覆盖 01-schema.sql 一个文件**。
//
// ★★ **覆盖缺口（本门补的就是它）**：00-prereqs.sql 与 02-seed.sql 的副本
//   一致性**只有** `deploy/sql/verify-migration.sh` 在 diff —— ★ 而那个脚本
//   **没有任何可执行调用方**（实测：Makefile / verify.sh / scripts/ / tests/ /
//   .workflow/ / .github/ 里搜 `verify-migration.sh`，非注释行 **0 命中**）
//   ⇒ ⇒ 按本仓纪律「**门没有调用方 = 声明不是门**」，它**不是门**
//   ⇒ ⇒ 那两个文件的一致性在强制执行路径上**无人看守**。
//
// ★ 所以本门：
//   ① 覆盖**全部 9 个实体**（3 个文件 × 3 个位置），不再只看 01-schema.sql；
//   ② 住在 **`./sql/schema` 包**里 ⇒ ★ 该包**已登记进 `GUARD_PACKAGES`**
//      （Makefile:137）⇒ ⇒ 本门**自动**在 `make guards` / `make guards-fast`
//      / verify.sh 上执行 ⇒ ★ 满足「门必须有调用方」这条纪律，**无需**再登记。
//
// # 为什么不删掉同包里的 TestSessionBodiesBaselineCopiesAreByteIdentical
//
// ★ 那是 2026-11-05 B1 条**直接引用**的测试名与 file:line 指针
//   （docs/全面审计v3/2026-11-05/02-审计发现与缺陷台账.md:20 与
//   03-修复与验证记录.md:10）⇒ ⇒ **删它会让审计台账的指针失效**。
//   ⇒ 故保留它不动（它只覆盖 01-schema.sql，断言更窄但独立有效），
//     本门承担**完整矩阵**。
//   ⇒ ★ 两处断言**有意重叠**：重叠的是 01-schema.sql 一项 ⇒ 代价是
//     多读一次 1MB 文件（实测毫秒级）⇒ 换来「老台账指针不失效」+「新覆盖面」。
//
// # SSOT 约定（本门把它钉住，而不是只写在文档里）
//
// ★ `sql/schema/` 是**正典**：改动流程 = 改 SSOT ⇒ 跑
//   `scripts/checks/sync-baseline-copies.sh --apply` 把两份副本同步过去。
//   ⇒ ⇒ **手改三处**这个动作从流程上消失了。
// ★ 副本的**来源**是活库的 `pg_dump`（`sql/scripts/dump-schema.sh` →
//   `scripts/_lib/db-init-lib.sh`），生成器**只输出一份**到临时目录 ⇒
//   ⇒ **不要手改那三份 dump 里的任何一行**（手改会被下一次 dump 覆盖，
//     且会让 dump 不再等于真库形状）—— 本门只保证「三处彼此相同」。

import (
	"os"
	"testing"
)

// baselineIdentityMatrix 是**基线副本的完整矩阵**：3 个文件 × 3 个位置。
//
// ★ 顺序即 SSOT 顺序：第 0 个是**正典**，其余两个是**派生副本**。
var baselineIdentityMatrix = []struct {
	file   string
	copies []string
}{
	{
		file: "00-prereqs.sql",
		copies: []string{
			"../../sql/schema/00-prereqs.sql",
			"../../installer/cmd/llm-gw-installer/embeddata/00-prereqs.sql",
			"../../deploy/sql/schemas/baseline/00-prereqs.sql",
		},
	},
	{
		file: "01-schema.sql",
		copies: []string{
			"../../sql/schema/01-schema.sql",
			"../../installer/cmd/llm-gw-installer/embeddata/01-schema.sql",
			"../../deploy/sql/schemas/baseline/01-schema.sql",
		},
	},
	{
		file: "02-seed.sql",
		copies: []string{
			"../../sql/schema/02-seed.sql",
			"../../installer/cmd/llm-gw-installer/embeddata/02-seed.sql",
			"../../deploy/sql/schemas/baseline/02-seed.sql",
		},
	},
}

// TestBaselineCopiesFullMatrixIsByteIdentical 是本门本体。
//
// ★ 判据是**逐字节相等**（不是「包含某段」「行数相同」）——
//
//	字节相同 ⇒ 内容必然相同；反向不成立 ⇒ ⇒ 字节判据是**最强**且**最省**的。
//
// ★ 报错信息必须**可执行**：直接告诉人「哪两个文件分叉了」＋「怎么修」
//
//	（从 SSOT 同步的命令），否则读的人还得自己查。
func TestBaselineCopiesFullMatrixIsByteIdentical(t *testing.T) {
	for _, entry := range baselineIdentityMatrix {
		entry := entry
		t.Run(entry.file, func(t *testing.T) {
			// ① 存在性：先确认 3 份都在，否则下面比的是空气。
			for _, path := range entry.copies {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("基线副本缺失：%s（%v）\n"+
						"★ 三处缺一 ⇒ 新装库 / 部署基线 / 正典的形状就分叉了 ⇒ "+
						"★ 请从 SSOT 补回：bash scripts/checks/sync-baseline-copies.sh --apply",
						path, err)
				}
			}

			// ② 逐字节相等。
			ref := entry.copies[0]
			refBytes, err := os.ReadFile(ref)
			if err != nil {
				t.Fatalf("read %s: %v", ref, err)
			}
			for _, path := range entry.copies[1:] {
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read %s: %v", path, err)
				}
				if string(got) != string(refBytes) {
					t.Errorf("%s 与 SSOT %s **不再逐字节相同**（SSOT %d B，本份 %d B）\n"+
						"★ 基线副本分叉了 ⇒ 新装出来的库与存量库的形状会不一样 ⇒\n"+
						"★ 修法（不要手改三处）：从正典同步 ⇒\n"+
						"★     bash scripts/checks/sync-baseline-copies.sh --apply",
						path, ref, len(refBytes), len(got))
				}
			}
		})
	}
}

// TestBaselineIdentityMatrixCoversEveryBaselineFile 守住**矩阵本身**的完整性。
//
// ★ 为什么需要：上门的覆盖面来自这张表 ⇒ ⇒ **表少一行 = 覆盖面悄悄缩小**
//
//	⇒ 而这正是本门要防的那类退化（2026-11-05 的 B1 就是「只更新了一处」的漂移）。
//	⇒ ⇒ 这里钉死「必须覆盖这 3 个文件 × 3 个位置」，少一个即红。
func TestBaselineIdentityMatrixCoversEveryBaselineFile(t *testing.T) {
	const wantFiles = 3
	const wantCopiesEach = 3

	if len(baselineIdentityMatrix) != wantFiles {
		t.Fatalf("基线矩阵只覆盖 %d 个文件，应为 %d 个（00-prereqs / 01-schema / 02-seed）⇒ "+
			"★ 覆盖面缩小了 ⇒ 请把漏掉的文件补进 baselineIdentityMatrix",
			len(baselineIdentityMatrix), wantFiles)
	}
	for _, entry := range baselineIdentityMatrix {
		if len(entry.copies) != wantCopiesEach {
			t.Errorf("%s 在矩阵里只有 %d 份副本，应为 %d 份（SSOT / installer embeddata / deploy baseline）",
				entry.file, len(entry.copies), wantCopiesEach)
		}
		// ★ SSOT 必须是**第一个**（同步脚本按这个顺序取正典）。
		if entry.copies[0] != "../../sql/schema/"+entry.file {
			t.Errorf("%s 的 SSOT 位置不是 sql/schema/（实际 %q）⇒ 矩阵顺序被改乱了",
				entry.file, entry.copies[0])
		}
	}
}
