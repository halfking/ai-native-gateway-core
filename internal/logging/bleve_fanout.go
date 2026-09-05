// Package logging — Bleve log full-text search fan-out (Phase A)
//
// BleveFanoutHandler wraps another slog.Handler (typically the
// lumberjack-backed JSON handler) and asynchronously indexes every
// emitted record into a Bleve index so the admin layer can run
// `GET /admin/logs/search?q=&tenant=&level=&from=&to=&...` without
// touching lumberjack or running grep.
//
// Failure isolation
// ─────────────────
// The whole point of the fan-out is that a Bleve write failure must
// NEVER break the primary log pipeline (lumberjack). We therefore:
//
//  1. Forward the record to the wrapped handler first, on the same
//     goroutine, so caller-visible latency stays identical to today.
//  2. Push a small envelope (already-serialized JSON + parsed
//     attributes) onto a bounded ring channel; if the channel is
//     full we DROP the record and increment a counter. Backpressure
//     on the indexer must never reach the caller.
//  3. The background indexer goroutine drains the channel and calls
//     bleve.Index; any error from bleve is logged to stderr (NOT to
//     slog, since slog itself is the pipeline we're protecting) and
//     counted, then the goroutine keeps running.
//
// Lifecycle
// ─────────
// Init registers the indexer on package init; Shutdown (called from
// cmd/gateway/main.go via logging.Shutdown) drains the channel,
// closes the Bleve index, and waits for the indexer goroutine to
// exit. The index lives at {log_dir}/../bleve unless overridden by
// LLM_GATEWAY_LOG_INDEX_DIR.

package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search"
	"github.com/blevesearch/bleve/v2/search/query"
)

// BleveConfig captures the fan-out knobs. It is populated from
// environment variables by bleveConfigFromEnv and passed into
// EnableBleveFanout.
type BleveConfig struct {
	// IndexDir is the Bleve scorch index directory. Defaults to
	// {LogDir}/../bleve where LogDir is the directory of the active
	// log file. Override with LLM_GATEWAY_LOG_INDEX_DIR.
	IndexDir string
	// BufferSize is the ring-channel capacity. Defaults to 8192.
	BufferSize int
	// FlushInterval bounds how long a record can sit in the indexer
	// queue before the indexer flushes the Bleve batch.
	FlushInterval time.Duration
	// BatchSize is the max number of records the indexer batches
	// into a single Bleve batch before forcing a flush.
	BatchSize int
	// Enabled gates the whole fan-out; when false Init/Register are
	// no-ops and the wrapped handler runs as today.
	Enabled bool
}

func bleveConfigFromEnv(logDir string) BleveConfig {
	cfg := BleveConfig{
		IndexDir:      filepath.Join(logDir, "..", "bleve"),
		BufferSize:    8192,
		FlushInterval: 500 * time.Millisecond,
		BatchSize:     256,
		Enabled:       false,
	}

	if v := os.Getenv("LLM_GATEWAY_LOG_INDEX_DIR"); v != "" {
		cfg.IndexDir = v
	}
	if v := os.Getenv("LLM_GATEWAY_LOG_BLEVE_ENABLED"); v == "true" || v == "1" {
		cfg.Enabled = true
	}
	if v := os.Getenv("LLM_GATEWAY_LOG_BLEVE_BUFFER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.BufferSize = n
		}
	}
	if v := os.Getenv("LLM_GATEWAY_LOG_BLEVE_FLUSH_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.FlushInterval = time.Duration(n) * time.Millisecond
		}
	}
	if v := os.Getenv("LLM_GATEWAY_LOG_BLEVE_BATCH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.BatchSize = n
		}
	}

	// Resolve to an absolute path so the JSON `index_dir` returned
	// from /admin/logs/status is unambiguous.
	if abs, err := filepath.Abs(cfg.IndexDir); err == nil {
		cfg.IndexDir = abs
	}
	return cfg
}

// bleveIndexRecord is what we actually push into the Bleve document
// store. We keep the parsed attributes flat so Bleve's default
// analyzer can index msg/timestamp/level cleanly.
type bleveIndexRecord struct {
	Timestamp time.Time              `json:"ts"`
	Level     string                 `json:"level"`
	Msg       string                 `json:"msg"`
	Attrs     map[string]interface{} `json:"-"`
	Raw       string                 `json:"raw"`
}

