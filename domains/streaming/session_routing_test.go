package streaming

import (
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
