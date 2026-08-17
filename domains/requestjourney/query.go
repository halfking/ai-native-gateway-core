package requestjourney

import (
	"context"
	"errors"
	"sort"
)

// ListResult makes observation degradation explicit without discarding usable
// fallback data.
type ListResult struct {
	ObservationStatus ObservationStatus `json:"observation_status"`
	Capacity          int               `json:"capacity"`
	Requests          []RequestSnapshot `json:"requests"`
}

// IngressListResult makes the cross-instance extent explicit. A degraded,
// instance_local result remains useful but must not be presented as global.
type IngressListResult struct {
	ObservationStatus ObservationStatus `json:"observation_status"`
	ObservationScope  string            `json:"observation_scope"`
	Capacity          int               `json:"capacity"`
	Requests          []IngressSnapshot `json:"requests"`
}

// DetailResult distinguishes a missing journey from unavailable observation
// infrastructure while keeping the transport contract stable.
type DetailResult struct {
	ObservationStatus ObservationStatus `json:"observation_status"`
	Journey           *RequestJourney   `json:"journey,omitempty"`
}

type ModelListResult struct {
	ObservationStatus ObservationStatus   `json:"observation_status"`
	Models            []ModelFIFOSnapshot `json:"model_snapshots"`
}

type NodeListResult struct {
	ObservationStatus ObservationStatus  `json:"observation_status"`
	Nodes             []NodeFIFOSnapshot `json:"node_snapshots"`
}

// QueryService reads the shared Redis projection first, PostgreSQL second, and
// the local projection last. Infrastructure failures degrade the observation
// status but do not hide data available from a lower tier.
type QueryService struct {
	redis  *RedisStore
	pg     *PostgresRepository
	memory *Projection
	config Config
}

func NewQueryService(redisStore *RedisStore, pg *PostgresRepository, memory *Projection, config Config) *QueryService {
	defaults := DefaultConfig()
	config.TotalRequestCapacity = positiveOrDefault(config.TotalRequestCapacity, defaults.TotalRequestCapacity)
	config.PerModelCapacity = positiveOrDefault(config.PerModelCapacity, defaults.PerModelCapacity)
	config.PerNodeCapacity = positiveOrDefault(config.PerNodeCapacity, defaults.PerNodeCapacity)
	return &QueryService{redis: redisStore, pg: pg, memory: memory, config: config}
}

func (s *QueryService) Detail(ctx context.Context, tenantID, requestID string) (DetailResult, error) {
	degraded := false
	var infraErr error
	if s != nil && s.redis != nil && s.redis.client != nil {
		journey, err := s.redis.Detail(ctx, tenantID, requestID)
		if err == nil {
			return DetailResult{ObservationStatus: journey.ObservationStatus, Journey: journey}, nil
		}
		if !errors.Is(err, ErrJourneyNotFound) {
			degraded = true
			infraErr = errors.Join(infraErr, err)
		}
	} else {
		degraded = true
	}

	if s != nil && s.pg != nil && s.pg.db != nil {
		journey, err := s.pg.Detail(ctx, tenantID, requestID)
		if err == nil {
			if degraded {
				markJourneyDegraded(journey)
			}
			return DetailResult{ObservationStatus: journey.ObservationStatus, Journey: journey}, infraErr
		}
		if !errors.Is(err, ErrJourneyNotFound) {
			degraded = true
			infraErr = errors.Join(infraErr, err)
		}
	} else {
		degraded = true
	}

	if s != nil && s.memory != nil {
		journey, err := s.memory.Detail(tenantID, requestID)
		if err == nil {
			if degraded {
				markJourneyDegraded(journey)
			}
			return DetailResult{ObservationStatus: journey.ObservationStatus, Journey: journey}, infraErr
		}
	}

	status := ObservationComplete
	if degraded {
		status = ObservationDegraded
	}
	return DetailResult{ObservationStatus: status}, infraErr
}

func (s *QueryService) RecentIngress(ctx context.Context) (IngressListResult, error) {
	if s != nil && s.redis != nil && s.redis.client != nil {
		requests, err := s.redis.RecentIngress(ctx)
		if err == nil {
			if s.memory != nil {
				requests = mergeIngressSnapshots(requests, s.memory.RecentIngress(), s.config.TotalRequestCapacity)
			}
			if requests == nil {
				requests = []IngressSnapshot{}
			}
			status := ObservationComplete
			if s.memory != nil && s.memory.IngressDegraded() {
				status = ObservationDegraded
			}
			return IngressListResult{
				ObservationStatus: status, ObservationScope: "shared_redis",
				Capacity: s.config.TotalRequestCapacity, Requests: requests,
			}, nil
		}
		requests = []IngressSnapshot{}
		if s.memory != nil {
			requests = s.memory.RecentIngress()
		}
		return IngressListResult{
			ObservationStatus: ObservationDegraded, ObservationScope: "instance_local",
			Capacity: s.config.TotalRequestCapacity, Requests: requests,
		}, err
	}
	requests := []IngressSnapshot{}
	capacity := DefaultTotalRequestCapacity
	if s != nil {
		capacity = s.config.TotalRequestCapacity
		if s.memory != nil {
			requests = s.memory.RecentIngress()
		}
	}
	return IngressListResult{
		ObservationStatus: ObservationDegraded, ObservationScope: "instance_local",
		Capacity: capacity, Requests: requests,
	}, nil
}

