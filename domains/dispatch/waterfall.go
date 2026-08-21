package dispatch

import (
	"sync"
	"time"
)

// waterfallRingCap is the in-memory ring capacity for recent completed
// requests used by GET /api/admin/dispatch/waterfall.
const waterfallRingCap = 200

// WaterfallRequest is one completed request's 9-stage timeline for the
// admin waterfall UI. Timestamps are RFC3339Nano when present.
type WaterfallRequest struct {
	RequestID  string             `json:"request_id"`
	TenantID   string             `json:"tenant_id,omitempty"`
	SessionID  string             `json:"session_id,omitempty"`
	Model      string             `json:"model,omitempty"`
	Credential int                `json:"credential_id,omitempty"`
	Result     string             `json:"result"`
	Vendor     string             `json:"vendor,omitempty"`
	Attempts   []WaterfallAttempt `json:"attempts,omitempty"`

	ArrivedAt       string `json:"arrived_at,omitempty"`
	TotalEnqueuedAt string `json:"total_enqueued_at,omitempty"`
	TotalDequeuedAt string `json:"total_dequeued_at,omitempty"`
	ModelEnqueuedAt string `json:"model_enqueued_at,omitempty"`
	ModelDequeuedAt string `json:"model_dequeued_at,omitempty"`
	CredEnqueuedAt  string `json:"cred_enqueued_at,omitempty"`
	CredDequeuedAt  string `json:"cred_dequeued_at,omitempty"`
	ForwardStartAt  string `json:"forward_start_at,omitempty"`
	ResponseStartAt string `json:"response_start_at,omitempty"`
	ResponseEndAt   string `json:"response_end_at,omitempty"`

	WaitingInTotalMS    int `json:"waiting_in_total_ms"`
	WaitingInModelMS    int `json:"waiting_in_model_ms"`
	WaitingInNodeMS     int `json:"waiting_in_node_ms"`
	RoutingMS           int `json:"routing_ms"`
	AcquireMS           int `json:"acquire_ms"`
	UpstreamLatencyMS   int `json:"upstream_latency_ms"`
	StreamingDurationMS int `json:"streaming_duration_ms"`
	QueueWaitMS         int `json:"queue_wait_ms"`
	TotalMS             int `json:"total_ms"`
}

