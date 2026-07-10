package licensing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

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

func (s *PgxStore) RevokeLicense(ctx context.Context, licenseKey string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE licenses SET revoked_at = NOW() WHERE license_key = $1
	`, licenseKey)
	return err
}

func (s *PgxStore) GetActiveDevices(ctx context.Context, licenseKey string) ([]Device, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.license_id, d.instance_id, d.hardware_hash, d.device_name,
		       d.activated_at, d.last_heartbeat, d.status, d.deactivated_at, COALESCE(d.deactivate_reason, '')
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
		       d.activated_at, d.last_heartbeat, d.status, d.deactivated_at, COALESCE(d.deactivate_reason, '')
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
	result, err := s.pool.Exec(ctx, `
		UPDATE offline_activation_requests
		SET approved_at = NOW(), signed_license = $2, status = 'approved', reject_reason = NULL
		WHERE request_id = $1
	`, requestID, signedJSON)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.New("offline request not found")
	}
	return nil
}

// ListOfflineRequests 列出离线激活请求（按创建时间倒序）
func (s *PgxStore) ListOfflineRequests(ctx context.Context, limit int) ([]OfflineActivationRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, license_key, hardware_hash, instance_id, device_name,
		       request_id, created_at, approved_at, signed_license, status, COALESCE(reject_reason, '')
		FROM offline_activation_requests
		ORDER BY created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OfflineActivationRow
	for rows.Next() {
		var r OfflineActivationRow
		var signed sql.NullString
		if err := rows.Scan(
			&r.ID, &r.LicenseKey, &r.HardwareHash, &r.InstanceID, &r.DeviceName,
			&r.RequestID, &r.CreatedAt, &r.ApprovedAt, &signed, &r.Status, &r.RejectReason,
		); err != nil {
			return nil, err
		}
		if signed.Valid {
			r.SignedLicense = []byte(signed.String)
		}
		if r.Status == "" {
			r.Status = "approved"
			if r.ApprovedAt == nil {
				r.Status = "pending"
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PgxStore) RejectOfflineRequest(ctx context.Context, requestID, reason string) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE offline_activation_requests
		SET status = 'rejected', reject_reason = $2
		WHERE request_id = $1
		  AND approved_at IS NULL
		  AND COALESCE(NULLIF(status, ''), 'pending') = 'pending'
	`, requestID, reason)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.New("offline request not found or already processed")
	}
	return nil
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

func (s *PgxStore) ListAllLicenses(ctx context.Context, offset, limit int) ([]License, int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, license_key, customer_name, customer_email, max_devices,
		       subscription_tier, features, expires_at, created_at, revoked_at
		FROM licenses
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
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

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM licenses`).Scan(&total); err != nil {
		return nil, 0, err
	}

	return licenses, total, rows.Err()
}

func (s *PgxStore) ListAllDevices(ctx context.Context, licenseKey string) ([]Device, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.license_id, d.instance_id, d.hardware_hash, d.device_name,
		       d.activated_at, d.last_heartbeat, d.status, d.deactivated_at, COALESCE(d.deactivate_reason, '')
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