// bleveIndexer is the package-singleton background worker.
type bleveIndexer struct {
	cfg     BleveConfig
	ch      chan *bleveIndexRecord
	index   bleve.Index
	once    sync.Once
	wg      sync.WaitGroup
	stop    chan struct{}
	dropped uint64 // records dropped because channel was full
	indexed uint64 // records successfully indexed
	failed  uint64 // records that failed to index (any reason)
	seq     uint64 // per-process sequence for bleve doc id uniqueness
}

var (
	bleveIdx  *bleveIndexer
	bleveMu   sync.Mutex
	bleveOnce sync.Once
)

// EnableBleveFanout is called from Init (when LLM_GATEWAY_LOG_BLEVE_ENABLED=true)
// to spin up the Bleve indexer and wrap the existing JSON handler.
//
// It is idempotent: a second call is a no-op so the bootstrap order in
// cmd/gateway/main.go does not have to be perfect.
//
// Returns the indexer handle (so callers like cmd/bleve-backfill or
// the admin handler can issue searches) or nil if disabled/failed.
// On failure the function logs to stderr and returns nil so the
// primary log pipeline keeps running unindexed.
func EnableBleveFanout(parent slog.Handler, cfg BleveConfig) *bleveIndexer {
	if !cfg.Enabled {
		return nil
	}

	bleveMu.Lock()
	defer bleveMu.Unlock()
	if bleveIdx != nil {
		// Already enabled — wrap parent with the existing handler.
		return bleveIdx
	}

	if err := os.MkdirAll(cfg.IndexDir, 0o755); err != nil {
		log.Printf("logging/bleve: create index dir %q: %v (fan-out disabled)", cfg.IndexDir, err)
		return nil
	}

	idx, err := bleve.Open(cfg.IndexDir)
	if err != nil {
		// Open failed. Distinguish "first boot / empty dir" from "existing
		// but corrupt": bleve.New() refuses a non-empty dir, so a corrupt
		// index would previously disable the fan-out forever with only a
		// log line. Rename the broken dir aside and rebuild.
		var empty bool
		if ents, rerr := os.ReadDir(cfg.IndexDir); rerr == nil && len(ents) == 0 {
			empty = true
		}
		if !empty {
			badDir := cfg.IndexDir + ".bad-" + time.Now().Format("20060102-150405")
			if rerr := os.Rename(cfg.IndexDir, badDir); rerr == nil {
				log.Printf("logging/bleve: corrupt index moved aside to %q; rebuilding", badDir)
			}
		}
		mapping := bleve.NewIndexMapping()
		idx, err = bleve.New(cfg.IndexDir, mapping)
		if err != nil {
			log.Printf("logging/bleve: open/create index at %q: %v (fan-out disabled)", cfg.IndexDir, err)
			return nil
		}
	}

	bi := &bleveIndexer{
		cfg:   cfg,
		ch:    make(chan *bleveIndexRecord, cfg.BufferSize),
		index: idx,
		stop:  make(chan struct{}),
	}
	bleveIdx = bi
	bi.wg.Add(1)
	go bi.run()

	log.Printf("logging/bleve: fan-out enabled at %q (buffer=%d, batch=%d, flush=%s)",
		cfg.IndexDir, cfg.BufferSize, cfg.BatchSize, cfg.FlushInterval)
	return bi
}

// DisableBleveFanout is called by Shutdown to drain the queue and
// close the index. Safe to call even if the fan-out was never
// enabled.
func DisableBleveFanout() {
	bleveMu.Lock()
	bi := bleveIdx
	bleveIdx = nil
	bleveMu.Unlock()
	if bi == nil {
		return
	}
	close(bi.stop)
	bi.wg.Wait()
	if err := bi.index.Close(); err != nil {
		log.Printf("logging/bleve: close index: %v", err)
	}
}

// CurrentBleveIndexer returns the active indexer (or nil). Used by
// the admin search handler and the cmd/bleve-backfill tool.
func CurrentBleveIndexer() *bleveIndexer {
	bleveMu.Lock()
	defer bleveMu.Unlock()
	return bleveIdx
}