// WaterfallAttempt is one immutable upstream attempt in a request waterfall.
// The request-level fields above remain for API compatibility.
type WaterfallAttempt struct {
	AttemptID    string `json:"attempt_id"`
	AttemptNo    int    `json:"attempt_no"`
	Model        string `json:"model,omitempty"`
	ProviderID   int64  `json:"provider_id,omitempty"`
	CredentialID int64  `json:"credential_id"`
	Vendor       string `json:"vendor,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	FirstByteAt  string `json:"first_byte_at,omitempty"`
	EndedAt      string `json:"ended_at,omitempty"`
	Outcome      string `json:"outcome,omitempty"`
	ErrorKind    string `json:"error_kind,omitempty"`
}

// WaterfallSnapshot is the admin waterfall API response body.
type WaterfallSnapshot struct {
	Requests            []WaterfallRequest  `json:"requests"`
	TimeRange           *WaterfallTimeRange `json:"time_range,omitempty"`
	BottleneckDiagnosis BottleneckDiagnosis `json:"bottleneck_diagnosis"`
	Enabled             bool                `json:"enabled"`
	Wired               bool                `json:"wired"`
	// Source describes where Requests came from for the admin UI.
	// memory | memory+db | db | none
	Source string `json:"source,omitempty"`
}

// WaterfallTimeRange bounds the returned request sample.
type WaterfallTimeRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// BottleneckDiagnosis is a coarse queue-congestion root-cause hint.
type BottleneckDiagnosis struct {
	Bottleneck string `json:"bottleneck"` // none|routing|model_capacity|node_concurrency
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

// waterfallRing is a fixed-capacity circular buffer of recent completions.
type waterfallRing struct {
	mu   sync.Mutex
	buf  []WaterfallRequest
	next int
	full bool
}

func newWaterfallRing(cap int) *waterfallRing {
	if cap <= 0 {
		cap = waterfallRingCap
	}
	return &waterfallRing{buf: make([]WaterfallRequest, cap)}
}

func (r *waterfallRing) push(item WaterfallRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = item
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// snapshot returns the newest-first slice, optionally filtered.
// tenantID empty means all tenants (platform ops); non-empty enforces isolation.
func (r *waterfallRing) snapshot(limit int, model string, credentialID int, tenantID string) []WaterfallRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := r.next
	if r.full {
		n = len(r.buf)
	}
	if n == 0 {
		return []WaterfallRequest{}
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	out := make([]WaterfallRequest, 0, min(limit, n))
	// Walk newest → oldest.
	for i := 0; i < n && len(out) < limit; i++ {
		idx := r.next - 1 - i
		if idx < 0 {
			idx += len(r.buf)
		}
		item := cloneWaterfallRequest(r.buf[idx])
		if tenantID != "" && item.TenantID != tenantID {
			continue
		}
		if model != "" && item.Model != model {
			continue
		}
		if credentialID > 0 && item.Credential != credentialID {
			continue
		}
		out = append(out, item)
	}
	return out
}

// Pipeline embeds a ring; allocated lazily so tests without Start still work.
func (p *Pipeline) ensureWaterfallRing() *waterfallRing {
	p.waterfallOnce.Do(func() {
		p.waterfall = newWaterfallRing(waterfallRingCap)
	})
	return p.waterfall
}

// recordWaterfall appends a completed request timeline into the ring.
func (p *Pipeline) recordWaterfall(qr *QueuedRequest, out ForwardOutcome) {
	if p == nil || qr == nil {
		return
	}
	ring := p.ensureWaterfallRing()
	item := buildWaterfallRequest(qr, out)
	ring.push(item)
}

// SnapshotWaterfall returns recent timelines + live bottleneck diagnosis.
// tenantID empty = all tenants (platform ops).
func (p *Pipeline) SnapshotWaterfall(limit int, model string, credentialID int, tenantID string) WaterfallSnapshot {
	snap := WaterfallSnapshot{
		Requests: []WaterfallRequest{},
		Enabled:  true, // AUDIT_24H B2b: dispatch is the only path
		Wired:    p != nil,
		BottleneckDiagnosis: BottleneckDiagnosis{
			Bottleneck: "none",
			Message:    "队列正常",
		},
	}
	if p == nil {
		snap.Source = "none"
		return snap
	}
	ring := p.ensureWaterfallRing()
	reqs := ring.snapshot(limit, model, credentialID, tenantID)
	snap.Requests = reqs
	if len(reqs) > 0 {
		snap.Source = "memory"
		// requests are newest-first; time range is oldest→newest among sample.
		end := reqs[0].ResponseEndAt
		if end == "" {
			end = reqs[0].ArrivedAt
		}
		start := reqs[len(reqs)-1].ArrivedAt
		snap.TimeRange = &WaterfallTimeRange{Start: start, End: end}
	} else {
		snap.Source = "none"
	}
	models, creds := p.Snapshot()
	snap.BottleneckDiagnosis = diagnoseBottleneck(models, creds)
	return snap
}

func buildWaterfallRequest(qr *QueuedRequest, out ForwardOutcome) WaterfallRequest {
	item := WaterfallRequest{
		RequestID:  qr.ID,
		TenantID:   qr.TenantID,
		SessionID:  qr.SessionID,
		Model:      firstNonEmpty(qr.ResolvedModel, qr.RequestedModel),
		Credential: qr.SelectedCred.CredentialID,
		Result:     resultLabel(out),
		Vendor:     firstNonEmpty(qr.vendor, qr.SelectedCred.Vendor),
		Attempts:   qr.waterfallAttempts(),
		ArrivedAt:  formatTS(qr.T0_ArrivedAt),
	}
	item.TotalEnqueuedAt = formatTSPtr(qr.T1_TotalEnqueuedAt)
	item.TotalDequeuedAt = formatTSPtr(qr.T2_TotalDequeuedAt)
	item.ModelEnqueuedAt = formatTSPtr(qr.T3_ModelEnqueuedAt)
	item.ModelDequeuedAt = formatTSPtr(qr.T4_ModelDequeuedAt)
	item.CredEnqueuedAt = formatTSPtr(qr.T5_CredEnqueuedAt)
	item.CredDequeuedAt = formatTSPtr(qr.T6_CredDequeuedAt)
	item.ForwardStartAt = formatTSPtr(qr.T7_ForwardStartAt)
	item.ResponseStartAt = formatTSPtr(qr.T8_ResponseStartAt)
	item.ResponseEndAt = formatTSPtr(qr.T9_ResponseEndAt)

	item.WaitingInTotalMS = durationMS(qr.T1_TotalEnqueuedAt, qr.T2_TotalDequeuedAt)
	item.WaitingInModelMS = durationMS(qr.T3_ModelEnqueuedAt, qr.T4_ModelDequeuedAt)
	item.WaitingInNodeMS = durationMS(qr.T5_CredEnqueuedAt, qr.T6_CredDequeuedAt)
	item.RoutingMS = durationMS(qr.T2_TotalDequeuedAt, qr.T5_CredEnqueuedAt)
	item.AcquireMS = durationMS(qr.T6_CredDequeuedAt, qr.T7_ForwardStartAt)
	item.UpstreamLatencyMS = durationMS(qr.T7_ForwardStartAt, qr.T8_ResponseStartAt)
	item.StreamingDurationMS = durationMS(qr.T8_ResponseStartAt, qr.T9_ResponseEndAt)
	if s, ok := stageSecondsFrom(qr.T0_ArrivedAt, qr.T6_CredDequeuedAt); ok {
		item.QueueWaitMS = int(s * 1000)
	}
	if s, ok := stageSecondsFrom(qr.T0_ArrivedAt, qr.T9_ResponseEndAt); ok {
		item.TotalMS = int(s * 1000)
	}
	return item
}

func diagnoseBottleneck(models, creds []QueueSnapshot) BottleneckDiagnosis {
	var totalDepth int64
	var maxModelDepth int64
	var maxModel string
	for _, m := range models {
		totalDepth += m.Depth
		if m.Depth > maxModelDepth {
			maxModelDepth = m.Depth
			maxModel = m.Model
		}
	}
	var maxCredDepth int64
	var maxCred int
	for _, c := range creds {
		if c.Depth > maxCredDepth {
			maxCredDepth = c.Depth
			maxCred = c.Credential
		}
	}

	if totalDepth > 50 && maxModelDepth < 10 {
		return BottleneckDiagnosis{
			Bottleneck: "routing",
			Message:    "路由决策慢，总队列积压但模型队列较浅",
			Suggestion: "检查 ModelResolve / Route 耗时与 dispatcher worker 数",
		}
	}
	if maxModelDepth > 30 {
		return BottleneckDiagnosis{
			Bottleneck: "model_capacity",
			Message:    "模型 " + maxModel + " 队列积压",
			Suggestion: "增加该模型的可用凭据/节点，或限流该模型入口",
		}
	}
	if maxCredDepth > 20 {
		return BottleneckDiagnosis{
			Bottleneck: "node_concurrency",
			Message:    "节点并发不足 (credential_id=" + itoa(maxCred) + ")",
			Suggestion: "提高 credential 并发上限或扩容同模型节点",
		}
	}
	return BottleneckDiagnosis{Bottleneck: "none", Message: "队列正常"}
}

func formatTS(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func formatTSPtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func durationMS(start, end *time.Time) int {
	s, ok := stageSeconds(start, end)
	if !ok {
		return 0
	}
	return int(s * 1000)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
