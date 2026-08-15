// Package durabletask persists encrypted, fenced durable LLM work in PostgreSQL.
package durabletask

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/secret"
)

const (
	// SnapshotVersionV1 identifies the first durable request snapshot schema.
	SnapshotVersionV1 = 1
)

var (
	// ErrUnknownSnapshotVersion reports a snapshot schema the package cannot decode.
	ErrUnknownSnapshotVersion = errors.New("durabletask: unknown request snapshot version")
	// ErrSnapshotBindingMismatch reports that encrypted snapshot identity does not match its AAD binding.
	ErrSnapshotBindingMismatch = errors.New("durabletask: request snapshot binding mismatch")
)

// ClientIdentityV1 contains stable, non-secret identity fields derived after authentication.
type ClientIdentityV1 struct {
	Subject string `json:"subject,omitempty"`
	Issuer  string `json:"issuer,omitempty"`
	Plan    string `json:"plan,omitempty"`
}

// RequestSafetyV1 freezes request features that constrain safe recovery.
type RequestSafetyV1 struct {
	HasTools          bool `json:"has_tools"`
	HasResponseFormat bool `json:"has_response_format"`
	HasMultimodal     bool `json:"has_multimodal"`
}

// DurableRequestSnapshotV1 is the post-authentication, normalized request needed to rebuild an attempt.
// It deliberately excludes credentials, provider candidates, cookies, authorization headers, and IP headers.
type DurableRequestSnapshotV1 struct {
	Version         int              `json:"version"`
	TaskID          string           `json:"task_id"`
	RequestID       string           `json:"request_id"`
	ParentRequestID string           `json:"parent_request_id,omitempty"`
	RequestHash     string           `json:"request_hash"`
	TenantID        string           `json:"tenant_id"`
	ApplicationID   string           `json:"application_id,omitempty"`
	SessionID       string           `json:"session_id,omitempty"`
	SessionSource   string           `json:"session_source,omitempty"`
	Endpoint        string           `json:"endpoint"`
	ClientProtocol  string           `json:"client_protocol"`
	ClientModel     string           `json:"client_model,omitempty"`
	NormalizedBody  json.RawMessage  `json:"normalized_body"`
	BodyHash        string           `json:"body_hash,omitempty"`
	APIKeyID        string           `json:"api_key_id,omitempty"`
	ClientIdentity  ClientIdentityV1 `json:"client_identity"`
	Safety          RequestSafetyV1  `json:"safety"`
	PolicyVersion   string           `json:"policy_version,omitempty"`
	Policy          json.RawMessage  `json:"policy,omitempty"`
}

// EncryptRequestSnapshotV1 serializes and encrypts a V1 snapshot in the durable-request AAD domain.
func EncryptRequestSnapshotV1(snapshot DurableRequestSnapshotV1, kr *secret.Keyring) (ciphertext, keyID string, err error) {
	if err := validateSnapshotV1(snapshot); err != nil {
		return "", "", err
	}
	plaintext, err := json.Marshal(snapshot)
	if err != nil {
		return "", "", fmt.Errorf("durabletask: marshal request snapshot: %w", err)
	}
	ciphertext, keyID, err = secret.EncryptWithAAD(plaintext, kr, secret.AADDomainDurableRequest, snapshotAAD(snapshot))
	if err != nil {
		return "", "", fmt.Errorf("durabletask: encrypt request snapshot: %w", err)
	}
	return ciphertext, keyID, nil
}

// DecryptRequestSnapshotV1 decrypts a durable-request envelope and validates its database and embedded versions.
func DecryptRequestSnapshotV1(ciphertext string, snapshotVersion int, kr *secret.Keyring, binding secret.AADBinding) (DurableRequestSnapshotV1, string, error) {
	if snapshotVersion != SnapshotVersionV1 {
		return DurableRequestSnapshotV1{}, "", fmt.Errorf("%w: %d", ErrUnknownSnapshotVersion, snapshotVersion)
	}
	plaintext, keyID, err := secret.DecryptWithAAD(ciphertext, kr, secret.AADDomainDurableRequest, binding)
	if err != nil {
		return DurableRequestSnapshotV1{}, "", fmt.Errorf("durabletask: decrypt request snapshot: %w", err)
	}

	var snapshot DurableRequestSnapshotV1
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return DurableRequestSnapshotV1{}, "", fmt.Errorf("durabletask: decode request snapshot V1: %w", err)
	}
	if err := validateSnapshotV1(snapshot); err != nil {
		return DurableRequestSnapshotV1{}, "", err
	}
	if snapshotAAD(snapshot) != binding {
		return DurableRequestSnapshotV1{}, "", ErrSnapshotBindingMismatch
	}
	return snapshot, keyID, nil
}

func validateSnapshotV1(snapshot DurableRequestSnapshotV1) error {
	if snapshot.Version != SnapshotVersionV1 {
		return fmt.Errorf("%w: %d", ErrUnknownSnapshotVersion, snapshot.Version)
	}
	if snapshot.TaskID == "" || snapshot.RequestID == "" || snapshot.RequestHash == "" || snapshot.TenantID == "" {
		return errors.New("durabletask: task_id, request_id, request_hash, and tenant_id are required")
	}
	if snapshot.Endpoint == "" || snapshot.ClientProtocol == "" {
		return errors.New("durabletask: endpoint and client_protocol are required")
	}
	if len(snapshot.NormalizedBody) == 0 || !json.Valid(snapshot.NormalizedBody) {
		return errors.New("durabletask: normalized_body must be valid JSON")
	}
	if len(snapshot.Policy) > 0 && !json.Valid(snapshot.Policy) {
		return errors.New("durabletask: policy must be valid JSON")
	}
	return nil
}

func snapshotAAD(snapshot DurableRequestSnapshotV1) secret.AADBinding {
	return secret.AADBinding{
		TenantID:    snapshot.TenantID,
		TaskID:      snapshot.TaskID,
		RequestHash: snapshot.RequestHash,
	}
}
