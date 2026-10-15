package requestjourney

import (
	"encoding/json"
	"time"
)

// RequestSnapshot is the content-free list representation of one journey.
type RequestSnapshot struct {
	TenantID          string       `json:"tenant_id,omitempty"`
	GatewayInstanceID string       `json:"gateway_instance_id,omitempty"`
	RequestID         string       `json:"request_id"`
	RequestedModel    string       `json:"requested_model,omitempty"`
	ResolvedModel     string       `json:"resolved_model,omitempty"`
	CurrentStage      JourneyStage `json:"current_stage"`
	LastSeq           int64        `json:"last_seq,omitempty"`
	LastEventType     EventType    `json:"last_event_type,omitempty"`
	Attempt           *AttemptRef  `json:"attempt,omitempty"`
	Outcome           Outcome      `json:"outcome,omitempty"`
	ErrorKind         string       `json:"error_kind,omitempty"`
	HTTPStatus        int          `json:"http_status,omitempty"`
	RetryReason       string       `json:"retry_reason,omitempty"`
	// LifecycleState is the derived request-registry state (v4 R1.1/T2:
	// pending / in_flight / completed). Empty on snapshots built by older
	// producers (additive, backward-compatible JSON field).
	LifecycleState LifecycleState `json:"lifecycle_state,omitempty"`
	// RetryAt is the pending timed-retry deadline; set while the request is
	// parked for a scheduled retry (additive, backward-compatible).
	RetryAt           *time.Time        `json:"retry_at,omitempty"`
	SwitchReason      string            `json:"switch_reason,omitempty"`
	NodeHealthStatus  NodeHealthStatus  `json:"node_health_status,omitempty"`
	ObservationStatus ObservationStatus `json:"observation_status,omitempty"`
	StartedAt         time.Time         `json:"started_at,omitempty"`
	UpdatedAt         time.Time         `json:"updated_at"`
	CompletedAt       *time.Time        `json:"completed_at,omitempty"`
}

// TotalRequestFIFOSnapshot is the bounded, cross-model recent request view.
type TotalRequestFIFOSnapshot struct {
	Capacity int               `json:"capacity"`
	Requests []RequestSnapshot `json:"requests"`
}

// LifecycleCounts tallies the snapshot by lifecycle state.
//
// ⚠️ 口径（别把它当全局队列深度）：这是**最近 capacity 条请求**窗口里的
// 分状态计数，不是网关上全部在途请求的总量。窗口外的在途请求不在其中。
// 移动端首页「实时请求队列」展示的三个数就是它。
func (s *TotalRequestFIFOSnapshot) LifecycleCounts() (pending, inFlight, completed int) {
	if s == nil {
		return 0, 0, 0
	}
	for _, request := range s.Requests {
		switch request.LifecycleState {
		case LifecyclePending:
			pending++
		case LifecycleInFlight:
			inFlight++
		case LifecycleCompleted:
			completed++
		}
	}
	return pending, inFlight, completed
}

// MarshalJSON adds the per-lifecycle tally to the wire shape.
//
// ⚠️ 为什么在**序列化时推导**、而不是存三个字段让 Append 维护
//　（2026-10-13 真机发现，这是个已上线的缺陷）：
//
//　移动端首页 `HomeView.vue:61` 读 `total_snapshot.pending/in_flight/completed`，
//　而本结构体**从来没有**这三个字段 ⇒ 线上接口只返回
//　`{capacity, requests}` ⇒ 前端拿到三个 `undefined` ⇒ 「实时请求队列」
//　在真机上**永远显示三个「—」**，无论队列里有没有东西。
//　（前端类型把这三项写成可选，等于把这个缺口藏了起来。）
//
//　推导式还顺手消掉一整类漂移：`admin/request_journey.go:49` 与 `:194`
//　都是**字面量构造**本结构体的，存字段的话那两处会静默停在 0 ——
//　正是这个 bug 本身。
func (s TotalRequestFIFOSnapshot) MarshalJSON() ([]byte, error) {
	type plain TotalRequestFIFOSnapshot
	pending, inFlight, completed := s.LifecycleCounts()
	return json.Marshal(struct {
		plain
		Pending   int `json:"pending"`
		InFlight  int `json:"in_flight"`
		Completed int `json:"completed"`
	}{plain(s), pending, inFlight, completed})
}

func NewTotalRequestFIFOSnapshot(capacity int) *TotalRequestFIFOSnapshot {
	capacity = positiveOrDefault(capacity, DefaultTotalRequestCapacity)
	return &TotalRequestFIFOSnapshot{Capacity: capacity, Requests: make([]RequestSnapshot, 0, capacity)}
}

func (s *TotalRequestFIFOSnapshot) Append(snapshot RequestSnapshot) {
	if s == nil {
		return
	}
	s.Capacity = positiveOrDefault(s.Capacity, DefaultTotalRequestCapacity)
	s.Requests = appendBounded(s.Requests, snapshot, s.Capacity)
}

// ModelFIFOSnapshot is the bounded recent request view for one model.
type ModelFIFOSnapshot struct {
	Model    string            `json:"model"`
	Capacity int               `json:"capacity"`
	Requests []RequestSnapshot `json:"requests"`
}

func NewModelFIFOSnapshot(model string, capacity int) *ModelFIFOSnapshot {
	capacity = positiveOrDefault(capacity, DefaultPerModelCapacity)
	return &ModelFIFOSnapshot{Model: model, Capacity: capacity, Requests: make([]RequestSnapshot, 0, capacity)}
}

func (s *ModelFIFOSnapshot) Append(snapshot RequestSnapshot) {
	if s == nil {
		return
	}
	s.Capacity = positiveOrDefault(s.Capacity, DefaultPerModelCapacity)
	s.Requests = appendBounded(s.Requests, snapshot, s.Capacity)
}

// NodeFIFOSnapshot is the bounded recent request view for one
// (model, provider, credential) routing node.
type NodeFIFOSnapshot struct {
	Model        string            `json:"model"`
	ProviderID   int64             `json:"provider_id,omitempty"`
	CredentialID int64             `json:"credential_id"`
	Capacity     int               `json:"capacity"`
	Requests     []RequestSnapshot `json:"requests"`
}

func NewNodeFIFOSnapshot(model string, providerID, credentialID int64, capacity int) *NodeFIFOSnapshot {
	capacity = positiveOrDefault(capacity, DefaultPerNodeCapacity)
	return &NodeFIFOSnapshot{
		Model: model, ProviderID: providerID, CredentialID: credentialID,
		Capacity: capacity, Requests: make([]RequestSnapshot, 0, capacity),
	}
}

func (s *NodeFIFOSnapshot) Append(snapshot RequestSnapshot) {
	if s == nil {
		return
	}
	s.Capacity = positiveOrDefault(s.Capacity, DefaultPerNodeCapacity)
	s.Requests = appendBounded(s.Requests, snapshot, s.Capacity)
}

func appendBounded(requests []RequestSnapshot, snapshot RequestSnapshot, capacity int) []RequestSnapshot {
	if len(requests) >= capacity {
		drop := len(requests) - capacity + 1
		copy(requests, requests[drop:])
		requests = requests[:len(requests)-drop]
	}
	return append(requests, snapshot)
}
