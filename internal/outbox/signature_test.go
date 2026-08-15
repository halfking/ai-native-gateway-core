package outbox

import "testing"

func TestSignPayload_KnownVector(t *testing.T) {
	body := []byte(`{"events":[{"event_id":"evt-001"}]}`)
	timestamp := "2026-08-15T04:05:06Z"
	nonce := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

	got := SignPayload("test-secret", timestamp, nonce, body)
	const want = "hmac-sha256=12564b83d5542ea4042a3549171897d33a625c0670852cfe3604172b20747a7c"
	if got != want {
		t.Fatalf("SignPayload() = %q, want %q", got, want)
	}
	if !VerifySignature("test-secret", timestamp, nonce, body, got) {
		t.Fatal("VerifySignature rejected the known valid signature")
	}
}

func TestVerifySignature_RejectsTamperingAndMalformedScheme(t *testing.T) {
	body := []byte(`{"events":[{"event_id":"evt-001"}]}`)
	timestamp := "2026-08-15T04:05:06Z"
	nonce := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	signature := SignPayload("test-secret", timestamp, nonce, body)

	tests := []struct {
		name      string
		secret    string
		timestamp string
		nonce     string
		body      []byte
		signature string
	}{
		{name: "wrong secret", secret: "wrong", timestamp: timestamp, nonce: nonce, body: body, signature: signature},
		{name: "changed timestamp", secret: "test-secret", timestamp: "2026-08-15T04:05:07Z", nonce: nonce, body: body, signature: signature},
		{name: "changed nonce", secret: "test-secret", timestamp: timestamp, nonce: "ff" + nonce[2:], body: body, signature: signature},
		{name: "changed body", secret: "test-secret", timestamp: timestamp, nonce: nonce, body: []byte(`{"events":[]}`), signature: signature},
		{name: "missing scheme", secret: "test-secret", timestamp: timestamp, nonce: nonce, body: body, signature: signature[len(SignatureScheme):]},
		{name: "bad hex", secret: "test-secret", timestamp: timestamp, nonce: nonce, body: body, signature: SignatureScheme + "zzzz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if VerifySignature(tt.secret, tt.timestamp, tt.nonce, tt.body, tt.signature) {
				t.Fatal("VerifySignature accepted invalid input")
			}
		})
	}
}
