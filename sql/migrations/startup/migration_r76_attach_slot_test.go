// migration_r76_attach_slot_test.go — R76 轮：**分区索引 ATTACH 槽位冲突**的回归门。
//
// # 这个门守的是什么
//
// PostgreSQL 的硬约束（一次性 PG 17 实测，非推理）：
//
//	一个分区在**同一个父索引**下只能有**一个**成员索引，且
//	**与成员定义是否同义无关** —— 同义成员也报：
//	  ERROR:  cannot attach index "X" as a partition of index "Y"
//	  DETAIL: Another index is already attached for partition "Z".
//
// 而 PG 在**分区加入父表**、或**父分区索引存在之后再建分区**时，会**自动**给分区
// 建一个匹配索引并**自动命名**（形如 `<partition>_<column>_idx`，如
// `session_turns_2026_10_search_text_idx`）——**那才是生产 252 上的真实形态**
// （实测：父索引 `idx_session_turns_search_trgm` 的 6 个成员全是自动名）。
//
// 877 / 887 / 888 的三段式第 4 段原来按**索引名**判「挂没挂」：
// 在「该分区已有自动命名的成员」这个真实形态下 ⇒ 判成「没挂」⇒ 去 ATTACH
// ⇒ 撞槽位 ⇒ 迁移失败 ⇒ 官方门禁 abort（R73 实测 exit=2）。
//
// ★ 877 头部 :72-73 早就写下了规格：「验收必须按**分区级 + 定义匹配**查 pg_index，
// **不能按索引名 grep**」—— 本门守的就是**实现服从它自己的规格**。
//
// # 为什么用「静态判据 + 真库判据」两道
//
// · 静态门：断言三份迁移的 ATTACH 守卫**不再出现按名字判挂载**的写法，
//
//	且**必须**按分区级（`mi.indrelid`）判定。⇒ 任何把判据改回按名字的提交立刻红。
//	静态门不需要 DSN ⇒ 在任何 CI 上都跑。
//
// · 真库门：复现**原缺陷形态**（先普通 CREATE INDEX 建父索引 ⇒ 自动命名的成员在位），
//
//	跑完三份迁移 ⇒ 断言**零报错 + 零孤儿**。⇒ 证明的是行为，不是文本。
//
// # 真库门怎么跑
//
//	TEST_PG_URL=postgres://user:pass@localhost:5432/<一次性库>?sslmode=disable \
//	go test -tags=integration ./sql/migrations/startup/ -run TestMigrationR76 -count=1 -v
//
// ⚠ TEST_PG_URL 必须指向**可丢弃**的一次性库：本门会建表、建索引、跑整份迁移。
// ⚠ 库名必须匹配 bench|test|tmp|scratch|pg_ 之一（同包其它门的既有约定），
//
//	否则 WA877_ALLOW_ANY_DSN=1 才放行 —— 本门复用同一约定。
package startup

import (
	"strings"
	"testing"
)

// r76Migrations 是本门覆盖的三份迁移（canonical 副本）。
//
// ⚠ canonical 与 embeddata 必须**逐字相同**（installer 读 embeddata 那份）；
//
//	本门只断言 canonical 的内容，另有一道全量门
//	（installer/cmd/llm-gw-installer/migration_embed_sync_gate_test.go）守两份一致。
var r76Migrations = []string{
	"877_session_turns_search_text_trgm.sql",
	"887_session_turns_search_text_fts.sql",
	"888_session_family_join_index_gaps.sql",
}

// r76AttachGuardNeedles 是「按**名字**判挂载」的旧写法特征。
//
// ★ 这些 needle 只在**剥掉 SQL 注释之后**才有意义（见 stripSQLComments）：
//
//	877 头部 :72-73 的说明文字里就写着「不能按索引名 grep」，而本门自己的注释里
//	也会出现同样的字样 ⇒ 不剥注释就会**恒绿**（M6 教训：断言 SQL 文本前必须剥注释）。
var r76AttachGuardNeedles = []string{
	"ci.inhrelid = to_regclass",
}

