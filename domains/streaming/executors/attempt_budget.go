package executors

import (
	"errors"
	"sync/atomic"
)

const DefaultUpstreamAttemptLimit = 100

var ErrUpstreamAttemptLimit = errors.New("upstream attempt limit exceeded")

// UpstreamAttemptBudget is the request-wide authority for real upstream HTTP
// calls. It is shared by Survival, dispatch, and protocol-local retry loops.
type UpstreamAttemptBudget struct {
	limit int64
	used  atomic.Int64
}

func NewUpstreamAttemptBudget(limit int) *UpstreamAttemptBudget {
	if limit <= 0 || limit > DefaultUpstreamAttemptLimit {
		limit = DefaultUpstreamAttemptLimit
	}
	return &UpstreamAttemptBudget{limit: int64(limit)}
}

func (b *UpstreamAttemptBudget) TryConsume() bool {
	if b == nil {
		return true
	}
	for {
		used := b.used.Load()
		if used >= b.limit {
			return false
		}
		if b.used.CompareAndSwap(used, used+1) {
			return true
		}
	}
}

func (b *UpstreamAttemptBudget) Used() int {
	if b == nil {
		return 0
	}
	return int(b.used.Load())
}

func (b *UpstreamAttemptBudget) Exhausted() bool {
	return b != nil && b.used.Load() >= b.limit
}

func consumeUpstreamAttempt(params *ExecParams) error {
	if params == nil || params.UpstreamAttempts == nil {
		return nil
	}
	if !params.UpstreamAttempts.TryConsume() {
		return ErrUpstreamAttemptLimit
	}
	return nil
}
