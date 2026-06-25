package identity

import (
	"testing"
)

func TestBuildEgressIdentity_BasicFields(t *testing.T) {
	eid := BuildEgressIdentity(7, 0, "tenant-x")

	if eid.CredentialID != 7 {
		t.Errorf("CredentialID = %d, want 7", eid.CredentialID)
	}
	if eid.SlotIndex != 0 {
		t.Errorf("SlotIndex = %d, want 0", eid.SlotIndex)
	}
	if eid.IdentityHash == "" {
		t.Fatal("IdentityHash is empty")
	}
	if len(eid.IdentityHash) != 64 {
		t.Errorf("IdentityHash len = %d, want 64", len(eid.IdentityHash))
	}
}

func TestBuildEgressIdentity_StableHash(t *testing.T) {
	a := BuildEgressIdentity(3, 1, "t1")
	b := BuildEgressIdentity(3, 1, "t1")
	if a.IdentityHash != b.IdentityHash {
		t.Fatal("EgressIdentity hash should be stable for identical inputs")
	}
	if a.VirtualIP != b.VirtualIP {
		t.Fatal("EgressIdentity virtual IP should be stable")
	}
	if a.VirtualMAC != b.VirtualMAC {
		t.Fatal("EgressIdentity virtual MAC should be stable")
	}
}

func TestBuildEgressIdentity_DiffersBySlot(t *testing.T) {
	a := BuildEgressIdentity(3, 0, "t1")
	b := BuildEgressIdentity(3, 1, "t1")
	if a.IdentityHash == b.IdentityHash {
		t.Fatal("Different slot indexes should yield different hashes")
	}
}

func TestBuildEgressIdentity_DefaultTenant(t *testing.T) {
	// 空 tenant 应回退到 "default"（与 BuildEgressIdentity 内部行为一致）
	withDefault := BuildEgressIdentity(1, 0, "")
	explicit := BuildEgressIdentity(1, 0, "default")
	if withDefault.IdentityHash != explicit.IdentityHash {
		t.Fatal("Empty tenant should hash the same as \"default\"")
	}
}

func TestBuildEgressIdentity_VCIDFormat(t *testing.T) {
	eid := BuildEgressIdentity(1, 0, "t1")
	if len(eid.VirtualClientID) < 3 || eid.VirtualClientID[:3] != "vc-" {
		t.Fatalf("VirtualClientID = %q, want prefix vc-", eid.VirtualClientID)
	}
	if eid.VirtualClientID != "vc-"+eid.IdentityHash[:16] {
		t.Fatalf("VirtualClientID = %q, want vc-%s", eid.VirtualClientID, eid.IdentityHash[:16])
	}
}
