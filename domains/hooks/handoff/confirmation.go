package handoff

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	confirmationTokenBytes                = 32
	confirmationStatusPending             = "pending"
	confirmationStatusAccountingConfirmed = "accounting_confirmed"
	confirmationStatusRestored            = "restored"
	confirmationStatusManualRequired      = "manual_required"
	confirmationStatusExpired             = "expired"
	legacyConfirmationStatusConfirmed     = "confirmed"
	maxPersistedGoalStateBytes            = 16 << 10
)

var (
	ErrConfirmationInvalid         = errors.New("handoff confirmation is invalid")
	ErrConfirmationExpired         = errors.New("handoff confirmation has expired")
	ErrConfirmationReplay          = errors.New("handoff confirmation has already been used")
	ErrConfirmationBudgetExhausted = errors.New("handoff confirmation budget is exhausted")
	ErrConfirmationCooldownActive  = errors.New("handoff confirmation cooldown is active")
	ErrGoalRestoreRetryable        = errors.New("handoff goal restore is incomplete; retry confirmation")
	ErrGoalRestoreManualRequired   = errors.New("handoff goal restore requires manual recovery")
)

// ConfirmationProposal is the durable one-time capability returned with an
// explicit handoff. TokenHash is the only representation of the token that may
// be stored or logged.
type ConfirmationProposal struct {
	ID                 string
	TenantID           string
	APIKeyID           int
	PreviousSessionID  string
	TokenHash          string
	ExpiresAt          time.Time
	Record             HandoffRecord
	Status             string
	ConfirmedAt        time.Time
	NewSessionID       string
	IdempotencyHash    string
	GoalState          *GoalState
	GoalStateVersion   int
	RestoreStatus      string
	RestoreError       string
	RestoreAttemptedAt time.Time
	RestoredAt         time.Time
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
	GoalState         *GoalState
	RestoreStatus     string
}

// GoalRestoreState is the durable portion of a confirmation that remains
// recoverable after accounting has committed or the process has restarted.
type GoalRestoreState struct {
	ProposalID         string
	TenantID           string
	NewSessionID       string
	Status             string
	GoalState          *GoalState
	RestoreError       string
	RestoreAttemptedAt time.Time
	RestoredAt         time.Time
}

// ConfirmationStore persists and atomically consumes confirmation proposals.
type ConfirmationStore interface {
	SavePending(ctx context.Context, proposal *ConfirmationProposal) error
	Confirm(ctx context.Context, input ConfirmationInput) (*ConfirmationResult, error)
}

// GoalRestoreStore tracks Goal restoration independently from handoff
// accounting. Implementations scope every operation by tenant and use terminal
// state transitions that are safe to retry.
type GoalRestoreStore interface {
	GetGoalRestoreState(ctx context.Context, proposalID, tenantID string) (*GoalRestoreState, error)
	MarkGoalRestoreAttempt(ctx context.Context, proposalID, tenantID, restoreError string) error
	MarkGoalRestored(ctx context.Context, proposalID, tenantID string) error
	MarkGoalRestoreManualRequired(ctx context.Context, proposalID, tenantID, restoreError string) error
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
		Status:            confirmationStatusPending,
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

func marshalPersistedGoalState(state *GoalState) ([]byte, int, error) {
	if state == nil {
		return nil, 0, nil
	}
	copy := cloneGoalState(state)
	copy.Version = GoalStateVersion
	copy.TaskDescription = truncateRunes(redactResumeSensitive(copy.TaskDescription), 4096)
	copy.RemainingWork = truncateRunes(redactResumeSensitive(copy.RemainingWork), 4096)
	copy.CurrentModel = truncateRunes(redactResumeSensitive(copy.CurrentModel), 256)
	if len(copy.CompletedSteps) > 64 {
		copy.CompletedSteps = copy.CompletedSteps[:64]
	}
	for i := range copy.CompletedSteps {
		copy.CompletedSteps[i] = truncateRunes(redactResumeSensitive(copy.CompletedSteps[i]), 512)
	}
	payload, err := json.Marshal(copy)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal goal state snapshot: %w", err)
	}
	if len(payload) > maxPersistedGoalStateBytes {
		return nil, 0, fmt.Errorf("goal state snapshot exceeds %d bytes", maxPersistedGoalStateBytes)
	}
	return payload, copy.Version, nil
}

