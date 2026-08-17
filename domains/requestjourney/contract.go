// Package requestjourney defines the shared, content-free contract for request
// lifecycle observation. It intentionally has no dependency on request routing,
// persistence, or transport packages.
package requestjourney

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// EventType identifies a lifecycle event. These values are persisted and form a
// shared contract between future producers, stores, and APIs.
type EventType string

const (
	EventRequestReceived     EventType = "request_received"
	EventRouteResolved       EventType = "route_resolved"
	EventModelEnqueued       EventType = "model_enqueued"
	EventCredentialSelected  EventType = "credential_selected"
	EventNodeEnqueued        EventType = "node_enqueued"
	EventNodeSelected        EventType = "node_selected"
	EventAttemptStarted      EventType = "attempt_started"
	EventFirstByte           EventType = "first_byte"
	EventAttemptSucceeded    EventType = "attempt_succeeded"
	EventAttemptFailed       EventType = "attempt_failed"
	EventRetryScheduled      EventType = "retry_scheduled"
	EventNodeSwitched        EventType = "node_switched"
	EventModelSwitched       EventType = "model_switched"
	EventRequestSucceeded    EventType = "request_succeeded"
	EventRequestFailed       EventType = "request_failed"
	EventRequestCanceled     EventType = "request_canceled"
	EventObservationDegraded EventType = "observation_degraded"
)

var eventTypes = []EventType{
	EventRequestReceived,
	EventRouteResolved,
	EventModelEnqueued,
	EventCredentialSelected,
	EventNodeEnqueued,
	EventNodeSelected,
	EventAttemptStarted,
	EventFirstByte,
	EventAttemptSucceeded,
	EventAttemptFailed,
	EventRetryScheduled,
	EventNodeSwitched,
	EventModelSwitched,
	EventRequestSucceeded,
	EventRequestFailed,
	EventRequestCanceled,
	EventObservationDegraded,
}

// AllEventTypes returns a copy of the frozen event type set.
func AllEventTypes() []EventType {
	return append([]EventType(nil), eventTypes...)
}

// Valid reports whether the event type is part of the shared contract.
func (t EventType) Valid() bool {
	for _, candidate := range eventTypes {
		if t == candidate {
			return true
		}
	}
	return false
}

// JourneyStage is the closed, display-oriented request lifecycle stage. It is
// intentionally coarser than EventType so snapshots remain stable as events are
// added within a stage.
type JourneyStage string

const (
	StageReceived        JourneyStage = "received"
	StageRouting         JourneyStage = "routing"
	StageModelQueue      JourneyStage = "model_queue"
	StageCredentialQueue JourneyStage = "credential_queue"
	StageNodeSelection   JourneyStage = "node_selection"
	StageUpstream        JourneyStage = "upstream"
	StageStreaming       JourneyStage = "streaming"
	StageRetrying        JourneyStage = "retrying"
	StageTerminal        JourneyStage = "terminal"
)

var journeyStages = []JourneyStage{
	StageReceived,
	StageRouting,
	StageModelQueue,
	StageCredentialQueue,
	StageNodeSelection,
	StageUpstream,
	StageStreaming,
	StageRetrying,
	StageTerminal,
}

// AllJourneyStages returns a copy of the frozen journey stage set.
func AllJourneyStages() []JourneyStage {
	return append([]JourneyStage(nil), journeyStages...)
}

func (s JourneyStage) Valid() bool {
	for _, candidate := range journeyStages {
		if s == candidate {
			return true
		}
	}
	return false
}

// ObservationStatus states whether the recorded journey is known to be
// complete. Degraded observations remain usable but must not be presented as a
// complete account of the request.
type ObservationStatus string

const (
	ObservationComplete ObservationStatus = "complete"
	ObservationDegraded ObservationStatus = "observation_degraded"
)

func (s ObservationStatus) Valid() bool {
	return s == ObservationComplete || s == ObservationDegraded
}

// IngressProtocol classifies the public HTTP protocol that reached the gateway.
// It deliberately excludes request content and authentication identity.
type IngressProtocol string

const (
	IngressProtocolChat      IngressProtocol = "chat"
	IngressProtocolMessages  IngressProtocol = "messages"
	IngressProtocolResponses IngressProtocol = "responses"
	IngressProtocolGemini    IngressProtocol = "gemini"
)

