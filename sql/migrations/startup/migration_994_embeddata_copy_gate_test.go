package startup

// migration_994_embeddata_copy_gate_test.go —— 迁移 994 的**副本一致性门**
// （2026-11-23 211 部署破树轮补）。
//
// # 这道门守什么
//
// ★ 994 有**两份**字节：★ canonical（`sql/migrations/startup/`）与
// ★ embeddata 副本（`installer/cmd/llm-gw-installer/embeddata/startup/`）。
// ★ 两份分别喂给两条投递通道：★ 升级通道读 canonical、★ **新装通道**读 embeddata。
// ⇒ ★ 两份必须**逐字节相同**，★ 否则同一个迁移在两条通道上**跑出不同的行为**。
//
// ★ 为什么这条要单独钉（★ 本轮连着踩了两次）：
//
//	① **2026-11-23 第一次**：★ 并行车 761e641ed「D2 去重修复」把 994 的三处
//	   `JOIN sessions s` 改写成 `JOIN (SELECT DISTINCT ON … <列清单> …) s`，
//	   ★ **只改了 canonical** ⇒ ★ embeddata 副本仍是旧的 `JOIN sessions s`。
//	② **2026-11-23 第二次**：★ 并行车 d587edc47「994 迁移关并行」给 canonical
//	   加了 `SET max_parallel_workers_per_gather = 0`（★ 修 DSM 扩容撞 /dev/shm 上限），
//	   ★ **又只改了 canonical** ⇒ ★ embeddata 副本没有这行
//	   ⇒ ★ **新装通道仍会撞 DSM 地雷**（★ 211 的 PG 容器 /dev/shm 只有 64MB）。
//
// ★ 两次都不是「写错了一个字符」，★ 而是**「改了一处、忘了另一处」**
// ⇒ ★ 而通用的副本身份门（★ scripts/ursmcheck/embeddata_copy_identity_gate_test.go）
// ★ **住在重包里**、★ 又**不在部署闸门的选包范围**（★ 闸门按改动的 .go 文件选包
//   ⇒ ★ 纯 .sql 改动一个包都不触发）⇒ ★ 那道门两次都是**红的**，★ 却没人看见。
// ⇒ ⇒ 本门把 994 的这条性质**钉进迁移包本身**（★ 轻包、★ 迁移一改就会被选中）。
//
// ★ 判据面（★ 2026-11-23 订正口径）：★ 不是「994 这一对文件」，★ 而是
//   **所有「embeddata 有、canonical 也有同名文件」的迁移**。
//   ★ 实测：**607 对**（★ 候选 = embeddata 下全部 609 个 .sql 文件，
//   ★ 扣掉 2 个 canonical 无同名的 installer-only 资产）
//   ⇒ ★ 其中 up 迁移 **393 对**、down 脚本 214 对 ⇒ ★ 未来任何迁移分叉都会红。
//   ★ ★ 旧注释写的「实测 137 对」与提交说明/文档写的「609 对」**都不对**：
//   ★ 137 是旧快照、★ 609 是**文件数**不是**比较对数**⇒ ★ 三者对不上账
//   ⇒ ★ 这就是「★ 计数口径必须和数字一起发布 ★」的反面教材。

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	mig994CanonRel = "sql/migrations/startup/" + mig994File
	mig994EmbedRel = "installer/cmd/llm-gw-installer/embeddata/startup/" + mig994File
)

