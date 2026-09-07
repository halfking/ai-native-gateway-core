package streaming

import (
	"net/http"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

func TestExtractSessionIDFromBody(t *testing.T) {
	resetSessionFieldPriorityForTest()
	defer resetSessionFieldPriorityForTest()

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "sessionId root", body: `{"sessionId":"client-1"}`, want: "client-1"},
		{name: "session_id nested metadata", body: `{"metadata":{"session_id":"client-2"}}`, want: "client-2"},
		{name: "conversationId nested info", body: `{"info":{"conversationId":"conv-1"}}`, want: "conv-1"},
		{name: "thread-id nested extra", body: `{"extra":{"thread-id":"thread-1"}}`, want: "thread-1"},
		{name: "gw session normalized", body: `{"frontend":{"gwSessionId":"gw_12345678-1234-1234-1234-123456789abc"}}`, want: "gw_12345678-1234-1234-1234-123456789abc"},
		{name: "invalid control character", body: "{\"sessionId\":\"bad\\u000avalue\"}", want: ""},
		{name: "missing", body: `{"messages":[]}`, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractSessionIDFromBody([]byte(tt.body)); got != tt.want {
				t.Fatalf("extractSessionIDFromBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractSessionIDFromBody_UsesConfiguredAliases(t *testing.T) {
	resetSessionFieldPriorityForTest()
	defer resetSessionFieldPriorityForTest()
	SetSessionIDBodyKeys([]string{"workspaceId", "room_session_key"})

	if got := extractSessionIDFromBody([]byte(`{"metadata":{"workspaceId":"ws-1"}}`)); got != "ws-1" {
		t.Fatalf("extractSessionIDFromBody(workspaceId) = %q, want ws-1", got)
	}
	if got := extractSessionIDFromBody([]byte(`{"extra":{"room_session_key":"room-2"}}`)); got != "room-2" {
		t.Fatalf("extractSessionIDFromBody(room_session_key) = %q, want room-2", got)
	}
}

func TestExtractSessionIDFromBody_UsesSettingsAliases(t *testing.T) {
	resetSessionFieldPriorityForTest()
	defer resetSessionFieldPriorityForTest()
	oldGlobal := settings.Global
	t.Cleanup(func() { settings.Global = oldGlobal })

	r := settings.NewRegistry()
	r.MustRegisterSpec(settings.SessionSpecs()[0])
	r.RegisterBackend(settings.ScopePlatform, fakeSettingsBackend{store: map[string][]byte{
		"session.id_body_keys": []byte(`"workspaceId,room_session_key"`),
	}})
	settings.Global = r

	if got := extractSessionIDFromBody([]byte(`{"metadata":{"workspaceId":"ws-1"}}`)); got != "ws-1" {
		t.Fatalf("extractSessionIDFromBody(settings workspaceId) = %q, want ws-1", got)
	}
	if got := extractSessionIDFromBody([]byte(`{"extra":{"room_session_key":"room-2"}}`)); got != "room-2" {
		t.Fatalf("extractSessionIDFromBody(settings room_session_key) = %q, want room-2", got)
	}
}

func resetSessionFieldPriorityForTest() {
	SetSessionIDBodyKeys(nil)
}

type fakeSettingsBackend struct {
	store map[string][]byte
}

func (f fakeSettingsBackend) Get(_ settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}

func (f fakeSettingsBackend) Set(_ settings.Scope, _ string, _ any) ([]byte, error) {
	return nil, nil
}

func (f fakeSettingsBackend) GetTenant(_, key string) ([]byte, error) {
	return f.store[key], nil
}

func (f fakeSettingsBackend) SetTenant(_, _ string, _ any) ([]byte, error) {
	return nil, nil
}

func TestDeriveGatewaySessionID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty stays empty", in: "", want: ""},
		{name: "gw prefix untouched", in: "gw_3e64af9f-6cbb-419a-b5af-2a6db72c9899", want: "gw_3e64af9f-6cbb-419a-b5af-2a6db72c9899"},
		{name: "auto-title branch untouched", in: "gt_sess-abc", want: "gt_sess-abc"},
		{name: "auto-summary branch untouched", in: "gs_sess-abc", want: "gs_sess-abc"},
		// ZCode sends a stable bare-UUID x-session-id; it must map onto the
		// gateway namespace instead of being re-minted per request.
		{name: "bare uuid derived", in: "de9f325e-2d25-428c-917c-94b006ac761a", want: "gw_de9f325e-2d25-428c-917c-94b006ac761a"},
		{name: "opaque id derived", in: "client-conv-42", want: "gw_client-conv-42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveGatewaySessionID(tt.in); got != tt.want {
				t.Fatalf("deriveGatewaySessionID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtractSessionIDFromHeaders_LegacyIDDerivesGatewayNamespace(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Session-Id", "de9f325e-2d25-428c-917c-94b006ac761a")

	if got := extractSessionIDFromHeaders(r); got != "de9f325e-2d25-428c-917c-94b006ac761a" {
		t.Fatalf("extractSessionIDFromHeaders() = %q, want the sanitized legacy value", got)
	}
	if got := deriveGatewaySessionID(extractSessionIDFromHeaders(r)); got != "gw_de9f325e-2d25-428c-917c-94b006ac761a" {
		t.Fatalf("derived = %q, want gw_-prefixed stable id", got)
	}
}
