package licensing

import "testing"

func TestLogActivationNotifier(t *testing.T) {
	n := &LogActivationNotifier{}
	err := n.NotifyApproved(t.Context(), &License{
		LicenseKey:    "LIC-TEST",
		CustomerEmail: "test@example.com",
	}, &OfflineRequest{
		LicenseKey: "LIC-TEST",
		RequestID:  "req-1",
		DeviceName: "dev",
	}, "ABCD1234")
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewActivationNotifierFromEnvDefaultsToLog(t *testing.T) {
	t.Setenv("LICENSE_AUTHORITY_SMTP_HOST", "")
	n := NewActivationNotifierFromEnv()
	if _, ok := n.(*LogActivationNotifier); !ok {
		t.Fatalf("expected LogActivationNotifier, got %T", n)
	}
}
