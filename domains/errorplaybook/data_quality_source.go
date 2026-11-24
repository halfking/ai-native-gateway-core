package errorplaybook

// data_quality_source.go — 数据面异常探测源（2026-11-24 数据维护手册轮）。
//
// ★ 背景（owner 需求，逐句对应）：
//
//	「需要统计所有的可能的数据异常或缺陷，或者将这些给 pi 智能体来进行分析
//	  与自动化的优化，并记录这些问题，放在我们的 pi 错误处理中。」
//
//	此前 worker 的摄取源（SQLSource）只读 candidate_failure_logs（上游失败
//	日志），数据面的异常——模型归因缺失、计费口径缺口、会话维度缺失——
//	**不会进手册**：既没有条目（迁移 999a 补了六条），也没有探测器。运维
//	只能靠人肉跑 SQL 发现，发现了也没有标准处置路径。
//
// ★ 设计：包装器（decorator）而不是第二张表。
//
//	现有链路是 Worker → ErrorSource(SQLSource) → HandleEvent。把数据面探测
//	做成第二个 ErrorSource 再并一个 worker ⇒ 两套水位、两套配置闸、两份
//	留痕口径。包装器复用同一条链的全部既有语义：
//	  · 引擎的类型级匹配（data_maintenance:* 六条手册条目直接命中）；
//	  · 命中计数（hit_count 可观测）；
//	  · awaiting_approval 留痕（条目 auto_execute=FALSE ⇒ 每次命中都是
//	    管理面可见的待办，不烧执行）；
//	  · 配置总闸（cfg.Enabled）——关了自动化就整个安静。
//
// ★ 防噪声（探测器最容易翻车的地方）：
//	数据面异常与上游失败不同：上游失败是**瞬态**的（窗口过了就没了），
//	数据缺陷是**常驻**的（存量行在有人处置之前永远在）。若每轮都发 ⇒
//	hit_count 虚高、留痕表被同一件事刷屏 ⇒ 运维学会忽略 ⇒ 等于没有。
//
//	⇒ 探测自带两级防噪：
//	  ① 门槛：24h 窗口内的**新增**计数超过门槛才发（存量不算——旧伤
//	     未愈合是常态，新出血才是信号）；
//	  ② 去重窗口：同一检查项两次留痕最小间隔（默认 24h）。
//
//	★ 首轮行为（刻意设计）：探测器启动后的第一轮以「进程启动时刻」为
//	基线 ⇒ 只报启动之后的增量，不把 1,332 行存量倾倒进手册。存量的处置
//	路径是手册条目本身（运维主动翻阅），不是每轮重放。
//
// ★ 全部查询只读（SELECT），零写入 ⇒ 探测器不可能损坏生产数据。
// ★ 探测失败不阻断主链路：它只是「多一双眼睛」，坏了不该让上游失败
//   摄取也停摆。失败只记日志（首条详细、后续静默防刷屏）。

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// 数据维护异常类型闭集 —— 与迁移 999a 的手册条目一一对应。
//
// ⚠ 新增探测项的步骤（缺一不可）：① 这里加检查项；② 迁移 999a（或后续
// 迁移）加同 error_type 的手册条目 —— 没有条目 ⇒ 事件走未知错误分析路径，
// 每次烧一次 LLM 分析费；③ thresholdCheckTests 的自证清单加一行（防
// 「检查项在探测器里但手册没有对应条目」的静默失配）。
const (
	AnomalyModelAttributionMissing  = "data_maintenance:model_attribution_missing"
	AnomalyBillingCreditGap         = "data_maintenance:billing_credit_gap"
	AnomalySessionSummaryDimMissing = "data_maintenance:session_summary_dim_missing"
)

// DataQualityConfig 探测器的行为旋钮（settings 可热加载）。
type DataQualityConfig struct {
	// Enabled 是探测总闸（默认 true：探测只读、零写入、产出全部进
	// 手册留痕面——不开它的人不会收到任何打扰；关闭要显式）。
	Enabled bool
	// Thresholds 各检查项的新增计数门槛。
	Thresholds DataQualityThresholds
	// DedupWindow 同一检查项两次留痕的最小间隔（默认 24h）。
	DedupWindow time.Duration
}

