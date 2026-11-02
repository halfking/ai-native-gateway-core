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

// TestPenaltyOfRequestSideFamilyIsNone 钉「请求侧错误族不罚任何人」。
//
// ★ 2026-11-02 第十六轮（F-4 死代码清理）：这条契约原先只由**死代码**的
// 测试守着（TestDecideFailoverClientBugScopeNone /
// TestDecideFailoverRequestRejected —— 测的是生产零引用的
// errorsx.DecideFailover）⇒ 死代码一删，契约就没人守了。
// ⇒ 契约**搬到活的那一层**（PenaltyOf + FailoverScopeOf）来守。
func TestPenaltyOfRequestSideFamilyIsNone(t *testing.T) {
	for _, k := range []ErrorKind{
		KindClientBug, KindToolCallIdMismatch, KindContextLength,
		KindContentFilter, KindUnsupportedFeature, KindConversion,
		// 请求侧错误族（上游回了 4xx，但错误体里没有更细的归因）：
		// 不罚、不冷却、绑定不翻红、探针不扇出。
		KindRequestRejected,
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
