package storage

// consistency.go — 2026-09-05 审计闭环4：lite 模式跨资源一致性补偿。
//
// lite 模式下轮次数据横跨两种介质：
//   - 元数据（SQLite session_turns / sessions / request_logs）
//   - 内容大对象（文件系统 bodies 目录，gzip JSON）
//
// 两者靠 (tenant_id, session_id, turn_no) 关联，没有跨介质事务：写入
// 顺序中断（进程崩溃 / 断电 / 磁盘满）会留下两类孤儿——
//   a) body 文件存在但 turn meta 缺失（先写 body 后写 meta 的窗口）；
//   b) turn meta 存在但 body 文件缺失（先写 meta 的实现或 body 写失败）。
//
// ReconcileTurnArtifacts 是审计要求的补偿原语：对比两侧 turn 集合，
// 输出结构化报告（可写审计日志/告警），并按策略清理 a 类孤儿
// （b 类内容已丢失、无法从元数据恢复，只报告不伪造）。
//
// 2026-09-05 round2（审计 G-#9 / B-#6）：写序为「body 先落盘、meta 后
// 提交」（lite_telemetry_sink.go），Reconcile 的读-读窗口（先读 meta 再列
// body）恰好落在最危险方向——窗口内完成 body→meta 提交的合法在途轮会被
// 误判为 OrphanBodies。因此 RepairTurnArtifacts 的删除路径带双保险：
//   1. 删除前复检（double-confirm）：重读一次 turn meta，只删「两次快照
//      都不在 meta 中」的 turn；
//   2. mtime 宽限：body 文件 mtime 距今不足宽限期（默认 10 分钟）的孤儿
//      视为可能仍在途，跳过删除仅报告。
// 即便调用方已用空闲阈值把在途写入挡在门外（见 bg.ConsistencyWorker），
// 删除路径仍保留上述保护（纵深防御）。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// DefaultOrphanBodyGrace 是孤儿 body 删除的默认 mtime 宽限期：文件落盘后
// 距今不足该时长的，视为可能仍处于「body 已落盘、meta 未提交」的在途窗口
// （覆盖 meta 提交的最坏延迟），跳过删除仅报告。取保守值 10 分钟。
const DefaultOrphanBodyGrace = 10 * time.Minute

// BodiesLister 列出某会话已落盘的 turn 编号。由 FileBodiesStore 实现；
// 独立接口避免 Reconciler 依赖具体存储。
type BodiesLister interface {
	ListTurns(ctx context.Context, tenantID, sessionID string) ([]int, error)
}

// TurnFileStater 查询单 turn 内容文件的修改时间（mtime），供孤儿删除的
// 宽限判定使用。由 FileBodiesStore 实现；未实现该接口的 bodies store 不做
// 宽限判定（删除路径仅剩复检一道保险）。
type TurnFileStater interface {
	TurnFileModTime(ctx context.Context, tenantID, sessionID string, turnNo int) (time.Time, error)
}

// TurnArtifactReport 是单会话的跨介质一致性报告。
type TurnArtifactReport struct {
	TenantID  string `json:"tenant_id"`
	SessionID string `json:"session_id"`
	// TurnsWithMeta 是元数据侧的全部 turn 编号（升序）。
	TurnsWithMeta []int `json:"turns_with_meta"`
	// TurnsWithBody 是内容侧的全部 turn 编号（升序）。
	TurnsWithBody []int `json:"turns_with_body"`
	// MissingBodies = 有 meta 无 body：内容已丢失（磁盘清理/写失败），
	// 无法自动补偿，只能报告。
	MissingBodies []int `json:"missing_bodies,omitempty"`
	// OrphanBodies = 有 body 无 meta：写 meta 前中断的孤儿文件。
	// 注意这是「读-读窗口」的瞬时快照，删除前须经 RepairTurnArtifacts
	// 复检确认（G-#9）。
	OrphanBodies []int `json:"orphan_bodies,omitempty"`
	// CheckedAt 是检查时间。
	CheckedAt time.Time `json:"checked_at"`
	// Consistent 为 true 表示两侧完全一致。
	Consistent bool `json:"consistent"`
}

// RepairAction 决定孤儿处理策略。
type RepairAction int

const (
	// RepairReportOnly 只报告不动数据（默认，最安全）。
	RepairReportOnly RepairAction = iota
	// RepairDeleteOrphanBodies 删除孤儿 body 文件（a 类清理）。
	RepairDeleteOrphanBodies
)

