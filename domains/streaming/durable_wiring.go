package streaming

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/secret"
)

const durableRequestSnapshotVersion = 1

var (
	ErrDurableNotAuthorized = errors.New("durable request is not authorized")
	ErrDurableSession       = errors.New("durable request requires a persisted owned session")
)

// DurableRequestStore is the narrow request-path adapter for the durable
// repository. The production adapter maps this call onto
// durable.Repository.CreateAndClaim.
type DurableRequestStore interface {
	CreateAndClaim(ctx context.Context, request DurableCreateRequest) (DurableLease, error)
}

// DurableLifecycleStore is the optional write-ahead/terminal surface used by
// the durable coordinator. Implementations must fence every mutation.
type DurableLifecycleStore interface {
	DurableRequestStore
	Checkpoint(ctx context.Context, lease DurableLease, state CommitState) error
	Reschedule(ctx context.Context, lease DurableLease, nextRetryAt time.Time, reason string) error
	Commit(ctx context.Context, lease DurableLease, body []byte, contentType string, decision TaskDecision) error
}

type DurableCreateRequest struct {
	TaskID             string
	TenantID           string
	RequestID          string
	SessionID          string
	RequestHash        string
	Protocol           string
	Endpoint           string
	SnapshotCiphertext string
	SnapshotVersion    int
	EncryptionKeyID    string
	APIKeyID           int
	ApplicationID      int
	DeadlineAt         time.Time
}

type DurableLease struct {
	TaskID       string
	TenantID     string
	RequestHash  string
	LeaseOwner   string
	FencingToken int64
}

type DurableSnapshotInput struct {
	Protocol       string
	RequestID      string
	ClientModel    string
	Body           []byte
	KeyInfo        *authentication.KeyInfo
	Session        *session.Session
	ClientIdentity identity.ClientIdentity
	ToolsRequested bool
	ResponseFormat bool
	Multimodal     bool
}

type durableRequestSnapshotV1 struct {
	Version        int             `json:"version"`
	Endpoint       string          `json:"endpoint"`
	ClientProtocol string          `json:"client_protocol"`
	ClientModel    string          `json:"client_model"`
	NormalizedBody json.RawMessage `json:"normalized_body"`
	RequestHash    string          `json:"request_hash"`
	APIKeyID       int             `json:"api_key_id"`
	TenantID       string          `json:"tenant_id"`
	ApplicationID  int             `json:"application_id"`
	SessionID      string          `json:"session_id"`
	SessionSource  string          `json:"session_source"`
	IdentityHash   string          `json:"identity_hash"`
	ClientProfile  string          `json:"client_profile,omitempty"`
	ToolsRequested bool            `json:"tools_requested"`
	ResponseFormat bool            `json:"response_format_requested"`
	Multimodal     bool            `json:"multimodal"`
	SurvivalPolicy string          `json:"survival_policy"`
	RequestID      string          `json:"request_id"`
	OriginalTaskID string          `json:"original_task_id,omitempty"`
	DurableTaskID  string          `json:"durable_task_id"`
}

type durableLeaseContextKey struct{}

func (h *ChatHandler) SetRequestDurability(
	store DurableRequestStore,
	keyring *secret.Keyring,
	tenantAllowed func(tenantID string) bool,
	deadline time.Duration,
) {
	h.durableStore = store
	h.durableKeyring = keyring
	h.durableTenantAllowed = tenantAllowed
	h.durableDeadline = deadline
}

func DurableLeaseFromContext(ctx context.Context) (DurableLease, bool) {
	if ctx == nil {
		return DurableLease{}, false
	}
	lease, ok := ctx.Value(durableLeaseContextKey{}).(DurableLease)
	return lease, ok
}

func durableRequested(r *http.Request) bool {
	if r == nil {
		return false
	}
	v := strings.TrimSpace(r.Header.Get("X-Gw-Durable"))
	return strings.EqualFold(v, "true") || v == "1"
}

func durableSnapshotFlags(body []byte) (responseFormat, multimodal bool) {
	if len(body) == 0 {
		return false, false
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) == nil {
		_, responseFormat = raw["response_format"]
	}
	return responseFormat, detectRequestModality(body) != modalityText
}

