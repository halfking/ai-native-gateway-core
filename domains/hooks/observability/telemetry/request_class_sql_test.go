package telemetry

import (
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// ★ 2026-10-20（迁移 890，v1 族退役）：本文件整体**改指**。
//
// 被改指的对象：「request_class / due_at 必须被 v1 宽表的每一条写语句携带」
// 这条 **SQL 级**契约（迁移 610 / 608）。它成立的前提是 v1 宽表有这两列
// 且写语句里有 $101/$102 与「判据用各自占位符」的 CASE 形态——v1 族随 890
// 删除 ⇒ 断言对象已不存在。
//
// 改指后守的不变式（**它没有随 v1 一起退役**，因为落点换了）：
//
//	request_class / due_at 的**领域不变式**必须在写路径上被强制：
//	① immediate ⇒ 没有 due_at；
//	② scheduled ⇒ 必须有 due_at。
//
// 这条不变式现在由谁承载：写路径在**交给会话族镜像之前**归一化 entry
// （normalizeRequestClassAndDueAt），而会话族镜像
// （internal/sessionv2mirror/s1b_fields.go:66-67）把 entry.RequestClass /
// entry.DueAt 搬进会话族的写入载荷 ⇒ 归一化若失效，库里就会出现
// 「scheduled 但没有 due_at」的行。
//
// ⇒ 本文件从「读源码比对 SQL 文本」改成「**跑写路径、看 hook 拿到的 entry**」：
// hook 拿到的 entry 正是会话族镜像会拿到的那一份（persistRequestLog 在写库
// 成功后触发 hooks），所以断言点选在这里比选在函数内部更贴近真实消费者。
//
// 注：requestClassArg（把 class 收敛成 immediate/scheduled 的绑定值助手）
// 随 v1 的 INSERT 一起失去了生产调用点——它现在只被单测覆盖。
// 「class 归一化之后写进哪张表」已不在本包（会话族承接），详见本轮报告。

// persistedEntryAfterWrite 跑一次写路径，返回 onPersisted hook 看到的 entry。
//
// op 决定走 insert 还是 update 路径；两条路径的期望语句不同（INSERT 19 参 /
// UPDATE 5 参），但归一化的断言点相同 ⇒ 抽成一个 helper，让 insert 与 update
// 两条用例断言**同一件事**。
func persistedEntryAfterWrite(t *testing.T, op RequestLogOp, entry *RequestLogEntry) *RequestLogEntry {
	t.Helper()
	mock, recording, _ := newRecordingDB(t)

	mock.ExpectBegin()
	if op == RequestLogInsert {
		mock.ExpectExec(`INSERT INTO usage_ledger_hot`).
			WithArgs(anyArgs(19)...).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	} else {
		mock.ExpectExec(`UPDATE usage_ledger_hot`).
			WithArgs(anyArgs(5)...).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}
	mock.ExpectCommit()

	c := &Client{requestLogDB: recording}
	var seen *RequestLogEntry
	c.AddOnRequestLogPersisted(func(e *RequestLogEntry) { seen = e })

	require.NoError(t, c.persistRequestLog(entry))
	require.NoError(t, mock.ExpectationsWereMet())
	require.NotNil(t, seen, "写库成功后 onPersisted hook 必须被触发（否则下面的断言是假绿）")
	return seen
}

// TestInsertRequestLog_NormalizesRequestClassDomainInvariant（★ 改指，原名
// TestInsertRequestLogCarriesRequestClass）：INSERT 路径必须把 class/due_at
// 归一到领域不变式上。
//
// 旧门守的是「$101/$102 必须出现在 v1 的 INSERT 里，且判据占位符与写入值
// 占位符各自独立」——那条 SQL 随 v1 族退役。
//
// 改指后守的是同一件事在**新落点**上的形态：insert 路径一定要给 class
// （缺失/非法 ⇒ immediate），scheduled 必须带 due_at。
func TestInsertRequestLog_NormalizesRequestClassDomainInvariant(t *testing.T) {
	due := time.Unix(1800000000, 0).UTC()
	scheduled := "scheduled"

	cases := []struct {
		name      string
		class     *string
		due       *time.Time
		wantClass string
		wantDue   bool
	}{
		{name: "missing class becomes immediate", class: nil, wantClass: "immediate"},
		{name: "invalid class becomes immediate", class: strptr("deferred"), wantClass: "immediate"},
		{name: "scheduled without due time becomes immediate", class: &scheduled, wantClass: "immediate"},
		{name: "scheduled with due time is kept", class: &scheduled, due: &due, wantClass: "scheduled", wantDue: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := persistedEntryAfterWrite(t, RequestLogInsert, &RequestLogEntry{
				Op:           RequestLogInsert,
				RequestID:    "req-class-insert",
				TenantID:     "tenant-1",
				Success:      true,
				RequestClass: tc.class,
				DueAt:        tc.due,
			})

			require.NotNil(t, seen.RequestClass, "insert 路径必须给出一条确定的 class")
			require.Equal(t, tc.wantClass, *seen.RequestClass)
			require.Equal(t, tc.wantDue, seen.DueAt != nil,
				"immediate ⇒ 没有 due_at；scheduled ⇒ 必须有 due_at（会话族镜像直接搬这两个字段）")
		})
	}
}

