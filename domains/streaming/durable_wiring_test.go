package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/secret"
)

type captureDurableStore struct {
	calls        int
	checkpoints  []CommitState
	reschedules  []time.Time
	commitBody   []byte
	commitType   string
	commitReason string
	got          DurableCreateRequest
	err          error
}

func (s *captureDurableStore) CreateAndClaim(_ context.Context, request DurableCreateRequest) (DurableLease, error) {
	s.calls++
	s.got = request
	if s.err != nil {
		return DurableLease{}, s.err
	}
	return DurableLease{TaskID: request.TaskID, TenantID: request.TenantID, RequestHash: request.RequestHash,
		LeaseOwner: "gateway/request", FencingToken: 1}, nil
}
func (s *captureDurableStore) Checkpoint(_ context.Context, _ DurableLease, state CommitState) error {
	s.checkpoints = append(s.checkpoints, state)
	return nil
}
func (s *captureDurableStore) Reschedule(_ context.Context, _ DurableLease, at time.Time, _ string) error {
	s.reschedules = append(s.reschedules, at)
	return nil
}
func (s *captureDurableStore) Commit(_ context.Context, _ DurableLease, body []byte, contentType string, decision TaskDecision) error {
	s.commitBody = append([]byte(nil), body...)
	s.commitType = contentType
	s.commitReason = decision.Reason
	return nil
}

func TestPrepareDurableRequestFlagOffIsNoOp(t *testing.T) {
	h := &ChatHandler{}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Durable", "true")
	got, err := h.prepareDurableRequest(r, DurableSnapshotInput{Body: []byte(`{"model":"gpt"}`)})
	if err != nil {
		t.Fatalf("prepareDurableRequest: %v", err)
	}
	if got != r {
		t.Fatal("flag-off path must return the original request pointer")
	}
}

func TestPrepareDurableRequestRequiresHeaderAndTenantGate(t *testing.T) {
	store := &captureDurableStore{}
	h := &ChatHandler{}
	h.SetRequestDurability(store, durableTestKeyring(t), func(tenantID string) bool { return tenantID == "tenant-a" }, time.Hour)
	in := validDurableSnapshotInput("openai-chat", []byte(`{"model":"gpt"}`))

	withoutHeader := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	got, err := h.prepareDurableRequest(withoutHeader, in)
	if err != nil || got != withoutHeader || store.calls != 0 {
		t.Fatalf("without header: got=%p err=%v calls=%d", got, err, store.calls)
	}

	denied := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	denied.Header.Set("X-Gw-Durable", "true")
	in.KeyInfo.TenantID = "tenant-b"
	in.Session.TenantID = "tenant-b"
	if _, err := h.prepareDurableRequest(denied, in); !errors.Is(err, ErrDurableNotAuthorized) {
		t.Fatalf("tenant denial error = %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("denied request reached store: %d", store.calls)
	}
}

func TestPrepareDurableRequestRequiresPersistedOwnedSession(t *testing.T) {
	store := &captureDurableStore{}
	h := &ChatHandler{}
	h.SetRequestDurability(store, durableTestKeyring(t), func(string) bool { return true }, time.Hour)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Durable", "true")
	in := validDurableSnapshotInput("openai-chat", []byte(`{"model":"gpt"}`))
	in.Session = nil
	if _, err := h.prepareDurableRequest(r, in); !errors.Is(err, ErrDurableSession) {
		t.Fatalf("session error = %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("invalid session reached store: %d", store.calls)
	}
}

func TestPrepareDurableRequestThreeProtocolsUseRequestAAD(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		protocol string
	}{
		{name: "chat", path: "/v1/chat/completions", protocol: "openai-chat"},
		{name: "messages", path: "/v1/messages", protocol: "anthropic-messages"},
		{name: "responses", path: "/v1/responses", protocol: "openai-responses"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kr := durableTestKeyring(t)
			store := &captureDurableStore{}
			h := &ChatHandler{}
			h.SetRequestDurability(store, kr, func(string) bool { return true }, time.Hour)
			body := []byte(`{"model":"gpt","messages":[{"role":"user","content":"hi"}]}`)
			r := httptest.NewRequest("POST", tt.path, nil)
			r.Header.Set("X-Gw-Durable", "true")
			r.Header.Set("X-Request-Id", "req-1")

			got, err := h.prepareDurableRequest(r, validDurableSnapshotInput(tt.protocol, body))
			if err != nil {
				t.Fatalf("prepareDurableRequest: %v", err)
			}
			lease, ok := DurableLeaseFromContext(got.Context())
			if !ok || lease.TaskID != store.got.TaskID || lease.FencingToken != 1 {
				t.Fatalf("lease = %+v ok=%v", lease, ok)
			}
			binding := secret.AADBinding{TenantID: store.got.TenantID, TaskID: store.got.TaskID, RequestHash: store.got.RequestHash}
			plaintext, keyID, err := secret.DecryptWithAAD(store.got.SnapshotCiphertext, kr, secret.AADDomainDurableRequest, binding)
			if err != nil {
				t.Fatalf("decrypt request snapshot: %v", err)
			}
			if keyID != store.got.EncryptionKeyID {
				t.Fatalf("key id = %q, record = %q", keyID, store.got.EncryptionKeyID)
			}
			if _, _, err := secret.DecryptWithAAD(store.got.SnapshotCiphertext, kr, secret.AADDomainDurableResult, binding); !errors.Is(err, secret.ErrAADMismatch) {
				t.Fatalf("cross-domain decrypt error = %v", err)
			}
			var snapshot durableRequestSnapshotV1
			if err := json.Unmarshal(plaintext, &snapshot); err != nil {
				t.Fatalf("unmarshal snapshot: %v", err)
			}
			if snapshot.Endpoint != tt.path || snapshot.ClientProtocol != tt.protocol {
				t.Fatalf("snapshot endpoint/protocol = %q/%q", snapshot.Endpoint, snapshot.ClientProtocol)
			}
			if !bytes.Equal(snapshot.NormalizedBody, body) {
				t.Fatalf("snapshot body = %s, want %s", snapshot.NormalizedBody, body)
			}
		})
	}
}

func validDurableSnapshotInput(protocol string, body []byte) DurableSnapshotInput {
	keyInfo := &authentication.KeyInfo{ID: 7, TenantID: "tenant-a", ApplicationID: 11}
	return DurableSnapshotInput{
		Protocol:    protocol,
		RequestID:   "req-1",
		ClientModel: "gpt",
		Body:        body,
		KeyInfo:     keyInfo,
		Session:     &session.Session{SessionID: "gw_session", APIKeyID: keyInfo.ID, TenantID: keyInfo.TenantID},
		ClientIdentity: identity.ClientIdentity{
			IdentityHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
	}
}

func durableTestKeyring(t *testing.T) *secret.Keyring {
	t.Helper()
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	kr, err := secret.NewKeyring(map[string][32]byte{"test": key}, "test")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return kr
}
