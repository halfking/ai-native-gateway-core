package streaming

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
)

const (
	DurableSnapshotVersionV1  = 1
	GatewayCapabilitiesHeader = "X-Gw-Capabilities"
	CapabilityDurableRecovery = "durable-recovery"
	CapabilityStatusEvents    = "status-events"
)

// DurableRequestSnapshotV1 is the versioned, credential-free request DTO used
// by RecoveryWorker. Dynamic candidates and credentials are deliberately absent.
type DurableRequestSnapshotV1 struct {
	Version              int             `json:"version"`
	Endpoint             string          `json:"endpoint"`
	ClientProtocol       string          `json:"client_protocol"`
	ClientModel          string          `json:"client_model"`
	NormalizedBody       json.RawMessage `json:"normalized_body"`
	RequestHash          string          `json:"request_hash"`
	APIKeyID             int             `json:"api_key_id"`
	TenantID             string          `json:"tenant_id"`
	ApplicationID        int             `json:"application_id"`
	SessionID            string          `json:"session_id"`
	SessionSource        string          `json:"session_source"`
	ClientIdentityHash   string          `json:"client_identity_hash"`
	ToolsRequested       bool            `json:"tools_requested"`
	ResponseFormat       string          `json:"response_format,omitempty"`
	HasMultimodalContent bool            `json:"has_multimodal_content"`
	PolicyVersion        string          `json:"policy_version"`
	RequestID            string          `json:"request_id"`
	TaskCorrelationID    string          `json:"task_correlation_id"`
	ParentRequestID      string          `json:"parent_request_id,omitempty"`
}

func (s DurableRequestSnapshotV1) Validate() error {
	if s.Version != DurableSnapshotVersionV1 {
		return errors.New("durable snapshot: unsupported version")
	}
	switch {
	case s.Endpoint == "":
		return errors.New("durable snapshot: endpoint required")
	case s.ClientProtocol == "":
		return errors.New("durable snapshot: client protocol required")
	case s.ClientModel == "":
		return errors.New("durable snapshot: client model required")
	case len(s.NormalizedBody) == 0 || !json.Valid(s.NormalizedBody):
		return errors.New("durable snapshot: normalized JSON body required")
	case s.RequestHash == "":
		return errors.New("durable snapshot: request hash required")
	case s.APIKeyID <= 0:
		return errors.New("durable snapshot: API key ID required")
	case s.TenantID == "":
		return errors.New("durable snapshot: tenant required")
	case s.SessionID == "":
		return errors.New("durable snapshot: real session required")
	case s.ClientIdentityHash == "":
		return errors.New("durable snapshot: client identity hash required")
	case s.PolicyVersion == "":
		return errors.New("durable snapshot: policy version required")
	case s.RequestID == "" || s.TaskCorrelationID == "":
		return errors.New("durable snapshot: request/task correlation required")
	}
	return nil
}

func MarshalDurableSnapshotV1(s DurableRequestSnapshotV1) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

func UnmarshalDurableSnapshotV1(body []byte) (*DurableRequestSnapshotV1, error) {
	var envelope struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if envelope.Version != DurableSnapshotVersionV1 {
		return nil, errors.New("durable snapshot: unsupported version")
	}
	var snapshot DurableRequestSnapshotV1
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return nil, err
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

type ClientCapabilities map[string]struct{}

func ParseClientCapabilities(value string) ClientCapabilities {
	out := ClientCapabilities{}
	for _, raw := range strings.Split(value, ",") {
		token := strings.ToLower(strings.TrimSpace(raw))
		switch token {
		case CapabilityDurableRecovery, CapabilityStatusEvents:
			out[token] = struct{}{}
		}
	}
	return out
}

func (c ClientCapabilities) Has(token string) bool {
	_, ok := c[strings.ToLower(token)]
	return ok
}

func (c ClientCapabilities) Tokens() []string {
	out := make([]string, 0, len(c))
	for token := range c {
		out = append(out, token)
	}
	sort.Strings(out)
	return out
}

func PreferRespondAsync(r *http.Request) bool {
	if r == nil {
		return false
	}
	for _, preference := range strings.Split(r.Header.Get("Prefer"), ",") {
		token := strings.TrimSpace(strings.SplitN(preference, ";", 2)[0])
		if strings.EqualFold(token, "respond-async") {
			return true
		}
	}
	return false
}

func DurableRequested(r *http.Request, isStream bool) bool {
	if r == nil {
		return false
	}
	capabilities := ParseClientCapabilities(r.Header.Get(GatewayCapabilitiesHeader))
	if !capabilities.Has(CapabilityDurableRecovery) {
		return false
	}
	return isStream || PreferRespondAsync(r)
}
