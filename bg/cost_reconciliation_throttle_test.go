package bg

// 共享节流槽的行为判据（2026-10-17 24h 审计轮）。
//
// 为什么这道门必须存在：本 worker 此前**没有任何跨实例协调**，
// 而 154 / 245 共享同一个 PostgreSQL，于是每台都跑一遍完全相同的
// `WITH deduped AS (SELECT DISTINCT ON (request_id) …)` 全月聚合
// （pg_stat_statements：87 次 / 均值 129.2s / 最大 533.9s / 累计 187.3 分钟，
//   而 PG 启动仅 11.8 天）。
//
// ⚠ 判据纪律第 3 条在**阻断侧**的同款陷阱在这里同样适用：如果只断言
// 「认领到就聚合、没认领到就不聚合」这类正向计数，一个「永远返回 false」
// 的桩会让「跳过」与「聚合」都表现为 0 次而全绿。所以下面显式断言
// **降级路径必须放行**——那是这道门最容易被「顺手收紧」改坏的地方：
// 收紧成「认领失败就跳过」会让对账在迁移 840 未应用 / 连接抖动时
// **静默不跑**，而对账表不再更新不会有任何告警（停摆的表现只是
// 「数字不再变新」）。

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestDecideSharedSlotRun 把决策表四格全钉死。
func TestDecideSharedSlotRun(t *testing.T) {
	cases := []struct {
		name    string
		claimed bool
		err     error
		hasDB   bool
		want    bool
		why     string
	}{
		{"claimed_runs", true, nil, true, true, "认领到了就聚合"},
		{"peer_holds_slot_skips", false, nil, true, false, "对端在窗口内已认领 → 本轮不重复"},
		{"single_instance_runs", false, nil, false, true, "未接 db：没有跨实例语义，照跑"},
		{"claim_error_runs", false, errors.New("conn refused"), true, true,
			"★ 认领失败必须降级放行；收紧成跳过＝对账静默停摆且无告警"},
		{"undefined_table_runs", false, &pgconn.PgError{Code: "42P01"}, true, true,
			"★ 迁移 840 未应用 → 回到节流前行为（两台各跑一遍），不是跳过"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decideSharedSlotRun(c.claimed, c.err, c.hasDB)
			if got != c.want {
				t.Fatalf("decideSharedSlotRun(claimed=%v, err=%v, hasDB=%v) = %v, want %v (%s)",
					c.claimed, c.err, c.hasDB, got, c.want, c.why)
			}
		})
	}
}

// TestDecideSharedSlotRunNeverSilentlySkips 是上面两条 ★ 的反向断言：
// 把 err != nil 改成 return claimed（即「认领失败就跳过」）会让
// 「claim_error_runs」与「undefined_table_runs」两格同时变红。
func TestDecideSharedSlotRunNeverSilentlySkips(t *testing.T) {
	for _, err := range []error{
		errors.New("conn refused"),
		&pgconn.PgError{Code: "42P01"},
		&pgconn.PgError{Code: "57014"},
	} {
		if !decideSharedSlotRun(false, err, true) {
			t.Errorf("err=%v 时必须放行聚合，不得跳过", err)
		}
	}
}

// TestThrottleWindowGuardsNonPositiveInterval 把「interval<=0 必须兜底」
// 钉住：把它原样当 interval 传给 claim 会让窗口永不推进 ⇒ **永远跳过**，
// 方向错误的默认值比多跑几遍危险得多。
func TestThrottleWindowGuardsNonPositiveInterval(t *testing.T) {
	for _, iv := range []time.Duration{0, -time.Second, -time.Hour} {
		w := &CostReconciliationWorker{interval: iv}
		if got := w.throttleWindow(); got != 24*time.Hour {
			t.Errorf("interval=%v 的兜底窗口 = %v，want 24h", iv, got)
		}
	}
	// 正间隔原样使用（目标是「整个集群每个间隔跑一次」）。
	w := &CostReconciliationWorker{interval: 90 * time.Minute}
	if got := w.throttleWindow(); got != 90*time.Minute {
		t.Errorf("正间隔被改写：got %v, want 90m", got)
	}
}

// TestSharedSlotNameIsDistinctFromAnalyze 保证两个节流槽互不干扰：
// 若误用同一个 task_name，对账会抢掉 analyze 的槽（或反之），
// 而两者节奏差三个数量级（90m vs 50min 窗、每日 vs 每小时）。
func TestSharedSlotNameIsDistinctFromAnalyze(t *testing.T) {
	if costReconciliationSlotName == "" {
		t.Fatal("槽行键不能为空")
	}
	if costReconciliationSlotName == analyzeThrottleTaskName {
		t.Fatal("对账与 analyze 必须用不同的节流槽行键")
	}
}

// TestUndefinedTableClassification 确认错误分类：只有 42P01 是 undefined_table。
// 本文件按它分流「缺表」与「其它故障」的**日志措辞**，分类错了不会改变
// 是否放行（两者都放行），但会让运维读错日志。
func TestUndefinedTableClassification(t *testing.T) {
	if !isUndefinedTable(&pgconn.PgError{Code: "42P01"}) {
		t.Fatal("42P01 应识别为 undefined_table")
	}
	for _, err := range []error{&pgconn.PgError{Code: "42P02"}, errors.New("boom"), nil} {
		if isUndefinedTable(err) {
			t.Errorf("err=%v 不应识别为 undefined_table", err)
		}
	}
}

// TestWorstCaseRunDurationCoversEveryAggregatedMonth 把
// worstCaseReconcileDuration 与「一轮聚合几个月」绑定在一起：
// 若将来 CostReconciliationMonthsToAggregate 从 2 个月改成 3 个月
// （例如加一个季结月），这个上界必须跟着涨，否则 Start() 的那条 Warn
// 会在真实配置下**不再触发**，风险重新静默。
func TestWorstCaseRunDurationCoversEveryAggregatedMonth(t *testing.T) {
	months := len(CostReconciliationMonthsToAggregate(time.Now()))
	if months == 0 {
		t.Fatal("一轮 reconcile 至少要聚合 1 个月")
	}
	want := time.Duration(months) * reconcileMonthTimeout
	if worstCaseReconcileDuration != want {
		t.Fatalf("worstCaseReconcileDuration = %v，但一轮聚合 %d 个月 ⇒ 应为 %v；"+
			"改 CostReconciliationMonthsToAggregate 时必须同步这里",
			worstCaseReconcileDuration, months, want)
	}
	if worstCaseReconcileDuration < reconcileMonthTimeout {
		t.Fatal("最坏耗时上界不应小于单月超时")
	}
}

// TestDefaultIntervalClearsTheWindowWarning 确认默认配置（86400s）
// 不会误报：一条每次启动都打的 Warn 等于没有 Warn。
func TestDefaultIntervalClearsTheWindowWarning(t *testing.T) {
	if 24*time.Hour <= worstCaseReconcileDuration {
		t.Fatalf("默认 interval(24h) 应大于最坏耗时 %v，否则 Start() 会误报", worstCaseReconcileDuration)
	}
}
