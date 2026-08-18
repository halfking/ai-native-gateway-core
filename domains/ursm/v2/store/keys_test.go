package store

import "testing"

func TestNodeKeyV2(t *testing.T) {
	if k := NodeKey("ursm:v2:", 12, "gpt-4"); k != "ursm:v2:node:12:gpt-4" {
		t.Fatalf("unexpected key %q", k)
	}
}

func TestWindowKeysDiffer(t *testing.T) {
	a := WindowKey("ursm:v2:", 12, "gpt-4", "1m")
	b := WindowKey("ursm:v2:", 12, "gpt-4", "5m")
	if a == b {
		t.Fatalf("windows must not collide")
	}
}

func TestParseNodeKey(t *testing.T) {
	legacy, ok := ParseNodeKey("ursm:v2:", "ursm:v2:node:12:model:with:colon")
	if !ok || legacy.TenantID != "" || legacy.CredentialID != 12 || legacy.RawModel != "model:with:colon" {
		t.Fatalf("legacy parse = %+v ok=%v", legacy, ok)
	}
	tenant, ok := ParseNodeKey("ursm:v2:", "ursm:v2:node:tenant-a:34:model:with:colon")
	if !ok || tenant.TenantID != "tenant-a" || tenant.CredentialID != 34 || tenant.RawModel != "model:with:colon" {
		t.Fatalf("tenant parse = %+v ok=%v", tenant, ok)
	}
	numericKey := NodeKeyForTenant("ursm:v2:", "123", 34, "model:with:colon")
	if numericKey != "ursm:v2:node:t:123:34:model:with:colon" {
		t.Fatalf("numeric tenant key = %q", numericKey)
	}
	numeric, ok := ParseNodeKey("ursm:v2:", numericKey)
	if !ok || numeric.TenantID != "123" || numeric.CredentialID != 34 || numeric.RawModel != "model:with:colon" {
		t.Fatalf("numeric tenant parse = %+v ok=%v", numeric, ok)
	}
	if _, ok := ParseNodeKey("ursm:v2:", "ursm:v2:node:tenant-a:not-a-number:model"); ok {
		t.Fatal("invalid credential ID must not parse")
	}
}

func TestReadyKey(t *testing.T) {
	if k := ReadyKey("ursm:v2:"); k != "ursm:v2:meta:ready" {
		t.Fatalf("unexpected ready key %q", k)
	}
}

func TestNodeKeySetForTenantMatchesIndividualConstructors(t *testing.T) {
	cases := []struct {
		name   string
		tenant string
		cid    int
		raw    string
	}{
		{"legacy empty tenant", "", 12, "gpt-4"},
		{"string tenant", "tenant-a", 34, "model:with:colon"},
		{"numeric tenant", "123", 34, "model:with:colon"},
	}
	for _, tc := range cases {
		set := NodeKeySetForTenant("ursm:v2:", tc.tenant, tc.cid, tc.raw)
		want := NodeKeySet{
			Node:   NodeKeyForTenant("ursm:v2:", tc.tenant, tc.cid, tc.raw),
			Win1m:  WindowKeyForTenant("ursm:v2:", tc.tenant, tc.cid, tc.raw, "1m"),
			Win5m:  WindowKeyForTenant("ursm:v2:", tc.tenant, tc.cid, tc.raw, "5m"),
			Win30m: WindowKeyForTenant("ursm:v2:", tc.tenant, tc.cid, tc.raw, "30m"),
		}
		if set != want {
			t.Fatalf("%s: key set = %+v, want %+v", tc.name, set, want)
		}
	}
}
