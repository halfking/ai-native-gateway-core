package licensing

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

type OfflineManager struct {
	crypto *CryptoConfig
	store  Store
}

func NewOfflineManager(crypto *CryptoConfig, store Store) *OfflineManager {
	return &OfflineManager{
		crypto: crypto,
		store:  store,
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

func (om *OfflineManager) ApproveOfflineRequest(ctx context.Context, requestID string) (*SignedLicense, error) {
	req, err := om.store.GetOfflineRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}

	lic, err := om.store.GetLicense(ctx, req.LicenseKey)
	if err != nil {
		return nil, err
	}

	signedLicense, err := om.crypto.SignLicense(lic)
	if err != nil {
		return nil, err
	}

	if err := om.store.ApproveOfflineRequest(ctx, requestID, signedLicense); err != nil {
		return nil, err
	}

	slog.Info("offline request approved", "request_id", requestID, "license_key", req.LicenseKey)

	return signedLicense, nil
}

func (om *OfflineManager) VerifyOfflineLicense(ctx context.Context, b64SignedLicense string) (*License, error) {
	signedLicense, err := UnmarshalFromBase64(b64SignedLicense)
	if err != nil {
		return nil, err
	}

	return om.crypto.VerifyLicense(signedLicense)
}
