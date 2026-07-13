package licensing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

func (om *OfflineManager) VerifyOfflineLicense(ctx context.Context, b64SignedLicense string) (*License, error) {
	signedLicense, err := UnmarshalFromBase64(b64SignedLicense)
	if err != nil {
		return nil, err
	}

	return om.crypto.VerifyLicense(signedLicense)
}
