package capabilitygate

import "testing"

func TestCheck_NilGrantDenied(t *testing.T) {
	g := NewGate()
	if err := g.Check(nil, "session.audit"); err == nil {
		t.Fatal("nil grant must be denied")
	}
}

func TestCheck_UnknownCapabilityDenied(t *testing.T) {
	g := NewGate()
	// handler 拼错能力名 → 静默放过是最危险的失败模式，必须拒绝
	if err := g.Check(&Grant{KeyID: 1, Capabilities: []string{"session.audit"}}, "session.auditt"); err == nil {
		t.Fatal("unknown capability id must be denied even if granted")
	}
}

func TestCheck_GrantedAllowed(t *testing.T) {
	g := NewGate()
	grant := &Grant{KeyID: 7, Capabilities: []string{"session.audit"}}
	if err := g.Check(grant, "session.audit"); err != nil {
		t.Fatalf("granted capability denied: %v", err)
	}
}

func TestCheck_NotGrantedDenied(t *testing.T) {
	g := NewGate()
	grant := &Grant{KeyID: 7, Capabilities: []string{"session.audit"}}
	if err := g.Check(grant, "session.summary"); err == nil {
		t.Fatal("capability not in grant must be denied")
	}
}

func TestCheck_IncompleteClosureDenied(t *testing.T) {
	g := NewGate()
	// forensics 依赖 audit；只授 forensics 属于非法授权，整单拒绝
	grant := &Grant{KeyID: 7, Capabilities: []string{"session.forensics"}}
	if err := g.Check(grant, "session.forensics"); err == nil {
		t.Fatal("grant violating dependency closure must be denied")
	}
}

func TestCheck_EmptyGrantDenied(t *testing.T) {
	g := NewGate()
	grant := &Grant{KeyID: 7, Capabilities: nil}
	if err := g.Check(grant, "session.audit"); err == nil {
		t.Fatal("empty capabilities list must be denied")
	}
}