// String implements fmt.Stringer for logs.
func (a RepairAction) String() string {
	if a == RepairDeleteOrphanBodies {
		return "delete_orphan_bodies"
	}
	return "report_only"
}

// RepairOptions 控制 RepairTurnArtifacts 删除路径的安全护栏。
type RepairOptions struct {
	// OrphanGrace 是孤儿 body 文件的 mtime 宽限期：mtime 距今不足宽限期的
	// 孤儿视为可能仍在途，跳过删除、仅报告（G-#9 第二道保险）。
	// 零值使用 DefaultOrphanBodyGrace（保守默认）；负值禁用宽限（仅测试）。
	OrphanGrace time.Duration
}

// grace 归一化宽限期：nil / 零值 → DefaultOrphanBodyGrace；负值 → 0（禁用）。
func (o *RepairOptions) grace() time.Duration {
	switch {
	case o == nil || o.OrphanGrace == 0:
		return DefaultOrphanBodyGrace
	case o.OrphanGrace < 0:
		return 0
	default:
		return o.OrphanGrace
	}
}

// RepairResult 汇总一次 Repair 的处置明细（供调用方结构化日志/告警）。
type RepairResult struct {
	// Deleted 是实际删除的孤儿 turn 编号。
	Deleted []int `json:"deleted,omitempty"`
	// SkippedInFlight 是删除前复检发现 meta 已提交的 turn：「body 落盘 →
	// meta 提交」的在途竞态自愈，body 为合法内容被保留（G-#9 第一道保险）。
	SkippedInFlight []int `json:"skipped_in_flight,omitempty"`
	// SkippedByGrace 是复检后仍为孤儿、但因安全护栏未删除的 turn（mtime 在
	// 宽限期内，或 mtime 查询失败按保守处理）。
	SkippedByGrace []int `json:"skipped_by_grace,omitempty"`
}

// ReconcileTurnArtifacts 对比 turn 元数据与 body 文件两侧的一致性。
// bodies 必须同时实现 BodiesStore（Delete 用）与 BodiesLister；
// turns 提供元数据读端。
//
// 注意：本函数先读 meta 后列 body，两次读取之间完成「body 落盘 → meta
// 提交」的在途轮会被误报为 OrphanBodies（G-#9 读-读窗口）。因此报告中的
// OrphanBodies 是瞬时快照，任何删除动作必须经 RepairTurnArtifacts（自带
// 复检 + mtime 宽限）执行，或由调用方保证会话已空闲（无在途写入）。
func ReconcileTurnArtifacts(ctx context.Context, tenantID, sessionID string, bodies BodiesStore, turns TurnsStore) (*TurnArtifactReport, error) {
	lister, ok := bodies.(BodiesLister)
	if !ok {
		return nil, fmt.Errorf("consistency: bodies store %T does not support ListTurns", bodies)
	}
	metaTurns, err := turns.GetTurnsMeta(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("consistency: load turn meta %s/%s: %w", tenantID, sessionID, err)
	}
	bodyTurns, err := lister.ListTurns(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("consistency: list body turns %s/%s: %w", tenantID, sessionID, err)
	}

	metaSet := make(map[int]struct{}, len(metaTurns))
	for _, m := range metaTurns {
		metaSet[m.TurnNo] = struct{}{}
	}
	bodySet := make(map[int]struct{}, len(bodyTurns))
	for _, n := range bodyTurns {
		bodySet[n] = struct{}{}
	}

	report := &TurnArtifactReport{
		TenantID:      tenantID,
		SessionID:     sessionID,
		CheckedAt:     time.Now(),
		Consistent:    true,
		TurnsWithMeta: sortedKeys(metaSet),
		TurnsWithBody: sortedKeys(bodySet),
	}
	for _, n := range report.TurnsWithMeta {
		if _, ok := bodySet[n]; !ok {
			report.MissingBodies = append(report.MissingBodies, n)
		}
	}
	for _, n := range report.TurnsWithBody {
		if _, ok := metaSet[n]; !ok {
			report.OrphanBodies = append(report.OrphanBodies, n)
		}
	}
	if len(report.MissingBodies) > 0 || len(report.OrphanBodies) > 0 {
		report.Consistent = false
	}
	return report, nil
}

