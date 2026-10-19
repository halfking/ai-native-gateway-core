package persist

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 2026-10-20 12h 审计轮补的门禁：ON CONFLICT 的**列序**必须与真实 pkey 的
// 列序**逐字一致**。
//
// ★ 缺陷本体（静默失效）：这一整套改动的全部收益，取决于
// **插入序 == 物理 btree 键序**。物理键序由
//
//	ursm_node_snapshot_min_pkey PRIMARY KEY (snapshot_ts, tenant_id, credential_id, raw_model_name)
//
// 决定；而代码把「排序列」的唯一来源定为
//
//	ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name)
//
// ★ **PostgreSQL 的 ON CONFLICT 推断按列集合匹配、不要求顺序** ⇒ 把该子句
// 改写成任何一种排列都是**合法编辑**。改写后：排序跟着变 → 插入序不再等于
// pkey 键序 → 密度从 ~90% 悄悄退回 ~70%（改动注释自陈的 ~22% 索引空间白费）。
//
// ★ 而**没有任何一层会响**：
//   - 既有门 TestSnapshotIndexSortCoversEveryConflictColumn 只做**集合**双向比较；
//   - 生产巡检脚本更彻底 —— string_agg(a.attname, ',' ORDER BY a.attname)
//     先按字母序排好再比，顺序信息被**丢弃**。
//
// ⇒ 两层对「顺序」完全无感。
//
// ⇒ 本门把 **pkey 的列序**纳入作用域：解析 pkey 声明，与
// snapshotConflictColumns() **逐列同序**比较，不一致即红。
//
// 变异：把 writer.go 里 snapshotInsertSQL 的 ON CONFLICT 列序打乱
// （例如把 tenant_id 与 credential_id 对调）⇒ 本用例会失败
// （且既有那道集合门**仍然是绿的** —— 这正是本门存在的理由）。
func TestSnapshotIndexSortColumnOrderMatchesPrimaryKey(t *testing.T) {
	const pkeyFile = "../../../../sql/objects/constraints/ursm_node_snapshot_min_ursm_node_snapshot_min_pkey.sql"
	raw, err := os.ReadFile(pkeyFile)
	if err != nil {
		t.Fatalf("读不到 pkey 声明文件 %s：%v", pkeyFile, err)
	}
	pkeyCols := parsePrimaryKeyColumns(string(raw))
	if len(pkeyCols) == 0 {
		t.Fatalf("从 %s 里解析不出 pkey 的列清单 ⇒ 本门没有测到东西", pkeyFile)
	}

	conflictCols := snapshotConflictColumns()
	if len(conflictCols) == 0 {
		t.Fatal("解析不出 ON CONFLICT 的列 —— 排序键的来源失效了")
	}

	if len(conflictCols) != len(pkeyCols) {
		t.Fatalf("ON CONFLICT 有 %d 列（%v），pkey 有 %d 列（%v）⇒ 列集合就不一致",
			len(conflictCols), conflictCols, len(pkeyCols), pkeyCols)
	}
	for i := range pkeyCols {
		if conflictCols[i] != pkeyCols[i] {
			t.Fatalf("ON CONFLICT 的列序与 pkey 不一致：\n"+
				"  ON CONFLICT = %v\n  pkey        = %v\n"+
				"⇒ 第 %d 列不同（%q vs %q）\n"+
				"★ 后果是**静默**的：插入序不再等于 btree 键序 ⇒ 索引密度从 ~90%% "+
				"掉回 ~70%%（§143.11 实测），~22%% 的索引空间白费；\n"+
				"  而既有的集合门与生产巡检脚本（按字母序归一）**都仍然是绿的**。",
				conflictCols, pkeyCols, i, conflictCols[i], pkeyCols[i])
		}
	}
}

// parsePrimaryKeyColumns 从 pkey 的 DDL 文本里解析出列清单（保持出现顺序）。
func parsePrimaryKeyColumns(ddl string) []string {
	re := regexp.MustCompile(`(?is)PRIMARY\s+KEY\s*\(([^)]*)\)`)
	m := re.FindStringSubmatch(ddl)
	if m == nil {
		return nil
	}
	parts := strings.Split(m[1], ",")
	cols := make([]string, 0, len(parts))
	for _, p := range parts {
		if c := strings.TrimSpace(p); c != "" {
			cols = append(cols, c)
		}
	}
	return cols
}
