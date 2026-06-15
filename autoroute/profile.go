package autoroute

// Profile represents the user's preferred routing strategy. It controls
// how the 6-dimension composite score is weighted when picking the best
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
// Sums:
//   - smart        : 100
//   - speed_first  : 110 (rebalanced to weight speed 2x over cost)
//   - cost_first   : 110 (rebalanced to weight cost 2x over speed)
type ProfileWeights struct {
	Price      float64
	Speed      float64
	Stability  float64
	Match      float64
	Pressure   float64
	ContextFit float64
}

// DefaultProfileWeights returns the weights matrix used by default.
//
// The numbers are chosen empirically based on the routing goal stated
// by the team: "guarantee AI quality & speed while minimising cost".
//
// Smart: balanced across the board.
// Speed-first: Price drops to 10, Speed rises to 50 (5x diff).
// Cost-first: Price rises to 50, Speed drops to 10 (5x diff).
func DefaultProfileWeights() map[Profile]ProfileWeights {
	return map[Profile]ProfileWeights{
		ProfileSmart: {
			Price: 25, Speed: 25, Stability: 20,
			Match: 25, Pressure: 10, ContextFit: 15,
		},
		ProfileSpeedFirst: {
			Price: 10, Speed: 50, Stability: 20,
			Match: 15, Pressure: 5, ContextFit: 10,
		},
		ProfileCostFirst: {
			Price: 50, Speed: 10, Stability: 15,
			Match: 20, Pressure: 5, ContextFit: 10,
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
func WeightsForDynamic(p Profile) ProfileWeights {
	if weightsStore != nil {
		return weightsStore.WeightsFor(p)
	}
	return WeightsFor(p)
}

// Sum returns the total weight (used for normalisation in scoring.go).
// Sum may exceed 100 when a profile intentionally biases a dimension.
func (w ProfileWeights) Sum() float64 {
	return w.Price + w.Speed + w.Stability + w.Match + w.Pressure + w.ContextFit
}