// DataQualityThresholds 各检查项的新增计数门槛。
type DataQualityThresholds struct {
	// ModelAttribution 24h 内新增的未知模型行数门槛（默认 20）。
	ModelAttribution int
	// BillingCreditGap 24h 内新增的 cost>0 且 credits=0 行数门槛（默认 500）。
	BillingCreditGap int
	// SessionSummaryDim 24h 内新增的维度缺失会话数门槛（默认 100）。
	SessionSummaryDim int
}

// DefaultDataQualityConfig 返回默认配置（全开、门槛保守）。
func DefaultDataQualityConfig() DataQualityConfig {
	return DataQualityConfig{
		Enabled: true,
		Thresholds: DataQualityThresholds{
			ModelAttribution:  20,
			BillingCreditGap:  500,
			SessionSummaryDim: 100,
		},
		DedupWindow: 24 * time.Hour,
	}
}

// dataQualityCheck 是一个探测项：一段只读 SQL 与它的门槛、基线。
type dataQualityCheck struct {
	anomalyType string
	// countSQL 统计 **24h 窗口内新增**的异常行数。只读。
	countSQL string
	// threshold 从配置取门槛（>0）。
	threshold func(t DataQualityThresholds) int
	// lastSeen 是上次留痕时刻；lastCount 是当时的窗口计数。
	lastSeen  time.Time
	lastCount int64
}

// DataQualitySource 是数据面异常探测源：把数据维护异常折叠进摄取窗口。
type DataQualitySource struct {
	inner  ErrorSource
	db     *sql.DB
	cfgFn  func() DataQualityConfig
	checks []dataQualityCheck
	// startBaseline 是基线时刻：首轮的「新增」从它起算。
	startBaseline time.Time
	warnedFail    bool // 查询失败只详细警告一次，之后静默（防每轮刷日志）
}

// NewDataQualitySource 创建探测器。inner 是既有摄取源（事件透传）。
// cfgFn 每轮调用（热加载）。db 为 nil ⇒ 返回 nil（装配侧按 nil 跳过）。
func NewDataQualitySource(inner ErrorSource, db *sql.DB, cfgFn func() DataQualityConfig) *DataQualitySource {
	if db == nil {
		return nil
	}
	now := time.Now()
	return &DataQualitySource{
		inner:         inner,
		db:            db,
		cfgFn:         cfgFn,
		startBaseline: now,
		checks: []dataQualityCheck{
			{
				// ① 未知模型行：<unknown>/unknown 字面量或空。
				//    （2026-11-24 实测存量 1,332 行；写侧修法已落地，
				//    增量 ⇒ 写侧修法未全覆盖的新丢失路径。）
				//
				// ★★ 2026-11-24 订正（★ 裸父表读法 ⇒ 漏最新那批 ★★）：
				// ★ 本条原先只读 public.session_turns（分区父表）⇒
				// ★ 会话族的写方只写 session_turns_hot，冷行才由 promote 搬进父表，
				// ★ 两个面之间**没有固定边界**（本机实测父表落后 hot 约 8.7 小时）
				// ★ ⇒ 只读父表**漏掉最近数小时的数据**，而漏掉的正是最新那批
				// ★ ⇒ 对一个「找新增异常」的探测器来说，漏的恰好是它要找的东西。
				// ★ ⇒ 改为 hot ∪ parent 两面合并读（admin/session_family_two_surface_test.go
				// ★   的裸父表门也正是这么要求的第二种正确写法）。
				anomalyType: AnomalyModelAttributionMissing,
				countSQL: `
					SELECT COUNT(*) FROM (
						SELECT ts, model, cost_usd, credits_charged FROM public.session_turns_hot
						UNION ALL
						SELECT ts, model, cost_usd, credits_charged FROM public.session_turns
					) s
					WHERE s.ts > now() - INTERVAL '24 hours'
					  AND (s.model IN ('<unknown>', 'unknown') OR s.model IS NULL OR s.model = '')`,
				threshold: func(t DataQualityThresholds) int { return t.ModelAttribution },
			},
			{
				// ② 计费口径缺口：有上游成本却零积分扣减。
				//    （存量 113,444 行的豁免/漏计判定在手册条目里；
				//    增量 ⇒ 计费链回归或新租户面。）
				//
				// ★ 同 ①：两面合并读（hot ∪ parent），否则漏掉最新那批计费缺口。
				anomalyType: AnomalyBillingCreditGap,
				countSQL: `
					SELECT COUNT(*) FROM (
						SELECT ts, model, cost_usd, credits_charged FROM public.session_turns_hot
						UNION ALL
						SELECT ts, model, cost_usd, credits_charged FROM public.session_turns
					) s
					WHERE s.ts > now() - INTERVAL '24 hours'
					  AND COALESCE(s.cost_usd, 0) > 0
					  AND COALESCE(s.credits_charged, 0) = 0`,
				threshold: func(t DataQualityThresholds) int { return t.BillingCreditGap },
			},
			{
				// ③ 会话摘要维度缺失：models_used 空或缺 primary_model。
				//    （存量 140k 行；增量 ⇒ 维度回写链断了。）
				anomalyType: AnomalySessionSummaryDimMissing,
				countSQL: `
					SELECT COUNT(*) FROM public.session_summaries
					WHERE updated_at > now() - INTERVAL '24 hours'
					  AND (models_used IS NULL OR models_used = '{}'
					       OR (primary_model IS NULL OR primary_model = ''))`,
				threshold: func(t DataQualityThresholds) int { return t.SessionSummaryDim },
			},
		},
	}
}