// migCopyPairs —— 找出 embeddata 目录下的所有 .sql 文件（★ 已排序）。
//
// ★ ★ 口径（★ 2026-11-23 订正）：★ 本函数返回的是**候选集**（★ 文件数），
//
//	★ **不是**「实际被逐字节比较的对数」⇒ ★ 调用方必须按 canonical
//	★ 是否存在同名文件来分「比较」与「跳过」，★ 并分别计数。
func migCopyPairs(t *testing.T) []string {
	t.Helper()
	embedRoot := filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup")

	entries, err := os.ReadDir(embedRoot)
	if err != nil {
		t.Fatalf("读 embeddata 目录 %s: %v", embedRoot, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// Test994EmbeddataCopyIsByteIdentical —— 994 的两份字节必须逐字相同。
func Test994EmbeddataCopyIsByteIdentical(t *testing.T) {
	canon, err := os.ReadFile(filepath.Join("..", "..", "..", mig994CanonRel))
	if err != nil {
		t.Fatalf("读 canonical: %v", err)
	}
	embed, err := os.ReadFile(filepath.Join("..", "..", "..", mig994EmbedRel))
	if err != nil {
		t.Fatalf("读 embeddata 副本: %v", err)
	}
	if !bytes.Equal(canon, embed) {
		t.Errorf("994 的 embeddata 副本与 canonical 【逐字不一致】（canonical %d 字节 / 副本 %d 字节）⇒ "+
			"升级通道与新装通道会跑【不同的字节】⇒ 新装通道仍缺 canonical 的改动"+
			"（★ 本轮已实测两次：★ D2 去重改写漏了副本、★ 关并行 SET 也漏了副本）",
			len(canon), len(embed))
	}
}

// TestAllMigrationCopiesAreByteIdentical —— ★ 判据面：★ 所有有副本的迁移都必须一致。
//
// ★ 为什么是「所有」而不是「994 一个」：★ 两次分叉都是**同一类**漏
// （★ 改一处忘另一处），★ 而 994 只是恰好被踩到的那一个
// ⇒ ★ 只钉 994 等于「下一个被踩的迁移没有门」。
func TestAllMigrationCopiesAreByteIdentical(t *testing.T) {
	canonRoot := filepath.Join("..", "..", "..", "sql", "migrations", "startup")
	embedRoot := filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup")

	names := migCopyPairs(t)
	// ★ 候选集（★ **文件数**）：★ embeddata/startup 下全部 .sql。
	candidates := len(names)

	// ★ 真正逐字节比较的对数（★ 这才是「判据面」）。
	compared := 0
	upCompared := 0
	skipped := 0 // ★ canonical 无同名 ⇒ 是 installer-only 资产（★ 由通用门管）
	mismatched := 0
	for _, name := range names {
		canon, err := os.ReadFile(filepath.Join(canonRoot, name))
		if err != nil {
			// ★ canonical 没有同名 ⇒ 是 installer-only 资产（★ 由通用门管）
			skipped++
			continue
		}
		compared++
		if !strings.HasSuffix(name, ".down.sql") {
			upCompared++
		}
		embed, err := os.ReadFile(filepath.Join(embedRoot, name))
		if err != nil {
			t.Errorf("%s：embeddata 副本读不出来: %v", name, err)
			mismatched++
			continue
		}
		if !bytes.Equal(canon, embed) {
			t.Errorf("%s：embeddata 副本与 canonical 逐字不一致（canonical %d 字节 / 副本 %d 字节）⇒ "+
				"新装通道与升级通道会跑不同的字节", name, len(canon), len(embed))
			mismatched++
		}
	}
	// ★ 判据面下限（★ 用**真正比较的对数**、★ 不是候选文件数）。
	//   ★ 修前用 `len(names)`（★ = 候选文件数）⇒ ★ 候选集缩水时**下限不会报警**
	//   ⇒ ★ 判据面退化**不会被发现**。
	//   ★ 实测 607 对 ⇒ 下限取 500（★ 留形态变化余量、★ 但挡住数量级退化）。
	if compared < 500 {
		t.Fatalf("判据面只覆盖了 %d 个副本对（下限 500）：★ 门要么退化了、★ 要么仓内形态变了", compared)
	}
	// ★ 跳过比例也要有界：★ 候选里绝大多数都该在 canonical 有同名文件。
	if candidates > 0 && skipped > candidates/10 {
		t.Errorf("判据面退化：%d/%d 个 embeddata 文件在 canonical 里没有同名文件（上限 1/10）",
			skipped, candidates)
	}
	if mismatched > 0 {
		t.Errorf("★ %d 个迁移的副本不一致 ⇒ 新装通道会跑不同的字节 ★", mismatched)
	}
	t.Logf("判据面：%d 个副本对（候选 %d 个文件；其中 up 迁移 %d 对；跳过 installer-only %d 个）",
		compared, candidates, upCompared, skipped)
}

// TestMigrationCopyJudgeHasTeeth —— ★ 量具自证（★ 合成夹具、★ 不动仓内真文件）。
//
// ★ 证明「逐字比较」这条判据真的能分辨好坏：★ 一份**故意不同**的副本必须被红。
func TestMigrationCopyJudgeHasTeeth(t *testing.T) {
	dir := t.TempDir()
	canonPath := filepath.Join(dir, "999_x.sql")
	embedPath := filepath.Join(dir, "999_x_copy.sql")
	if err := os.WriteFile(canonPath, []byte("SET x = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(embedPath, []byte("SET x = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustRead(t, canonPath), mustRead(t, embedPath)) {
		t.Fatal("★ 量具自证失败：两份相同字节被判成不一致 ⇒ 判据失效")
	}
	// ★ 对照臂：★ 改一个字节 ⇒ 必须被判成不一致。
	if err := os.WriteFile(embedPath, []byte("SET x = 2;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(mustRead(t, canonPath), mustRead(t, embedPath)) {
		t.Fatal("★ 量具自证失败：差一个字节的两份被判成一致 ⇒ 判据恒绿")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