// RepairTurnArtifacts 按 action 处理报告中的孤儿：仅
// RepairDeleteOrphanBodies 会删数据。MissingBodies 任何策略下都只报告
// ——内容无法从元数据恢复，绝不伪造空 body。
//
// 删除路径（G-#9 双保险）：
//   - 删除前复检：对每个孤儿重读一次 turn meta，只删「Reconcile 快照与本
//     次复检都不在 meta 中」的 turn，两次快照之间提交 meta 的在途轮被
//     跳过（body 合法保留，记入 RepairResult.SkippedInFlight）；
//   - mtime 宽限：bodies 实现 TurnFileStater 时，文件 mtime 距今不足
//     opts 宽限期的孤儿跳过删除（记入 SkippedByGrace）。
//
// B-#6：action=RepairDeleteOrphanBodies 且复检后仍有待删孤儿、但 bodies 未
// 实现 TurnFileDeleter 时返回 error（不再静默 no-op 让调用方误以为修复成功）。
// turns 为 nil 时无法复检，删除动作直接拒绝（返回 error）。
func RepairTurnArtifacts(ctx context.Context, report *TurnArtifactReport, bodies BodiesStore, turns TurnsStore, action RepairAction, opts *RepairOptions) (*RepairResult, error) {
	if report == nil || action != RepairDeleteOrphanBodies || len(report.OrphanBodies) == 0 {
		return &RepairResult{}, nil
	}
	if turns == nil {
		return nil, errors.New("consistency: repair with delete requires a turns store for orphan double-confirm (got nil)")
	}

	res := &RepairResult{}
	grace := opts.grace()
	var stater TurnFileStater
	if grace > 0 {
		stater, _ = bodies.(TurnFileStater)
	}
	now := time.Now()

	pending := make([]int, 0, len(report.OrphanBodies))
	for _, n := range report.OrphanBodies {
		// 第一道保险：删除前重读 meta，复检孤儿身份。
		stillOrphan, err := turnAbsentInMeta(ctx, turns, report.TenantID, report.SessionID, n)
		if err != nil {
			return res, fmt.Errorf("consistency: recheck turn meta %s/%s#%d: %w", report.TenantID, report.SessionID, n, err)
		}
		if !stillOrphan {
			res.SkippedInFlight = append(res.SkippedInFlight, n)
			continue
		}
		// 第二道保险：mtime 宽限。刚落盘的文件可能是下一秒才提交 meta 的
		// 在途写；mtime 查询失败按保守处理（宁漏删不误删）。
		if stater != nil {
			mt, err := stater.TurnFileModTime(ctx, report.TenantID, report.SessionID, n)
			switch {
			case err == nil && now.Sub(mt) < grace:
				res.SkippedByGrace = append(res.SkippedByGrace, n)
				continue
			case err != nil && errors.Is(err, ErrNotFound):
				// 文件已消失（并发清理 / 删自灭）：无须删除，跳过。
				continue
			case err != nil:
				res.SkippedByGrace = append(res.SkippedByGrace, n)
				continue
			}
		}
		pending = append(pending, n)
	}
	if len(pending) == 0 {
		return res, nil
	}

	deleter, ok := bodies.(TurnFileDeleter)
	if !ok {
		return res, fmt.Errorf(
			"consistency: bodies store %T does not support TurnFileDeleter: %d confirmed orphan bodies of %s/%s cannot be deleted (B-#6: repair is NOT silently skipped anymore)",
			bodies, len(pending), report.TenantID, report.SessionID)
	}
	for _, n := range pending {
		// 单 turn 孤儿删除由 FileBodiesStore.DeleteTurnFile 直接落盘
		// （见 file 包），幂等；文件在复检与删除之间被外部移除时同样幂等返回。
		if err := deleter.DeleteTurnFile(ctx, report.TenantID, report.SessionID, n); err != nil {
			return res, fmt.Errorf("consistency: delete orphan body %s/%s#%d: %w",
				report.TenantID, report.SessionID, n, err)
		}
		res.Deleted = append(res.Deleted, n)
	}
	return res, nil
}

// turnAbsentInMeta 复检单个 turn 是否确实不在元数据中（double-confirm）。
func turnAbsentInMeta(ctx context.Context, turns TurnsStore, tenantID, sessionID string, turnNo int) (bool, error) {
	metas, err := turns.GetTurnsMeta(ctx, tenantID, sessionID)
	if err != nil {
		return false, err
	}
	for _, m := range metas {
		if m.TurnNo == turnNo {
			return false, nil
		}
	}
	return true, nil
}

// TurnFileDeleter 单 turn 级文件删除（FileBodiesStore 实现）。
type TurnFileDeleter interface {
	DeleteTurnFile(ctx context.Context, tenantID, sessionID string, turnNo int) error
}

func sortedKeys(set map[int]struct{}) []int {
	out := make([]int, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
