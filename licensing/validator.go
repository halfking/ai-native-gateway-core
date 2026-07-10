package licensing

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

var (
	ErrLicenseExpired    = errors.New("license expired")
	ErrLicenseRevoked    = errors.New("license revoked")
	ErrModuleNotLicensed = errors.New("module not licensed")
	ErrDeviceLimit       = errors.New("device limit exceeded")
	ErrInvalidSignature  = errors.New("invalid license signature")
)

type Validator struct {
	crypto *CryptoConfig
	store  Store
	cache  *validatorCache
}

type validatorCache struct {
	mu    sync.RWMutex
	items map[string]*cachedLicense
}

type cachedLicense struct {
	license   *License
	modules   map[string]*LicenseModule
	expiresAt time.Time
	lastCheck time.Time
}

func NewValidator(crypto *CryptoConfig, store Store) *Validator {
	return &Validator{
		crypto: crypto,
		store:  store,
		cache: &validatorCache{
			items: make(map[string]*cachedLicense),
		},
	}
}

func (v *Validator) ValidateLicense(ctx context.Context, licenseKey string) (*License, error) {
	cached := v.cache.get(licenseKey)
	if cached != nil && time.Since(cached.lastCheck) < 5*time.Minute {
		if time.Now().After(cached.license.ExpiresAt) {
			return nil, ErrLicenseExpired
		}
		return cached.license, nil
	}

	lic, err := v.store.GetLicense(ctx, licenseKey)
	if err != nil {
		return nil, err
	}

	if time.Now().After(lic.ExpiresAt) {
		return nil, ErrLicenseExpired
	}

	if lic.RevokedAt != nil {
		return nil, ErrLicenseRevoked
	}

	v.cache.put(licenseKey, lic)
	return lic, nil
}

func (v *Validator) ValidateModule(ctx context.Context, licenseKey, moduleKey string) error {
	if isBaseModule(moduleKey) {
		return nil
	}

	cached := v.cache.get(licenseKey)
	if cached == nil {
		lic, err := v.store.GetLicense(ctx, licenseKey)
		if err != nil {
			return err
		}
		modules, err := v.store.GetLicenseModules(ctx, licenseKey)
		if err != nil {
			return err
		}
		cached = &cachedLicense{
			license:   lic,
			modules:   modules,
			expiresAt: lic.ExpiresAt,
			lastCheck: time.Now(),
		}
		v.cache.put(licenseKey, lic)
	}

	mod, ok := cached.modules[moduleKey]
	if !ok || !mod.Enabled {
		return ErrModuleNotLicensed
	}

	if mod.ExpiresAt != nil && time.Now().After(*mod.ExpiresAt) {
		return ErrLicenseExpired
	}

	return nil
}

func (v *Validator) ValidateDevice(ctx context.Context, licenseKey, hardwareHash string) error {
	lic, err := v.ValidateLicense(ctx, licenseKey)
	if err != nil {
		return err
	}

	activeDevices, err := v.store.GetActiveDevices(ctx, licenseKey)
	if err != nil {
		return err
	}

	for _, dev := range activeDevices {
		if dev.HardwareHash == hardwareHash && dev.Status == "active" {
			return nil
		}
	}

	if len(activeDevices) >= lic.MaxDevices {
		return ErrDeviceLimit
	}

	return nil
}

func (v *validatorCache) get(licenseKey string) *cachedLicense {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.items[licenseKey]
}

func (v *validatorCache) put(licenseKey string, lic *License) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.items[licenseKey] = &cachedLicense{
		license:   lic,
		expiresAt: lic.ExpiresAt,
		lastCheck: time.Now(),
	}
}

func (v *validatorCache) invalidate(licenseKey string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.items, licenseKey)
}

func isBaseModule(moduleKey string) bool {
	baseModules := map[string]bool{
		"routing":        true,
		"authentication": true,
		"audit":          true,
	}
	return baseModules[moduleKey]
}

func (v *Validator) InvalidateCache(licenseKey string) {
	v.cache.invalidate(licenseKey)
	slog.Info("license cache invalidated", "license_key", licenseKey)
}