func mergeIngressSnapshots(shared, local []IngressSnapshot, capacity int) []IngressSnapshot {
	byID := make(map[string]IngressSnapshot, len(shared)+len(local))
	for _, snapshot := range append(append([]IngressSnapshot(nil), shared...), local...) {
		key := ingressIdentity(snapshot.GatewayInstanceID, snapshot.RequestID)
		if current, ok := byID[key]; !ok || snapshot.UpdatedAt.After(current.UpdatedAt) {
			byID[key] = snapshot
		}
	}
	merged := make([]IngressSnapshot, 0, len(byID))
	for _, snapshot := range byID {
		merged = append(merged, snapshot)
	}
	sort.Slice(merged, func(i, j int) bool {
		if !merged[i].ArrivedAt.Equal(merged[j].ArrivedAt) {
			return merged[i].ArrivedAt.Before(merged[j].ArrivedAt)
		}
		if merged[i].GatewayInstanceID != merged[j].GatewayInstanceID {
			return merged[i].GatewayInstanceID < merged[j].GatewayInstanceID
		}
		return merged[i].RequestID < merged[j].RequestID
	})
	if capacity > 0 && len(merged) > capacity {
		merged = merged[len(merged)-capacity:]
	}
	return merged
}

func (s *QueryService) RecentTotal(ctx context.Context, tenantID string) (ListResult, error) {
	return s.recent(ctx, tenantID, s.config.TotalRequestCapacity,
		func() ([]RequestSnapshot, error) { return s.redis.RecentTotal(ctx, tenantID) },
		func() ([]RequestSnapshot, error) {
			return s.pg.RecentTotal(ctx, tenantID, s.config.TotalRequestCapacity)
		},
		func() []RequestSnapshot { return s.memory.RecentTotal(tenantID) },
	)
}

func (s *QueryService) RecentModel(ctx context.Context, tenantID, model string) (ListResult, error) {
	return s.recent(ctx, tenantID, s.config.PerModelCapacity,
		func() ([]RequestSnapshot, error) { return s.redis.RecentModel(ctx, tenantID, model) },
		func() ([]RequestSnapshot, error) {
			return s.pg.RecentModel(ctx, tenantID, model, s.config.PerModelCapacity)
		},
		func() []RequestSnapshot { return s.memory.RecentModel(tenantID, model) },
	)
}

func (s *QueryService) RecentNode(ctx context.Context, tenantID string, key NodeKey) (ListResult, error) {
	return s.recent(ctx, tenantID, s.config.PerNodeCapacity,
		func() ([]RequestSnapshot, error) { return s.redis.RecentNode(ctx, tenantID, key) },
		func() ([]RequestSnapshot, error) {
			return s.pg.RecentNode(ctx, tenantID, key, s.config.PerNodeCapacity)
		},
		func() []RequestSnapshot { return s.memory.RecentNode(tenantID, key) },
	)
}

func (s *QueryService) RecentModels(ctx context.Context, tenantID string) (ModelListResult, error) {
	degraded := s == nil || s.redis == nil || s.redis.client == nil
	var infraErr error
	if !degraded {
		models, err := s.redis.RecentModels(ctx, tenantID)
		if err == nil && len(models) > 0 {
			return ModelListResult{ObservationStatus: modelSnapshotsStatus(models), Models: models}, nil
		}
		if err != nil {
			degraded = true
			infraErr = errors.Join(infraErr, err)
		}
	}
	if s != nil && s.pg != nil && s.pg.db != nil {
		keys, err := s.pg.ModelKeys(ctx, tenantID)
		if err == nil && len(keys) > 0 {
			models := make([]ModelFIFOSnapshot, 0, len(keys))
			for _, model := range keys {
				requests, readErr := s.pg.RecentModel(ctx, tenantID, model, s.config.PerModelCapacity)
				if readErr != nil {
					err = readErr
					break
				}
				models = append(models, ModelFIFOSnapshot{Model: model, Capacity: s.config.PerModelCapacity, Requests: requests})
			}
			if err == nil {
				if degraded {
					markModelSnapshotsDegraded(models)
				}
				return ModelListResult{ObservationStatus: modelSnapshotsStatus(models), Models: models}, infraErr
			}
		}
		if err != nil {
			degraded = true
			infraErr = errors.Join(infraErr, err)
		}
	} else {
		degraded = true
	}
	models := []ModelFIFOSnapshot{}
	if s != nil && s.memory != nil {
		models = s.memory.RecentModels(tenantID)
	}
	if degraded {
		markModelSnapshotsDegraded(models)
		return ModelListResult{ObservationStatus: ObservationDegraded, Models: models}, infraErr
	}
	return ModelListResult{ObservationStatus: modelSnapshotsStatus(models), Models: models}, infraErr
}

