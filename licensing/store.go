package licensing

import (
	"context"
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
	ApproveOfflineRequest(ctx context.Context, requestID string, signedLicense *SignedLicense) error

	CountActiveDevices(ctx context.Context, licenseKey string) (int, error)
	ListAllLicenses(ctx context.Context, offset, limit int) ([]License, int, error)
	ListAllDevices(ctx context.Context, licenseKey string) ([]Device, error)

	GetLicenseModules(ctx context.Context, licenseKey string) (map[string]*LicenseModule, error)
}