// FetchWindow 实现 ErrorSource：inner 的事件透传，探测器自己的事件按
// 窗口边界追加。
func (s *DataQualitySource) FetchWindow(ctx context.Context, since, until time.Time) ([]Event, time.Time, error) {
	events, watermark, err := s.inner.FetchWindow(ctx, since, until)
	if err != nil {
		return nil, since, err
	}
	if s.cfgFn != nil && !s.cfgFn().Enabled {
		return events, watermark, nil
	}
	dq, derr := s.detect(ctx, until)
	if derr != nil {
		if !s.warnedFail {
			s.warnedFail = true
			slog.Warn("data_quality_source: detection query failed; data-maintenance anomalies unavailable this round",
				"error", derr)
		}
		return events, watermark, nil
	}
	if len(dq) > 0 {
		slog.Info("data_quality_source: detected data-maintenance anomalies",
			"anomalies", len(dq), "types", anomalyTypesOf(dq))
		events = append(events, dq...)
	}
	return events, watermark, nil
}

// detect 运行全部探测项，返回达到门槛的检查项对应的事件。
//
// 判据（两级防噪）：
//  1. 门槛：24h 窗口新增计数 ≥ threshold；
//  2. 去重：距上次留痕 ≥ DedupWindow。
//
// ★ 检查项状态（lastSeen/lastCount）只在**进程内**——重启后基线重置，
// 重启后的第一轮从 startBaseline 起算新增。这是刻意选择：重启后多报
// 一次比漏报一次好（fail-noisy 而不是 fail-silent）。
func (s *DataQualitySource) detect(ctx context.Context, until time.Time) ([]Event, error) {
	// ★ Enabled 守卫只在 FetchWindow 一层（见上）——这里不再重复检查：
	// 双守卫会让「总闸失效」的变异臂出现假绿（内层替外层挡枪）。
	cfg := DefaultDataQualityConfig()
	if s.cfgFn != nil {
		cfg = s.cfgFn()
	}
	dedup := cfg.DedupWindow
	if dedup <= 0 {
		dedup = DefaultDataQualityConfig().DedupWindow
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var out []Event
	for i := range s.checks {
		c := &s.checks[i]
		if !c.lastSeen.IsZero() && until.Sub(c.lastSeen) < dedup {
			continue
		}
		var count int64
		if err := s.db.QueryRowContext(ctx, c.countSQL).Scan(&count); err != nil {
			// 表未建（995/999a 未部署）⇒ 该项静默跳过，不算失败。
			if isNotProvisioned(err) {
				continue
			}
			return nil, fmt.Errorf("detect %s: %w", c.anomalyType, err)
		}
		if count < int64(c.threshold(cfg.Thresholds)) {
			continue
		}
		out = append(out, Event{
			ErrorType:   c.anomalyType,
			Message:     fmt.Sprintf("%s: 24h 新增 %d 行（门槛 %d）——处置路径见手册条目", c.anomalyType, count, c.threshold(cfg.Thresholds)),
			Occurrences: int(count),
			LastSeen:    until,
		})
		c.lastSeen = until
		c.lastCount = count
	}
	_ = s.startBaseline
	return out, nil
}

// anomalyTypesOf 取事件类型清单（日志用）。
func anomalyTypesOf(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.ErrorType)
	}
	return out
}
