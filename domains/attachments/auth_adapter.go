package attachments

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
)

// keyVerifierAdapter 将 authentication.KeyVerifier 适配为 attachments.KeyVerifier。
// 用于解耦两个包，避免循环依赖。
type keyVerifierAdapter struct {
	verifier *authentication.KeyVerifier
}

// NewKeyVerifierAdapter 创建适配器。
func NewKeyVerifierAdapter(verifier *authentication.KeyVerifier) KeyVerifier {
	if verifier == nil {
		return nil
	}
	return &keyVerifierAdapter{verifier: verifier}
}

func (a *keyVerifierAdapter) Verify(ctx context.Context, rawKey string) (*KeyInfo, error) {
	authKeyInfo, err := a.verifier.Verify(ctx, rawKey)
	if err != nil {
		return nil, err
	}

	// 转换为 attachments.KeyInfo
	// Status 为 "active" 且非 disabled 视为 enabled
	enabled := authKeyInfo.Status == "active"

	return &KeyInfo{
		ID:        authKeyInfo.ID,
		KeyPrefix: authKeyInfo.KeyPrefix,
		TenantID:  authKeyInfo.TenantID,
		Status:    authKeyInfo.Status,
		Enabled:   enabled,
	}, nil
}

func (a *keyVerifierAdapter) Enabled() bool {
	return a.verifier != nil && a.verifier.Enabled()
}