func (p IngressProtocol) Valid() bool {
	return p == IngressProtocolChat || p == IngressProtocolMessages || p == IngressProtocolResponses || p == IngressProtocolGemini
}

// IngressPathClass is a bounded endpoint classification, not the raw URL path.
type IngressPathClass string

const (
	IngressPathChatCompletions IngressPathClass = "chat_completions"
	IngressPathMessages        IngressPathClass = "messages"
	IngressPathResponses       IngressPathClass = "responses"
	IngressPathGeminiModels    IngressPathClass = "gemini_models"
)

func (p IngressPathClass) Valid() bool {
	return p == IngressPathChatCompletions || p == IngressPathMessages || p == IngressPathResponses || p == IngressPathGeminiModels
}

// IngressStatus is the content-free lifecycle state of an HTTP arrival.
type IngressStatus string

const (
	IngressStatusArrived   IngressStatus = "arrived"
	IngressStatusSucceeded IngressStatus = "succeeded"
	IngressStatusFailed    IngressStatus = "failed"
	IngressStatusCanceled  IngressStatus = "canceled"
)

func (s IngressStatus) Valid() bool {
	return s == IngressStatusArrived || s == IngressStatusSucceeded || s == IngressStatusFailed || s == IngressStatusCanceled
}

// IngressEvent records only transport-level arrival and terminal information.
// It must never acquire tenant identity, credentials, headers, bodies, or raw
// authentication keys.
type IngressEvent struct {
	RequestID         string           `json:"request_id"`
	GatewayInstanceID string           `json:"gateway_instance_id"`
	Protocol          IngressProtocol  `json:"protocol"`
	PathClass         IngressPathClass `json:"path_class"`
	ArrivedAt         time.Time        `json:"arrived_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
	Status            IngressStatus    `json:"status"`
	ErrorKind         string           `json:"error_kind,omitempty"`
	HTTPStatus        int              `json:"http_status,omitempty"`
}

func (e IngressEvent) Validate() error {
	if e.RequestID == "" || e.GatewayInstanceID == "" {
		return errors.New("request_id and gateway_instance_id are required")
	}
	if !e.Protocol.Valid() || !e.PathClass.Valid() {
		return errors.New("valid protocol and path_class are required")
	}
	if e.ArrivedAt.IsZero() || e.UpdatedAt.IsZero() || e.UpdatedAt.Before(e.ArrivedAt) {
		return errors.New("valid arrived_at and updated_at are required")
	}
	if !e.Status.Valid() {
		return fmt.Errorf("invalid ingress status %q", e.Status)
	}
	if e.HTTPStatus < 0 || e.HTTPStatus > 599 || (e.HTTPStatus > 0 && e.HTTPStatus < 100) {
		return fmt.Errorf("invalid http_status %d", e.HTTPStatus)
	}
	return nil
}

// IngressSnapshot is the global FIFO representation. It is intentionally an
// alias so persistence and API code cannot add tenant-only fields to the view.
type IngressSnapshot = IngressEvent

// NodeHealthStatus is the normalized health state observed for one routing
// node. Unknown is explicit so absence of evidence is not interpreted as
// healthy.
type NodeHealthStatus string

const (
	NodeHealthUnknown     NodeHealthStatus = "unknown"
	NodeHealthHealthy     NodeHealthStatus = "healthy"
	NodeHealthSuspect     NodeHealthStatus = "suspect"
	NodeHealthDegraded    NodeHealthStatus = "degraded"
	NodeHealthCooling     NodeHealthStatus = "cooling"
	NodeHealthProbing     NodeHealthStatus = "probing"
	NodeHealthRecovering  NodeHealthStatus = "recovering"
	NodeHealthQuarantined NodeHealthStatus = "quarantined"
	NodeHealthDisabled    NodeHealthStatus = "disabled"
	// NodeHealthUnhealthy is retained only as a compatibility classification
	// for producers that cannot yet distinguish degraded from quarantined.
	NodeHealthUnhealthy NodeHealthStatus = "unhealthy"
)

var nodeHealthStatuses = []NodeHealthStatus{
	NodeHealthUnknown,
	NodeHealthHealthy,
	NodeHealthSuspect,
	NodeHealthDegraded,
	NodeHealthCooling,
	NodeHealthProbing,
	NodeHealthRecovering,
	NodeHealthQuarantined,
	NodeHealthDisabled,
	NodeHealthUnhealthy,
}

// AllNodeHealthStatuses returns a copy of the frozen node health state set.
func AllNodeHealthStatuses() []NodeHealthStatus {
	return append([]NodeHealthStatus(nil), nodeHealthStatuses...)
}

func (s NodeHealthStatus) Valid() bool {
	for _, candidate := range nodeHealthStatuses {
		if s == candidate {
			return true
		}
	}
	return false
}

// Outcome is the normalized terminal result for an attempt or request.
type Outcome string

const (
	OutcomeSuccess  Outcome = "success"
	OutcomeFailure  Outcome = "failure"
	OutcomeCanceled Outcome = "canceled"
)

// Valid reports whether the outcome follows the project's persisted/API
// vocabulary. The empty value means no terminal outcome has been observed yet.
func (o Outcome) Valid() bool {
	return o == "" || o == OutcomeSuccess || o == OutcomeFailure || o == OutcomeCanceled
}

// AttemptRef identifies one execution attempt without carrying request or
// response content, credentials, headers, or secrets.
type AttemptRef struct {
	AttemptID    string `json:"attempt_id"`
	AttemptNo    int    `json:"attempt_no"`
	Model        string `json:"model,omitempty"`
	ProviderID   int64  `json:"provider_id,omitempty"`
	Provider     string `json:"provider,omitempty"`
	CredentialID int64  `json:"credential_id,omitempty"`
}

func (a AttemptRef) validate() error {
	if a.AttemptID == "" {
		return errors.New("attempt_id is required")
	}
	if a.AttemptNo <= 0 {
		return errors.New("attempt_no must be positive")
	}
	if a.ProviderID <= 0 || a.CredentialID <= 0 {
		return errors.New("provider_id and credential_id must be positive actual IDs")
	}
	return nil
}

// JourneyEvent is the append-only lifecycle record shared by observation
// producers and consumers. It deliberately has no arbitrary metadata or body
// field; only bounded diagnostic classifications and identifiers are allowed.
type JourneyEvent struct {
	TenantID          string            `json:"tenant_id"`
	GatewayInstanceID string            `json:"gateway_instance_id"`
	RequestID         string            `json:"request_id"`
	Seq               int64             `json:"seq"`
	Type              EventType         `json:"event_type"`
	Stage             JourneyStage      `json:"stage"`
	RequestedModel    string            `json:"requested_model,omitempty"`
	ResolvedModel     string            `json:"resolved_model,omitempty"`
	Model             string            `json:"model,omitempty"`
	ProviderID        int64             `json:"provider_id,omitempty"`
	Provider          string            `json:"provider,omitempty"`
	CredentialID      int64             `json:"credential_id,omitempty"`
	FromModel         string            `json:"from_model,omitempty"`
	ToModel           string            `json:"to_model,omitempty"`
	FromCredentialID  int64             `json:"from_credential_id,omitempty"`
	ToCredentialID    int64             `json:"to_credential_id,omitempty"`
	Attempt           *AttemptRef       `json:"attempt,omitempty"`
	Outcome           Outcome           `json:"outcome,omitempty"`
	ErrorKind         string            `json:"error_kind,omitempty"`
	HTTPStatus        int               `json:"http_status,omitempty"`
	RetryReason       string            `json:"retry_reason,omitempty"`
	SwitchReason      string            `json:"switch_reason,omitempty"`
	NodeHealthStatus  NodeHealthStatus  `json:"node_health_status,omitempty"`
	ObservationStatus ObservationStatus `json:"observation_status"`
	OccurredAt        time.Time         `json:"occurred_at"`
}

// Validate enforces the closed vocabulary and event-specific identity fields.
func (e JourneyEvent) Validate() error {
	if e.TenantID == "" || e.GatewayInstanceID == "" || e.RequestID == "" {
		return errors.New("tenant_id, gateway_instance_id, and request_id are required")
	}
	if e.Seq <= 0 {
		return errors.New("seq must be positive")
	}
	if !e.Type.Valid() {
		return fmt.Errorf("invalid event_type %q", e.Type)
	}
	if !e.Stage.Valid() {
		return fmt.Errorf("invalid stage %q", e.Stage)
	}
	for name, id := range map[string]int64{
		"provider_id": e.ProviderID, "credential_id": e.CredentialID,
		"from_credential_id": e.FromCredentialID, "to_credential_id": e.ToCredentialID,
	} {
		if id < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	if e.Attempt != nil {
		if err := e.Attempt.validate(); err != nil {
			return fmt.Errorf("invalid attempt: %w", err)
		}
	}
	if !e.Outcome.Valid() {
		return fmt.Errorf("invalid outcome %q", e.Outcome)
	}
	if e.HTTPStatus < 0 || e.HTTPStatus > 599 || (e.HTTPStatus > 0 && e.HTTPStatus < 100) {
		return fmt.Errorf("invalid http_status %d", e.HTTPStatus)
	}
	if e.NodeHealthStatus != "" && !e.NodeHealthStatus.Valid() {
		return fmt.Errorf("invalid node_health_status %q", e.NodeHealthStatus)
	}
	if !e.ObservationStatus.Valid() {
		return fmt.Errorf("invalid observation_status %q", e.ObservationStatus)
	}
	switch e.Type {
	case EventCredentialSelected, EventNodeEnqueued, EventNodeSelected:
		if e.CredentialID <= 0 {
			return fmt.Errorf("%s requires a positive credential_id", e.Type)
		}
	case EventNodeSwitched:
		if e.FromCredentialID <= 0 || e.ToCredentialID <= 0 {
			return errors.New("node_switched requires positive from_credential_id and to_credential_id")
		}
	case EventModelSwitched:
		if e.FromModel == "" || e.ToModel == "" {
			return errors.New("model_switched requires from_model and to_model")
		}
	case EventRequestCanceled:
		if e.Outcome != OutcomeCanceled {
			return errors.New("request_canceled requires canceled outcome")
		}
	case EventObservationDegraded:
		if e.ObservationStatus != ObservationDegraded {
			return errors.New("observation_degraded event requires degraded observation status")
		}
	}
	if e.OccurredAt.IsZero() {
		return errors.New("occurred_at is required")
	}
	return nil
}

// RequestJourney is a tenant-scoped, ordered view of one request lifecycle.
type RequestJourney struct {
	TenantID          string            `json:"tenant_id"`
	GatewayInstanceID string            `json:"gateway_instance_id"`
	RequestID         string            `json:"request_id"`
	ObservationStatus ObservationStatus `json:"observation_status"`
	StartedAt         time.Time         `json:"started_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	Events            []JourneyEvent    `json:"events"`
}

