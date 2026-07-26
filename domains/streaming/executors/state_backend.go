// Package executors — state_backend.go
//
// 状态后端抽象层：把"哪些候选可用"的判定收敛到一个接口，避免 router.go
// 和 executor.go 中散落多套状态系统的条件分支。
//
// 设计原则：
// - URSM v2 authoritative 模式 + Ready → URSMv2Backend（唯一权威源）
// - 否则 → LegacyStateBackend（StateManager 内存缓存）或 DBOnlyBackend（兜底）
//
// 防封锁机制（FpSlots/Limiter/RPM/DisguisePool/EgressIdentity）保持完全独立，
// 不通过此接口管理。状态后端仅负责"节点是否可用"的健康判断。
//
// 2026-07-24: Phase 1 of routing simplification plan.
// 2026-07-26: URSM v1→v2 统一。文档同步更新：v1 (domains/ursm) 已迁入
//
//	_to-be-deprecated/ursm/，本接口不再有 v1 分支。
package executors

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	ursmv2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// StateBackend 是路由时的状态判断接口。
// 负责过滤出"可用"的候选节点（基于健康状态、冷却期、熔断器等）。
// 不负责防封锁机制（FpSlots/Limiter/RPM 由 executor 独立管理）。
type StateBackend interface {
	// FilterAvailable 过滤出可用的候选节点。
	// 返回的候选列表是原始列表的子集，顺序可能被重排。
	FilterAvailable(ctx context.Context, candidates []provider.Candidate) []provider.Candidate

	// IsAuthoritative 返回此后端是否是权威的状态源。
	// true 表示后端已接管所有健康判断逻辑（如 URSM v2 authoritative 模式），
	// 调用方应跳过旧的健康检查（filterHealthyNodes）。
	IsAuthoritative() bool

	// Name 返回后端名称，用于日志和调试。
	Name() string
}

// URSMv2Backend 是 URSM v2 authoritative 模式的状态后端。
// 在此模式下，URSM v2 是唯一的状态权威源，旧的 StateManager/FpSlots
// NodeState 健康检查应被跳过。
type URSMv2Backend struct {
	mgr URSMv2Manager
}

func (b *URSMv2Backend) FilterAvailable(ctx context.Context, candidates []provider.Candidate) []provider.Candidate {
	if b.mgr == nil || len(candidates) == 0 {
		return candidates
	}

	// URSM v2 FilterAndScore 已在 router.go:103-148 调用过，
	// 此处仅需确认后端模式。实际过滤逻辑在 PlanCandidates 早期完成。
	// 这里返回原始候选，因为 authoritative 模式下 PlanCandidates
	// 已经在 line 103-148 完成了 FilterAndScore 过滤。
	return candidates
}

func (b *URSMv2Backend) IsAuthoritative() bool {
	return true
}

func (b *URSMv2Backend) Name() string {
	return "ursm_v2_authoritative"
}

// LegacyStateBackend 是旧 StateManager 的状态后端。
// 用于 URSM v2 未启用或处于 shadow/canary 模式时的降级路径。
type LegacyStateBackend struct {
	sm credentialstate.StateProvider
}

func (b *LegacyStateBackend) FilterAvailable(ctx context.Context, candidates []provider.Candidate) []provider.Candidate {
	if b.sm == nil || !b.sm.Enabled() {
		return filterAvailable(candidates)
	}

	var out []provider.Candidate
	for _, c := range candidates {
		available, reason := b.sm.IsAvailable(ctx, c.CredentialID, c.RawModel)
		if !available {
			slog.Debug("router: filtered by legacy state manager",
				"credential_id", c.CredentialID,
				"model", c.RawModel,
				"reason", reason,
			)
			continue
		}
		// 回退到原有逻辑：StateManager 通过后还需检查 DB 字段
		if c.IsAvailable() {
			out = append(out, c)
		}
	}
	return out
}

func (b *LegacyStateBackend) IsAuthoritative() bool {
	return false
}

func (b *LegacyStateBackend) Name() string {
	return "legacy_state_manager"
}

// DBOnlyBackend 是纯 DB 字段的状态后端。
// 当 StateManager 未启用且 URSM v2 未生效时使用。
// 仅依赖 provider.Candidate 的 IsAvailable() 方法（基于 DB 查询结果）。
type DBOnlyBackend struct{}

func (b *DBOnlyBackend) FilterAvailable(ctx context.Context, candidates []provider.Candidate) []provider.Candidate {
	return filterAvailable(candidates)
}

func (b *DBOnlyBackend) IsAuthoritative() bool {
	return false
}

func (b *DBOnlyBackend) Name() string {
	return "db_only"
}

// selectStateBackend 选择当前请求使用的状态后端。
// 决策逻辑（顺序敏感）：
//  1. URSM v2 authoritative 模式 + Redis Ready 标记 → URSMv2Backend
//  2. StateManager 启用 → LegacyStateBackend
//  3. 否则 → DBOnlyBackend
//
// 此方法应在路由决策开始时调用一次，避免热路径重复检查。
//
// 2026-07-26 URSM v1→v2 统一：旧的"v1 (domains/ursm.Manager) 优先" 分支
// 已删除——v1 在 main.go 中从未 wire。
func selectStateBackend(ursmv2Mgr URSMv2Manager, stateMgr credentialstate.StateProvider, ctx context.Context) StateBackend {
	// URSM v2 authoritative 模式优先
	if ursmv2Mgr != nil && ursmv2Mgr.Mode() == ursmv2api.ModeAuthoritative {
		readyCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		if ursmv2Mgr.Ready(readyCtx) {
			return &URSMv2Backend{mgr: ursmv2Mgr}
		}
		// Ready 检查失败，降级到 StateManager
		slog.Warn("ursm_v2 authoritative mode but not ready, falling back to legacy",
			"mode", ursmv2Mgr.Mode(),
		)
	}

	// StateManager 降级路径
	if stateMgr != nil && stateMgr.Enabled() {
		return &LegacyStateBackend{sm: stateMgr}
	}

	// 兜底：仅依赖 DB 字段
	return &DBOnlyBackend{}
}

// URSMv2Manager 是 URSM v2 Manager 的最小接口，用于避免测试时的类型耦合。
type URSMv2Manager interface {
	Mode() ursmv2api.RolloutMode
	Ready(ctx context.Context) bool
}
