package maas

import "testing"

func TestEffectiveRate(t *testing.T) {
	base := int64(10000)
	custom := int64(15000)
	if got := effectiveRate(nil, base); got != base {
		t.Fatalf("nil custom: got %d want %d", got, base)
	}
	zero := int64(0)
	if got := effectiveRate(&zero, base); got != base {
		t.Fatalf("zero custom: got %d want %d", got, base)
	}
	if got := effectiveRate(&custom, base); got != custom {
		t.Fatalf("custom: got %d want %d", got, custom)
	}
}
