package requestjourney

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrJourneyNotFound  = errors.New("request journey not found")
	ErrSequenceConflict = errors.New("request journey sequence conflict")
)

// NodeKey identifies a model/provider/credential routing node.
type NodeKey struct {
	Model        string
	ProviderID   int64
	CredentialID int64
}

type orderedSnapshots struct {
	capacity int
	order    []string
	items    map[string]RequestSnapshot
}

type orderedIngressSnapshots struct {
	capacity int
	order    []string
	items    map[string]IngressSnapshot
}

type tenantProjection struct {
	details map[string]*RequestJourney
	total   *orderedSnapshots
	models  map[string]*orderedSnapshots
	nodes   map[NodeKey]*orderedSnapshots
}

// Projection is a tenant-isolated, concurrency-safe in-memory projection.
type Projection struct {
	mu              sync.RWMutex
	config          Config
	ingress         *orderedIngressSnapshots
	ingressDegraded atomic.Bool
	tenants         map[string]*tenantProjection
}

func NewProjection(config Config) *Projection {
	defaults := DefaultConfig()
	config.TotalRequestCapacity = positiveOrDefault(config.TotalRequestCapacity, defaults.TotalRequestCapacity)
	config.PerModelCapacity = positiveOrDefault(config.PerModelCapacity, defaults.PerModelCapacity)
	config.PerNodeCapacity = positiveOrDefault(config.PerNodeCapacity, defaults.PerNodeCapacity)
	if config.DetailTTL <= 0 {
		config.DetailTTL = defaults.DetailTTL
	}
	return &Projection{
		config:  config,
		ingress: newOrderedIngressSnapshots(config.TotalRequestCapacity),
		tenants: make(map[string]*tenantProjection),
	}
}