// TestUpdateRequestLog_PreservesRequestClassAndDueAt（★ 改指，原名
// TestUpdateRequestLogCarriesRequestClass）：UPDATE 路径**不得**凭空造 class
// （库里已有的值要保住），但非法组合仍然要归一。
//
// 旧门守的是「v1 的 UPDATE 里 request_class / due_at 各自的 CASE 判据必须用
// 自己的占位符（§9.90.1 解耦）」——那条 SQL 随 v1 族退役。
//
// 改指后守的是**同一个意图**在新落点上的形态：update 只在调用方**显式**
// 给了 class 时才动它（normalizeRequestClassAndDueAt(entry, true)）；没给就
// 原样保留 ⇒ 终态回填不会把库里已写好的 scheduled 抹成 immediate。
func TestUpdateRequestLog_PreservesRequestClassAndDueAt(t *testing.T) {
	due := time.Unix(1800000000, 0).UTC()
	scheduled := "scheduled"

	cases := []struct {
		name      string
		class     *string
		due       *time.Time
		wantClass string // "" ⇒ 期望保持 nil
		wantDue   bool
	}{
		{name: "unset class stays unset", class: nil},
		{name: "scheduled with due time is kept", class: &scheduled, due: &due, wantClass: "scheduled", wantDue: true},
		{name: "scheduled without due time is normalized", class: &scheduled, wantClass: "immediate"},
		{name: "invalid class is normalized", class: strptr("deferred"), wantClass: "immediate"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := persistedEntryAfterWrite(t, RequestLogUpdate, &RequestLogEntry{
				Op:           RequestLogUpdate,
				RequestID:    "req-class-update",
				TenantID:     "tenant-1",
				Success:      true,
				RequestClass: tc.class,
				DueAt:        tc.due,
			})

			if tc.wantClass == "" {
				require.Nil(t, seen.RequestClass,
					"update 路径不得凭空造 class（库里已有的值必须保住）")
			} else {
				require.NotNil(t, seen.RequestClass)
				require.Equal(t, tc.wantClass, *seen.RequestClass)
			}
			require.Equal(t, tc.wantDue, seen.DueAt != nil)
		})
	}
}

func TestRequestLogEntryHasClassFields(t *testing.T) {
	e := &RequestLogEntry{}
	c := "scheduled"
	d := time.Unix(1800000000, 0).UTC()
	e.RequestClass = &c
	e.DueAt = &d
	if *e.RequestClass != "scheduled" || !e.DueAt.Equal(d) {
		t.Fatalf("entry class fields not settable")
	}
}
