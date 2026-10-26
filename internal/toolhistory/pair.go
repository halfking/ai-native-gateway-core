package toolhistory

import (
	"errors"
	"fmt"
)

// ErrAmbiguousHistory means a batch cannot be paired without guessing which
// actual call/result to discard. Callers must surface this as a request error.
var ErrAmbiguousHistory = errors.New("ambiguous tool history")

// Interrupted explicitly distinguishes an unavailable result from successful execution.
const Interrupted = "[The result of this tool call is unavailable: the turn was interrupted.]"

// Pair returns each call's result index, or -1 if it was interrupted. Both
// arguments belong to one batch; a later batch must never answer this one.
// Results for calls outside this slice are left to the caller's replay policy.
func Pair(calls, results []string) ([]int, error) {
	positions := make(map[string]int, len(calls))
	paired := make([]int, len(calls))
	for i, id := range calls {
		if _, exists := positions[id]; id == "" || exists {
			return nil, fmt.Errorf("%w: missing or repeated call ID %q", ErrAmbiguousHistory, id)
		}
		positions[id] = i
		paired[i] = -1
	}
	for i, id := range results {
		if j, ok := positions[id]; ok {
			if paired[j] >= 0 {
				return nil, fmt.Errorf("%w: repeated result for call ID %q", ErrAmbiguousHistory, id)
			}
			paired[j] = i
		}
	}
	return paired, nil
}