func (s *QueryService) RecentNodes(ctx context.Context, tenantID string) (NodeListResult, error) {
	degraded := s == nil || s.redis == nil || s.redis.client == nil
	var infraErr error
	if !degraded {
		nodes, err := s.redis.RecentNodes(ctx, tenantID)
		if err == nil && len(nodes) > 0 {
			return NodeListResult{ObservationStatus: nodeSnapshotsStatus(nodes), Nodes: nodes}, nil
		}
		if err != nil {
			degraded = true
			infraErr = errors.Join(infraErr, err)
		}
	}
	if s != nil && s.pg != nil && s.pg.db != nil {
		keys, err := s.pg.NodeKeys(ctx, tenantID)
		if err == nil && len(keys) > 0 {
			nodes := make([]NodeFIFOSnapshot, 0, len(keys))
			for _, key := range keys {
				requests, readErr := s.pg.RecentNode(ctx, tenantID, key, s.config.PerNodeCapacity)
				if readErr != nil {
					err = readErr
					break
				}
				nodes = append(nodes, NodeFIFOSnapshot{Model: key.Model, ProviderID: key.ProviderID, CredentialID: key.CredentialID, Capacity: s.config.PerNodeCapacity, Requests: requests})
			}
			if err == nil {
				if degraded {
					markNodeSnapshotsDegraded(nodes)
				}
				return NodeListResult{ObservationStatus: nodeSnapshotsStatus(nodes), Nodes: nodes}, infraErr
			}
		}
		if err != nil {
			degraded = true
			infraErr = errors.Join(infraErr, err)
		}
	} else {
		degraded = true
	}
	nodes := []NodeFIFOSnapshot{}
	if s != nil && s.memory != nil {
		nodes = s.memory.RecentNodes(tenantID)
	}
	if degraded {
		markNodeSnapshotsDegraded(nodes)
		return NodeListResult{ObservationStatus: ObservationDegraded, Nodes: nodes}, infraErr
	}
	return NodeListResult{ObservationStatus: nodeSnapshotsStatus(nodes), Nodes: nodes}, infraErr
}

func (s *QueryService) recent(
	_ context.Context,
	_ string,
	capacity int,
	redisRead func() ([]RequestSnapshot, error),
	pgRead func() ([]RequestSnapshot, error),
	memoryRead func() []RequestSnapshot,
) (ListResult, error) {
	degraded := false
	var infraErr error
	if s != nil && s.redis != nil && s.redis.client != nil {
		requests, err := redisRead()
		if err == nil && len(requests) > 0 {
			return ListResult{ObservationStatus: aggregateStatus(requests), Capacity: capacity, Requests: requests}, nil
		}
		if err != nil {
			degraded = true
			infraErr = errors.Join(infraErr, err)
		}
	} else {
		degraded = true
	}

	if s != nil && s.pg != nil && s.pg.db != nil {
		requests, err := pgRead()
		if err == nil && len(requests) > 0 {
			if degraded {
				markSnapshotsDegraded(requests)
			}
			return ListResult{ObservationStatus: aggregateStatus(requests), Capacity: capacity, Requests: requests}, infraErr
		}
		if err != nil {
			degraded = true
			infraErr = errors.Join(infraErr, err)
		}
	} else {
		degraded = true
	}

	requests := []RequestSnapshot{}
	if s != nil && s.memory != nil {
		requests = memoryRead()
	}
	if degraded {
		markSnapshotsDegraded(requests)
		return ListResult{ObservationStatus: ObservationDegraded, Capacity: capacity, Requests: requests}, infraErr
	}
	return ListResult{ObservationStatus: aggregateStatus(requests), Capacity: capacity, Requests: requests}, infraErr
}

func aggregateStatus(requests []RequestSnapshot) ObservationStatus {
	for _, request := range requests {
		if request.ObservationStatus == ObservationDegraded {
			return ObservationDegraded
		}
	}
	return ObservationComplete
}

func markJourneyDegraded(journey *RequestJourney) {
	if journey == nil {
		return
	}
	journey.ObservationStatus = ObservationDegraded
	for i := range journey.Events {
		journey.Events[i].ObservationStatus = ObservationDegraded
	}
}

func markSnapshotsDegraded(requests []RequestSnapshot) {
	for i := range requests {
		requests[i].ObservationStatus = ObservationDegraded
	}
}

func modelSnapshotsStatus(models []ModelFIFOSnapshot) ObservationStatus {
	for _, model := range models {
		if aggregateStatus(model.Requests) == ObservationDegraded {
			return ObservationDegraded
		}
	}
	return ObservationComplete
}

func nodeSnapshotsStatus(nodes []NodeFIFOSnapshot) ObservationStatus {
	for _, node := range nodes {
		if aggregateStatus(node.Requests) == ObservationDegraded {
			return ObservationDegraded
		}
	}
	return ObservationComplete
}

func markModelSnapshotsDegraded(models []ModelFIFOSnapshot) {
	for i := range models {
		markSnapshotsDegraded(models[i].Requests)
	}
}

func markNodeSnapshotsDegraded(nodes []NodeFIFOSnapshot) {
	for i := range nodes {
		markSnapshotsDegraded(nodes[i].Requests)
	}
}
