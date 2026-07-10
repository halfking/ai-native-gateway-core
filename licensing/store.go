package licensing

import (
	"context"
	"time"
)

type Store interface {
	GetLicense(ctx context.Context, licenseKey string) (*License, error)
	CreateLicense(ctx context.Context, lic *License) error
	RevokeLicense(ctx context.Context, licenseKey string) error

	GetActiveDevices(ctx context.Context, licenseKey string) ([]Device, error)
	GetDeviceByHardwareHash(ctx context.Context, licenseKey, hardwareHash string) (*Device, error)
	ActivateDevice(ctx context.Context, dev *Device) error
	DeactivateDevice(ctx context.Context, licenseKey, hardwareHash, reason string) error
	UpdateHeartbeat(ctx context.Context, licenseKey, hardwareHash string) error

	CreateOfflineRequest(ctx context.Context, req *OfflineRequest) error
	GetOfflineRequest(ctx context.Context, requestID string) (*OfflineRequest, error)
	ListOfflineRequests(ctx context.Context, limit int) ([]OfflineActivationRow, error)
	RejectOfflineRequest(ctx context.Context, requestID, reason string) error
	ApproveOfflineRequest(ctx context.Context, requestID string, signedLicense *SignedLicense) error

	CountActiveDevices(ctx context.Context, licenseKey string) (int, error)
	ListAllLicenses(ctx context.Context, offset, limit int) ([]License, int, error)
	ListAllDevices(ctx context.Context, licenseKey string) ([]Device, error)

	GetLicenseModules(ctx context.Context, licenseKey string) (map[string]*LicenseModule, error)
}

// OfflineActivationRow 用于 admin 列表展示的离线激活请求行
type OfflineActivationRow struct {
	ID            int64
	LicenseKey    string
	HardwareHash  string
	InstanceID    string
	DeviceName    string
	RequestID     string
	CreatedAt     time.Time
	ApprovedAt    *time.Time
	SignedLicense []byte
	Status        string
	RejectReason  string
}
