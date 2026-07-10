package licensing

import (
	"context"
	"log/slog"
	"time"
)

type Activator struct {
	crypto        *CryptoConfig
	store         Store
	deviceManager *DeviceManager
}

func NewActivator(crypto *CryptoConfig, store Store, deviceManager *DeviceManager) *Activator {
	return &Activator{
		crypto:        crypto,
		store:         store,
		deviceManager: deviceManager,
	}
}

func (a *Activator) Activate(ctx context.Context, req *ActivationRequest) (*ActivationResponse, error) {
	resp, err := a.deviceManager.ActivateDevice(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return resp, nil
	}

	lic, err := a.store.GetLicense(ctx, req.LicenseKey)
	if err != nil {
		return nil, err
	}

	signedLicense, err := a.crypto.SignLicense(lic)
	if err != nil {
		return nil, err
	}

	resp.SignedLicense = signedLicense
	slog.Info("license activated", "license_key", req.LicenseKey, "instance_id", req.InstanceID)

	return resp, nil
}

func (a *Activator) Deactivate(ctx context.Context, req *DeactivateRequest) error {
	if err := a.deviceManager.DeactivateDevice(ctx, req); err != nil {
		return err
	}

	slog.Info("license deactivated", "license_key", req.LicenseKey, "hardware_hash", req.HardwareHash)
	return nil
}

func (a *Activator) Heartbeat(ctx context.Context, licenseKey, hardwareHash string) error {
	return a.deviceManager.UpdateHeartbeat(ctx, licenseKey, hardwareHash)
}

func (a *Activator) ValidateAndRefresh(ctx context.Context, licenseKey, hardwareHash string) (*SignedLicense, error) {
	if err := a.deviceManager.UpdateHeartbeat(ctx, licenseKey, hardwareHash); err != nil {
		return nil, err
	}

	lic, err := a.store.GetLicense(ctx, licenseKey)
	if err != nil {
		return nil, err
	}

	if time.Now().After(lic.ExpiresAt) {
		return nil, ErrLicenseExpired
	}

	if lic.RevokedAt != nil {
		return nil, ErrLicenseRevoked
	}

	return a.crypto.SignLicense(lic)
}
