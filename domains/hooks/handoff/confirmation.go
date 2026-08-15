package handoff

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

const confirmationTokenBytes = 32

var (
	ErrConfirmationInvalid         = errors.New("handoff confirmation is invalid")
	ErrConfirmationExpired         = errors.New("handoff confirmation has expired")
	ErrConfirmationReplay          = errors.New("handoff confirmation has already been used")
	ErrConfirmationBudgetExhausted = errors.New("handoff confirmation budget is exhausted")
	ErrConfirmationCooldownActive  = errors.New("handoff confirmation cooldown is active")
)

// ConfirmationProposal is the durable one-time capability returned with an
// explicit handoff. TokenHash is the only representation of the token that may
// be stored or logged.
type ConfirmationProposal struct {
	ID                string
	TenantID          string
	APIKeyID          int
	PreviousSessionID string
	TokenHash         string
	ExpiresAt         time.Time
	Record            HandoffRecord
	Status            string
	ConfirmedAt       time.Time
	NewSessionID      string
	IdempotencyHash   string
}

// ConfirmationInput is derived from an authenticated data-plane request.
// TenantID and APIKeyID must never come from client JSON.
type ConfirmationInput struct {
	ProposalID      string
	TenantID        string
	APIKeyID        int
	Token           string
	NewSessionID    string
	TargetCreatedAt time.Time
	IdempotencyKey  string
	MaxPerSession   int
	CooldownSeconds int
}

type ConfirmationResult struct {
	ProposalID        string
	PreviousSessionID string
	NewSessionID      string
	ConfirmedAt       time.Time
	FirstConfirmation bool
	Record            HandoffRecord
}

// ConfirmationStore persists and atomically consumes confirmation proposals.
type ConfirmationStore interface {
	SavePending(ctx context.Context, proposal *ConfirmationProposal) error
	Confirm(ctx context.Context, input ConfirmationInput) (*ConfirmationResult, error)
}

func NewConfirmationProposal(record *HandoffRecord, apiKeyID int, expiresAt time.Time) (*ConfirmationProposal, string, error) {
	if record == nil || record.TenantID == "" || record.SessionKey == "" || apiKeyID <= 0 {
		return nil, "", fmt.Errorf("invalid handoff confirmation proposal")
	}
	if !expiresAt.After(time.Now()) {
		return nil, "", fmt.Errorf("handoff confirmation expiry must be in the future")
	}
	raw := make([]byte, confirmationTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("generate handoff confirmation token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	copy := *record
	if copy.CreatedAt.IsZero() {
		copy.CreatedAt = time.Now().UTC()
	}
	return &ConfirmationProposal{
		ID:                uuid.NewString(),
		TenantID:          copy.TenantID,
		APIKeyID:          apiKeyID,
		PreviousSessionID: copy.SessionKey,
		TokenHash:         hashConfirmationValue(token),
		ExpiresAt:         expiresAt.UTC(),
		Record:            copy,
		Status:            "pending",
	}, token, nil
}

func (p *ConfirmationProposal) MatchesToken(token string) bool {
	if p == nil || token == "" || p.TokenHash == "" {
		return false
	}
	expected, err := hex.DecodeString(p.TokenHash)
	if err != nil {
		return false
	}
	actual, err := hex.DecodeString(hashConfirmationValue(token))
	if err != nil || len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func hashConfirmationValue(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// MemoryConfirmationStore is a concurrency-safe test implementation.
type MemoryConfirmationStore struct {
	mu        sync.Mutex
	byID      map[string]*ConfirmationProposal
	bySession map[string]string
}

func NewMemoryConfirmationStore() *MemoryConfirmationStore {
	return &MemoryConfirmationStore{byID: map[string]*ConfirmationProposal{}, bySession: map[string]string{}}
}

func (s *MemoryConfirmationStore) SavePending(_ context.Context, proposal *ConfirmationProposal) error {
	if proposal == nil || proposal.ID == "" || proposal.TenantID == "" || proposal.PreviousSessionID == "" || proposal.TokenHash == "" {
		return fmt.Errorf("invalid handoff confirmation proposal")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := proposal.TenantID + "\x00" + proposal.PreviousSessionID
	if prior := s.bySession[key]; prior != "" {
		delete(s.byID, prior)
	}
	copy := *proposal
	s.byID[copy.ID] = &copy
	s.bySession[key] = copy.ID
	return nil
}

func (s *MemoryConfirmationStore) Confirm(_ context.Context, input ConfirmationInput) (*ConfirmationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	proposal := s.byID[input.ProposalID]
	if proposal == nil || proposal.TenantID != input.TenantID || proposal.APIKeyID != input.APIKeyID || !proposal.MatchesToken(input.Token) {
		return nil, ErrConfirmationInvalid
	}
	inputHash := hashConfirmationValue(input.IdempotencyKey)
	if proposal.Status == "confirmed" {
		if proposal.IdempotencyHash == inputHash && proposal.NewSessionID == input.NewSessionID {
			return &ConfirmationResult{ProposalID: proposal.ID, PreviousSessionID: proposal.PreviousSessionID, NewSessionID: proposal.NewSessionID, ConfirmedAt: proposal.ConfirmedAt, Record: proposal.Record}, nil
		}
		return nil, ErrConfirmationReplay
	}
	if !time.Now().Before(proposal.ExpiresAt) {
		proposal.Status = "expired"
		return nil, ErrConfirmationExpired
	}
	if proposal.Status != "pending" || input.NewSessionID == "" || input.NewSessionID == proposal.PreviousSessionID || input.IdempotencyKey == "" || (!input.TargetCreatedAt.IsZero() && input.TargetCreatedAt.Before(proposal.Record.CreatedAt)) {
		return nil, ErrConfirmationInvalid
	}
	proposal.Status = "confirmed"
	proposal.NewSessionID = input.NewSessionID
	proposal.IdempotencyHash = inputHash
	proposal.ConfirmedAt = time.Now().UTC()
	proposal.Record.NewSessionID = input.NewSessionID
	return &ConfirmationResult{ProposalID: proposal.ID, PreviousSessionID: proposal.PreviousSessionID, NewSessionID: proposal.NewSessionID, ConfirmedAt: proposal.ConfirmedAt, FirstConfirmation: true, Record: proposal.Record}, nil
}
