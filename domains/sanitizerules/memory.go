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
	e, err := prepareEvent(event)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	if old, ok := s.events[e.Fingerprint]; ok {
		old.Count++
		old.LastSeen = e.LastSeen
		s.events[e.Fingerprint] = old
	} else {
		s.nextEvent++
		e.ID = s.nextEvent
		s.events[e.Fingerprint] = e
	}
	for len(s.events) > retentionLimit(limit) {
		var oldest string
		var candidate Event
		for k, e := range s.events {
			if oldest == "" || e.LastSeen.Before(candidate.LastSeen) || (e.LastSeen.Equal(candidate.LastSeen) && e.ID < candidate.ID) {
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
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
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
