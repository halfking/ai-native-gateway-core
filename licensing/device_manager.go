package licensing

import (
	"context"
	"errors"
	"log/slog"
)

type DeviceManager struct {
	store     Store
	validator *Validator
}

func NewDeviceManager(store Store, validator *Validator) *DeviceManager {
	return &DeviceManager{
		store:     store,
		validator: validator,
	}
}

func (dm *DeviceManager) ActivateDevice(ctx context.Context, req *ActivationRequest) (*ActivationResponse, error) {
	lic, err := dm.validator.ValidateLicense(ctx, req.LicenseKey)
	if err != nil {
		return &ActivationResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	newDev := &Device{
		LicenseID:    lic.ID,
		InstanceID:   req.InstanceID,
		HardwareHash: req.HardwareHash,
		DeviceName:   req.DeviceName,
		Status:       "active",
	}

	// Single transaction: the store rejects with ErrDeviceLimitExceeded or
	// ErrDeviceAlreadyActivated when the corresponding invariant would be
	// violated, so two concurrent activations cannot exceed MaxDevices.
	err = dm.store.ActivateDeviceIfUnderLimit(ctx, newDev, lic.MaxDevices)
	switch {
	case err == nil:
		// fall through to the success path below.
	case errors.Is(err, ErrDeviceAlreadyActivated):
		existingDev, _ := dm.store.GetDeviceByHardwareHash(ctx, req.LicenseKey, req.HardwareHash)
		if existingDev != nil && existingDev.Status == "active" {
			slog.Info("device already activated", "hardware_hash", req.HardwareHash)
			activeDevices, _ := dm.store.GetActiveDevices(ctx, req.LicenseKey)
			return &ActivationResponse{
				Success:       true,
				ActiveDevices: activeDevices,
				Message:       "device already activated",
			}, nil
		}
		return nil, err
	case errors.Is(err, ErrDeviceLimitExceeded):
		activeDevices, _ := dm.store.GetActiveDevices(ctx, req.LicenseKey)
		return &ActivationResponse{
			Success:        false,
			ActiveDevices:  activeDevices,
			NeedDeactivate: true,
			Message:        "device limit exceeded, please deactivate one device",
		}, nil
	default:
		return nil, err
	}

	activeDevices, _ := dm.store.GetActiveDevices(ctx, req.LicenseKey)

	slog.Info("device activated", "license_key", req.LicenseKey, "hardware_hash", req.HardwareHash)

	return &ActivationResponse{
		Success:       true,
		ActiveDevices: activeDevices,
		ExpiresAt:     &lic.ExpiresAt,
		Message:       "activation successful",
	}, nil
}

func (dm *DeviceManager) DeactivateDevice(ctx context.Context, req *DeactivateRequest) error {
	if _, err := dm.validator.ValidateLicense(ctx, req.LicenseKey); err != nil {
		return err
	}

	if err := dm.store.DeactivateDevice(ctx, req.LicenseKey, req.HardwareHash, req.Reason); err != nil {
		return err
	}

	dm.validator.InvalidateCache(req.LicenseKey)
	slog.Info("device deactivated", "license_key", req.LicenseKey, "hardware_hash", req.HardwareHash, "reason", req.Reason)
	return nil
}

func (dm *DeviceManager) UpdateHeartbeat(ctx context.Context, licenseKey, hardwareHash string) error {
	if err := dm.validator.ValidateDevice(ctx, licenseKey, hardwareHash); err != nil {
		return err
	}

	return dm.store.UpdateHeartbeat(ctx, licenseKey, hardwareHash)
}

func (dm *DeviceManager) GetActiveDevices(ctx context.Context, licenseKey string) ([]Device, error) {
	if _, err := dm.validator.ValidateLicense(ctx, licenseKey); err != nil {
		return nil, err
	}

	return dm.store.GetActiveDevices(ctx, licenseKey)
}

func (dm *DeviceManager) CheckDeviceLimit(ctx context.Context, licenseKey string) (bool, int, int, error) {
	lic, err := dm.validator.ValidateLicense(ctx, licenseKey)
	if err != nil {
		return false, 0, 0, err
	}

	activeCount, err := dm.store.CountActiveDevices(ctx, licenseKey)
	if err != nil {
		return false, 0, 0, err
	}

	canActivate := activeCount < lic.MaxDevices
	return canActivate, activeCount, lic.MaxDevices, nil
}
