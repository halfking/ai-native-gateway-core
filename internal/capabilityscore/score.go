package capabilityscore

import (
	"math"
	"sort"
	"strings"
)

type Profile struct {
	ModalityCaps       []string
	IntelligenceLevel  *int
	ContextLevel       *int
	ResponseSpeedLevel *int
	PriceLevel         *int
	CapabilityScore    *float64
}

type Inputs struct {
	IntelligenceLevel  *int
	ContextLevel       *int
	ModalityFitLevel   *int
	ResponseSpeedLevel *int
	PriceEfficiency    *int
}

type Breakdown struct {
	Intelligence      float64
	Context           float64
	ModalityFit       float64
	ResponseSpeed     float64
	PriceEfficiency   float64
	CapabilityScore   float64
	UnknownDimensions []string
}

const CalculationVersion = "v1"

func Calculate(inputs Inputs) Breakdown {
	breakdown := Breakdown{}
	breakdown.Intelligence = normalized(inputs.IntelligenceLevel, "intelligence", &breakdown)
	breakdown.Context = normalized(inputs.ContextLevel, "context", &breakdown)
	breakdown.ModalityFit = normalized(inputs.ModalityFitLevel, "modality_fit", &breakdown)
	breakdown.ResponseSpeed = normalized(inputs.ResponseSpeedLevel, "response_speed", &breakdown)
	breakdown.PriceEfficiency = normalized(inputs.PriceEfficiency, "price_efficiency", &breakdown)
	breakdown.CapabilityScore = 100 * (0.45*breakdown.Intelligence +
		0.20*breakdown.Context +
		0.15*breakdown.ModalityFit +
		0.10*breakdown.ResponseSpeed +
		0.10*breakdown.PriceEfficiency)
	return breakdown
}

func SupportsAll(profile Profile, requiredCaps []string) bool {
	supported := make(map[string]struct{}, len(profile.ModalityCaps))
	for _, cap := range profile.ModalityCaps {
		supported[strings.ToLower(strings.TrimSpace(cap))] = struct{}{}
	}
	for _, cap := range requiredCaps {
		if _, ok := supported[strings.ToLower(strings.TrimSpace(cap))]; !ok {
			return false
		}
	}
	return true
}

func SimilarityDistance(requested, candidate Profile) (float64, bool) {
	requestedValues := []*int{
		requested.IntelligenceLevel,
		requested.ContextLevel,
		requested.ResponseSpeedLevel,
		requested.PriceLevel,
	}
	candidateValues := []*int{
		candidate.IntelligenceLevel,
		candidate.ContextLevel,
		candidate.ResponseSpeedLevel,
		candidate.PriceLevel,
	}
	weights := []float64{0.55, 0.25, 0.10, 0.10}

	var totalWeight, distance float64
	for index := range requestedValues {
		if requestedValues[index] == nil || candidateValues[index] == nil {
			continue
		}
		totalWeight += weights[index]
		distance += weights[index] * math.Abs(float64(*requestedValues[index]-*candidateValues[index])) / 10
	}
	if totalWeight == 0 {
		return 0, false
	}
	return 100 * distance / totalWeight, true
}

func normalized(value *int, name string, breakdown *Breakdown) float64 {
	if value == nil || *value < 1 || *value > 10 {
		breakdown.UnknownDimensions = append(breakdown.UnknownDimensions, name)
		return 0
	}
	return float64(*value) / 10
}

func NormalizedCaps(caps []string) []string {
	seen := make(map[string]struct{}, len(caps))
	for _, cap := range caps {
		normalized := strings.ToLower(strings.TrimSpace(cap))
		if normalized != "" {
			seen[normalized] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for cap := range seen {
		out = append(out, cap)
	}
	sort.Strings(out)
	return out
}
