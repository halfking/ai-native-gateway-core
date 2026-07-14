package licensing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

type OfflineManager struct {
	crypto   *CryptoConfig
	store    Store
	notifier ActivationNotifier
}

func NewOfflineManager(crypto *CryptoConfig, store Store) *OfflineManager {
	return &OfflineManager{
		crypto:   crypto,
		store:    store,
		notifier: &LogActivationNotifier{},
	}
}

func (om *OfflineManager) SetActivationNotifier(notifier ActivationNotifier) {
	if notifier != nil {
		om.notifier = notifier
	}
}

func (om *OfflineManager) CreateOfflineRequest(ctx context.Context, req *OfflineRequest) (string, error) {
	req.RequestID = uuid.New().String()
	req.Timestamp = time.Now()

	if err := om.store.CreateOfflineRequest(ctx, req); err != nil {
		return "", err
	}

	requestData, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	encrypted, err := om.crypto.EncryptAES(requestData)
	if err != nil {
		return "", err
	}

	slog.Info("offline request created", "request_id", req.RequestID, "license_key", req.LicenseKey)

	return MarshalToBase64(&SignedLicense{
		Data:      encrypted,
		Signature: nil,
	})
}

func (om *OfflineManager) ApproveOfflineRequest(ctx context.Context, requestID string) (*OfflineApprovalResult, error) {
	req, err := om.store.GetOfflineRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}

	if req.Status == "approved" && req.ActivationCode != "" {
		if req.ApprovedLicense == nil {
			return nil, fmt.Errorf("approved request missing signed license payload")
		}
		return &OfflineApprovalResult{
			ActivationCode: req.ActivationCode,
			SignedLicense:  req.ApprovedLicense,
			RequestID:      requestID,
		}, nil
	}
	if req.Status == "rejected" {
		return nil, errors.New("request was rejected")
	}

	lic, err := om.store.GetLicense(ctx, req.LicenseKey)
	if err != nil {
		return nil, err
	}

	activationCode, err := GenerateActivationCode()
	if err != nil {
		return nil, fmt.Errorf("generate activation code: %w", err)
	}

	signedLicense, err := om.crypto.SignLicense(lic)
	if err != nil {
		return nil, err
	}

	if err := om.store.ApproveOfflineRequest(ctx, requestID, signedLicense, activationCode); err != nil {
		return nil, err
	}

	slog.Info("offline request approved",
		"request_id", requestID,
		"license_key", req.LicenseKey,
		"activation_code", activationCode,
	)

	if om.notifier != nil {
		if err := om.notifier.NotifyApproved(ctx, lic, req, activationCode); err != nil {
			slog.Warn("activation notification failed", "request_id", requestID, "error", err)
		}
	}

	return &OfflineApprovalResult{
		ActivationCode: activationCode,
		SignedLicense:  signedLicense,
		RequestID:      requestID,
	}, nil
}

// ImportSignedRequest decodes a customer-exported activation.req file and
// registers it in the authority store when not already present.
func (om *OfflineManager) ImportSignedRequest(ctx context.Context, b64Signed string) (*OfflineRequest, error) {
	signed, err := UnmarshalFromBase64(strings.TrimSpace(b64Signed))
	if err != nil {
		return nil, fmt.Errorf("invalid signed request: %w", err)
	}
	if len(signed.Data) == 0 {
		return nil, errors.New("signed request missing payload")
	}
	plain, err := om.crypto.DecryptAES(signed.Data)
	if err != nil {
		return nil, fmt.Errorf("decrypt request: %w", err)
	}
	var req OfflineRequest
	if err := json.Unmarshal(plain, &req); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	if strings.TrimSpace(req.LicenseKey) == "" || strings.TrimSpace(req.HardwareHash) == "" {
		return nil, errors.New("request missing license_key or hardware_hash")
	}
	if req.RequestID == "" {
		req.RequestID = uuid.New().String()
	}
	if req.Status == "" {
		req.Status = "pending"
	}
	if req.Timestamp.IsZero() {
		req.Timestamp = time.Now()
	}

	existing, err := om.store.GetOfflineRequest(ctx, req.RequestID)
	if err == nil && existing != nil {
		return existing, nil
	}
	if err := om.store.CreateOfflineRequest(ctx, &req); err != nil {
		return nil, err
	}
	slog.Info("offline request imported", "request_id", req.RequestID, "license_key", req.LicenseKey)
	return &req, nil
}

func (om *OfflineManager) VerifyOfflineLicense(ctx context.Context, b64SignedLicense string) (*License, error) {
	signedLicense, err := UnmarshalFromBase64(b64SignedLicense)
	if err != nil {
		return nil, err
	}

	license, err := om.crypto.VerifyLicense(signedLicense)
	if err != nil {
		return nil, err
	}

	// P2 修复：增加过期/吊销检查（与在线验证保持一致）
	// 之前只验证 RSA 签名，离线 license 可在过期后继续使用。
	if time.Now().After(license.ExpiresAt) {
		return nil, fmt.Errorf("%w: expired at %s", ErrLicenseExpired, license.ExpiresAt.Format(time.RFC3339))
	}
	if license.RevokedAt != nil {
		return nil, fmt.Errorf("%w: revoked at %s", ErrLicenseRevoked, license.RevokedAt.Format(time.RFC3339))
	}

	return license, nil
}
