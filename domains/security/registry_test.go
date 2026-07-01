package security

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

type stubPlugin struct {
	name   string
	allow  bool
	err    error
	fixedV *governance.Verdict
}

func (s *stubPlugin) Name() string      { return s.name }
func (s *stubPlugin) Direction() string { return DirectionInput }
func (s *stubPlugin) Inspect(_ context.Context, _ *domain.PipelineRequest) (*governance.Verdict, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.fixedV != nil {
		return s.fixedV, nil
	}
	return &governance.Verdict{
		PluginName: s.name,
		Allow:      s.allow,
		Severity:   0,
		Code:       "ok",
	}, nil
}

func TestRegistry_NilSafe(t *testing.T) {
	var r *Registry
	if err := r.Register(&stubPlugin{name: "x"}); err == nil {
		t.Fatal("nil registry Register should error")
	}
	if got := r.List(); got != nil {
		t.Fatalf("nil registry List = %v, want nil", got)
	}
	if got := r.Count(); got != 0 {
		t.Fatalf("nil registry Count = %d, want 0", got)
	}
}

func TestRegistry_RegisterAndList(t *testing.T) {
	r := NewRegistry()
	mustReg(t, r, &stubPlugin{name: "a"})
	mustReg(t, r, &stubPlugin{name: "b"})
	if got := r.Count(); got != 2 {
		t.Fatalf("Count = %d, want 2", got)
	}
	list := r.List()
	if len(list) != 2 || list[0].Name() != "a" || list[1].Name() != "b" {
		t.Fatalf("List order wrong: %+v", list)
	}
}

func TestRegistry_DuplicateNameErrors(t *testing.T) {
	r := NewRegistry()
	mustReg(t, r, &stubPlugin{name: "dup"})
	if err := r.Register(&stubPlugin{name: "dup"}); err == nil {
		t.Fatal("duplicate name should error")
	}
}

func TestRegistry_MustRegisterPanicsOnDup(t *testing.T) {
	r := NewRegistry()
	mustReg(t, r, &stubPlugin{name: "x"})
	defer func() {
		if recover() == nil {
			t.Fatal("MustRegister on duplicate should panic")
		}
	}()
	r.MustRegister(&stubPlugin{name: "x"})
}

func TestRegistry_RunAll_CollectsVerdicts(t *testing.T) {
	r := NewRegistry()
	mustReg(t, r, &stubPlugin{name: "ok1", allow: true})
	mustReg(t, r, &stubPlugin{name: "ok2", allow: true})
	mustReg(t, r, &stubPlugin{name: "block", allow: false})

	env := &domain.PipelineRequest{TenantID: "t1"}
	vs, err := r.RunAll(context.Background(), env, Scope{})
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if len(vs) != 3 {
		t.Fatalf("verdicts len = %d, want 3", len(vs))
	}
	if vs[0].PluginName != "ok1" || vs[2].PluginName != "block" {
		t.Fatalf("verdict order wrong: %+v", vs)
	}
}

func TestRegistry_RunAll_PluginErrorBecomesVerdict(t *testing.T) {
	r := NewRegistry()
	mustReg(t, r, &stubPlugin{name: "broken", err: errors.New("boom")})
	mustReg(t, r, &stubPlugin{name: "ok", allow: true})

	vs, err := r.RunAll(context.Background(), &domain.PipelineRequest{}, Scope{})
	if err != nil {
		t.Fatalf("RunAll should not propagate single plugin error: %v", err)
	}
	if len(vs) != 2 {
		t.Fatalf("verdicts len = %d, want 2", len(vs))
	}
	if vs[0].Code != "plugin_error" || vs[0].Allow {
		t.Fatalf("broken plugin verdict wrong: %+v", vs[0])
	}
}

func TestRegistry_RunAll_NilEnvelopeErrors(t *testing.T) {
	r := NewRegistry()
	if _, err := r.RunAll(context.Background(), nil, Scope{}); err == nil {
		t.Fatal("RunAll with nil envelope should error")
	}
}

func TestRegistry_Scope_TenantFilter(t *testing.T) {
	r := NewRegistry()
	mustReg(t, r, &stubPlugin{name: "p", allow: true})

	env := &domain.PipelineRequest{TenantID: "t1"}
	vs, _ := r.RunAll(context.Background(), env, Scope{TenantIDs: []string{"t1"}})
	if len(vs) != 1 {
		t.Fatalf("matching tenant: vs len = %d, want 1", len(vs))
	}
	vs, _ = r.RunAll(context.Background(), env, Scope{TenantIDs: []string{"other"}})
	if len(vs) != 0 {
		t.Fatalf("non-matching tenant: vs len = %d, want 0", len(vs))
	}
}

func TestRegistry_Scope_EmptyMeansAll(t *testing.T) {
	r := NewRegistry()
	mustReg(t, r, &stubPlugin{name: "p", allow: true})
	vs, _ := r.RunAll(context.Background(), &domain.PipelineRequest{TenantID: "anything"}, Scope{})
	if len(vs) != 1 {
		t.Fatalf("empty scope: vs len = %d, want 1", len(vs))
	}
}

func mustReg(t *testing.T, r *Registry, p Plugin) {
	t.Helper()
	if err := r.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
}