// prepareDurableRequest runs after auth/session/tool normalization and before
// candidate lookup. The disabled/no-header path returns the exact request
// pointer without hashing, marshaling, encryption or store calls.
func (h *ChatHandler) prepareDurableRequest(r *http.Request, in DurableSnapshotInput) (*http.Request, error) {
	if h == nil || h.durableStore == nil || !durableRequested(r) {
		return r, nil
	}
	if h.durableKeyring == nil || h.durableTenantAllowed == nil || in.KeyInfo == nil {
		return r, ErrDurableNotAuthorized
	}
	if !h.durableTenantAllowed(in.KeyInfo.TenantID) {
		return r, ErrDurableNotAuthorized
	}
	if in.Session == nil || in.Session.SessionID == "" ||
		in.Session.APIKeyID != in.KeyInfo.ID || in.Session.TenantID != in.KeyInfo.TenantID {
		return r, ErrDurableSession
	}

	hashBytes := sha256.Sum256(in.Body)
	requestHash := hex.EncodeToString(hashBytes[:])
	taskID := uuid.NewString()
	requestID := strings.TrimSpace(in.RequestID)
	if requestID == "" {
		return r, errors.New("durable request requires request id")
	}
	snapshot := durableRequestSnapshotV1{
		Version:        durableRequestSnapshotVersion,
		Endpoint:       r.URL.Path,
		ClientProtocol: in.Protocol,
		ClientModel:    in.ClientModel,
		NormalizedBody: append(json.RawMessage(nil), in.Body...),
		RequestHash:    requestHash,
		APIKeyID:       in.KeyInfo.ID,
		TenantID:       in.KeyInfo.TenantID,
		ApplicationID:  in.KeyInfo.ApplicationID,
		SessionID:      in.Session.SessionID,
		SessionSource:  "server_persisted",
		IdentityHash:   in.ClientIdentity.IdentityHash,
		ClientProfile:  in.ClientIdentity.Fingerprint.ClientProfile,
		ToolsRequested: in.ToolsRequested,
		ResponseFormat: in.ResponseFormat,
		Multimodal:     in.Multimodal,
		SurvivalPolicy: "request-survival:v1",
		RequestID:      requestID,
		OriginalTaskID: strings.TrimSpace(r.Header.Get("X-Gw-Task-Id")),
		DurableTaskID:  taskID,
	}
	plaintext, err := json.Marshal(snapshot)
	if err != nil {
		return r, fmt.Errorf("marshal durable request snapshot: %w", err)
	}
	binding := secret.AADBinding{TenantID: in.KeyInfo.TenantID, TaskID: taskID, RequestHash: requestHash}
	ciphertext, keyID, err := secret.EncryptWithAAD(
		plaintext, h.durableKeyring, secret.AADDomainDurableRequest, binding,
	)
	if err != nil {
		return r, fmt.Errorf("encrypt durable request snapshot: %w", err)
	}
	deadline := h.durableDeadline
	if deadline <= 0 {
		deadline = 24 * time.Hour
	}
	lease, err := h.durableStore.CreateAndClaim(r.Context(), DurableCreateRequest{
		TaskID:             taskID,
		TenantID:           in.KeyInfo.TenantID,
		RequestID:          requestID,
		SessionID:          in.Session.SessionID,
		RequestHash:        requestHash,
		Protocol:           in.Protocol,
		Endpoint:           r.URL.Path,
		SnapshotCiphertext: ciphertext,
		SnapshotVersion:    durableRequestSnapshotVersion,
		EncryptionKeyID:    keyID,
		APIKeyID:           in.KeyInfo.ID,
		ApplicationID:      in.KeyInfo.ApplicationID,
		DeadlineAt:         time.Now().Add(deadline),
	})
	if err != nil {
		return r, fmt.Errorf("create and claim durable request: %w", err)
	}
	if lease.TaskID == "" {
		lease.TaskID = taskID
	}
	if lease.TaskID != taskID {
		return r, errors.New("create and claim durable request: task id mismatch")
	}
	lease.TenantID = in.KeyInfo.TenantID
	lease.RequestHash = requestHash
	return r.WithContext(context.WithValue(r.Context(), durableLeaseContextKey{}, lease)), nil
}
