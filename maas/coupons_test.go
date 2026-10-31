package maas

import (
	"strings"
	"testing"
)

func TestNormalizeCouponCode(t *testing.T) {
	if normalizeCouponCode("  ab-cd  ") != "AB-CD" {
		t.Fatal(normalizeCouponCode("  ab-cd  "))
	}
}

func TestGenerateCouponCode(t *testing.T) {
	c, err := generateCouponCode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c, "CPN-") || len(c) != 12 {
		t.Fatalf("code shape: %q", c)
	}
}

func TestActivateCoupon_Gating(t *testing.T) {
	svc := NewService(nil)
	if _, err := svc.ActivateCoupon(nil, "t1", "CPN-1"); err == nil {
		t.Fatal("disabled must error")
	}
	svc2 := &Service{} // pool nil → Enabled false
	_ = svc2
}

func TestCreateCoupon_Gating(t *testing.T) {
	svc := NewService(nil)
	if _, err := svc.CreateCoupon(nil, CreateCouponRequest{CreditsAmount: 10}); err == nil {
		t.Fatal("disabled must error")
	}
}
