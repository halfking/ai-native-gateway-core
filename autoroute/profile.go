package autoroute

// Profile represents the user's preferred routing strategy. It controls
// how the 8-dimension composite score is weighted when picking the best
// candidate for a model=auto request.
//
// Default is ProfileSmart. Clients can override per-request via the
// X-Gw-Auto-Profile header. Per-API-Key preference is sticky via
// api_key_auto_profile (30min TTL).
type Profile string

const (
	// ProfileSmart is the default. Balances all 6 dimensions equally —
	// no extreme bias toward cost or speed.
	ProfileSmart Profile = "smart"

	// ProfileSpeedFirst prioritises low latency (P95) over cost.
	// Suitable for interactive chat, real-time agents.
	ProfileSpeedFirst Profile = "speed_first"

	// ProfileCostFirst minimises $/token. Suitable for bulk jobs,
	// background summarisation, embedding generation.
	ProfileCostFirst Profile = "cost_first"
)

// AllProfiles is the canonical profile list.
var AllProfiles = []Profile{ProfileSmart, ProfileSpeedFirst, ProfileCostFirst}

// ProfileWeights defines how heavily each scoring dimension contributes
// to the composite score. Weights are normalised to sum to 100 for
// interpretability; the composite formula multiplies by these weights
// directly (see scoring.go Score).
//
// 新增（需求 #3）：8 维权重，包含 VersionRecency 和 StrengthMatch
//
// Sums:
//   - smart        : 120 (新增 2 维各 10 分)
//   - speed_first  : 125 (新增 2 维各 7.5 分)
//   - cost_first   : 125 (新增 2 维各 7.5 分)
type ProfileWeights struct {
	Price          float64
	Speed          float64
	Stability      float64
	Match          float64
	Pressure       float64
	ContextFit     float64
	VersionRecency float64 // 新增：模型新旧度权重
	StrengthMatch  float64 // 新增：优势方向匹配权重
}

// Sum returns the sum of all weights (used for normalisation).
func (w ProfileWeights) Sum() float64 {
	return w.Price + w.Speed + w.Stability + w.Match + w.Pressure + w.ContextFit +
		w.VersionRecency + w.StrengthMatch
}

// DefaultProfileWeights returns the weights matrix used by default.
//
// The numbers are chosen empirically based on the routing goal stated
// by the team: "guarantee AI quality & speed while minimising cost".
//
// Smart: balanced across the 8 dimensions (total 120)
// SpeedFirst: prioritise Speed × 2.5 over Price (total 125)
// CostFirst: prioritise Price × 2.5 over Speed (total 125)
//
// 新增（需求 #3）：VersionRecency 和 StrengthMatch 各占 ~8% 权重
func DefaultProfileWeights() map[Profile]ProfileWeights {
	return map[Profile]ProfileWeights{
		ProfileSmart: {
			Price: 20, Speed: 20, Stability: 20,
			Match: 20, Pressure: 10, ContextFit: 15,
			VersionRecency: 10, StrengthMatch: 10,
		},
		ProfileSpeedFirst: {
			Price: 8, Speed: 50, Stability: 20,
			Match: 15, Pressure: 5, ContextFit: 10,
			VersionRecency: 7, StrengthMatch: 10,
		},
		ProfileCostFirst: {
			Price: 50, Speed: 8, Stability: 15,
			Match: 20, Pressure: 5, ContextFit: 10,
			VersionRecency: 7, StrengthMatch: 10,
		},
	}
}

// WeightsFor returns the weights for the given profile. Unknown profiles
// fall back to ProfileSmart to be conservative.
func WeightsFor(p Profile) ProfileWeights {
	all := DefaultProfileWeights()
	if w, ok := all[p]; ok {
		return w
	}
	return all[ProfileSmart]
}

// weightsStore is an optional global override source for profile weights.
// When set (via SetTuningStore), WeightsForDynamic consults it before
// falling back to compiled defaults. This keeps the hot-path Score()
// function allocation-free while still supporting runtime tuning.
var weightsStore *TuningStore

// SetTuningStore wires a global TuningStore for profile-weight lookups.
// Called once at startup from cmd/gateway/main.go. Pass nil to disable
// dynamic weights (use compiled defaults).
func SetTuningStore(ts *TuningStore) {
	weightsStore = ts
}

// WeightsForDynamic returns weights from the tuning store if configured,
// otherwise falls back to WeightsFor (compiled defaults).
//
// This is the function Score() calls when a TuningStore is active.
//
// 已废弃（需求 #3）：使用 WeightsForTask 替代，支持任务级动态权重调整
func WeightsForDynamic(p Profile) ProfileWeights {
	if weightsStore != nil {
		return weightsStore.WeightsFor(p)
	}
	return WeightsFor(p)
}

// WeightsForTask 返回针对特定任务类型调整后的权重（需求 #3 新增）。
//
// 编程任务（TaskCode）的 ContextFit 权重动态提升 ×1.5，因为编程场景
// 对上下文容量敏感度更高（长代码文件、多文件修改、plan mode）。
//
// 其他任务类型使用标准权重（通过 TuningStore 或 compiled defaults）。
func WeightsForTask(p Profile, task TaskType) ProfileWeights {
	w := WeightsForDynamic(p)

	// 编程任务的 ContextFit 权重提升（需求 #3/#4）
	if task == TaskCode {
		w.ContextFit *= 1.5
	}

	return w
}