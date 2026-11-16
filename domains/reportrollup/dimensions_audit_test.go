package reportrollup

// dimensions_audit_test.go —— 候选维度完整性的回归门（2026-11-16 审计轮）。
//
// 本轮审计在 211 真库上量到的三处「数据丢失」，每一条都在这里钉死：
//
//  1. **未分配桶（provider_id IS NULL）整桶从候选里消失**：报告读面把 NULL
//     映射成哨兵 -1 显示成一行（真库实测占全量流量 30.8%），候选查询却用
//     `IS NOT NULL` 把它滤掉 ⇒ 看得见筛不出来。
//  2. **零流量供应商不可见**：候选只来自快照 ⇒ 只有区间内有流量的供应商
//     才进下拉框。「开着却一单没有」这个最该被审计看见的信号被藏了起来。
//  3. **静默截断**：按 500 项硬砍且不告诉任何人（代码注释自记「真库 30 天就有
//     527 个模型名」＝已知会砍；2026-11-16 复核该旧测量已漂移，现库 30 天窗口
//     `usage_facts` 358 个 raw 名、快照全量 357 个），砍掉的正是请求数靠后的那批。
//
// 判据纪律：门必须**双向**（会触发 + 不误触发各半），且必须锚到真源
// （PG 实际返回的行 / 真实拼出的 SQL），不能只断言「字符串里有没有某段
// 看起来像的文字」。

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func newMockPool(t *testing.T) (pgxmock.PgxPoolIface, func()) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	return mock, func() { mock.Close() }
}

func auditDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// dimsRows 造一份候选查询的返回行（dim / key / requests）。
// requests 以文本给出便于读用例，落到 mock 行里转成 int64（扫描目标是 int64）。
func dimsRows(items ...[3]string) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"dim", "key", "requests"})
	for _, it := range items {
		n, err := strconv.ParseInt(it[2], 10, 64)
		if err != nil {
			panic("requests 必须是整数：" + it[2])
		}
		rows.AddRow(it[0], it[1], n)
	}
	return rows
}

// expectDimsQuery 匹配候选主查询（SQL 形态只钉开头，细节由分支级用例守）。
func expectDimsQuery(mock pgxmock.PgxPoolIface) *pgxmock.ExpectedQuery {
	return mock.ExpectQuery("SELECT dim, key, requests FROM").
		WithArgs(
			pgxmock.AnyArg(), // $1 grain scope
			pgxmock.AnyArg(), // $2 legacy scope
			pgxmock.AnyArg(), // $3 start
			pgxmock.AnyArg(), // $4 end
			pgxmock.AnyArg(), // $5 grain scope（排除旧口径回落日）
		)
}

// TestLoadDimensionOptions_UnassignedProviderBucket 钉住未分配桶：
// 候选里必须有 key = "-1"（与读面的 UnassignedID 同键），且请求数正确。
func TestLoadDimensionOptions_UnassignedProviderBucket(t *testing.T) {
	mock, closer := newMockPool(t)
	defer closer()

	expectDimsQuery(mock).WillReturnRows(dimsRows(
		[3]string{"provider", "-1", "38035"},
		[3]string{"provider", "587", "25300"},
		[3]string{"model", "glm-5.2", "1200"},
	))
	mock.ExpectQuery("SELECT id::text").WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("587"))

	out, err := LoadDimensionOptions(context.Background(), mock, ViewProvider,
		auditDay(2026, time.November, 9), auditDay(2026, time.November, 15))
	if err != nil {
		t.Fatalf("LoadDimensionOptions: %v", err)
	}
	var found bool
	for _, item := range out.Providers {
		if item.Key == "-1" {
			found = true
			if item.Requests != 38035 {
				t.Errorf("未分配桶请求数 = %d，期望 38035", item.Requests)
			}
		}
	}
	if !found {
		t.Fatalf("候选里没有未分配桶（key=-1）：%+v", out.Providers)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("未满足的期望：%v", err)
	}
}

// TestLoadDimensionOptions_IdleProvidersMerged 钉住零流量供应商：
// providers 表里的在册供应商即使区间内没流量也必须进候选（requests=0），
// 且排在有请求的之后（"优先显示有请求的"）。
func TestLoadDimensionOptions_IdleProvidersMerged(t *testing.T) {
	mock, closer := newMockPool(t)
	defer closer()

	expectDimsQuery(mock).WillReturnRows(dimsRows(
		[3]string{"provider", "587", "25300"},
		[3]string{"provider", "18", "16202"},
	))
	// providers 表：587（已有流量）与 95464（零流量）——后者必须被补进来。
	mock.ExpectQuery("SELECT id::text").WillReturnRows(
		pgxmock.NewRows([]string{"id"}).AddRow("587").AddRow("95464"))

	out, err := LoadDimensionOptions(context.Background(), mock, ViewProvider,
		auditDay(2026, time.November, 9), auditDay(2026, time.November, 15))
	if err != nil {
		t.Fatalf("LoadDimensionOptions: %v", err)
	}
	var idle *DimensionOption
	for i := range out.Providers {
		if out.Providers[i].Key == "95464" {
			idle = &out.Providers[i]
		}
	}
	if idle == nil {
		t.Fatalf("零流量供应商 95464 没进候选：%+v", out.Providers)
	}
	if idle.Requests != 0 {
		t.Errorf("零流量供应商请求数 = %d，期望 0", idle.Requests)
	}
	if len(out.Providers) < 3 {
		t.Fatalf("候选数不足：%+v", out.Providers)
	}
	if out.Providers[len(out.Providers)-1].Requests != 0 {
		t.Errorf("零请求候选没排在最后：%+v", out.Providers)
	}
	for i := 1; i < len(out.Providers); i++ {
		if out.Providers[i-1].Requests < out.Providers[i].Requests {
			t.Errorf("候选未按请求数降序：%+v", out.Providers)
			break
		}
	}
}

