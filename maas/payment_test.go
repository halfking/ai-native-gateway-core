package maas

import "testing"

func TestStubQRProvider_AlipayStubMode(t *testing.T) {
	p := StubQRProvider{Settings: PaymentSettings{}}
	qr := p.GenerateQR("MO20260616001", 2900, PaymentAlipay)
	if !qr.StubMode {
		t.Fatal("expected stub mode when alipay account empty")
	}
	if qr.QRPayload == "" {
		t.Fatal("expected qr payload")
	}
	if qr.Hint == "" {
		t.Fatal("expected hint")
	}
}

func TestStubQRProvider_AlipayConfigured(t *testing.T) {
	p := StubQRProvider{Settings: PaymentSettings{AlipayAccount: "test@alipay.com"}}
	qr := p.GenerateQR("MO20260616001", 2900, PaymentAlipay)
	if qr.StubMode {
		t.Fatal("expected non-stub when alipay account set")
	}
	if qr.Hint == "" {
		t.Fatal("expected hint with account")
	}
}

func TestStubQRProvider_WechatStubMode(t *testing.T) {
	p := StubQRProvider{Settings: PaymentSettings{}}
	qr := p.GenerateQR("MO20260616001", 1000, PaymentWechat)
	if !qr.StubMode {
		t.Fatal("expected stub mode when wechat mch empty")
	}
}

func TestGenerateOrderNo_unique(t *testing.T) {
	a, err := generateOrderNo()
	if err != nil {
		t.Fatal(err)
	}
	b, err := generateOrderNo()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("order numbers should differ")
	}
	if len(a) < 20 {
		t.Fatalf("order no too short: %s", a)
	}
}
