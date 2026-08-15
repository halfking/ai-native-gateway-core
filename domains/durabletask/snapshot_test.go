package durabletask

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/stretchr/testify/require"
)

func TestRequestSnapshotV1RoundTrip(t *testing.T) {
	kr := testKeyring(t)
	want := DurableRequestSnapshotV1{
		Version:         SnapshotVersionV1,
		TaskID:          "018f-task",
		RequestID:       "request-1",
		ParentRequestID: "parent-1",
		RequestHash:     "sha256:request",
		TenantID:        "tenant-text-id",
		ApplicationID:   "app-1",
		SessionID:       "session-1",
		SessionSource:   "trusted",
		Endpoint:        "/v1/responses",
		ClientProtocol:  "openai-responses",
		ClientModel:     "gpt-5",
		NormalizedBody:  json.RawMessage(`{"model":"gpt-5","stream":true}`),
		BodyHash:        "sha256:body",
		APIKeyID:        "key-42",
		ClientIdentity: ClientIdentityV1{
			Subject: "user-7",
			Issuer:  "gateway",
		},
		Safety: RequestSafetyV1{
			HasTools:          true,
			HasResponseFormat: true,
			HasMultimodal:     false,
		},
		PolicyVersion: "survival-v1",
		Policy:        json.RawMessage(`{"max_attempts":4}`),
	}

	ciphertext, keyID, err := EncryptRequestSnapshotV1(want, kr)
	require.NoError(t, err)
	require.Equal(t, "current", keyID)
	require.NotContains(t, ciphertext, string(want.NormalizedBody))

	binding := secret.AADBinding{
		TenantID:    want.TenantID,
		TaskID:      want.TaskID,
		RequestHash: want.RequestHash,
	}
	got, decryptedKeyID, err := DecryptRequestSnapshotV1(ciphertext, SnapshotVersionV1, kr, binding)
	require.NoError(t, err)
	require.Equal(t, keyID, decryptedKeyID)
	require.Equal(t, want, got)
}

func TestRequestSnapshotV1FailsClosed(t *testing.T) {
	kr := testKeyring(t)
	snapshot := DurableRequestSnapshotV1{
		Version:        SnapshotVersionV1,
		TaskID:         "task-1",
		RequestID:      "request-1",
		RequestHash:    "request-hash",
		TenantID:       "tenant-1",
		Endpoint:       "/v1/chat/completions",
		ClientProtocol: "openai-chat",
		NormalizedBody: json.RawMessage(`{"model":"test"}`),
	}
	ciphertext, _, err := EncryptRequestSnapshotV1(snapshot, kr)
	require.NoError(t, err)

	t.Run("unknown database version", func(t *testing.T) {
		_, _, err := DecryptRequestSnapshotV1(ciphertext, 2, kr, snapshotAAD(snapshot))
		require.ErrorIs(t, err, ErrUnknownSnapshotVersion)
	})

	t.Run("wrong binding", func(t *testing.T) {
		binding := snapshotAAD(snapshot)
		binding.TenantID = "other-tenant"
		_, _, err := DecryptRequestSnapshotV1(ciphertext, SnapshotVersionV1, kr, binding)
		require.ErrorIs(t, err, secret.ErrAADMismatch)
	})

	t.Run("embedded identity mismatch", func(t *testing.T) {
		other := snapshot
		other.TenantID = "other-tenant"
		plaintext, err := json.Marshal(other)
		require.NoError(t, err)
		envelope, _, err := secret.EncryptWithAAD(plaintext, kr, secret.AADDomainDurableRequest, snapshotAAD(snapshot))
		require.NoError(t, err)

		_, _, err = DecryptRequestSnapshotV1(envelope, SnapshotVersionV1, kr, snapshotAAD(snapshot))
		require.ErrorIs(t, err, ErrSnapshotBindingMismatch)
	})

	t.Run("missing key", func(t *testing.T) {
		_, _, err := DecryptRequestSnapshotV1(ciphertext, SnapshotVersionV1, nil, snapshotAAD(snapshot))
		require.True(t, errors.Is(err, secret.ErrAADNoKey))
	})
}

func testKeyring(t *testing.T) *secret.Keyring {
	t.Helper()
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	kr, err := secret.NewKeyring(map[string][32]byte{"current": key}, "current")
	require.NoError(t, err)
	return kr
}