// TestLoadDimensionOptions_IdleProvidersTableMissing providers 表查不到时
// **不致命**：候选退化成「只有有流量的」，但必须留痕（不静默）。
func TestLoadDimensionOptions_IdleProvidersTableMissing(t *testing.T) {
	mock, closer := newMockPool(t)
	defer closer()

	expectDimsQuery(mock).WillReturnRows(dimsRows(
		[3]string{"provider", "587", "25300"},
	))
	mock.ExpectQuery("SELECT id::text").WillReturnError(errors.New("relation \"providers\" does not exist"))

	out, err := LoadDimensionOptions(context.Background(), mock, ViewProvider,
		auditDay(2026, time.November, 9), auditDay(2026, time.November, 15))
	if err != nil {
		t.Fatalf("providers 表缺失不该让整个候选查询失败：%v", err)
	}
	if len(out.Providers) != 1 || out.Providers[0].Key != "587" {
		t.Errorf("降级后的候选不对：%+v", out.Providers)
	}
}

// TestLoadDimensionOptions_ModelCandidatesComplete 模型候选不得被砍：
// 快照里有几个就是几个（1200 个 > 旧上限 500）。
func TestLoadDimensionOptions_ModelCandidatesComplete(t *testing.T) {
	mock, closer := newMockPool(t)
	defer closer()

	rows := dimsRows([3]string{"provider", "587", "10"})
	for i := 0; i < 1200; i++ {
		rows.AddRow("model", "m"+strconv.Itoa(i), int64(1))
	}
	expectDimsQuery(mock).WillReturnRows(rows)
	mock.ExpectQuery("SELECT id::text").WillReturnRows(pgxmock.NewRows([]string{"id"}))

	out, err := LoadDimensionOptions(context.Background(), mock, ViewProvider,
		auditDay(2026, time.November, 9), auditDay(2026, time.November, 15))
	if err != nil {
		t.Fatalf("LoadDimensionOptions: %v", err)
	}
	if len(out.Models) != 1200 {
		t.Fatalf("模型候选数 = %d，期望 1200（不得被 500 旧上限砍掉）", len(out.Models))
	}
	if len(out.Truncated) != 0 {
		t.Errorf("未超上限却回报了截断：%+v", out.Truncated)
	}
}

// TestLoadDimensionOptions_TruncationReported 超上限时必须**显式回报**
// 砍了几项（旧实现按 500 静默砍，砍掉的正是请求数靠后的长尾）。
func TestLoadDimensionOptions_TruncationReported(t *testing.T) {
	mock, closer := newMockPool(t)
	defer closer()

	rows := dimsRows()
	for i := 0; i < maxDimensionOptions+37; i++ {
		rows.AddRow("model", "m"+strconv.Itoa(i), int64(1))
	}
	expectDimsQuery(mock).WillReturnRows(rows)
	mock.ExpectQuery("SELECT id::text").WillReturnRows(pgxmock.NewRows([]string{"id"}))

	out, err := LoadDimensionOptions(context.Background(), mock, ViewProvider,
		auditDay(2026, time.November, 9), auditDay(2026, time.November, 15))
	if err != nil {
		t.Fatalf("LoadDimensionOptions: %v", err)
	}
	if len(out.Models) != maxDimensionOptions {
		t.Fatalf("模型候选数 = %d，期望 %d", len(out.Models), maxDimensionOptions)
	}
	if out.Truncated["model"] != 37 {
		t.Errorf("截断回报 = %d，期望 37（截断必须显式，不能静默）", out.Truncated["model"])
	}
}

// TestDimensionBranchSQL_ProviderKeepsNullRows 钉住 provider 分支形态：
// 必须用 COALESCE(..., '-1') 出键，且**不得**再过滤 NULL 行。
//
// 这条是本轮最隐蔽的一处回归点：旧实现给 provider 分支加了
// `WHERE s.provider_id IS NOT NULL`，于是「未落定」那一桶在候选里整桶
// 消失，而报告里明明有一行。
func TestDimensionBranchSQL_ProviderKeepsNullRows(t *testing.T) {
	var provider dimensionColumn
	for _, d := range dimensionColumns {
		if d.dim == "provider" {
			provider = d
		}
	}
	if provider.col == "" {
		t.Fatal("dimensionColumns 里没有 provider 分支")
	}
	sql := dimensionBranchSQL(provider, "SELECT 1")
	if !strings.Contains(sql, "COALESCE(s.provider_id::text, '-1')") {
		t.Errorf("provider 分支没用 COALESCE 出 '-1' 键：\n%s", sql)
	}
	if strings.Contains(sql, "provider_id IS NOT NULL") {
		t.Errorf("provider 分支仍在过滤 NULL 行（未分配桶会整桶消失）：\n%s", sql)
	}
}

// TestDimensionBranchSQL_OthersExcludeNull 阴性对照：其余维度**仍要**
// 排除 NULL（否则会多出一行空桶），确保 provider 是特例而非普遍改动。
func TestDimensionBranchSQL_OthersExcludeNull(t *testing.T) {
	for _, d := range dimensionColumns {
		if d.dim == "provider" {
			continue
		}
		sql := dimensionBranchSQL(d, "SELECT 1")
		if !strings.Contains(sql, "IS NOT NULL") {
			t.Errorf("%s 分支不再排除 NULL：\n%s", d.dim, sql)
		}
	}
}
