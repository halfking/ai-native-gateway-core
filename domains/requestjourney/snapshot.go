package requestjourney

import "time"

// RequestSnapshot is the content-free list representation of one journey.
type RequestSnapshot struct {
	TenantID          string            `json:"tenant_id,omitempty"`
	GatewayInstanceID string            `json:"gateway_instance_id,omitempty"`
	RequestID         string            `json:"request_id"`
	RequestedModel    string            `json:"requested_model,omitempty"`
	ResolvedModel     string            `json:"resolved_model,omitempty"`
	CurrentStage      JourneyStage      `json:"current_stage"`
	LastSeq           int64             `json:"last_seq,omitempty"`
	LastEventType     EventType         `json:"last_event_type,omitempty"`
	Attempt           *AttemptRef       `json:"attempt,omitempty"`
	Outcome           Outcome           `json:"outcome,omitempty"`
	ErrorKind         string            `json:"error_kind,omitempty"`
	HTTPStatus        int               `json:"http_status,omitempty"`
	RetryReason       string            `json:"retry_reason,omitempty"`
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
