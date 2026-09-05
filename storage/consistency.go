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

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// BodiesLister 列出某会话已落盘的 turn 编号。由 FileBodiesStore 实现；
// 独立接口避免 Reconciler 依赖具体存储。
type BodiesLister interface {
	ListTurns(ctx context.Context, tenantID, sessionID string) ([]int, error)
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

// ReconcileTurnArtifacts 对比 turn 元数据与 body 文件两侧的一致性。
// bodies 必须同时实现 BodiesStore（Delete 用）与 BodiesLister；
// turns 提供元数据读端。
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
func RepairTurnArtifacts(ctx context.Context, report *TurnArtifactReport, bodies BodiesStore, action RepairAction) error {
	if report == nil || action != RepairDeleteOrphanBodies {
		return nil
	}
	for _, n := range report.OrphanBodies {
		// Delete 按 session 粒度；单 turn 孤儿通过再次写空+删不可行，
		// 这里走 bodies 的 Delete 语义需要 turn 级接口。孤儿文件删除由
		// FileBodiesStore.DeleteTurnFile 直接落盘（见 file 包）。
		if deleter, ok := bodies.(TurnFileDeleter); ok {
			if err := deleter.DeleteTurnFile(ctx, report.TenantID, report.SessionID, n); err != nil {
				return fmt.Errorf("consistency: delete orphan body %s/%s#%d: %w",
					report.TenantID, report.SessionID, n, err)
			}
		}
	}
	return nil
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
