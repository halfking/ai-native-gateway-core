package mcp

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"
)

var (
	errClaimToken  = errors.New("invalid_claim_token")
	errClaimed     = errors.New("already_claimed")
	errNotApproved = errors.New("not_approved")
)

func consumeBootstrap(record BootstrapRecord, hash string, at time.Time) (BootstrapRecord, error) {
	if record.ClaimTokenHash == "" || subtle.ConstantTimeCompare([]byte(record.ClaimTokenHash), []byte(hash)) != 1 {
		return record, errClaimToken
	}
	if record.Status == "claimed" || record.Status == "approved" && record.Plaintext == "" {
		return record, errClaimed
	}
	if record.Status != "approved" {
		return record, errNotApproved
	}
	record.Plaintext = ""
	record.Status = "claimed"
	record.UpdatedAt = at
	return record, nil
}

func (s *memoryStore) ClaimBootstrap(_ context.Context, id, hash string, at time.Time) (*BootstrapRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "apply\x00" + id
	old, exists := s.bootstrap[key]
	if !exists {
		return nil, ErrNotFound
	}
	updated, err := consumeBootstrap(old, hash, at)
	if err != nil {
		return nil, err
	}
	s.bootstrap[key] = updated
	return &old, nil
}

func (s *fileStore) ClaimBootstrap(_ context.Context, id, hash string, at time.Time) (*BootstrapRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "apply\x00" + id
	old, exists := s.data.Bootstrap[key]
	if !exists {
		return nil, ErrNotFound
	}
	updated, err := consumeBootstrap(old, hash, at)
	if err != nil {
		return nil, err
	}
	s.data.Bootstrap[key] = updated
	if err := s.persistLocked(); err != nil {
		s.data.Bootstrap[key] = old
		return nil, err
	}
	return &old, nil
}