// ApplyIngress inserts an arrival once and updates later states in place without
// changing the global arrival order.
func (p *Projection) ApplyIngress(event IngressEvent) error {
	if p == nil {
		return errors.New("request journey projection is nil")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key := ingressIdentity(event.GatewayInstanceID, event.RequestID)
	if existing, ok := p.ingress.items[key]; ok {
		if existing.ArrivedAt != event.ArrivedAt || existing.Protocol != event.Protocol || existing.PathClass != event.PathClass {
			return fmt.Errorf("%w: ingress identity changed", ErrSequenceConflict)
		}
		if event.UpdatedAt.Before(existing.UpdatedAt) {
			return nil
		}
		p.ingress.items[key] = event
		return nil
	}
	p.ingress.upsert(key, event)
	return nil
}

// RecentIngress returns the instance-local global FIFO in arrival order.
func (p *Projection) MarkIngressDegraded() {
	if p != nil {
		p.ingressDegraded.Store(true)
	}
}

func (p *Projection) IngressDegraded() bool {
	return p != nil && p.ingressDegraded.Load()
}

func (p *Projection) RecentIngress() []IngressSnapshot {
	if p == nil {
		return []IngressSnapshot{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ingress.snapshots()
}

// Apply appends an event by sequence. Replaying the same event is a no-op;
// reusing a sequence for different data is rejected.
func (p *Projection) Apply(event JourneyEvent) error {
	if p == nil {
		return errors.New("request journey projection is nil")
	}
	if err := event.Validate(); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	tenant := p.tenant(event.TenantID)
	journey := tenant.details[event.RequestID]
	if journey == nil {
		journey = &RequestJourney{
			TenantID:          event.TenantID,
			GatewayInstanceID: event.GatewayInstanceID,
			RequestID:         event.RequestID,
			ObservationStatus: event.ObservationStatus,
			StartedAt:         event.OccurredAt,
			UpdatedAt:         event.OccurredAt,
		}
		tenant.details[event.RequestID] = journey
	} else {
		if journey.GatewayInstanceID != event.GatewayInstanceID {
			return fmt.Errorf("%w: gateway_instance_id changed", ErrSequenceConflict)
		}
		for _, existing := range journey.Events {
			if existing.Seq != event.Seq {
				continue
			}
			if reflect.DeepEqual(existing, event) {
				return nil
			}
			return fmt.Errorf("%w: seq %d", ErrSequenceConflict, event.Seq)
		}
		if len(journey.Events) > 0 && event.Seq < journey.Events[len(journey.Events)-1].Seq {
			return fmt.Errorf("%w: seq %d follows %d", ErrSequenceConflict, event.Seq, journey.Events[len(journey.Events)-1].Seq)
		}
	}

	journey.Events = append(journey.Events, cloneEvent(event))
	if event.OccurredAt.After(journey.UpdatedAt) {
		journey.UpdatedAt = event.OccurredAt
	}
	if event.ObservationStatus == ObservationDegraded {
		journey.ObservationStatus = ObservationDegraded
	}

	snapshot := projectSnapshot(
		tenant.total.items[event.RequestID], event, journey.StartedAt,
		journey.UpdatedAt, journey.ObservationStatus,
	)
	tenant.total.upsert(event.RequestID, snapshot)

	for model := range modelsForEvent(event) {
		fifo := tenant.models[model]
		if fifo == nil {
			fifo = newOrderedSnapshots(p.config.PerModelCapacity)
			tenant.models[model] = fifo
		}
		fifo.upsert(event.RequestID, snapshot)
	}
	for _, fifo := range tenant.models {
		fifo.update(event.RequestID, snapshot)
	}

	if event.Attempt != nil {
		key := nodeKeyForEvent(event)
		fifo := tenant.nodes[key]
		if fifo == nil {
			fifo = newOrderedSnapshots(p.config.PerNodeCapacity)
			tenant.nodes[key] = fifo
		}
		fifo.upsert(event.Attempt.AttemptID, snapshot)
	}
	return nil
}

func (p *Projection) Detail(tenantID, requestID string) (*RequestJourney, error) {
	if p == nil {
		return nil, ErrJourneyNotFound
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	tenant := p.tenants[tenantID]
	if tenant == nil || tenant.details[requestID] == nil {
		return nil, ErrJourneyNotFound
	}
	return cloneJourney(tenant.details[requestID]), nil
}

func (p *Projection) RecentTotal(tenantID string) []RequestSnapshot {
	return p.recent(tenantID, func(tenant *tenantProjection) *orderedSnapshots { return tenant.total })
}

func (p *Projection) RecentModel(tenantID, model string) []RequestSnapshot {
	return p.recent(tenantID, func(tenant *tenantProjection) *orderedSnapshots { return tenant.models[model] })
}

func (p *Projection) RecentNode(tenantID string, key NodeKey) []RequestSnapshot {
	return p.recent(tenantID, func(tenant *tenantProjection) *orderedSnapshots { return tenant.nodes[key] })
}

func (p *Projection) RecentModels(tenantID string) []ModelFIFOSnapshot {
	if p == nil {
		return []ModelFIFOSnapshot{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	tenant := p.tenants[tenantID]
	if tenant == nil {
		return []ModelFIFOSnapshot{}
	}
	models := make([]string, 0, len(tenant.models))
	for model := range tenant.models {
		models = append(models, model)
	}
	sort.Strings(models)
	result := make([]ModelFIFOSnapshot, 0, len(models))
	for _, model := range models {
		result = append(result, ModelFIFOSnapshot{Model: model, Capacity: tenant.models[model].capacity, Requests: tenant.models[model].snapshots()})
	}
	return result
}

func (p *Projection) RecentNodes(tenantID string) []NodeFIFOSnapshot {
	if p == nil {
		return []NodeFIFOSnapshot{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	tenant := p.tenants[tenantID]
	if tenant == nil {
		return []NodeFIFOSnapshot{}
	}
	keys := make([]NodeKey, 0, len(tenant.nodes))
	for key := range tenant.nodes {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Model != keys[j].Model {
			return keys[i].Model < keys[j].Model
		}
		if keys[i].ProviderID != keys[j].ProviderID {
			return keys[i].ProviderID < keys[j].ProviderID
		}
		return keys[i].CredentialID < keys[j].CredentialID
	})
	result := make([]NodeFIFOSnapshot, 0, len(keys))
	for _, key := range keys {
		result = append(result, NodeFIFOSnapshot{Model: key.Model, ProviderID: key.ProviderID, CredentialID: key.CredentialID, Capacity: tenant.nodes[key].capacity, Requests: tenant.nodes[key].snapshots()})
	}
	return result
}

func (p *Projection) recent(tenantID string, selectFIFO func(*tenantProjection) *orderedSnapshots) []RequestSnapshot {
	if p == nil {
		return []RequestSnapshot{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	tenant := p.tenants[tenantID]
	if tenant == nil {
		return []RequestSnapshot{}
	}
	fifo := selectFIFO(tenant)
	if fifo == nil {
		return []RequestSnapshot{}
	}
	return fifo.snapshots()
}

func (p *Projection) tenant(tenantID string) *tenantProjection {
	tenant := p.tenants[tenantID]
	if tenant == nil {
		tenant = &tenantProjection{
			details: make(map[string]*RequestJourney),
			total:   newOrderedSnapshots(p.config.TotalRequestCapacity),
			models:  make(map[string]*orderedSnapshots),
			nodes:   make(map[NodeKey]*orderedSnapshots),
		}
		p.tenants[tenantID] = tenant
	}
	return tenant
}

func newOrderedSnapshots(capacity int) *orderedSnapshots {
	return &orderedSnapshots{
		capacity: capacity,
		order:    make([]string, 0, capacity),
		items:    make(map[string]RequestSnapshot),
	}
}

func newOrderedIngressSnapshots(capacity int) *orderedIngressSnapshots {
	return &orderedIngressSnapshots{
		capacity: capacity,
		order:    make([]string, 0, capacity),
		items:    make(map[string]IngressSnapshot),
	}
}

func (f *orderedIngressSnapshots) upsert(id string, snapshot IngressSnapshot) {
	if _, exists := f.items[id]; exists {
		f.items[id] = snapshot
		return
	}
	insertAt := sort.Search(len(f.order), func(i int) bool {
		existing := f.items[f.order[i]]
		if existing.ArrivedAt.Equal(snapshot.ArrivedAt) {
			return f.order[i] >= id
		}
		return !existing.ArrivedAt.Before(snapshot.ArrivedAt)
	})
	f.order = append(f.order, "")
	copy(f.order[insertAt+1:], f.order[insertAt:])
	f.order[insertAt] = id
	f.items[id] = snapshot
	if len(f.order) > f.capacity {
		delete(f.items, f.order[0])
		copy(f.order, f.order[1:])
		f.order = f.order[:len(f.order)-1]
	}
}

func (f *orderedIngressSnapshots) snapshots() []IngressSnapshot {
	result := make([]IngressSnapshot, 0, len(f.order))
	for _, id := range f.order {
		result = append(result, f.items[id])
	}
	return result
}

func ingressIdentity(gatewayInstanceID, requestID string) string {
	return gatewayInstanceID + "\x00" + requestID
}

func (f *orderedSnapshots) upsert(id string, snapshot RequestSnapshot) {
	if _, exists := f.items[id]; exists {
		f.items[id] = cloneSnapshot(snapshot)
		return
	}
	if len(f.order) >= f.capacity {
		delete(f.items, f.order[0])
		copy(f.order, f.order[1:])
		f.order = f.order[:len(f.order)-1]
	}
	f.order = append(f.order, id)
	f.items[id] = cloneSnapshot(snapshot)
}

func (f *orderedSnapshots) update(id string, snapshot RequestSnapshot) {
	if _, exists := f.items[id]; exists {
		f.items[id] = cloneSnapshot(snapshot)
	}
}

func (f *orderedSnapshots) snapshots() []RequestSnapshot {
	result := make([]RequestSnapshot, 0, len(f.order))
	for _, id := range f.order {
		result = append(result, cloneSnapshot(f.items[id]))
	}
	return result
}

func modelsForEvent(event JourneyEvent) map[string]struct{} {
	models := make(map[string]struct{}, 4)
	for _, model := range []string{event.ResolvedModel, event.Model, event.ToModel} {
		if model != "" {
			models[model] = struct{}{}
		}
	}
	if event.Attempt != nil && event.Attempt.Model != "" {
		models[event.Attempt.Model] = struct{}{}
	}
	return models
}

func nodeKeyForEvent(event JourneyEvent) NodeKey {
	model := event.Attempt.Model
	if model == "" {
		model = event.Model
	}
	if model == "" {
		model = event.ResolvedModel
	}
	return NodeKey{
		Model:        model,
		ProviderID:   event.Attempt.ProviderID,
		CredentialID: event.Attempt.CredentialID,
	}
}

func projectSnapshot(
	previous RequestSnapshot,
	event JourneyEvent,
	startedAt, updatedAt time.Time,
	observationStatus ObservationStatus,
) RequestSnapshot {
	snapshot := previous
	snapshot.TenantID = event.TenantID
	snapshot.GatewayInstanceID = event.GatewayInstanceID
	snapshot.RequestID = event.RequestID
	snapshot.CurrentStage = event.Stage
	snapshot.LastSeq = event.Seq
	snapshot.LastEventType = event.Type
	snapshot.ObservationStatus = observationStatus
	snapshot.StartedAt = startedAt
	snapshot.UpdatedAt = updatedAt
	if event.RequestedModel != "" {
		snapshot.RequestedModel = event.RequestedModel
	}
	if event.ResolvedModel != "" {
		snapshot.ResolvedModel = event.ResolvedModel
	}
	if event.ToModel != "" {
		snapshot.ResolvedModel = event.ToModel
	}
	if event.Attempt != nil {
		attempt := *event.Attempt
		snapshot.Attempt = &attempt
	}
	if event.Outcome != "" {
		snapshot.Outcome = event.Outcome
	}
	if event.ErrorKind != "" {
		snapshot.ErrorKind = event.ErrorKind
	}
	if event.HTTPStatus != 0 {
		snapshot.HTTPStatus = event.HTTPStatus
	}
	if event.RetryReason != "" {
		snapshot.RetryReason = event.RetryReason
	}
	if event.SwitchReason != "" {
		snapshot.SwitchReason = event.SwitchReason
	}
	if event.NodeHealthStatus != "" {
		snapshot.NodeHealthStatus = event.NodeHealthStatus
	}
	if event.Stage == StageTerminal {
		completed := event.OccurredAt
		snapshot.CompletedAt = &completed
	}
	return snapshot
}

func cloneEvent(event JourneyEvent) JourneyEvent {
	if event.Attempt != nil {
		attempt := *event.Attempt
		event.Attempt = &attempt
	}
	return event
}

func cloneJourney(journey *RequestJourney) *RequestJourney {
	clone := *journey
	clone.Events = make([]JourneyEvent, len(journey.Events))
	for i, event := range journey.Events {
		clone.Events[i] = cloneEvent(event)
	}
	return &clone
}

func cloneSnapshot(snapshot RequestSnapshot) RequestSnapshot {
	if snapshot.Attempt != nil {
		attempt := *snapshot.Attempt
		snapshot.Attempt = &attempt
	}
	if snapshot.CompletedAt != nil {
		completed := *snapshot.CompletedAt
		snapshot.CompletedAt = &completed
	}
	return snapshot
}
