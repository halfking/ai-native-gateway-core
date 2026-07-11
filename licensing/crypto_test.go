package licensing

import (
	"strings"
	"testing"
	"time"
)

func TestCryptoConfigFailsClosedWithoutKeys(t *testing.T) {
	t.Parallel()

	config := &CryptoConfig{}
	license := &License{LicenseKey: "test-license"}

	tests := []struct {
		name string
		run  func() error
	}{
		{name: "sign", run: func() error { _, err := config.SignLicense(license); return err }},
		{name: "verify", run: func() error { _, err := config.VerifyLicense(&SignedLicense{}); return err }},
		{name: "encrypt", run: func() error { _, err := config.EncryptAES([]byte("data")); return err }},
		{name: "decrypt", run: func() error { _, err := config.DecryptAES([]byte("data")); return err }},
		{name: "generate JWT", run: func() error {
			_, err := config.GenerateJWT("instance", "license", time.Now().Add(time.Hour))
			return err
		}},
		{name: "verify JWT", run: func() error { _, err := config.VerifyJWT("token"); return err }},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.run(); err == nil || !strings.Contains(err.Error(), "not configured") {
				t.Fatalf("expected not configured error, got %v", err)
			}
		})
	}
}
