package mcp

import (
	"context"
	"time"
)

func (s *memoryStore) TouchKey(_ context.Context, tenantID, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[id]
	if !ok || !visible(tenantID, k.TenantID) {
		return ErrNotFound
	}
	k.LastUsedAt = &at
	s.keys[id] = k
	return nil
}

func (s *fileStore) TouchKey(_ context.Context, tenantID, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.data.Keys[id]
	if !ok || !visible(tenantID, k.TenantID) {
		return ErrNotFound
	}
	old := k
	k.LastUsedAt = &at
	s.data.Keys[id] = k
	if err := s.persistLocked(); err != nil {
		s.data.Keys[id] = old
		return err
	}
	return nil
}
