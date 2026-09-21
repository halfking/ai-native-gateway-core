package main

import "sort"

// labelStats accumulates the confusion counts for one expected label.
type labelStats struct {
	TP, FP, FN int
}

func (l *labelStats) precision() float64 {
	if l.TP+l.FP == 0 {
		return 0
	}
	return float64(l.TP) / float64(l.TP+l.FP)
}

func (l *labelStats) recall() float64 {
	if l.TP+l.FN == 0 {
		return 0
	}
	return float64(l.TP) / float64(l.TP+l.FN)
}

func (l *labelStats) f1() float64 {
	p, r := l.precision(), l.recall()
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

// report aggregates benchmark outcomes. Error samples (HTTP failures,
// timeouts, network errors) are tracked separately and excluded from the
// accuracy denominator so a rate-limited provider does not silently deflate
// quality numbers — check the error counters before trusting accuracy.
type report struct {
	labels    []string
	labelSet  map[string]struct{}
	correct   int
	total     int
	invalid   int // responses that parsed but were not one of the known labels
	confusion map[string]map[string]int
	stats     map[string]*labelStats
	latencies []float64
	errors    map[string]int
}

func newReport(labels []string) *report {
	set := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		set[l] = struct{}{}
	}
	return &report{
		labels:    labels,
		labelSet:  set,
		confusion: make(map[string]map[string]int),
		stats:     make(map[string]*labelStats),
		errors:    make(map[string]int),
	}
}

func (r *report) validLabel(s string) bool {
	_, ok := r.labelSet[s]
	return ok
}

// add records one sample outcome. A non-empty errKind means the request never
// produced a usable answer; it counts toward errors only.
func (r *report) add(expected, predicted, errKind string, latencyMS float64) {
	if errKind != "" {
		r.errors[errKind]++
		return
	}
	r.total++
	r.latencies = append(r.latencies, latencyMS)

	valid := r.validLabel(predicted)
	if !valid {
		r.invalid++
	}
	if r.confusion[expected] == nil {
		r.confusion[expected] = make(map[string]int)
	}
	r.confusion[expected][predicted]++

	if r.stats[expected] == nil {
		r.stats[expected] = &labelStats{}
	}
	if expected == predicted {
		r.correct++
		r.stats[expected].TP++
		return
	}
	r.stats[expected].FN++
	// An invalid answer is not any real label's false positive.
	if valid {
		if r.stats[predicted] == nil {
			r.stats[predicted] = &labelStats{}
		}
		r.stats[predicted].FP++
	}
}

func (r *report) accuracy() float64 {
	if r.total == 0 {
		return 0
	}
	return float64(r.correct) / float64(r.total)
}

func (r *report) macroF1() float64 {
	if len(r.stats) == 0 {
		return 0
	}
	sum := 0.0
	for _, s := range r.stats {
		sum += s.f1()
	}
	return sum / float64(len(r.stats))
}

// latencyPercentile returns the nearest-rank p'th percentile (p in (0,1]) of
// the recorded latencies in ms; 0 when nothing was recorded.
func (r *report) latencyPercentile(p float64) float64 {
	if len(r.latencies) == 0 {
		return 0
	}
	sorted := append([]float64(nil), r.latencies...)
	sort.Float64s(sorted)
	rank := int(float64(len(sorted))*p + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// topMisclassifications returns up to n (expected, predicted, count) triples
// sorted by descending count, excluding correct predictions.
func (r *report) topMisclassifications(n int) []struct {
	Expected  string
	Predicted string
	Count     int
} {
	type triple = struct {
		Expected  string
		Predicted string
		Count     int
	}
	var out []triple
	for expected, preds := range r.confusion {
		for predicted, count := range preds {
			if expected == predicted || count == 0 {
				continue
			}
			out = append(out, triple{expected, predicted, count})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Expected+out[i].Predicted < out[j].Expected+out[j].Predicted
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
