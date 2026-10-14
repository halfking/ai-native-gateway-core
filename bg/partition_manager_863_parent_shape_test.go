package bg

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// 863 判据（211 日志审计轮）：声明了 parentTable 的 ensure spec，24h tick
// 必须先探父表形状——非分区父表（迁移 830 未应用/已回滚/restore 丢失分区
// 性）是合法世界态，应跳过而不是每 tick 报 42P17。db.go 的 boot ensure
// （ensureURSMNodeSnapshotMinDailyPartition 的 0) 探测）自 830 起就有这个
// 契约，本门钉住 bg tick 与之同口径（r1021「两路径政策必须一致」同族）。
// 注：pgxmock v4 的 Times(0) 受 fulfilled()=max(planned,1) 限制永远不满足，
// 「不调用」方向经 ensureSpecEligible 单元钉住；「tick 真的走探测」由
// 正向腿（探测期望被消费 + ensure 恰两次）钉住——两个变异方向都有牙。
func TestEnsureSpecEligibleFalseWhenParentNotPartitioned(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock pool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (")).
		WithArgs("ursm_node_snapshot_min").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	pm := NewPartitionManager(mock, time.Hour)
	s := archiveSpec{
		fnName:      "ensure_ursm_node_snapshot_min_daily_partition",
		label:       "ursm_node_snapshot_min (daily)",
		parentTable: "ursm_node_snapshot_min",
	}
	if pm.ensureSpecEligible(context.Background(), s) {
		t.Fatal("parent not partitioned (relkind <> 'p') must make the spec ineligible — 830-less worlds are legitimate, not an error state")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// 无 parentTable 的 spec 不发探测、直接合格（旧 spec 零行为变化）。
func TestEnsureSpecEligibleTrueWithoutParentTable(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock pool: %v", err)
	}
	defer mock.Close()

	pm := NewPartitionManager(mock, time.Hour)
	s := archiveSpec{fnName: "ensure_handoff_logs_partition", label: "handoff_logs"}
	if !pm.ensureSpecEligible(context.Background(), s) {
		t.Fatal("spec without parentTable must stay eligible without probing")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// 正向腿：父表是分区父表时，行为与旧实现完全一致——探测一次被消费、
// 当日+次日两次 ensure（863 只加探测，不改变已分区世界的促升节奏）。
func TestEnsureTickCallsSpecWhenParentPartitioned(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock pool: %v", err)
	}
	defer mock.Close()

	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (")).
		WithArgs("ursm_node_snapshot_min").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("SELECT ensure_ursm_node_snapshot_min_daily_partition")).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 0)).
		Times(2)

	pm := NewPartitionManager(mock, time.Hour)
	pm.ensureNextMonthPartitions(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// 形状契约：830 家族的 spec 必须声明 parentTable（漏声明 = 回到 42P17 每
// 日噪音；本门是 473 同族「迁移建了分区/函数，接线散在 SQL/Go 两处，任一
// 处漏掉都等跨日才显现」的 Go 侧钉子）。
func TestURSMSnapshotEnsureSpecDeclaresParentTable(t *testing.T) {
	for _, s := range ensureSpecs() {
		if s.fnName != "ensure_ursm_node_snapshot_min_daily_partition" {
			continue
		}
		if s.parentTable != "ursm_node_snapshot_min" {
			t.Fatalf("ensure_ursm_node_snapshot_min_daily_partition must declare parentTable=ursm_node_snapshot_min, got %q", s.parentTable)
		}
		return
	}
	t.Fatal("ensure_ursm_node_snapshot_min_daily_partition not wired in ensureSpecs — 830 contract regression")
}