// Validate enforces identity consistency and a strictly increasing, stable
// sequence within the tenant/request journey.
func (j RequestJourney) Validate() error {
	if j.TenantID == "" || j.GatewayInstanceID == "" || j.RequestID == "" {
		return errors.New("tenant_id, gateway_instance_id, and request_id are required")
	}
	if !j.ObservationStatus.Valid() {
		return fmt.Errorf("invalid observation_status %q", j.ObservationStatus)
	}
	if j.StartedAt.IsZero() || j.UpdatedAt.IsZero() {
		return errors.New("started_at and updated_at are required")
	}
	if j.UpdatedAt.Before(j.StartedAt) {
		return errors.New("updated_at must not precede started_at")
	}

	var previousSeq int64
	for i, event := range j.Events {
		if err := event.Validate(); err != nil {
			return fmt.Errorf("event %d: %w", i, err)
		}
		if event.TenantID != j.TenantID || event.GatewayInstanceID != j.GatewayInstanceID || event.RequestID != j.RequestID {
			return fmt.Errorf("event %d identity does not match journey", i)
		}
		if event.Seq <= previousSeq {
			return fmt.Errorf("event %d seq %d is not strictly increasing", i, event.Seq)
		}
		previousSeq = event.Seq
	}
	return nil
}

// MarshalRequestJourney validates and serializes the content-free contract.
func MarshalRequestJourney(journey RequestJourney) ([]byte, error) {
	if err := journey.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(journey)
}

// UnmarshalRequestJourney rejects unknown fields so future producers cannot
// silently introduce request bodies, headers, secrets, or unreviewed metadata.
func UnmarshalRequestJourney(body []byte) (*RequestJourney, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var journey RequestJourney
	if err := decoder.Decode(&journey); err != nil {
		return nil, fmt.Errorf("decode request journey: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if err := journey.Validate(); err != nil {
		return nil, err
	}
	return &journey, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("decode request journey: trailing JSON value")
		}
		return fmt.Errorf("decode request journey: %w", err)
	}
	return nil
}
