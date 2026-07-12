package licensing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PgxStore struct {
	pool *pgxpool.Pool
}

func NewPgxStore(pool *pgxpool.Pool) *PgxStore {
	return &PgxStore{pool: pool}
}

func (s *PgxStore) GetLicense(ctx context.Context, licenseKey string) (*License, error) {
	var lic License
	var featuresJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, license_key, customer_name, customer_email, max_devices,
		       subscription_tier, features, expires_at, created_at, revoked_at
		FROM licenses WHERE license_key = $1
	`, licenseKey).Scan(
		&lic.ID, &lic.LicenseKey, &lic.CustomerName, &lic.CustomerEmail,
		&lic.MaxDevices, &lic.SubscriptionTier, &featuresJSON,
		&lic.ExpiresAt, &lic.CreatedAt, &lic.RevokedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("license not found")
		}
		return nil, err
	}
	if err := json.Unmarshal(featuresJSON, &lic.Features); err != nil {
		return nil, err
	}
	return &lic, nil
}

func (s *PgxStore) GetLicenseByID(ctx context.Context, id int64) (*License, error) {
	var lic License
	var featuresJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, license_key, customer_name, customer_email, max_devices,
		       subscription_tier, features, expires_at, created_at, revoked_at
		FROM licenses WHERE id = $1
	`, id).Scan(
		&lic.ID, &lic.LicenseKey, &lic.CustomerName, &lic.CustomerEmail,
		&lic.MaxDevices, &lic.SubscriptionTier, &featuresJSON,
		&lic.ExpiresAt, &lic.CreatedAt, &lic.RevokedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("license not found")
		}
		return nil, err
	}
	if err := json.Unmarshal(featuresJSON, &lic.Features); err != nil {
		return nil, err
	}
	return &lic, nil
}

func (s *PgxStore) CreateLicense(ctx context.Context, lic *License) error {
	featuresJSON, err := json.Marshal(lic.Features)
	if err != nil {
		return err
	}
	return s.pool.QueryRow(ctx, `
		INSERT INTO licenses (license_key, customer_name, customer_email, max_devices,
		                      subscription_tier, features, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		RETURNING id, created_at
	`, lic.LicenseKey, lic.CustomerName, lic.CustomerEmail, lic.MaxDevices,
		lic.SubscriptionTier, featuresJSON, lic.ExpiresAt,
	).Scan(&lic.ID, &lic.CreatedAt)
}

func (s *PgxStore) UpdateLicense(ctx context.Context, lic *License) error {
	featuresJSON, err := json.Marshal(lic.Features)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE licenses SET
			customer_name = $1, customer_email = $2, max_devices = $3,
			subscription_tier = $4, features = $5, expires_at = $6,
			updated_at = NOW()
		WHERE id = $7
	`, lic.CustomerName, lic.CustomerEmail, lic.MaxDevices,
		lic.SubscriptionTier, featuresJSON, lic.ExpiresAt, lic.ID)
	return err
}

func (s *PgxStore) RevokeLicense(ctx context.Context, licenseKey string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE licenses SET revoked_at = NOW() WHERE license_key = $1
	`, licenseKey)
	return err
}

