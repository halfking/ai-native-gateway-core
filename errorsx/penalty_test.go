package errorsx

import (
	"testing"
)

func TestPenaltyOfClosedSet(t *testing.T) {
	seen := map[ErrorKind]bool{}
	for _, k := range AllErrorKinds() {
		if seen[k] {
			t.Fatalf("duplicate ErrorKind in AllErrorKinds: %q", k)
		}
		seen[k] = true
		_ = PenaltyOf(k) // must not panic
	}
	// 前提在场：闭集覆盖 classify.go 里每一个 Kind* 常量（反射扫包内常量
	// 过重；用字面量清单 + 「漏登即无法从 AllErrorKinds 取出」的纪律）。
	required := []ErrorKind{
		KindClientBug, KindAuth, KindQuota, KindModelNotFound,
		KindUpstreamDown, KindCircuitOpen, KindFpSlotSaturated,
	}
	for _, k := range required {
		if !seen[k] {
			t.Fatalf("AllErrorKinds missing %q", k)
		}
	}
}

func TestPenaltyOfClientBugIsNone(t *testing.T) {
	for _, k := range []ErrorKind{
		KindClientBug, KindToolCallIdMismatch, KindContextLength,
		KindContentFilter, KindUnsupportedFeature, KindConversion,
	} {
		p := PenaltyOf(k)
		if p.Target != PenaltyNone {
			t.Fatalf("PenaltyOf(%s) = %v, want none (magpie: shape/content never punish account)", k, p)
		}
		if FailoverScopeOf(p) != ScopeNone {
			t.Fatalf("FailoverScopeOf(%s) = %q, want none", k, FailoverScopeOf(p))
		}
	}
}

func TestPenaltyOfAuthIsCredentialPermanent(t *testing.T) {
	p := PenaltyOf(KindAuth)
	if p.Target != PenaltyCredential || !p.Fuse || !p.Permanent {
		t.Fatalf("PenaltyOf(auth) = %+v, want credential+fuse+permanent", p)
	}
}

func TestPenaltyOfModelNotFoundIsBinding(t *testing.T) {
	p := PenaltyOf(KindModelNotFound)
	if p.Target != PenaltyBinding {
		t.Fatalf("PenaltyOf(model_not_found) = %+v, want binding", p)
	}
	if FailoverScopeOf(p) != ScopeModel {
		t.Fatalf("scope = %q, want credential_model", FailoverScopeOf(p))
	}
}

func TestDecideFailoverClientBugScopeNone(t *testing.T) {
	// P0-C 回归：请求形状 400 不再 ScopeModel（避免级联冷却语义混淆）。
	got := DecideFailover(400, []byte(`{"type":"error","error":{"type":"bad_request_error","message":"invalid params, invalid thinking.type: \"enabled\" (allowed: adaptive, disabled) (2013)","http_code":"400"}}`), "", false)
	if got.Kind != KindClientBug {
		t.Fatalf("kind = %q, want client_bug", got.Kind)
	}
	if got.Scope != ScopeNone {
		t.Fatalf("scope = %q, want none (P0-C PenaltyOf)", got.Scope)
	}
	if got.EnqueueProbe {
		t.Fatal("client_bug must not enqueue probe")
	}
}
