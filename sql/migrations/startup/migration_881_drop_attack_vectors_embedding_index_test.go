package startup

// migration_881_drop_attack_vectors_embedding_index_test.go —— 迁移 881 的契约门。
//
// # 这道门守的是什么
//
// 881 删掉 `injection_attack_vectors` 上那条**只写不读**的 ivfflat 索引。
// 删索引这件事最容易出的错是**"删错了"**或**"以后悄悄长回来"**，所以本门钉三件事：
//
//  ① 迁移确实删的是**那一条**索引（不是别的），且 down 文件能原样重建它；
//  ② 守卫在**迁移文件里现算**（不是写死的结论）—— 文件里必须同时出现
//     "embedding 有非 NULL 值就拒绝删" 这条**反向条件**；
//  ③ 两条 spec 存在**同一份字节**（sql/migrations/startup/ 与 installer 的
//     embeddata/ 各一份，内容必须一致）—— 只改一份会让装出来的库与仓库里的库分叉。
//
// # 为什么 ① 里的"守卫"必须反向
//
// 876 的守卫证的是"**有覆盖方**"（证明删了不亏），881 的守卫证的是
// "**没人写、没人读**"（同样证明删了不亏）。两者的共同点：证不出来就整条回滚。
// 本门检查的就是这个"证不出来就停"的分支**在文件里存在**。
//
// ★ 一条容易踩的坑（2026-10-20 审计实测）：PL/pgSQL 的 `RAISE` **只认裸 `%`**，
//   `%s` 是 SQL 函数 format() 的 specifier，在 RAISE 里不是 —— 解析器把 `%`
//   当作"取下一个参数"、紧跟的字符照原样输出。⇒ 迁移文件里写 `%s` 会让错误
//   消息渲染成"值+s"（本轮就踩过一次：'已有 1s 行'）。本门顺手钉住这一点。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	mig881Name     = "881_drop_attack_vectors_embedding_index.sql"
	mig881DownName = "881_drop_attack_vectors_embedding_index.down.sql"
)

// mig881Tree 返回仓库根（测试在 <root>/sql/migrations/startup/ 下）。
func mig881Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// <root>/sql/migrations/startup → 上溯三级到 <root>
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig881Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TestMigration881_TargetsOnlyTheEmbeddingIndex 钉住 ①：删的是那一条。
func TestMigration881_TargetsOnlyTheEmbeddingIndex(t *testing.T) {
	root := mig881Tree(t)
	up := mig881Read(t, root, filepath.Join("sql/migrations/startup", mig881Name))

	if !strings.Contains(up, "DROP INDEX IF EXISTS public.idx_attack_vectors_embedding") {
		t.Errorf("881 迁移里没有 DROP INDEX IF EXISTS public.idx_attack_vectors_embedding ⇒ 目标索引变了")
	}
	// 不得顺手删掉同表另外三条索引（categories / tenant / unique_attack_hash）。
	for _, keep := range []string{
		"idx_attack_vectors_categories",
		"idx_attack_vectors_tenant",
		"unique_attack_hash",
	} {
		if strings.Contains(up, "DROP INDEX IF EXISTS public."+keep) {
			t.Errorf("881 迁移删掉了 %s —— 它有读方，不在 881 的范围内", keep)
		}
	}
	// down 必须能把它重建回来（否则这条索引一旦误删就回不去了）。
	down := mig881Read(t, root, filepath.Join("sql/migrations/startup", mig881DownName))
	if !strings.Contains(down, "CREATE INDEX idx_attack_vectors_embedding") {
		t.Errorf("881 的 down 文件没有重建 idx_attack_vectors_embedding")
	}
	if !strings.Contains(down, "USING ivfflat (embedding vector_cosine_ops)") {
		t.Errorf("881 的 down 文件重建的索引形态与原定义不一致（应为 ivfflat + vector_cosine_ops）")
	}
}