func (s *PgxStore) GetActiveDevices(ctx context.Context, licenseKey string) ([]Device, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.license_id, d.instance_id, d.hardware_hash, d.device_name,
		       d.activated_at, d.last_heartbeat, d.status, d.deactivated_at, d.deactivate_reason
		FROM license_devices d
		JOIN licenses l ON d.license_id = l.id
		WHERE l.license_key = $1 AND d.status = 'active'
		ORDER BY d.activated_at DESC
	`, licenseKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []Device
	for rows.Next() {
		var dev Device
		if err := rows.Scan(
			&dev.ID, &dev.LicenseID, &dev.InstanceID, &dev.HardwareHash, &dev.DeviceName,
			&dev.ActivatedAt, &dev.LastHeartbeat, &dev.Status, &dev.DeactivatedAt, &dev.DeactivateReason,
		); err != nil {
			return nil, err
		}
		devices = append(devices, dev)
	}
	return devices, rows.Err()
}

func (s *PgxStore) GetDeviceByHardwareHash(ctx context.Context, licenseKey, hardwareHash string) (*Device, error) {
	var dev Device
	err := s.pool.QueryRow(ctx, `
		SELECT d.id, d.license_id, d.instance_id, d.hardware_hash, d.device_name,
		       d.activated_at, d.last_heartbeat, d.status, d.deactivated_at, d.deactivate_reason
		FROM license_devices d
		JOIN licenses l ON d.license_id = l.id
		WHERE l.license_key = $1 AND d.hardware_hash = $2
	`, licenseKey, hardwareHash).Scan(
		&dev.ID, &dev.LicenseID, &dev.InstanceID, &dev.HardwareHash, &dev.DeviceName,
		&dev.ActivatedAt, &dev.LastHeartbeat, &dev.Status, &dev.DeactivatedAt, &dev.DeactivateReason,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &dev, nil
}

func (s *PgxStore) ActivateDevice(ctx context.Context, dev *Device) error {
	return s.pool.QueryRow(ctx, `
		INSERT INTO license_devices (license_id, instance_id, hardware_hash, device_name,
		                             activated_at, last_heartbeat, status)
		VALUES ($1, $2, $3, $4, NOW(), NOW(), 'active')
		RETURNING id, activated_at, last_heartbeat
	`, dev.LicenseID, dev.InstanceID, dev.HardwareHash, dev.DeviceName,
	).Scan(&dev.ID, &dev.ActivatedAt, &dev.LastHeartbeat)
}

func (s *PgxStore) DeactivateDevice(ctx context.Context, licenseKey, hardwareHash, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE license_devices d
		SET status = 'deactivated',
		    deactivated_at = NOW(),
		    deactivate_reason = $3
		FROM licenses l
		WHERE d.license_id = l.id
		  AND l.license_key = $1
		  AND d.hardware_hash = $2
	`, licenseKey, hardwareHash, reason)
	return err
}

func (s *PgxStore) UpdateHeartbeat(ctx context.Context, licenseKey, hardwareHash string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE license_devices d
		SET last_heartbeat = NOW()
		FROM licenses l
		WHERE d.license_id = l.id
		  AND l.license_key = $1
		  AND d.hardware_hash = $2
	`, licenseKey, hardwareHash)
	return err
}

func (s *PgxStore) CreateOfflineRequest(ctx context.Context, req *OfflineRequest) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO offline_activation_requests (license_key, hardware_hash, instance_id,
		                                         device_name, request_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, req.LicenseKey, req.HardwareHash, req.InstanceID, req.DeviceName, req.RequestID, req.Timestamp)
	return err
}

func (s *PgxStore) ListOfflineRequests(ctx context.Context) ([]OfflineRequest, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT license_key, hardware_hash, instance_id, device_name, request_id,
		       created_at, approved_at, signed_license
		FROM offline_activation_requests
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []OfflineRequest
	for rows.Next() {
		var req OfflineRequest
		var approvedAt sql.NullTime
		var signedLicense sql.NullString
		if err := rows.Scan(
			&req.LicenseKey, &req.HardwareHash, &req.InstanceID, &req.DeviceName,
			&req.RequestID, &req.Timestamp, &approvedAt, &signedLicense,
		); err != nil {
			return nil, err
		}
		if approvedAt.Valid {
			req.ApprovedAt = &approvedAt.Time
		}
		if signedLicense.Valid {
			req.ActivationCode = signedLicense.String
		}
		requests = append(requests, req)
	}
	return requests, rows.Err()
}

func (s *PgxStore) RejectOfflineRequest(ctx context.Context, requestID, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE offline_activation_requests
		SET status = 'rejected', reject_reason = $2
		WHERE request_id = $1
	`, requestID, reason)
	return err
}

