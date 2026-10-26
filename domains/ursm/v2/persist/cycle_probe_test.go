package persist

import (
	"context"
	"testing"
	"time"
)

// Probe (not a gate): what does each cycle shape actually WRITE when Collect
// needs more than its own slice?
//
// scaled 1:1000 against production (collect=20s/flush=10s, ~1800 rows, tick=60s)
type probeStages struct {
	collectCost time.Duration
	rows        []Row

	collectOK bool
	written   int
	flushLeft time.Duration
	hadFlush  bool
}

func (p *probeStages) Collect(ctx context.Context) ([]Row, error) {
	select {
	case <-time.After(p.collectCost):
		return p.rows, nil
	case <-ctx.Done():
		return nil, ctx.Err() // 生产真实语义：writer.go:218 return nil, err
	}
}

func (p *probeStages) Flush(ctx context.Context, rows []Row) error {
	p.hadFlush = true
	deadline, ok := ctx.Deadline()
	if ok {
		p.flushLeft = time.Until(deadline)
	}
	for i := 0; i < len(rows); i++ {
		select {
		case <-time.After(time.Millisecond): // 1 行 / ms = 1000 行/s
			p.written++
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// sharedCycle is the production shape today: ONE ctx for both stages.
func sharedCycle(parent context.Context, s *probeStages, total time.Duration) {
	ctx, cancel := context.WithTimeout(parent, total)
	defer cancel()
	rows, err := s.Collect(ctx)
	if err != nil || len(rows) == 0 {
		return
	}
	_ = s.Flush(ctx, rows)
}

func runProbe(t *testing.T, name string, run func(context.Context, *probeStages)) {
	s := &probeStages{
		collectCost: 25 * time.Millisecond, // 需要「25s」才能采完
		rows:        make([]Row, 1800),
	}
	run(context.Background(), s)
	t.Logf("%-28s collect_ok=%-5v flush_called=%-5v flush_budget=%-8s rows_written=%d",
		name, s.collectOK, s.hadFlush, s.flushLeft.Round(time.Millisecond), s.written)
}

func TestProbeCompareShapes(t *testing.T) {
	runProbe(t, "生产形态(共用 30s ctx)", func(ctx context.Context, s *probeStages) {
		sharedCycle(ctx, s, 30*time.Millisecond)
	})
	runProbe(t, "预切(Collect 20s+Flush 10s)", func(ctx context.Context, s *probeStages) {
		RunCycle(ctx, s, CycleTimeouts{Collect: 20 * time.Millisecond, Flush: 10 * time.Millisecond})
	})
	runProbe(t, "Collect 无上限", func(ctx context.Context, s *probeStages) {
		cctx, cc := context.WithCancel(ctx)
		rows, err := s.Collect(cctx)
		cc()
		if err != nil || len(rows) == 0 {
			return
		}
		fctx, fc := context.WithTimeout(ctx, 10*time.Millisecond)
		_ = s.Flush(fctx, rows)
		fc()
	})
}
