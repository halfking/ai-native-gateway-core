package licensing

import (
	"context"
)

type Store interface {
	GetLicense(ctx context.Context, licenseKey string) (*License, error)
	GetLicenseByID(ctx context.Context, id int64) (*License, error)
	// GetLicenseByHardwareHash returns the (active) License whose device with
	// the given hardware_hash exists. Returns nil, nil if no such device exists.
	GetLicenseByHardwareHash(ctx context.Context, hardwareHash string) (*License, error)
	CreateLicense(ctx context.Context, lic *License) error
	UpdateLicense(ctx context.Context, lic *License) error
	RevokeLicense(ctx context.Context, licenseKey string) error

	// ActivateDeviceIfUnderLimit inserts a device row only when the active
	// device count for the license is below MaxDevices. The check + insert
	// runs inside a single transaction, so concurrent activations cannot
	// exceed MaxDevices. Returns ErrDeviceLimitExceeded when the limit is
	// reached, or ErrDeviceAlreadyActive when this hardware_hash already
	// has an active device.
	ActivateDeviceIfUnderLimit(ctx context.Context, dev *Device, maxDevices int) error

	GetActiveDevices(ctx context.Context, licenseKey string) ([]Device, error)
	GetDeviceByHardwareHash(ctx context.Context, licenseKey, hardwareHash string) (*Device, error)
	ActivateDevice(ctx context.Context, dev *Device) error
	DeactivateDevice(ctx context.Context, licenseKey, hardwareHash, reason string) error
	UpdateHeartbeat(ctx context.Context, licenseKey, hardwareHash string) error

	CreateOfflineRequest(ctx context.Context, req *OfflineRequest) error
	GetOfflineRequest(ctx context.Context, requestID string) (*OfflineRequest, error)
	ApproveOfflineRequest(ctx context.Context, requestID string, signedLicense *SignedLicense, activationCode string) error
	GetOfflineActivationCode(ctx context.Context, requestID string) (string, error)
	ListOfflineRequests(ctx context.Context) ([]OfflineRequest, error)
	RejectOfflineRequest(ctx context.Context, requestID, reason string) error

	CountActiveDevices(ctx context.Context, licenseKey string) (int, error)
	ListAllLicenses(ctx context.Context, offset, limit int, query, statusFilter string) ([]License, int, error)
	ListAllDevices(ctx context.Context, licenseKey string) ([]Device, error)

	GetLicenseModules(ctx context.Context, licenseKey string) (map[string]*LicenseModule, error)
	ListProductModules(ctx context.Context) ([]ProductModule, error)
	ListProductModuleFeatures(ctx context.Context) ([]ProductModuleFeature, error)
	ListSubscriptionTiers(ctx context.Context) ([]SubscriptionTier, error)
	ListTierModuleMaps(ctx context.Context) ([]TierModuleMap, error)
	ListLicenseModulesByID(ctx context.Context, licenseID int64) ([]LicenseModule, error)
	UpsertLicenseModule(ctx context.Context, lm *LicenseModule) error
	DeleteLicenseModule(ctx context.Context, licenseID int64, moduleKey string) error
}

// TrialConsentStore atomically creates a Trial license and its agreement audit
// evidence. License Authority refuses Trial issuance when this is unavailable.
type TrialConsentStore interface {
	CreateTrialLicenseWithConsent(ctx context.Context, lic *License, consent *TrialConsent) error
}
