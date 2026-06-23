package domain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnvelopeBuilder_Defaults(t *testing.T) {
	env := NewEnvelopeBuilder("req-1").Build()
	if env.RequestID != "req-1" {
		t.Fatalf("RequestID = %q, want %q", env.RequestID, "req-1")
	}
	if env.CreatedAt.IsZero() {
		t.Fatal("CreatedAt should be set")
	}
}

func TestEnvelopeBuilder_WithHTTP(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := []byte(`{"model":"gpt-4o"}`)

	env := NewEnvelopeBuilder("req-2").
		WithHTTP(context.Background(), w, r, body).
		Build()

	if env.GoContext == nil {
		t.Fatal("GoContext should be set")
	}
	if env.Transport == nil {
		t.Fatal("Transport should be set")
	}
	if env.Transport.W != w {
		t.Fatal("Transport.W mismatch")
	}
	if env.Transport.R != r {
		t.Fatal("Transport.R mismatch")
	}
	if string(env.Transport.BodyBytes) != string(body) {
		t.Fatalf("BodyBytes = %q, want %q", env.Transport.BodyBytes, body)
	}
}

func TestEnvelopeBuilder_AllContexts(t *testing.T) {
	env := NewEnvelopeBuilder("req-3").
		WithGoContext(context.Background()).
		WithTransport(&TransportContext{ClientProtocol: "openai-chat"}).
		WithSecurity(&SecurityContext{Authenticated: true}).
		WithTenant(&TenantContext{ID: "tenant-1"}).
		WithTaskRoute(&TaskRouteContext{ClientModel: "gpt-4o"}).
		WithCredRoute(&CredRouteContext{KeyID: 42}).
		WithSession(&SessionContext{Key: "sess-1"}).
		WithCompression(&CompressionContext{MinSizeBytes: 1024}).
		WithCost(&CostContext{BudgetOK: true}).
		WithSummary(&SummaryContext{Truncate: true}).
		WithAudit(&AuditContext{SamplingRate: 0.1}).
		Build()

	if !env.HasTransport() {
		t.Fatal("HasTransport = false, want true")
	}
	if !env.HasSecurity() {
		t.Fatal("HasSecurity = false, want true")
	}
	if !env.HasTenant() {
		t.Fatal("HasTenant = false, want true")
	}
	if env.Transport.ClientProtocol != "openai-chat" {
		t.Fatalf("ClientProtocol = %q, want %q", env.Transport.ClientProtocol, "openai-chat")
	}
	if env.Tenant.ID != "tenant-1" {
		t.Fatalf("Tenant.ID = %q, want %q", env.Tenant.ID, "tenant-1")
	}
	if env.CredRoute.KeyID != 42 {
		t.Fatalf("CredRoute.KeyID = %d, want 42", env.CredRoute.KeyID)
	}
}

func TestExtensionsBag_IsZero(t *testing.T) {
	tests := []struct {
		name string
		bag  ExtensionsBag
		want bool
	}{
		{"empty", ExtensionsBag{}, true},
		{"empty_map_client_raw", ExtensionsBag{ClientRaw: map[string]json.RawMessage{}}, true},
		{"empty_map_headers", ExtensionsBag{Headers: map[string]string{}}, true},
		{"empty_map_custom", ExtensionsBag{Custom: map[string]any{}}, true},
		{"client_raw_populated", ExtensionsBag{ClientRaw: map[string]json.RawMessage{"k": json.RawMessage(`null`)}, Headers: map[string]string{"h": "v"}}, false},
		{"headers_populated", ExtensionsBag{Headers: map[string]string{"h": "v"}}, false},
		{"custom_populated", ExtensionsBag{Custom: map[string]any{"k": "v"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.bag.IsZero(); got != tt.want {
				t.Fatalf("IsZero() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRequestEnvelope_HasHelpers(t *testing.T) {
	env := &RequestEnvelope{}
	if env.HasTransport() {
		t.Fatal("HasTransport should be false on zero envelope")
	}
	if env.HasSecurity() {
		t.Fatal("HasSecurity should be false on zero envelope")
	}
	if env.HasTenant() {
		t.Fatal("HasTenant should be false on zero envelope")
	}

	env.Security = &SecurityContext{}
	if !env.HasSecurity() {
		t.Fatal("HasSecurity should be true after setting Security")
	}
}