// TestMigration881_GuardHasBothBranches 钉住 ②：守卫在文件里现算，且含反向分支。
func TestMigration881_GuardHasBothBranches(t *testing.T) {
	root := mig881Tree(t)
	up := mig881Read(t, root, filepath.Join("sql/migrations/startup", mig881Name))

	// 正向：证明"embedding 没有非 NULL 值"才能删。
	if !strings.Contains(up, "embedding IS NOT NULL") {
		t.Errorf("881 的守卫里没有「embedding IS NOT NULL」这一条件 ⇒ 守卫不会在真有人写向量时拦住")
	}
	// 反向：拦住时必须**中止**，不是只打个 NOTICE。
	if !strings.Contains(up, "RAISE EXCEPTION") {
		t.Errorf("881 的守卫里没有 RAISE EXCEPTION ⇒ 守卫证不出来时不会中止，会照删")
	}
	// 反向分支必须真的带这个条件（而不是别的条件的 EXCEPTION）。
	if !strings.Contains(up, "v_nonnull > 0") {
		t.Errorf("881 的守卫没有把「非 NULL 行数 > 0」接到中止分支上")
	}
}

// TestMigration881_EmbeddedCopyIsIdentical 钉住 ③：两份 spec 同一份字节。
func TestMigration881_EmbeddedCopyIsIdentical(t *testing.T) {
	root := mig881Tree(t)
	canonical := mig881Read(t, root, filepath.Join("sql/migrations/startup", mig881Name))
	embedded := mig881Read(t, root, filepath.Join("installer/cmd/llm-gw-installer/embeddata/startup", mig881Name))
	if canonical != embedded {
		t.Errorf("881 的 installer embeddata 副本与 sql/migrations/startup 的**不一致** ⇒ 装出来的库与仓库里的库会分叉")
	}
	downCanonical := mig881Read(t, root, filepath.Join("sql/migrations/startup", mig881DownName))
	downEmbedded := mig881Read(t, root, filepath.Join("installer/cmd/llm-gw-installer/embeddata/startup", mig881DownName))
	if downCanonical != downEmbedded {
		t.Errorf("881 的 down 文件两份副本不一致")
	}
}

// TestMigration881_RaiseUsesBarePercent 钉住 RAISE 的 spec 写法。
//
// PL/pgSQL 的 RAISE 只认**裸 `%`**（取下一个参数）；`%s` 是 SQL 函数 format()
// 的 specifier，在 RAISE 里**不是** spec —— 解析器把 `%` 当作"取下一个参数"、
// 紧跟的 `s` 照原样输出 ⇒ 消息会渲染成"值+s"。
// 实测（本轮踩到）：'已有 %s 行' 渲染成 '已有 1s 行'。
//
// ★ 判据只查 **RAISE 语句的格式串**，不查全文 ——
//   ① 同一文件里的 `format('SELECT count(*) FROM %s …', v_table)` 用 `%s` 是**对的**
//      （那是 SQL 函数 format()，不是 RAISE）⇒ 全文搜 %s 会误报；
//   ② 注释里提到 %s 也不该算 ⇒ 先剥 SQL 注释再查。
//   （本门第一版就栽在这两条上：误报了一次 format() 调用。）
func TestMigration881_RaiseUsesBarePercent(t *testing.T) {
	root := mig881Tree(t)
	raiseFmt := regexp.MustCompile(`RAISE\s+(?:EXCEPTION|NOTICE|WARNING|DEBUG)\s+((?:'(?:[^']|'')*'\s*)+)`)
	for _, rel := range []string{
		filepath.Join("sql/migrations/startup", mig881Name),
		filepath.Join("sql/migrations/startup", mig881DownName),
	} {
		body := stripMig881Comments(mig881Read(t, root, rel))
		for _, m := range raiseFmt.FindAllStringSubmatch(body, -1) {
			if strings.Contains(m[1], "%s") {
				t.Errorf("%s：RAISE 的格式串里出现了 %%s —— PL/pgSQL 的 RAISE 只认裸 %%，"+
					"%%s 会让消息渲染成「值+s」（本轮实测过）", rel)
			}
		}
	}
}

// stripMig881Comments 剥掉 SQL 行注释与块注释。
// （本门第一版没剥，于是注释里解释 %s 的那几行会把自己当违规。）
func stripMig881Comments(s string) string {
	for {
		i := strings.Index(s, "/*")
		if i < 0 {
			break
		}
		j := strings.Index(s[i+2:], "*/")
		if j < 0 {
			s = s[:i]
			break
		}
		s = s[:i] + " " + s[i+2+j+2:]
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if k := strings.Index(line, "--"); k >= 0 {
			line = line[:k]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