func (s *PgxStore) GetOfflineRequest(ctx context.Context, requestID string) (*OfflineRequest, error) {
	var req OfflineRequest
	err := s.pool.QueryRow(ctx, `
		SELECT license_key, hardware_hash, instance_id, device_name, request_id, created_at
		FROM offline_activation_requests WHERE request_id = $1
	`, requestID).Scan(
		&req.LicenseKey, &req.HardwareHash, &req.InstanceID, &req.DeviceName, &req.RequestID, &req.Timestamp,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("request not found")
		}
		return nil, err
	}
	return &req, nil
}

func (s *PgxStore) ApproveOfflineRequest(ctx context.Context, requestID string, signedLicense *SignedLicense) error {
	signedJSON, err := json.Marshal(signedLicense)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE offline_activation_requests
		SET approved_at = NOW(), signed_license = $2
		WHERE request_id = $1
	`, requestID, signedJSON)
	return err
}

func (s *PgxStore) CountActiveDevices(ctx context.Context, licenseKey string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM license_devices d
		JOIN licenses l ON d.license_id = l.id
		WHERE l.license_key = $1 AND d.status = 'active'
	`, licenseKey).Scan(&count)
	return count, err
}

func (s *PgxStore) ListAllLicenses(ctx context.Context, offset, limit int, query, statusFilter string) ([]License, int, error) {
	where := "WHERE 1=1"
	args := []interface{}{}
	argIdx := 1

	if query != "" {
		where += " AND (customer_name ILIKE $" + strconv.Itoa(argIdx) +
			" OR customer_email ILIKE $" + strconv.Itoa(argIdx) +
			" OR license_key ILIKE $" + strconv.Itoa(argIdx) + ")"
		args = append(args, "%"+query+"%")
		argIdx++
	}

	switch statusFilter {
	case "active":
		where += " AND revoked_at IS NULL AND expires_at > NOW()"
	case "expired":
		where += " AND revoked_at IS NULL AND expires_at <= NOW()"
	case "revoked":
		where += " AND revoked_at IS NOT NULL"
	}

	// Count
	countSQL := "SELECT COUNT(*) FROM licenses " + where
	var total int
	if err := s.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Data query
	dataSQL := "SELECT id, license_key, customer_name, customer_email, max_devices, " +
		"subscription_tier, features, expires_at, created_at, revoked_at " +
		"FROM licenses " + where +
		" ORDER BY created_at DESC" +
		" LIMIT $" + strconv.Itoa(argIdx) + " OFFSET $" + strconv.Itoa(argIdx+1)
	args = append(args, limit, offset)

	rows, err := s.pool.Query(ctx, dataSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var licenses []License
	for rows.Next() {
		var lic License
		var featuresJSON []byte
		if err := rows.Scan(
			&lic.ID, &lic.LicenseKey, &lic.CustomerName, &lic.CustomerEmail,
			&lic.MaxDevices, &lic.SubscriptionTier, &featuresJSON,
			&lic.ExpiresAt, &lic.CreatedAt, &lic.RevokedAt,
		); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(featuresJSON, &lic.Features); err != nil {
			return nil, 0, err
		}
		licenses = append(licenses, lic)
	}

	return licenses, total, rows.Err()
}

func (s *PgxStore) ListAllDevices(ctx context.Context, licenseKey string) ([]Device, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.license_id, d.instance_id, d.hardware_hash, d.device_name,
		       d.activated_at, d.last_heartbeat, d.status, d.deactivated_at, d.deactivate_reason
		FROM license_devices d
		JOIN licenses l ON d.license_id = l.id
		WHERE l.license_key = $1
		ORDER BY d.activated_at DESC
	`, licenseKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []Device
	for rows.Next() {
		var dev Device
		if err := rows.Scan(
			&dev.ID, &dev.LicenseID, &dev.InstanceID, &dev.HardwareHash, &dev.DeviceName,
			&dev.ActivatedAt, &dev.LastHeartbeat, &dev.Status, &dev.DeactivatedAt, &dev.DeactivateReason,
		); err != nil {
			return nil, err
		}
		devices = append(devices, dev)
	}
	return devices, rows.Err()
}

func (s *PgxStore) ListProductModules(ctx context.Context) ([]ProductModule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, key, name, description, category, icon, setting_key,
		       is_base, sort_order, enabled, created_at, updated_at
		FROM product_modules
		ORDER BY sort_order, name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var modules []ProductModule
	for rows.Next() {
		var m ProductModule
		if err := rows.Scan(
			&m.ID, &m.Key, &m.Name, &m.Description, &m.Category,
			&m.Icon, &m.SettingKey, &m.IsBase, &m.SortOrder,
			&m.Enabled, &m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			return nil, err
		}
		modules = append(modules, m)
	}
	return modules, rows.Err()
}

func (s *PgxStore) ListProductModuleFeatures(ctx context.Context) ([]ProductModuleFeature, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, module_key, feature_key, feature_name, description, setting_key, enabled, created_at
		FROM product_module_features
		ORDER BY module_key, feature_key
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var features []ProductModuleFeature
	for rows.Next() {
		var f ProductModuleFeature
		if err := rows.Scan(
			&f.ID, &f.ModuleKey, &f.FeatureKey, &f.FeatureName,
			&f.Description, &f.SettingKey, &f.Enabled, &f.CreatedAt,
		); err != nil {
			return nil, err
		}
		features = append(features, f)
	}
	return features, rows.Err()
}

func (s *PgxStore) ListSubscriptionTiers(ctx context.Context) ([]SubscriptionTier, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT code, name, description, price_cents, sort_order
		FROM subscription_tiers
		ORDER BY sort_order
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tiers []SubscriptionTier
	for rows.Next() {
		var t SubscriptionTier
		if err := rows.Scan(&t.Code, &t.Name, &t.Description, &t.PriceCents, &t.SortOrder); err != nil {
			return nil, err
		}
		tiers = append(tiers, t)
	}
	return tiers, rows.Err()
}

func (s *PgxStore) ListTierModuleMaps(ctx context.Context) ([]TierModuleMap, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tier_code, module_key, COALESCE(max_features, '')
		FROM tier_module_map
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var maps []TierModuleMap
	for rows.Next() {
		var m TierModuleMap
		if err := rows.Scan(&m.TierCode, &m.ModuleKey, &m.MaxFeatures); err != nil {
			return nil, err
		}
		maps = append(maps, m)
	}
	return maps, rows.Err()
}

func (s *PgxStore) ListLicenseModulesByID(ctx context.Context, licenseID int64) ([]LicenseModule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT license_id, module_key, enabled, config, expires_at
		FROM license_modules
		WHERE license_id = $1
		ORDER BY module_key
	`, licenseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var mods []LicenseModule
	for rows.Next() {
		var m LicenseModule
		if err := rows.Scan(&m.LicenseID, &m.ModuleKey, &m.Enabled, &m.Config, &m.ExpiresAt); err != nil {
			return nil, err
		}
		mods = append(mods, m)
	}
	return mods, rows.Err()
}

func (s *PgxStore) UpsertLicenseModule(ctx context.Context, lm *LicenseModule) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO license_modules (license_id, module_key, enabled, config, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (license_id, module_key)
		DO UPDATE SET enabled = EXCLUDED.enabled, config = EXCLUDED.config, expires_at = EXCLUDED.expires_at
	`, lm.LicenseID, lm.ModuleKey, lm.Enabled, lm.Config, lm.ExpiresAt)
	return err
}

func (s *PgxStore) DeleteLicenseModule(ctx context.Context, licenseID int64, moduleKey string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM license_modules WHERE license_id = $1 AND module_key = $2
	`, licenseID, moduleKey)
	return err
}

func (s *PgxStore) GetLicenseModules(ctx context.Context, licenseKey string) (map[string]*LicenseModule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT lm.module_key, lm.enabled, lm.config, lm.expires_at
		FROM license_modules lm
		JOIN licenses l ON lm.license_id = l.id
		WHERE l.license_key = $1
	`, licenseKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	modules := make(map[string]*LicenseModule)
	for rows.Next() {
		var mod LicenseModule
		if err := rows.Scan(&mod.ModuleKey, &mod.Enabled, &mod.Config, &mod.ExpiresAt); err != nil {
			return nil, err
		}
		modules[mod.ModuleKey] = &mod
	}
	return modules, rows.Err()
}
