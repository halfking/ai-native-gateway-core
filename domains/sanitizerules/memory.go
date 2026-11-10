package sanitizerules

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore implements the same transaction semantics for isolated unit tests.
// Production wiring must use PostgresStore.
type MemoryStore struct {
	mu        sync.Mutex
	head      int64
	versions  map[int64]Version
	events    map[string]Event
	nextEvent int64
	instances map[string]Instance
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{versions: map[int64]Version{}, events: map[string]Event{}, instances: map[string]Instance{}}
}
func cloneVersion(v Version) Version {
	v.Config = cloneConfig(v.Config)
	v.ReviewLinks = append([]ReviewLink(nil), v.ReviewLinks...)
	return v
}
func (s *MemoryStore) Current(ctx context.Context) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Version{}, err
	}
	if s.head == 0 {
		return Version{}, nil
	}
	return cloneVersion(s.versions[s.head]), nil
}
func (s *MemoryStore) GetVersion(ctx context.Context, id int64) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Version{}, err
	}
	v, ok := s.versions[id]
	if !ok {
		return Version{}, ErrNotFound
	}
	return cloneVersion(v), nil
}
func (s *MemoryStore) ListVersions(ctx context.Context, limit int) ([]Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []Version{}
	for id := s.head; id > 0 && len(out) < boundedLimit(limit); id-- {
		if v, ok := s.versions[id]; ok {
			out = append(out, cloneVersion(v))
		}
	}
	return out, nil
}
func (s *MemoryStore) Publish(ctx context.Context, base int64, cfg Config, actor, comment string, rollback int64, links ...ReviewLink) (Version, error) {
	if err := validatePublish(cfg, actor, comment, rollback); err != nil {
		return Version{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Version{}, err
	}
	if base != s.head {
		return Version{}, ErrConflict
	}
	if rollback != 0 {
		target, ok := s.versions[rollback]
		if !ok {
			return Version{}, ErrNotFound
		}
		cfg = target.Config
	}
	for _, link := range links {
		found := false
		for _, e := range s.events {
			if e.ID == link.ID && e.TenantID == link.TenantID && e.Status != "pending" {
				found = true
				break
			}
		}
		if !found {
			return Version{}, ErrNotFound
		}
	}
	v := Version{ID: s.head + 1, ParentID: s.head, RollbackOf: rollback, Config: cloneConfig(cfg), Actor: actor, Comment: comment, CreatedAt: time.Now().UTC(), ReviewLinks: append([]ReviewLink(nil), links...)}
	for key, e := range s.events {
		for _, link := range links {
			if e.ID == link.ID && e.TenantID == link.TenantID {
				e.ResolvedVersion = v.ID
				s.events[key] = e
			}
		}
	}
	s.versions[v.ID] = v
	s.head = v.ID
	return cloneVersion(v), nil
}
func validatePublish(cfg Config, actor, comment string, rollback int64) error {
	if err := Validate(cfg); err != nil {
		return err
	}
	if actor == "" || len(actor) > 256 || len(comment) > 2000 || rollback < 0 {
		return fmt.Errorf("%w: invalid publication metadata", ErrInvalid)
	}
	return nil
}
func (s *MemoryStore) RecordEvent(ctx context.Context, event Event, limit int) error {
	return s.RecordEvents(ctx, []Event{event}, limit)
}

// RecordEvents is the batch twin of RecordEvent: duplicate fingerprints are
// aggregated first (count += hits, last_seen = newest) so the upsert keeps the
// per-event count semantics, then one retention pass bounds the map.
func (s *MemoryStore) RecordEvents(ctx context.Context, events []Event, limit int) error {
	if len(events) == 0 {
		return nil
	}
	agg := make(map[string]Event, len(events))
	order := make([]string, 0, len(events))
	for _, raw := range events {
		e, err := prepareEvent(raw)
		if err != nil {
			return err
		}
		if prev, ok := agg[e.Fingerprint]; ok {
			prev.Count += e.Count
			if e.LastSeen.After(prev.LastSeen) {
				prev.LastSeen = e.LastSeen
			}
			if e.FirstSeen.Before(prev.FirstSeen) {
				prev.FirstSeen = e.FirstSeen
			}
			agg[e.Fingerprint] = prev
			continue
		}
		agg[e.Fingerprint] = e
		order = append(order, e.Fingerprint)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, fp := range order {
		e := agg[fp]
		if old, ok := s.events[fp]; ok {
			old.Count += e.Count
			old.LastSeen = e.LastSeen
			s.events[fp] = old
		} else {
			s.nextEvent++
			e.ID = s.nextEvent
			s.events[fp] = e
		}
	}
	for len(s.events) > retentionLimit(limit) {
		// 2026-11-10：LastSeen 同刻并列（同一批/同毫秒写入）时的平局裁决改为
		// **活跃度优先**（Count ASC，再 ID ASC）——纯创建序（ID ASC）会把本批
		// 刚被重复聚合、Count 最高的条目（恰是最活跃的指纹）当"最老"删掉
		//（TestEventRetentionKeepsRecentlyAggregatedEntry 自 2026-10-16 起红）。
		// LastSeen 不同刻时行为与原先逐字节一致。
		var oldest string
		var candidate Event
		for k, e := range s.events {
			if oldest == "" ||
				e.LastSeen.Before(candidate.LastSeen) ||
				(e.LastSeen.Equal(candidate.LastSeen) && (e.Count < candidate.Count ||
					(e.Count == candidate.Count && e.ID < candidate.ID))) {
				oldest = k
				candidate = e
			}
		}
		delete(s.events, oldest)
	}
	return nil
}
func (s *MemoryStore) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []Event{}
	for _, e := range s.events {
		if (f.TenantID == "" || e.TenantID == f.TenantID) && (f.Status == "" || e.Status == f.Status) && (f.Action == "" || e.Action == f.Action) {
			out = append(out, cloneEvent(e))
		}
	}
	// 2026-11-10：补 ID DESC 平局裁决——LastSeen 同刻并列时非稳定排序会随机
	// 翻转 rows[0]（TestReviewedEventsAreLinkedInPublicationTransaction 的
	// flaky 根源：sort.Slice 对相等键无顺序保证）。
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > boundedLimit(f.Limit) {
		out = out[:boundedLimit(f.Limit)]
	}
	return out, nil
}
func cloneEvent(e Event) Event {
	if e.ReviewedAt != nil {
		at := *e.ReviewedAt
		e.ReviewedAt = &at
	}
	return e
}
func (s *MemoryStore) ReviewEvent(ctx context.Context, tenant string, id int64, status, reviewer, comment string) (Event, error) {
	if err := validReview(status, reviewer, comment); err != nil {
		return Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	for k, e := range s.events {
		if e.ID == id && e.TenantID == tenant {
			now := time.Now().UTC()
			e.Status = status
			e.Reviewer = reviewer
			e.ReviewComment = comment
			e.ReviewedAt = &now
			s.events[k] = e
			return cloneEvent(e), nil
		}
	}
	return Event{}, ErrNotFound
}
func (s *MemoryStore) Heartbeat(ctx context.Context, i Instance) error {
	if !validID(i.InstanceID) {
		return fmt.Errorf("%w: invalid instance id", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.instances[i.InstanceID] = safeInstance(i)
	return nil
}
func (s *MemoryStore) ListInstances(ctx context.Context) ([]Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []Instance{}
	for _, i := range s.instances {
		out = append(out, i)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out, nil
}

func (s *MemoryStore) GetEvent(ctx context.Context, id int64) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	for _, e := range s.events {
		if e.ID == id {
			return cloneEvent(e), nil
		}
	}
	return Event{}, ErrNotFound
}
