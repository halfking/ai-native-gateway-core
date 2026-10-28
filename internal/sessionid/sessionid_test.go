package sessionid

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

func TestResolve_BodyBeatsHeaders(t *testing.T) {
	h := make(http.Header)
	h.Set(HeaderXGw, "gw_header-should-lose")
	h.Set("X-Session-Id", "legacy-should-lose")
	id, prov := Resolve(h, []byte(`{"session_id":"from-body"}`))
	if id != "gw_from-body" {
		t.Fatalf("id = %q, want gw_from-body", id)
	}
	if prov != ProvenanceBody {
		t.Fatalf("prov = %q, want %q", prov, ProvenanceBody)
	}
}

func TestResolve_XGwBeatsLegacy(t *testing.T) {
	h := make(http.Header)
	h.Set(HeaderXGw, "gw_canonical-1")
	h.Set("X-Session-Id", "legacy-2")
	id, prov := Resolve(h, nil)
	if id != "gw_canonical-1" {
		t.Fatalf("id = %q, want gw_canonical-1", id)
	}
	if prov != ProvenanceHeaderGw {
		t.Fatalf("prov = %q, want %q", prov, ProvenanceHeaderGw)
	}
}

func TestResolve_LegacyBareUUIDDerives(t *testing.T) {
	h := make(http.Header)
	h.Set("X-Session-Id", "de9f325e-2d25-428c-917c-94b006ac761a")
	id, prov := Resolve(h, nil)
	want := "gw_de9f325e-2d25-428c-917c-94b006ac761a"
	if id != want {
		t.Fatalf("id = %q, want %q", id, want)
	}
	if prov != ProvenanceHeaderNative {
		t.Fatalf("prov = %q, want %q", prov, ProvenanceHeaderNative)
	}
}

func TestResolve_DeepseekHarnessHeader(t *testing.T) {
	h := make(http.Header)
	h.Set("x-deepseek-harness-session-id", "dsh-conv-9")
	id, prov := Resolve(h, nil)
	if id != "gw_dsh-conv-9" {
		t.Fatalf("id = %q, want gw_dsh-conv-9", id)
	}
	if prov != ProvenanceHeaderNative {
		t.Fatalf("prov = %q, want %q", prov, ProvenanceHeaderNative)
	}
}

func TestResolve_EmptyMeansCallerDoesAssignmentOrProvisional(t *testing.T) {
	id, prov := Resolve(nil, nil)
	if id != "" || prov != ProvenanceNone {
		t.Fatalf("got (%q, %q); empty+None means caller may assignment then provisional", id, prov)
	}
	if ProvenanceAssignment == "" || ProvenanceProvisional == "" {
		t.Fatal("assignment/provisional provenance constants must stay non-empty for logs")
	}
}

func TestDeriveGatewayID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"gw_abc", "gw_abc"},
		{"gt_branch", "gt_branch"},
		{"gs_branch", "gs_branch"},
		{"de9f325e-2d25-428c-917c-94b006ac761a", "gw_de9f325e-2d25-428c-917c-94b006ac761a"},
		{"client-conv-42", "gw_client-conv-42"},
	}
	for _, tc := range cases {
		if got := DeriveGatewayID(tc.in); got != tc.want {
			t.Fatalf("DeriveGatewayID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFromHeaders_CaseInsensitiveLegacy(t *testing.T) {
	h := make(http.Header)
	h.Set("X-Session-ID", "session_root")
	id, prov := FromHeaders(h)
	if id != "session_root" || prov != ProvenanceHeaderNative {
		t.Fatalf("got (%q, %q)", id, prov)
	}
}

func TestHeaders_HotOverride(t *testing.T) {
	SetHeaderOverrides(nil)
	t.Cleanup(func() { SetHeaderOverrides(nil) })

	base := Headers()
	found := false
	for _, item := range base {
		if strings.EqualFold(item, "X-Deepseek-Harness-Session-Id") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("default table missing deepseek harness: %v", base)
	}

	SetHeaderOverrides([]string{"X-Custom-Session"})
	got := Headers()
	found = false
	for _, item := range got {
		if strings.EqualFold(item, "X-Custom-Session") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("override not merged: %v", got)
	}
	if got[0] != HeaderXGw {
		t.Fatalf("X-Gw must stay first, got %q", got[0])
	}
}

func TestHeaders_HotSettings(t *testing.T) {
	SetHeaderOverrides(nil)
	t.Cleanup(func() { SetHeaderOverrides(nil) })
	old := settings.Global
	t.Cleanup(func() { settings.Global = old })

	r := settings.NewRegistry()
	var headerSpec *settings.Spec
	for _, sp := range settings.SessionSpecs() {
		if sp.Key == "session.id_headers" {
			headerSpec = sp
			break
		}
	}
	if headerSpec == nil {
		t.Fatal("session.id_headers spec missing — register in settings/spec_session.go")
	}
	r.MustRegisterSpec(headerSpec)
	r.RegisterBackend(settings.ScopePlatform, fakeBackend{store: map[string][]byte{
		"session.id_headers": []byte(`"X-Agent-Session"`),
	}})
	settings.Global = r

	got := Headers()
	found := false
	for _, item := range got {
		if strings.EqualFold(item, "X-Agent-Session") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("settings hot header not merged: %v", got)
	}
}

func TestFromBody_ConfiguredAliases(t *testing.T) {
	SetBodyKeyOverrides([]string{"workspaceId", "room_session_key"})
	t.Cleanup(func() { SetBodyKeyOverrides(nil) })
	if got := FromBody([]byte(`{"metadata":{"workspaceId":"ws-1"}}`)); got != "ws-1" {
		t.Fatalf("workspaceId = %q", got)
	}
}

type fakeBackend struct {
	store map[string][]byte
}

func (f fakeBackend) Get(_ settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f fakeBackend) Set(_ settings.Scope, _ string, _ any) ([]byte, error) { return nil, nil }
func (f fakeBackend) GetTenant(_, key string) ([]byte, error)               { return f.store[key], nil }
func (f fakeBackend) SetTenant(_, _ string, _ any) ([]byte, error)          { return nil, nil }
