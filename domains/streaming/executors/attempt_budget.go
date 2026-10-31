package executors

import (
	"errors"
	"sync/atomic"

	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	// DefaultUpstreamAttemptLimit is the ordinary daytime request budget.
	DefaultUpstreamAttemptLimit = 100
	// MaxUpstreamAttemptLimit is the hard safety ceiling used by the night
	// recovery policy: one initial attempt plus at most 600 retries.
	MaxUpstreamAttemptLimit = 601
)

var ErrUpstreamAttemptLimit = errors.New("upstream attempt limit exceeded")

// UpstreamAttemptBudget is the request-wide authority for real upstream HTTP
// calls. It is shared by Survival, dispatch, and protocol-local retry loops.
type UpstreamAttemptBudget struct {
	limit int64
	used  atomic.Int64
}

func NewUpstreamAttemptBudget(limit int) *UpstreamAttemptBudget {
	if limit <= 0 || limit > MaxUpstreamAttemptLimit {
		limit = DefaultUpstreamAttemptLimit
	}
	return &UpstreamAttemptBudget{limit: int64(limit)}
}

// RequestAttemptLimit 是一次客户端请求的尝试预算读数：settings_kv 的
// survival.attempt_budget 覆盖值优先，0（未设置）回落内置默认。
// 2026-10-30 magpie 对比轮（docs/research/2026-10-30-*.md §四 P0-2）：
// 从两处构造点各自硬编码 DefaultUpstreamAttemptLimit 收敛到这一个读数点，
// 预算大小可经热加载调整而无需改部署。
func RequestAttemptLimit() int {
	if v := settings.SurvivalAttemptBudget(); v > 0 {
		return v
	}
	return DefaultUpstreamAttemptLimit
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
