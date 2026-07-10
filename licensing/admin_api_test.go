package licensing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

type stubStore struct{}

func (stubStore) GetLicense(context.Context, string) (*License, error) {
	return nil, errors.New("not implemented")
}
func (stubStore) CreateLicense(context.Context, *License) error              { return nil }
func (stubStore) RevokeLicense(context.Context, string) error                { return nil }
func (stubStore) GetActiveDevices(context.Context, string) ([]Device, error) { return nil, nil }
func (stubStore) GetDeviceByHardwareHash(context.Context, string, string) (*Device, error) {
	return nil, nil
}
func (stubStore) ActivateDevice(context.Context, *Device) error                  { return nil }
func (stubStore) DeactivateDevice(context.Context, string, string, string) error { return nil }
func (stubStore) UpdateHeartbeat(context.Context, string, string) error          { return nil }
func (stubStore) CreateOfflineRequest(context.Context, *OfflineRequest) error    { return nil }
func (stubStore) GetOfflineRequest(context.Context, string) (*OfflineRequest, error) {
	return nil, errors.New("not implemented")
}
func (stubStore) ListOfflineRequests(context.Context, int) ([]OfflineActivationRow, error) {
	return nil, nil
}
func (stubStore) RejectOfflineRequest(context.Context, string, string) error          { return nil }
func (stubStore) ApproveOfflineRequest(context.Context, string, *SignedLicense) error { return nil }
func (stubStore) CountActiveDevices(context.Context, string) (int, error)             { return 0, nil }
func (stubStore) ListAllLicenses(context.Context, int, int) ([]License, int, error) {
	return nil, 0, nil
}
func (stubStore) ListAllDevices(context.Context, string) ([]Device, error) { return nil, nil }
func (stubStore) GetLicenseModules(context.Context, string) (map[string]*LicenseModule, error) {
	return nil, nil
}

func TestCreateLicense_MaxDevicesValidation(t *testing.T) {
	e := echo.New()
	h := NewAdminHandler(stubStore{}, nil, nil, nil, nil)

	body, _ := json.Marshal(map[string]any{
		"customer_name": "Acme",
		"max_devices":   0,
		"expires_at":    time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	})
	req := httptest.NewRequest(http.MethodPost, "/licenses", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateLicense(c); err != nil {
		t.Fatalf("CreateLicense returned err: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGenerateLicenseKey_Format(t *testing.T) {
	key, err := generateLicenseKey()
	if err != nil {
		t.Fatalf("generateLicenseKey: %v", err)
	}
	if len(key) != len("LIC-")+32 {
		t.Fatalf("len(key) = %d, want %d", len(key), len("LIC-")+32)
	}
	if key[:4] != "LIC-" {
		t.Fatalf("prefix = %q, want LIC-", key[:4])
	}
}

type stubStoreApproveNotFound struct{ stubStore }

func (stubStoreApproveNotFound) GetOfflineRequest(context.Context, string) (*OfflineRequest, error) {
	return nil, errors.New("request not found")
}

func TestApproveOfflineRequest_NotFoundMaps404(t *testing.T) {
	e := echo.New()
	h := NewAdminHandler(stubStoreApproveNotFound{}, &CryptoConfig{}, nil, NewOfflineManager(&CryptoConfig{}, stubStoreApproveNotFound{}), nil)

	req := httptest.NewRequest(http.MethodPost, "/licenses/offline-requests/missing/approve", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("missing")

	if err := h.ApproveOfflineRequest(c); err != nil {
		t.Fatalf("ApproveOfflineRequest returned err: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
