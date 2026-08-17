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
	_, ok := b.Consume()
	return ok
}

func (b *UpstreamAttemptBudget) Consume() (int, bool) {
	if b == nil {
		return 1, true
	}
	for {
		used := b.used.Load()
		if used >= b.limit {
			return 0, false
		}
		if b.used.CompareAndSwap(used, used+1) {
			return int(used + 1), true
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

func consumeUpstreamAttempt(params *ExecParams) (int, error) {
	if params == nil {
		return 1, nil
	}
	if params.UpstreamAttempts == nil {
		params.AttemptNo++
		return params.AttemptNo, nil
	}
	attempt, ok := params.UpstreamAttempts.Consume()
	if !ok {
		return 0, ErrUpstreamAttemptLimit
	}
	return attempt, nil
}
