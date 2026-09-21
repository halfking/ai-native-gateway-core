package licensing

import (
	"errors"
	"net/http"
	"testing"
)

func TestMapValidatorError(t *testing.T) {
	code, msg := mapValidatorError(ErrLicenseExpired)
	if code != CodeLicenseExpired || msg == "" {
		t.Fatalf("expired: code=%q msg=%q", code, msg)
	}

	code, msg = mapValidatorError(ErrLicenseRevoked)
	if code != CodeLicenseRevoked || msg == "" {
		t.Fatalf("revoked: code=%q msg=%q", code, msg)
	}

	code, msg = mapValidatorError(errors.New("license not found"))
	if code != CodeLicenseNotFound {
		t.Fatalf("not found: code=%q", code)
	}
}

func TestActivationHTTPStatus(t *testing.T) {
	cases := map[string]int{
		CodeLicenseNotFound:        http.StatusNotFound,
		CodeLicenseExpired:         http.StatusGone,
		CodeLicenseRevoked:         http.StatusForbidden,
		CodeDeviceLimitExceeded:    http.StatusConflict,
		CodeDeviceAlreadyActivated: http.StatusConflict,
		"":                           http.StatusBadRequest,
	}
	for code, want := range cases {
		if got := activationHTTPStatus(code); got != want {
			t.Fatalf("activationHTTPStatus(%q) = %d, want %d", code, got, want)
		}
	}
}
