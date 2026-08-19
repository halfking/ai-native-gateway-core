package token

import (
	"testing"
	"time"
)

var testSecret = []byte("this-is-a-32-byte-shared-secret-1234")

func mkIssuer(name string) Issuer { return Issuer{Name: name, Secret: testSecret} }

func TestSignAndVerify_RoundTrip(t *testing.T) {
	iss := mkIssuer("memora")
	c := &Claims{
		Subject:  "u-1",
		Audience: "memora-api",
		UserID:   "u-1",
		TenantID: "default",
		Roles:    []string{"user"},
	}
	raw, err := SignHS256(iss, c, time.Minute)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	issuers := []Issuer{iss, mkIssuer("llm-gateway"), mkIssuer("pocket")}
	got, err := VerifyMultiIssuer(raw, issuers, "memora-api")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Issuer != "memora" {
		t.Errorf("iss = %q, want memora", got.Issuer)
	}
	if got.Subject != "u-1" {
		t.Errorf("sub = %q, want u-1", got.Subject)
	}
	if got.UserID != "u-1" {
		t.Errorf("user_id = %q, want u-1", got.UserID)
	}
	if got.TenantID != "default" {
		t.Errorf("tenant_id = %q, want default", got.TenantID)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "user" {
		t.Errorf("roles = %v, want [user]", got.Roles)
	}
}

func TestVerify_RejectsUnknownIssuer(t *testing.T) {
	signer := mkIssuer("rogue")
	c := &Claims{Subject: "u-1", Audience: "x"}
	raw, err := SignHS256(signer, c, time.Minute)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	allowlist := []Issuer{mkIssuer("memora"), mkIssuer("llm-gateway")}
	if _, err := VerifyMultiIssuer(raw, allowlist, "x"); err == nil {
		t.Fatal("expected error for unknown issuer")
	}
}

func TestVerify_RejectsWrongAudience(t *testing.T) {
	iss := mkIssuer("memora")
	c := &Claims{Subject: "u-1", Audience: "wrong-api"}
	raw, _ := SignHS256(iss, c, time.Minute)

	issuers := []Issuer{iss}
	if _, err := VerifyMultiIssuer(raw, issuers, "memora-api"); err == nil {
		t.Fatal("expected error for audience mismatch")
	}
}

func TestVerify_RejectsEmptyAudienceParameter(t *testing.T) {
	iss := mkIssuer("memora")
	c := &Claims{Subject: "u-1", Audience: "anything"}
	raw, _ := SignHS256(iss, c, time.Minute)

	if _, err := VerifyMultiIssuer(raw, []Issuer{iss}, ""); err == nil {
		t.Fatal("expected empty expected audience rejection")
	}
}

func TestVerify_RejectsExpired(t *testing.T) {
	iss := mkIssuer("memora")
	c := &Claims{Subject: "u-1", Audience: "memora-api"}
	raw, _ := SignHS256(iss, c, -time.Second) // 已过期

	if _, err := VerifyMultiIssuer(raw, []Issuer{iss}, "memora-api"); err == nil {
		t.Fatal("expected expired error")
	}
}

func TestVerify_RejectsTampered(t *testing.T) {
	iss := mkIssuer("memora")
	c := &Claims{Subject: "u-1", Audience: "memora-api"}
	raw, _ := SignHS256(iss, c, time.Minute)
	tampered := raw[:len(raw)-2] + "xx"

	if _, err := VerifyMultiIssuer(tampered, []Issuer{iss}, "memora-api"); err == nil {
		t.Fatal("expected tamper detection")
	}
}

func TestVerify_DifferentSecretFails(t *testing.T) {
	issA := Issuer{Name: "memora", Secret: []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}
	issB := Issuer{Name: "memora", Secret: []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")}

	c := &Claims{Subject: "u-1", Audience: "x"}
	raw, _ := SignHS256(issA, c, time.Minute)

	if _, err := VerifyMultiIssuer(raw, []Issuer{issB}, "x"); err == nil {
		t.Fatal("expected different-secret failure")
	}
}

func TestAllowlist_RejectsShortSecret(t *testing.T) {
	if _, err := Allowlist("memora", []byte("short")); err == nil {
		t.Fatal("expected short-secret rejection")
	}
}

func TestAllowlist_RejectsEmptyList(t *testing.T) {
	if _, err := Allowlist("", testSecret); err == nil {
		t.Fatal("expected empty-list rejection")
	}
}

func TestAllowlist_ParsesAndDedups(t *testing.T) {
	out, err := Allowlist(" memora , llm-gateway , memora ", testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d issuers, want 2 (dedup)", len(out))
	}
	if out[0].Name != "memora" || out[1].Name != "llm-gateway" {
		t.Errorf("got %+v", out)
	}
}

func TestClaims_ExtraRoundTrip(t *testing.T) {
	iss := mkIssuer("memora")
	c := &Claims{
		Subject:  "u-1",
		Audience: "memora-api",
		Extra: map[string]any{
			"workspace_id": "ws-1",
			"isAdmin":      true,
			"legacy_field": 42,
		},
	}
	raw, err := SignHS256(iss, c, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyMultiIssuer(raw, []Issuer{iss}, "memora-api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Extra["workspace_id"] != "ws-1" {
		t.Errorf("Extra[workspace_id] = %v", got.Extra["workspace_id"])
	}
	if got.Extra["isAdmin"] != true {
		t.Errorf("Extra[isAdmin] = %v", got.Extra["isAdmin"])
	}
	if got.Extra["legacy_field"] != float64(42) { // JSON 数字反序列化为 float64
		t.Errorf("Extra[legacy_field] = %v (%T)", got.Extra["legacy_field"], got.Extra["legacy_field"])
	}
}
