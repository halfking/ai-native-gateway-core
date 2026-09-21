// role_llm_router.go — R48（2026-09-20）按「会话角色 × 细粒度任务类型」
// 选择 LLM 的偏好路由器。
//
// 用户口径（handoff §1）：
//   - 轻量池（搜索/总结/git 操作/运维 + 无法判定任务时兜底）：
//     minimax-m3 / glm-5.3-flash / kimi-k3 / deepseek-v4-flash
//   - 重量池（分析/规划/方案编写）：
//     glm-5.3 / claude-opus-5 / gpt-5.6-sol / grok-4.6 / deepseek-v4-pro
//   - 仅子代理角色（worker/planner/orchestrator）介入；main 保持 Decider
//     默认路径（需求原文是"子任务按任务类型重新选择 LLM"）。
//
// 偏好顺序语义：SelectLLM 返回有序 canonical_name 列表，Decide 管线把
// 列表中**首个存在于候选池**的模型提升到第一（promoteFirstPresent）；
// 全不存在则维持原候选（role 路由静默让位，绝不因偏好模型不可用而失败）。
//
// 并发模型：与 WorkTypeRouteStore 同款 atomic.Pointer snapshot ——
// Reload 整体替换快照，读路径无锁；失败保留上次好快照。
// 灰度开关 AUTO_ROLE_ROUTING_ENABLED 由 Decider 侧检查（本文件不查 flag，
// 便于单测直接验证表语义）。
package autoroute

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// builtinRoleLLMPreference 是内存默认表：TaskKind → 有序偏好模型
// （kind 定池、三个子代理角色共用；角色差异经 DB role_task_llm_mapping
// 覆盖）。与 730 迁移的种子行一一镜像，两处口径必须一致。
//
// 2026-09-20 R48 首版按用户口径设定；canonical_name 需与
// models_canonical 表核对（handoff §7 待办），入库前如有出入以库内
// 实测为准同步修正本表与 730 种子。
var builtinRoleLLMPreference = map[TaskKind][]string{
	KindSearch:    {"minimax-m3", "glm-5.3-flash"},
	KindSummarize: {"glm-5.3-flash", "minimax-m3"},
	KindGitOps:    {"kimi-k3", "deepseek-v4-flash"},
	KindOps:       {"deepseek-v4-flash", "kimi-k3"},
	KindAnalysis:  {"glm-5.3", "deepseek-v4-pro"},
	KindPlanning:  {"claude-opus-5", "glm-5.3"},
	KindSolution:  {"gpt-5.6-sol", "grok-4.6"},
	// 用户口径 #4：无法确定任务类型时默认 glm-5.3-flash / minimax-m3。
	KindUnknown: {"glm-5.3-flash", "minimax-m3"},
}

// roleRoutedRoles 是允许介入 role 路由的角色集合。main 不介入（保持
// 既有默认路由字节级不变）；unknown 无角色信息，同样不介入。
var roleRoutedRoles = map[AgentRole]bool{
	RoleWorker:       true,
	RolePlanner:      true,
	RoleOrchestrator: true,
}

// roleKindKey 是 (agent_role, task_kind) 二维键。
type roleKindKey struct {
	Role AgentRole
	Kind TaskKind
}

// roleRouteEntry 是 DB 快照里一行偏好的内存态。
type roleRouteEntry struct {
	CanonicalName string
	Priority      int
}

// roleRouteSnapshot 是 Reload 产出的不可变点时视图。
type roleRouteSnapshot struct {
	byRoleKind map[roleKindKey][]roleRouteEntry // priority 升序
	LoadedAt   time.Time
	Version    uint64
}

// RoleLLMRouter 加载并暴露 role_task_llm_mapping 偏好。
// pool == nil（单测/未装配 DB）时永远走内存默认表。
type RoleLLMRouter struct {
	pool     *pgxpool.Pool
	snapshot atomic.Pointer[roleRouteSnapshot]
	version  atomic.Uint64
}

// NewRoleLLMRouter constructs an empty router. Reload 之前（或 pool 为 nil）
// SelectLLM 走内存默认表。
func NewRoleLLMRouter(pool *pgxpool.Pool) *RoleLLMRouter {
	return &RoleLLMRouter{pool: pool}
}

// current returns the active snapshot (nil-safe).
func (r *RoleLLMRouter) current() *roleRouteSnapshot {
	if r == nil {
		return nil
	}
	return r.snapshot.Load()
}

