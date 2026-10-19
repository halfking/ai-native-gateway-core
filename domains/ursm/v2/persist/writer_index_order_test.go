package persist

// writer 的插入顺序必须与唯一约束的列序一致（2026-10-19，§143.11）
//
// # 这道门补的是什么
//
// 生产实测（252，2026-10-19）：`ursm_node_snapshot_min` 的日分区 pkey
// （`(snapshot_ts, tenant_id, credential_id, raw_model_name)`）
// 的 `avg_leaf_density` 只有 **70.25%** —— 叶页空着三成。
//
// 而它**不是**删除造成的：同一个分区的
// · 索引 `deleted_pages = 0`
// · 索引条目数（reltuples）**等于**堆活行数（4,121,383）
// · 堆死元组 **0**
//
// ⇒ 排除「删除 churn」⇒ 剩下唯一解释是**插入顺序与键序不一致**。
//
// # 机制（代码 + 生产数据双向确认）
//
//  ① `Collect` 用 Redis SCAN 迭代器读 key ⇒ 行序 = 哈希表序，**不是键序**。
//  ② `Collect` 注释明写「所有行使用同一个快照时间戳，保证原子性」
//     ⇒ 同一批的**全部行**落进 btree 的**同一个窄键区间**（首列 snapshot_ts 相同），
//       而区间内部是随机的 ⇒ 页只能填到 ~70%。
//  ③ 生产侧实测：每 20~40 秒一批、**每批 1,881 行**共享同一个 snapshot_ts。
//
// # 收益（本地沙箱判定实验，PG 17.10，20 万行，同一份数据）
//
//	随机序插入 ⇒ 密度 **70.20%**（与生产的 70.25% 一致）
//	键序插入   ⇒ 密度 **89.82%**（与工具基线实测的「自然填充率 90.20%」一致）
//	⇒ 索引体积少 **21.98%**
//
// ⇒ 所以「排序列 = 唯一约束的列序」不是洁癖，是**每张日分区省约 22% 的索引空间**。
//
// # 三条门的分工
//
//	1. 列集合覆盖：ON CONFLICT 的列集合 == 比较器表的键集合（防「加了列忘了加比较器」）
//	2. 纯函数：排序结果确实按约束列序；且**不改调用方的切片**
//	3. 行为级：insertSnapshotRows **实际发出去的顺序**就是键序
//	   （前两条都只是「函数存在且正确」，这条才是「生产路径真的用了它」）
//
// ★ 变异验证（3 条都实测转红）：
//
//	M1 去掉 insertSnapshotRows 里的排序        ⇒ 3 红
//	M2 从比较器表里删掉一列（raw_model_name）   ⇒ 1 红
//	M3 把排序键缩成只剩 credential_id            ⇒ 3 红
//
// ⇒ 判据落在**同一个函数**里（insertSnapshotRows），所以「去掉排序」这种变异
//   一定被抓得到 —— 这也是为什么排序不放在调用方。

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// fakeExec 记录 insertSnapshotRows 实际发出去的**行序**。
//
// ★ 它只记顺序，不执行 SQL ⇒ 不连库也能观测「生产路径的插入顺序」，
//
//	这正是 §143.11 此前**缺失的观测点**（顺序错了没有任何地方会响）。
type fakeExec struct {
	sqls   []string
	orders []string
}

func (f *fakeExec) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.sqls = append(f.sqls, sql)
	// 实参布局（见 insertSnapshotRows）：
	//	$1 snapshot_ts, $2 recovery_epoch, $3 provider_id, $4 credential_id,
	//	$5 raw_model_name, ..., $7 tenant_id
	cred, _ := args[3].(int)
	model, _ := args[4].(string)
	tenant, _ := args[6].(string)
	f.orders = append(f.orders, tenant+"|"+itoa(cred)+"|"+model)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// snapshotRowsFixture 造一批**刻意打乱**的行：
// 全部共享同一个 snapshot_ts（与 Collect 的「所有行使用同一个快照时间戳」一致），
// tenant/credential/model 三列随机 ⇒ 这正是生产的形态。
func snapshotRowsFixture() []Row {
	ts := time.Date(2026, 10, 19, 22, 0, 0, 0, time.UTC)
	rows := []Row{
		{SnapshotTS: ts, TenantID: "t-b", CredentialID: 7, RawModel: "zeta"},
		{SnapshotTS: ts, TenantID: "t-a", CredentialID: 3, RawModel: "beta"},
		{SnapshotTS: ts, TenantID: "t-b", CredentialID: 2, RawModel: "alpha"},
		{SnapshotTS: ts, TenantID: "t-a", CredentialID: 9, RawModel: "gamma"},
		{SnapshotTS: ts, TenantID: "t-a", CredentialID: 3, RawModel: "alpha"},
		{SnapshotTS: ts, TenantID: "t-b", CredentialID: 7, RawModel: "alpha"},
	}
	return rows
}

