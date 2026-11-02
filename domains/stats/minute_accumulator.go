package stats

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/internal/probelane"
)

const flushInterval = 30 * time.Second

// MinuteAccumulator buffers per-minute dashboard stats and flushes to PostgreSQL.
//
// ★ 2026-11-02 审计钉死：本类型在生产**未接线**（cmd/gateway 注释写明
// 「Keep the type available for a future event projection, but do not wire
// it into the live request path」；全仓无 NewMinuteAccumulator 调用方）。
// 生产分钟表写入者 = bg.StatsMinuteRollup（业务臂 + 探测臂
// stats_minute_probe_rollup.go）。probeMain 分流保留仅供若将来复活本累加器
// 时与 rollup 同口径；**不要**把「这里有探测分流」读成「生产在写探测分钟表」。
type MinuteAccumulator struct {
	db        *pgxpool.Pool
	mu        sync.Mutex
	main      map[string]*MinuteRow
	dims      map[string]*DimRow
	drills    map[string]*ErrorDrillRow
	probeMain map[string]*MinuteRow // dead unless NewMinuteAccumulator is wired
	cancel    context.CancelFunc
	done      chan struct{}
}

func NewMinuteAccumulator(db *pgxpool.Pool) *MinuteAccumulator {
	return &MinuteAccumulator{
		db:        db,
		main:      make(map[string]*MinuteRow),
		dims:      make(map[string]*DimRow),
		drills:    make(map[string]*ErrorDrillRow),
		probeMain: make(map[string]*MinuteRow),
		done:      make(chan struct{}),
	}
}

func (a *MinuteAccumulator) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	go a.loop(cctx)
	slog.Info("stats minute accumulator started", "flush_interval", flushInterval.String())
}

func (a *MinuteAccumulator) Stop() {
	if a.cancel != nil {
		a.cancel()
		<-a.done
	}
}

func (a *MinuteAccumulator) loop(ctx context.Context) {
	defer close(a.done)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.flush(context.Background())
			return
		case <-ticker.C:
			a.flush(ctx)
		}
	}
}

// Record ingests a completed request log entry (call from onPersisted hook).
func (a *MinuteAccumulator) Record(entry *telemetry.RequestLogEntry) {
	now := time.Now().UTC()
	if IsProbeTraffic(entry) {
		if !probelane.PhysicalEnabled() {
			return
		}
		main, ok := FromProbeTelemetryEntry(entry, now)
		if !ok {
			return
		}
		a.mu.Lock()
		mergeMinuteRow(a.probeMain, mainKey(main), &main)
		a.mu.Unlock()
		return
	}
	main, dims, drills, ok := FromTelemetryEntry(entry, now)
	if !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	mergeMinuteRow(a.main, mainKey(main), &main)
	for i := range dims {
		mergeDimRow(a.dims, dimKey(dims[i]), &dims[i])
	}
	for i := range drills {
		mergeDrillRow(a.drills, drillKey(drills[i]), &drills[i])
	}
}

func mainKey(r MinuteRow) string {
	return r.Bucket.Format(time.RFC3339) + "|" + r.TenantID + "|" +
		itoa(r.ProviderID) + "|" + itoa(r.CanonicalID)
}

func dimKey(r DimRow) string {
	return r.Bucket.Format(time.RFC3339) + "|" + r.TenantID + "|" + r.DimType + "|" + r.DimKey
}

func drillKey(r ErrorDrillRow) string {
	return r.Bucket.Format(time.RFC3339) + "|" + r.TenantID + "|" + r.ErrorKind + "|" +
		r.ModelName + "|" + itoa(r.ProviderID) + "|" + r.ClientProfile
}

func mergeMinuteRow(m map[string]*MinuteRow, key string, row *MinuteRow) {
	existing, ok := m[key]
	if !ok {
		copy := *row
		m[key] = &copy
		return
	}
	existing.Requests += row.Requests
	existing.SuccessCount += row.SuccessCount
	existing.FailureCount += row.FailureCount
	existing.PromptTokens += row.PromptTokens
	existing.CompletionTokens += row.CompletionTokens
	existing.TotalTokens += row.TotalTokens
	existing.CreditsCharged += row.CreditsCharged
	existing.CostUSD += row.CostUSD
	existing.LatencyMsSum += row.LatencyMsSum
}

func mergeDimRow(m map[string]*DimRow, key string, row *DimRow) {
	existing, ok := m[key]
	if !ok {
		copy := *row
		m[key] = &copy
		return
	}
	existing.Requests += row.Requests
	existing.SuccessCount += row.SuccessCount
	existing.FailureCount += row.FailureCount
	existing.TotalTokens += row.TotalTokens
	existing.CreditsCharged += row.CreditsCharged
	existing.CostUSD += row.CostUSD
}

func mergeDrillRow(m map[string]*ErrorDrillRow, key string, row *ErrorDrillRow) {
	existing, ok := m[key]
	if !ok {
		copy := *row
		m[key] = &copy
		return
	}
	existing.Requests += row.Requests
}

func (a *MinuteAccumulator) flush(ctx context.Context) {
	a.mu.Lock()
	if len(a.main) == 0 && len(a.dims) == 0 && len(a.drills) == 0 && len(a.probeMain) == 0 {
		a.mu.Unlock()
		return
	}
	main := a.main
	dims := a.dims
	drills := a.drills
	probeMain := a.probeMain
	a.main = make(map[string]*MinuteRow)
	a.dims = make(map[string]*DimRow)
	a.drills = make(map[string]*ErrorDrillRow)
	a.probeMain = make(map[string]*MinuteRow)
	a.mu.Unlock()

	if a.db == nil {
		return
	}
	flushCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := flushMinuteRows(flushCtx, a.db, main, dims, drills); err != nil {
		slog.Warn("stats minute flush failed", "error", err)
	}
	if err := flushProbeMinuteRows(flushCtx, a.db, probeMain); err != nil {
		slog.Warn("stats probe minute flush failed", "error", err)
	}
}