func unmarshalPersistedGoalState(payload []byte, version int) (*GoalState, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	if len(payload) > maxPersistedGoalStateBytes || version != GoalStateVersion {
		return nil, fmt.Errorf("unsupported goal state snapshot version %d", version)
	}
	var state GoalState
	if err := json.Unmarshal(payload, &state); err != nil {
		return nil, fmt.Errorf("decode goal state snapshot: %w", err)
	}
	if state.Version != GoalStateVersion || state.TenantID == "" || state.SourceSessionID == "" {
		return nil, fmt.Errorf("invalid goal state snapshot")
	}
	return &state, nil
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
	copy.GoalState = cloneGoalState(proposal.GoalState)
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
	if proposal.Status == legacyConfirmationStatusConfirmed || proposal.Status == confirmationStatusAccountingConfirmed || proposal.Status == confirmationStatusRestored || proposal.Status == confirmationStatusManualRequired {
		if proposal.IdempotencyHash == inputHash && proposal.NewSessionID == input.NewSessionID {
			return confirmationResult(proposal, false), nil
		}
		return nil, ErrConfirmationReplay
	}
	if !time.Now().Before(proposal.ExpiresAt) {
		proposal.Status = confirmationStatusExpired
		return nil, ErrConfirmationExpired
	}
	if proposal.Status != confirmationStatusPending || input.NewSessionID == "" || input.NewSessionID == proposal.PreviousSessionID || input.IdempotencyKey == "" || (!input.TargetCreatedAt.IsZero() && input.TargetCreatedAt.Before(proposal.Record.CreatedAt)) {
		return nil, ErrConfirmationInvalid
	}
	proposal.Status = confirmationStatusAccountingConfirmed
	proposal.NewSessionID = input.NewSessionID
	proposal.IdempotencyHash = inputHash
	proposal.ConfirmedAt = time.Now().UTC()
	proposal.Record.NewSessionID = input.NewSessionID
	proposal.RestoreStatus = confirmationStatusAccountingConfirmed
	return confirmationResult(proposal, true), nil
}

func (s *MemoryConfirmationStore) GetGoalRestoreState(_ context.Context, proposalID, tenantID string) (*GoalRestoreState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	proposal := s.byID[proposalID]
	if proposal == nil || proposal.TenantID != tenantID {
		return nil, ErrConfirmationInvalid
	}
	return &GoalRestoreState{ProposalID: proposal.ID, TenantID: proposal.TenantID, NewSessionID: proposal.NewSessionID, Status: proposal.RestoreStatus, GoalState: cloneGoalState(proposal.GoalState), RestoreError: proposal.RestoreError, RestoreAttemptedAt: proposal.RestoreAttemptedAt, RestoredAt: proposal.RestoredAt}, nil
}

func (s *MemoryConfirmationStore) MarkGoalRestoreAttempt(_ context.Context, proposalID, tenantID, restoreError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	proposal := s.byID[proposalID]
	if proposal == nil || proposal.TenantID != tenantID {
		return ErrConfirmationInvalid
	}
	if proposal.Status == confirmationStatusAccountingConfirmed || proposal.Status == legacyConfirmationStatusConfirmed {
		proposal.RestoreStatus = confirmationStatusAccountingConfirmed
		proposal.RestoreError = truncateRunes(restoreError, 512)
		proposal.RestoreAttemptedAt = time.Now().UTC()
	}
	return nil
}

func (s *MemoryConfirmationStore) MarkGoalRestored(_ context.Context, proposalID, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	proposal := s.byID[proposalID]
	if proposal == nil || proposal.TenantID != tenantID {
		return ErrConfirmationInvalid
	}
	if proposal.Status == confirmationStatusAccountingConfirmed || proposal.Status == legacyConfirmationStatusConfirmed {
		proposal.Status = confirmationStatusRestored
		proposal.RestoreStatus = confirmationStatusRestored
		proposal.RestoredAt = time.Now().UTC()
		proposal.RestoreError = ""
	}
	return nil
}

func (s *MemoryConfirmationStore) MarkGoalRestoreManualRequired(_ context.Context, proposalID, tenantID, restoreError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	proposal := s.byID[proposalID]
	if proposal == nil || proposal.TenantID != tenantID {
		return ErrConfirmationInvalid
	}
	if proposal.Status == confirmationStatusAccountingConfirmed || proposal.Status == legacyConfirmationStatusConfirmed {
		proposal.Status = confirmationStatusManualRequired
		proposal.RestoreStatus = confirmationStatusManualRequired
		proposal.RestoreError = truncateRunes(restoreError, 512)
		proposal.RestoreAttemptedAt = time.Now().UTC()
	}
	return nil
}

func confirmationResult(proposal *ConfirmationProposal, first bool) *ConfirmationResult {
	return &ConfirmationResult{ProposalID: proposal.ID, PreviousSessionID: proposal.PreviousSessionID, NewSessionID: proposal.NewSessionID, ConfirmedAt: proposal.ConfirmedAt, FirstConfirmation: first, Record: proposal.Record, GoalState: cloneGoalState(proposal.GoalState), RestoreStatus: proposal.RestoreStatus}
}