// Reload 拉取平台级（tenant_id IS NULL）启用行并原子替换快照。失败保留
// 上次好快照（瞬时 DB 故障不能让 role 路由突然回退默认表造成行为抖动）。
// 租户级行暂不加载——灰度首版只读平台级，租户覆盖属后续迭代。
func (r *RoleLLMRouter) Reload(ctx context.Context) error {
	if r == nil || r.pool == nil {
		return nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT agent_role, task_kind, llm_canonical_name, priority
		FROM role_task_llm_mapping
		WHERE enabled = TRUE AND tenant_id IS NULL
		ORDER BY agent_role, task_kind, priority, llm_canonical_name
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	snap := &roleRouteSnapshot{
		byRoleKind: make(map[roleKindKey][]roleRouteEntry),
		LoadedAt:   time.Now(),
	}
	for rows.Next() {
		var (
			role, kind, model string
			priority          int
		)
		if err := rows.Scan(&role, &kind, &model, &priority); err != nil {
			return err
		}
		k := roleKindKey{Role: AgentRole(role), Kind: TaskKind(kind)}
		snap.byRoleKind[k] = append(snap.byRoleKind[k], roleRouteEntry{
			CanonicalName: model,
			Priority:      priority,
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	snap.Version = r.version.Add(1)
	r.snapshot.Store(snap)
	return nil
}

// SelectLLM 返回 (role, kind) 的有序偏好 canonical_name 列表：
// DB 快照有该 (role,kind) 行 → 按 priority 升序返回 DB 偏好（整体覆盖，
// 且对**任意角色**生效——管理员显式给 main 配行属于有意覆盖，内置
// 保守口径不得拦截）；无 DB 行时回退内存默认表（仅 worker/planner/
// orchestrator 三个子代理角色；main/unknown 无意见）。
// 返回 nil 表示"无意见"，调用方维持原候选不动。
func (r *RoleLLMRouter) SelectLLM(role AgentRole, kind TaskKind) []string {
	if role == "" {
		return nil
	}
	if snap := r.current(); snap != nil {
		if entries, ok := snap.byRoleKind[roleKindKey{Role: role, Kind: kind}]; ok && len(entries) > 0 {
			out := make([]string, 0, len(entries))
			for _, e := range entries {
				out = append(out, e.CanonicalName)
			}
			return out
		}
	}
	if !roleRoutedRoles[role] {
		return nil
	}
	if prefs, ok := builtinRoleLLMPreference[kind]; ok && len(prefs) > 0 {
		return append([]string(nil), prefs...)
	}
	return nil
}

// LoadedAt returns the timestamp of the last successful reload (zero value =
// 从未成功 Reload，SelectLLM 一直走内存默认表)。
func (r *RoleLLMRouter) LoadedAt() time.Time {
	if snap := r.current(); snap != nil {
		return snap.LoadedAt
	}
	return time.Time{}
}

// Version returns the active snapshot version (0 = 无 DB 快照)。
func (r *RoleLLMRouter) Version() uint64 {
	if snap := r.current(); snap != nil {
		return snap.Version
	}
	return 0
}

// promoteFirstPresent 把 prefs 中第一个存在于 candidates 的模型提升到
// 首位（保持其余相对顺序）。返回 (新列表, 命中的模型；"" = 无一存在，
// 原样返回)。与 promoteCanonical 同语义，但支持多候选依次尝试——
// 轻量池首选模型凭证全下线时自动落到备选，而不是放弃 role 路由。
func promoteFirstPresent(candidates []ScoredCandidate, prefs []string) ([]ScoredCandidate, string) {
	for _, want := range prefs {
		if want == "" {
			continue
		}
		for i, c := range candidates {
			if c.Candidate.CanonicalName == want {
				if i == 0 {
					return candidates, want
				}
				out := make([]ScoredCandidate, 0, len(candidates))
				out = append(out, candidates[i])
				out = append(out, candidates[:i]...)
				out = append(out, candidates[i+1:]...)
				return out, want
			}
		}
	}
	return candidates, ""
}

// withRoleFailoverHead（R50 F14）把 role 意图并入 tier 恢复计划头部：
// prefs 中仍在候选池者按 role 顺序打头（首个命中者即选中模型，满足
// TierFailoverModels "starts with the selected model" 契约），tierPlan
// 保序去重随后。成员资格以候选池为准——被 tier 滤除后靠豁免复活的
// role 偏好（不在 tierPlan 里）由此进入恢复链。
func withRoleFailoverHead(tierPlan, prefs []string, candidates []ScoredCandidate) []string {
	if len(tierPlan) == 0 {
		return nil
	}
	inPool := make(map[string]struct{}, len(candidates))
	for i := range candidates {
		inPool[candidates[i].Candidate.CanonicalName] = struct{}{}
	}
	out := make([]string, 0, len(tierPlan)+len(prefs))
	seen := make(map[string]struct{}, len(tierPlan)+len(prefs))
	add := func(name string) {
		if name == "" {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, want := range prefs {
		if _, ok := inPool[want]; ok {
			add(want)
		}
	}
	for _, name := range tierPlan {
		add(name)
	}
	return out
}
