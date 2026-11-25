package capability

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuiltinRegistry_HasSixCapabilities(t *testing.T) {
	r := NewRegistry()
	got := r.List()
	if len(got) != 6 {
		t.Fatalf("List() = %d capabilities, want 6: %+v", len(got), got)
	}
	wantIDs := []string{
		"analytics.dimensions", "analytics.timeline",
		"session.audit", "session.cluster", "session.forensics", "session.summary",
	}
	var gotIDs []string
	for _, c := range got {
		gotIDs = append(gotIDs, c.ID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("List() IDs = %v, want %v", gotIDs, wantIDs)
	}
}

func TestBuiltinRegistry_AllProvidedByASM(t *testing.T) {
	r := NewRegistry()
	for _, c := range r.List() {
		if c.Provider != "ai-session-manager" {
			t.Errorf("capability %q provider = %q, want ai-session-manager", c.ID, c.Provider)
		}
		if strings.TrimSpace(c.Name) == "" {
			t.Errorf("capability %q missing name", c.ID)
		}
	}
}

func TestLookup_FailClosedOnUnknown(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Lookup("session.nonexistent"); err == nil {
		t.Fatal("Lookup unknown capability should fail")
	}
	if _, err := r.Lookup("SESSION.AUDIT"); err == nil {
		t.Fatal("Lookup is case-sensitive; uppercase must fail")
	}
}

func TestRegister_DuplicateRejected(t *testing.T) {
	r := NewRegistry()
	err := r.Register(Capability{ID: "session.audit", Provider: "x", Name: "dup"})
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate register err = %v, want 'already registered'", err)
	}
}

func TestRegister_InvalidIDRejected(t *testing.T) {
	r := NewRegistry()
	cases := []string{"", "UPPER.x", "with space", "trailing.", ".leading", "sub_dir", "中文"}
	for _, id := range cases {
		if err := r.Register(Capability{ID: id, Provider: "p"}); err == nil {
			t.Errorf("Register(%q) should fail", id)
		}
	}
	if err := r.Register(Capability{ID: "x", Provider: ""}); err == nil {
		t.Error("Register without provider should fail")
	}
}

func TestValidateSet_ValidAndInvalid(t *testing.T) {
	r := NewRegistry()

	// 合法：audit 单独可用
	if err := r.ValidateSet([]string{"session.audit"}); err != nil {
		t.Fatalf("ValidateSet([audit]) = %v, want nil", err)
	}
	// 合法：forensics + 依赖闭包
	if err := r.ValidateSet([]string{"session.forensics", "session.audit"}); err != nil {
		t.Fatalf("ValidateSet([forensics, audit]) = %v, want nil", err)
	}
	// 非法：依赖缺失
	if err := r.ValidateSet([]string{"session.forensics"}); err == nil {
		t.Fatal("forensics without audit should fail")
	}
	// 非法：未注册能力
	if err := r.ValidateSet([]string{"session.audit", "bogus.cap"}); err == nil {
		t.Fatal("unknown capability should fail")
	}
	// 非法：重复
	if err := r.ValidateSet([]string{"session.audit", "session.audit"}); err == nil {
		t.Fatal("duplicate should fail")
	}
	// 合法：空集
	if err := r.ValidateSet(nil); err != nil {
		t.Fatalf("ValidateSet(nil) = %v, want nil", err)
	}
}

func TestMissingDeps_ClosureAndDedup(t *testing.T) {
	r := NewRegistry()
	// cluster 依赖 summary（summary 无再上层依赖）
	got := r.MissingDeps([]string{"session.cluster"})
	want := []string{"session.summary"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MissingDeps([cluster]) = %v, want %v", got, want)
	}
	// forensics 依赖 audit
	got = r.MissingDeps([]string{"session.forensics"})
	want = []string{"session.audit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MissingDeps([forensics]) = %v, want %v", got, want)
	}
	// 闭包已满足 → 空
	if got := r.MissingDeps([]string{"session.cluster", "session.summary"}); len(got) != 0 {
		t.Fatalf("MissingDeps full closure = %v, want empty", got)
	}
	// 未知能力不计入 MissingDeps（由 ValidateSet 报错）
	if got := r.MissingDeps([]string{"bogus.cap"}); len(got) != 0 {
		t.Fatalf("MissingDeps([bogus]) = %v, want empty", got)
	}
}

func TestListByPlugin_FiltersAndSorts(t *testing.T) {
	r := NewRegistry()
	asm := r.ListByPlugin("ai-session-manager")
	if len(asm) != 6 {
		t.Fatalf("ListByPlugin(asm) = %d, want 6", len(asm))
	}
	if other := r.ListByPlugin("unknown-plugin"); len(other) != 0 {
		t.Fatalf("ListByPlugin(unknown) = %v, want empty", other)
	}
}