// r76PartitionLevelNeedles 是「按**分区级**判成员」的写法特征。
var r76PartitionLevelNeedles = []string{
	"mi.indrelid =",
}

// r76StripSQLComments 去掉 SQL 注释（`--` 行注释与 `/* */` 块注释）。
//
// ★ 这不是洁癖：判据本身要能区分「代码里有」与「注释里提到」。
//
//	877 的头部注释逐字写着「不能按索引名 grep」，若不剥注释，
//	needle "按索引名" 会命中注释 ⇒ 门恒绿 ⇒ 门没有牙。
func r76StripSQLComments(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); {
		if strings.HasPrefix(src[i:], "--") {
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				break
			}
			i += j
			continue
		}
		if strings.HasPrefix(src[i:], "/*") {
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				break
			}
			i += 2 + j + 2
			continue
		}
		b.WriteByte(src[i])
		i++
	}
	return b.String()
}

// TestMigrationR76AttachGuardsArePartitionLevel —— 静态门（无 DSN 也能跑）。
//
// 判据：
//  1. 三份迁移剥注释后的正文里**不得**再出现「按名字判挂载」的写法；
//  2. 三份迁移剥注释后的正文里**必须**出现按分区级判成员（`mi.indrelid =`）的写法；
//  3. 剥离器自检（量具有牙）：把一段**含注释**的样本喂进去，断言注释里的 needle
//     **不会**被误判为命中 —— 否则上面两条判据会因「注释里写了同样的话」而恒绿。
func TestMigrationR76AttachGuardsArePartitionLevel(t *testing.T) {
	for _, name := range r76Migrations {
		name := name
		t.Run(name, func(t *testing.T) {
			raw := readMigrationContract(t, name)
			body := r76StripSQLComments(raw)

			for _, needle := range r76AttachGuardNeedles {
				if strings.Contains(body, needle) {
					t.Errorf("%s：ATTACH 守卫里仍出现「按索引名判挂载」的写法 %q\n"+
						"⇒ PG 的约束是「一个分区在同一个父索引下只能有一个成员索引」，\n"+
						"   与成员定义是否同义无关；按名字判会在「该分区已有 PG 自动命名的成员」\n"+
						"   （生产 252 的真实形态）时判成「没挂」⇒ 去 ATTACH ⇒ 撞槽位 ⇒ 迁移失败。\n"+
						"修法：判据改成「该分区在父索引下有没有成员（名字不限）」，\n"+
						"   即 pg_inherits JOIN pg_index，用 mi.indrelid 指向分区。",
						name, needle)
				}
			}

			for _, needle := range r76PartitionLevelNeedles {
				if !strings.Contains(body, needle) {
					t.Errorf("%s：找不到按分区级判成员的写法 %q\n"+
						"⇒ 三份迁移的 ATTACH 守卫必须改成 pg_inherits JOIN pg_index + mi.indrelid。",
						name, needle)
				}
			}
		})
	}
}

// TestMigrationR76CommentStripperHasTeeth —— 剥离器自检（量具有牙）。
//
// 若剥离器失效（不剥注释 / 剥错），上面的静态门会因注释里写着同样的字样而恒绿。
// ⇒ 这里用**含注释的样本**正面断言：注释里的 needle 必须被剥掉。
func TestMigrationR76CommentStripperHasTeeth(t *testing.T) {
	sample := "-- ci.inhrelid = to_regclass(format('public.%I', part || '_search_trgm'))\n" +
		"/* ci.inhrelid = to_regclass('x') */\n" +
		"SELECT mi.indrelid = c.oid FROM pg_index i;\n"
	got := r76StripSQLComments(sample)
	if strings.Contains(got, "ci.inhrelid") {
		t.Fatalf("剥离器没有剥掉注释里的 needle：\n%s", got)
	}
	if !strings.Contains(got, "mi.indrelid = c.oid") {
		t.Fatalf("剥离器把代码也剥掉了：\n%s", got)
	}
}