// TestSnapshotIndexSortCoversEveryConflictColumn —— 排序列集合必须**恰好**等于
// ON CONFLICT 的列集合。
//
// ★ 两个方向都查：少了 ⇒ 排序维度不足 ⇒ 密度悄悄退化；
//
//	多了 ⇒ 排序键里有不在约束里的列 ⇒ 排出来的序不是索引的序。
//
// ★ 为什么必须有这条：§10.59 的形状是「ON CONFLICT 列与唯一约束不一致 ⇒
//
//	写入 100% 失败、701 次、62 小时无告警」。这里的同族是
//	「ON CONFLICT 列与排序列不一致 ⇒ 密度悄悄退化、无任何告警」。
func TestSnapshotIndexSortCoversEveryConflictColumn(t *testing.T) {
	cols := snapshotConflictColumns()
	if len(cols) == 0 {
		t.Fatal("解析不出 ON CONFLICT 的列 —— 排序键的来源失效了")
	}
	for _, c := range cols {
		if _, ok := snapshotIndexColumnCmp[c]; !ok {
			t.Errorf("ON CONFLICT 的列 %q 没有对应的比较器 ⇒ 排序会少一维，"+
				"索引密度会悄悄从 ~90%% 掉回 ~70%%（实测 §143.11），且**没有任何报错**。\n"+
				"⇒ 请在 snapshotIndexColumnCmp 里补上它。", c)
		}
	}
	for name := range snapshotIndexColumnCmp {
		found := false
		for _, c := range cols {
			if c == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("比较器表里有 %q，但它不在 ON CONFLICT 的列集合里 ⇒ "+
				"排序键与索引键不是一回事", name)
		}
	}
}

// TestSortRowsForIndexKeyOrdersByConstraintColumns —— 纯函数：排出来的序必须
// 落在约束列序上；且**不得改调用方的切片**。
func TestSortRowsForIndexKeyOrdersByConstraintColumns(t *testing.T) {
	rows := snapshotRowsFixture()
	before := make([]string, len(rows))
	for i, r := range rows {
		before[i] = r.TenantID + "|" + itoa(r.CredentialID) + "|" + r.RawModel
	}

	got := sortRowsForIndexKey(rows)

	// ① 结果必须是有序的：按 (snapshot_ts, tenant_id, credential_id, raw_model_name)。
	want := make([]string, len(before))
	copy(want, before)
	sort.SliceStable(want, func(i, j int) bool {
		a := strings.SplitN(want[i], "|", 3)
		b := strings.SplitN(want[j], "|", 3)
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		ai, _ := atoiSafe(a[1])
		bi, _ := atoiSafe(b[1])
		if ai != bi {
			return ai < bi
		}
		return a[2] < b[2]
	})

	for i := range got {
		key := got[i].TenantID + "|" + itoa(got[i].CredentialID) + "|" + got[i].RawModel
		if key != want[i] {
			t.Fatalf("第 %d 行的键序不对：得到 %s，期望 %s\n全量：\n got=%v\nwant=%v",
				i, key, want[i], gotOf(got), want)
		}
	}

	// ② 调用方的切片必须原样不动（Flush 的日志会打 rows[0] 作为「第一行样本」，
	//    那是**采集顺序**的样本，排序不该改变它）。
	for i := range rows {
		key := rows[i].TenantID + "|" + itoa(rows[i].CredentialID) + "|" + rows[i].RawModel
		if key != before[i] {
			t.Fatalf("sortRowsForIndexKey 改了调用方的切片：第 %d 行 %s → %s\n"+
				"★ 它必须返回**新切片**：Flush 的审计日志用 rows[0] 当「第一行样本」。",
				i, before[i], key)
		}
	}
}

func gotOf(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.TenantID+"|"+itoa(r.CredentialID)+"|"+r.RawModel)
	}
	return out
}

func atoiSafe(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, nil
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// TestInsertSnapshotRowsEmitsRowsInIndexOrder —— **行为级**门：
// insertSnapshotRows 实际发出去的顺序必须就是键序。
//
// ★ 前两条门只证明「函数存在且正确」；这条才证明「**生产路径真的用了它**」。
//
//	排序若被挪到调用方、或被删掉，这一条立刻红。
func TestInsertSnapshotRowsEmitsRowsInIndexOrder(t *testing.T) {
	rows := snapshotRowsFixture()
	before := make([]string, len(rows))
	for i, r := range rows {
		before[i] = r.TenantID + "|" + itoa(r.CredentialID) + "|" + r.RawModel
	}

	f := &fakeExec{}
	if err := insertSnapshotRows(context.Background(), f, rows); err != nil {
		t.Fatalf("insertSnapshotRows: %v", err)
	}
	if len(f.orders) != len(rows) {
		t.Fatalf("发出了 %d 行，期望 %d 行", len(f.orders), len(rows))
	}
	// ① 每一行都发的必须是那份带 INSERT 的 SQL（不能顺手改了语句）。
	for i, s := range f.sqls {
		if s != snapshotInsertSQL {
			t.Fatalf("第 %d 次 Exec 用的 SQL 与 snapshotInsertSQL 不同", i)
		}
	}
	// ② 顺序必须按 (tenant_id, credential_id, raw_model_name) 升序
	//    （同一批共享 snapshot_ts ⇒ 首列恒等）。
	for i := 1; i < len(f.orders); i++ {
		if f.orders[i-1] > f.orders[i] {
			t.Fatalf("插入顺序不是键序：第 %d 行 %s > 第 %d 行 %s\n"+
				"★ 这正是生产上 70.25%% 密度的成因（§143.11）：\n"+
				"   整批行共享同一个 snapshot_ts ⇒ 落进 btree 的同一个窄键区间，\n"+
				"   区间内部随机 ⇒ 叶页只能填到 ~70%%。",
				i-1, f.orders[i-1], i, f.orders[i])
		}
	}
	// ③ 调用方的切片仍应原样不动。
	for i := range rows {
		key := rows[i].TenantID + "|" + itoa(rows[i].CredentialID) + "|" + rows[i].RawModel
		if key != before[i] {
			t.Fatalf("insertSnapshotRows 改了调用方的切片：第 %d 行 %s → %s", i, before[i], key)
		}
	}
}