func (b *bleveIndexer) run() {
	defer b.wg.Done()
	batch := b.index.NewBatch()
	tick := time.NewTicker(b.cfg.FlushInterval)
	defer tick.Stop()

	flush := func() {
		if batch.Size() == 0 {
			return
		}
		sz := uint64(batch.Size())
		if err := b.index.Batch(batch); err != nil {
			atomic.AddUint64(&b.failed, sz)
			log.Printf("logging/bleve: batch flush: %v", err)
		} else {
			atomic.AddUint64(&b.indexed, sz)
		}
		batch = b.index.NewBatch()
	}

	for {
		select {
		case <-b.stop:
			// Drain anything remaining, best-effort. Two safety margins:
			//   1) batch inside the drain (no giant one-shot batch), and
			//   2) a bounded remaining count so suppliers that keep
			//      producing during shutdown cannot spin this loop forever
			//      (their enqueue is non-blocking and would otherwise win
			//      the select every time).
			remaining := len(b.ch) + b.cfg.BatchSize
			for i := 0; i < remaining; i++ {
				select {
				case rec := <-b.ch:
					b.addToBatch(batch, rec)
					if batch.Size() >= b.cfg.BatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
			flush()
			return
		case rec := <-b.ch:
			b.addToBatch(batch, rec)
			if batch.Size() >= b.cfg.BatchSize {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

func (b *bleveIndexer) addToBatch(batch *bleve.Batch, rec *bleveIndexRecord) {
	// Build a flat doc map so Bleve's default analyzer can tokenize
	// msg/timestamp/level cleanly. We promote the most common
	// correlation fields up to the top so search filters can use
	// them directly.
	doc := map[string]interface{}{
		"ts":    rec.Timestamp,
		"level": rec.Level,
		"msg":   rec.Msg,
		"raw":   rec.Raw,
		"attrs": rec.Attrs,
	}
	for k, v := range rec.Attrs {
		// Lift common trace ids so /admin/logs/search?request_id=...
		// can be a term query without nested-doc gymnastics.
		switch k {
		case "request_id", "tenant_id", "user_id", "session_id",
			"trace_id", "span_id", "model", "provider_id", "method", "path":
			doc[k] = v
		}
	}
	// Doc ID: UnixNano alone collides for two records at the same
	// nanosecond (bleve Index is an upsert — the later record would
	// silently overwrite the earlier one). Serialize with a per-batch
	// sequence suffix.
	seq := atomic.AddUint64(&b.seq, 1)
	if err := batch.Index(strconv.FormatInt(rec.Timestamp.UnixNano(), 10)+"-"+strconv.FormatUint(seq, 36), doc); err != nil {
		atomic.AddUint64(&b.failed, 1)
	}
}

// enqueueRecord serializes the JSON envelope and pushes it to the
// indexer. Drops on a full channel and increments the dropped counter.
func (b *bleveIndexer) enqueueRecord(rawJSON []byte, ts time.Time, level, msg string, attrs map[string]interface{}) {
	if b == nil {
		return
	}
	rec := &bleveIndexRecord{
		Timestamp: ts,
		Level:     level,
		Msg:       msg,
		Attrs:     attrs,
		Raw:       string(rawJSON),
	}
	select {
	case b.ch <- rec:
	default:
		atomic.AddUint64(&b.dropped, 1)
	}
}

// Stats returns a JSON-friendly snapshot of the fan-out's counters.
func (b *bleveIndexer) Stats() map[string]interface{} {
	if b == nil {
		return map[string]interface{}{"enabled": false}
	}
	return map[string]interface{}{
		"enabled":         true,
		"index_dir":       b.cfg.IndexDir,
		"buffer_size":     b.cfg.BufferSize,
		"batch_size":      b.cfg.BatchSize,
		"flush_interval":  b.cfg.FlushInterval.String(),
		"queue_depth":     len(b.ch),
		"records_indexed": atomic.LoadUint64(&b.indexed),
		"records_dropped": atomic.LoadUint64(&b.dropped),
		"records_failed":  atomic.LoadUint64(&b.failed),
	}
}

// BleveFanoutHandler is a slog.Handler that wraps `parent` and pushes
// a copy of each record into the Bleve indexer. The wrapped handler
// runs first on the calling goroutine so the caller's write latency
// matches today's lumberjack-only path.
type BleveFanoutHandler struct {
	parent  slog.Handler
	indexer *bleveIndexer
}

// NewBleveFanoutHandler constructs a fan-out wrapper. When the
// indexer is nil (fan-out disabled) it is a transparent pass-through
// to parent so call-sites do not need to special-case the disabled
// path.
func NewBleveFanoutHandler(parent slog.Handler, indexer *bleveIndexer) *BleveFanoutHandler {
	return &BleveFanoutHandler{parent: parent, indexer: indexer}
}

func (h *BleveFanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.parent.Enabled(ctx, level)
}

func (h *BleveFanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	// 1) Forward to lumberjack (synchronous, must succeed).
	if err := h.parent.Handle(ctx, r); err != nil {
		// We still try to index even if lumberjack hiccupped —
		// the JSON envelope below is independently derived.
		log.Printf("logging/bleve: parent handler error: %v", err)
	}

	// 2) Build the Bleve document and enqueue asynchronously.
	if h.indexer == nil {
		return nil
	}
	rec := recordToIndexRecord(r)
	h.indexer.enqueueRecord([]byte(rec.Raw), rec.Timestamp, rec.Level, rec.Msg, rec.Attrs)
	return nil
}

func (h *BleveFanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &BleveFanoutHandler{parent: h.parent.WithAttrs(attrs), indexer: h.indexer}
}

func (h *BleveFanoutHandler) WithGroup(name string) slog.Handler {
	return &BleveFanoutHandler{parent: h.parent.WithGroup(name), indexer: h.indexer}
}

// recordToIndexRecord flattens a slog.Record into a JSON envelope
// and a typed struct for the indexer. We always rebuild the JSON
// from the structured Record so search hits contain the same payload
// the file log emits — no risk of the two drifts drifting.
func recordToIndexRecord(r slog.Record) *bleveIndexRecord {
	attrs := make(map[string]interface{}, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = attrValue(a)
		return true
	})

	envelope := map[string]interface{}{
		"ts":    r.Time.UTC().Format(time.RFC3339Nano),
		"level": r.Level.String(),
		"msg":   r.Message,
	}
	for k, v := range attrs {
		envelope[k] = v
	}
	buf, err := json.Marshal(envelope)
	if err != nil {
		buf = []byte(fmt.Sprintf(`{"ts":"%s","level":"%s","msg":%q,"_marshal_err":%q}`,
			r.Time.UTC().Format(time.RFC3339Nano), r.Level.String(), r.Message, err.Error()))
	}

	return &bleveIndexRecord{
		Timestamp: r.Time,
		Level:     r.Level.String(),
		Msg:       r.Message,
		Attrs:     attrs,
		Raw:       string(buf),
	}
}

func attrValue(a slog.Attr) interface{} {
	switch a.Value.Kind() {
	case slog.KindString:
		return a.Value.String()
	case slog.KindInt64:
		return a.Value.Int64()
	case slog.KindFloat64:
		return a.Value.Float64()
	case slog.KindBool:
		return a.Value.Bool()
	case slog.KindDuration:
		return a.Value.Duration().String()
	case slog.KindTime:
		return a.Value.Time().UTC().Format(time.RFC3339Nano)
	case slog.KindGroup:
		// Flatten one level deep; deeper groups are out of scope
		// for the current search UX.
		g := a.Value.Group()
		out := make(map[string]interface{}, len(g))
		for _, sub := range g {
			out[sub.Key] = attrValue(sub)
		}
		return out
	case slog.KindLogValuer:
		return attrValue(slog.Attr{Key: a.Key, Value: a.Value.Resolve()})
	default:
		// Anything else: stringify so the doc is at least present.
		return a.Value.String()
	}
}

// SearchEnvelope is the public search result struct returned by
// the indexer when the admin handler is invoked.
type SearchEnvelope struct {
	Total  uint64      `json:"total"`
	TookMs int64       `json:"took_ms"`
	Hits   []SearchHit `json:"hits"`
}

// SearchHit is one search result document.
type SearchHit struct {
	Timestamp time.Time              `json:"ts"`
	Level     string                 `json:"level"`
	Msg       string                 `json:"msg"`
	RequestID string                 `json:"request_id,omitempty"`
	TenantID  string                 `json:"tenant_id,omitempty"`
	Attrs     map[string]interface{} `json:"attrs,omitempty"`
	Raw       string                 `json:"raw"`
}

// SearchSpec is the parsed admin query.
type SearchSpec struct {
	Query    string
	TenantID string
	Level    string
	From     *time.Time
	To       *time.Time
	Regex    string
	Fuzzy    bool
	Page     int
	Size     int
}

// Search runs the spec against the live index. It returns an empty
// envelope (not nil) when the indexer is unavailable so the admin
// handler does not need to special-case the disabled state.
func (b *bleveIndexer) Search(spec SearchSpec) (*SearchEnvelope, error) {
	if b == nil {
		return &SearchEnvelope{}, nil
	}
	start := time.Now()

	var topQuery query.Query = query.NewMatchAllQuery()
	if spec.Query != "" {
		if spec.Regex != "" {
			rq := query.NewRegexpQuery(spec.Regex)
			topQuery = query.NewConjunctionQuery([]query.Query{
				query.NewMatchQuery(spec.Query),
				rq,
			})
		} else if spec.Fuzzy {
			topQuery = query.NewFuzzyQuery(spec.Query)
		} else {
			topQuery = query.NewMatchQuery(spec.Query)
		}
	}

	conjuncts := []query.Query{topQuery}
	if spec.TenantID != "" {
		tq := query.NewTermQuery(spec.TenantID)
		tq.SetField("tenant_id")
		conjuncts = append(conjuncts, tq)
	}
	if spec.Level != "" {
		lq := query.NewTermQuery(spec.Level)
		lq.SetField("level")
		conjuncts = append(conjuncts, lq)
	}
	if spec.From != nil || spec.To != nil {
		rng := query.NewDateRangeQuery(*timeOrMin(spec.From), *timeOrMax(spec.To))
		rng.SetField("ts")
		conjuncts = append(conjuncts, rng)
	}
	final := query.NewConjunctionQuery(conjuncts)

	page := spec.Page
	if page < 1 {
		page = 1
	}
	size := spec.Size
	if size <= 0 || size > 200 {
		size = 50
	}
	from := (page - 1) * size

	req := bleve.NewSearchRequestOptions(final, size, from, false)
	req.Sort = search.SortOrder{search.ParseSearchSortString("-ts")}
	req.Fields = []string{"ts", "level", "msg", "request_id", "tenant_id"}

	res, err := b.index.Search(req)
	if err != nil {
		return nil, fmt.Errorf("bleve search: %w", err)
	}

	hits := make([]SearchHit, 0, len(res.Hits))
	for _, h := range res.Hits {
		sh := SearchHit{
			Raw: string(h.ID),
		}
		// h.Fields is populated from the indexed document for the
		// fields named in SearchRequest.Fields. Bleve coerces dates
		// to RFC3339 strings; we re-parse back into time.Time so the
		// admin UI can render them directly.
		if v, ok := h.Fields["ts"].(string); ok && v != "" {
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				sh.Timestamp = t
			} else if t, err := time.Parse(time.RFC3339, v); err == nil {
				sh.Timestamp = t
			}
		}
		if v, ok := h.Fields["level"].(string); ok {
			sh.Level = v
		}
		if v, ok := h.Fields["msg"].(string); ok {
			sh.Msg = v
		}
		if v, ok := h.Fields["request_id"].(string); ok {
			sh.RequestID = v
		}
		if v, ok := h.Fields["tenant_id"].(string); ok {
			sh.TenantID = v
		}
		hits = append(hits, sh)
	}

	return &SearchEnvelope{
		Total:  res.Total,
		TookMs: time.Since(start).Milliseconds(),
		Hits:   hits,
	}, nil
}

func timeOrMin(t *time.Time) *time.Time {
	if t != nil {
		return t
	}
	z := time.Unix(0, 0).UTC()
	return &z
}

func timeOrMax(t *time.Time) *time.Time {
	if t != nil {
		return t
	}
	z := time.Now().Add(100 * 365 * 24 * time.Hour).UTC()
	return &z
}

// silence unused imports in case we trim deps during refactors.
var (
	_ = io.Discard
	_ = debug.Stack
)
