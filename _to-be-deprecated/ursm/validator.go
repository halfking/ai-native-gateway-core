package ursm

import (
	"fmt"
	"time"

	"github.com/go-playground/validator/v10"
)

// 全局validator实例
var validate = validator.New()

// ValidateRecordRequest 验证RecordRequest参数
func ValidateRecordRequest(req RecordRequestAPI) error {
	if err := validate.Struct(req); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}
	return nil
}

// ValidateUpdateProvider 验证UpdateProvider参数
func ValidateUpdateProvider(req UpdateProviderAPI) error {
	if err := validate.Struct(req); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// 至少提供一个更新字段
	if req.Enabled == nil && req.ManualDisabled == nil {
		return fmt.Errorf("at least one field (enabled, manual_disabled) must be provided")
	}

	return nil
}

// ValidateUpdateCredential 验证UpdateCredential参数
func ValidateUpdateCredential(req UpdateCredentialAPI) error {
	if err := validate.Struct(req); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// 至少提供一个更新字段
	if req.AvailabilityState == nil && req.ManualDisabled == nil && req.QuotaState == nil {
		return fmt.Errorf("at least one field (availability_state, manual_disabled, quota_state) must be provided")
	}

	return nil
}

// RecordRequestAPI 请求状态回写API
type RecordRequestAPI struct {
	RequestID    string `validate:"required"`
	CredentialID int    `validate:"required,gt=0"`
	RawModel     string `validate:"required"`
	SessionID    string `validate:"required"`
	Success      bool
	LatencyMs    int       `validate:"gte=0"`
	ErrorKind    string    // 可选
	Timestamp    time.Time `validate:"required"`
}

// UpdateProviderAPI 供应商状态修改API
type UpdateProviderAPI struct {
	ProviderID     int    `validate:"required,gt=0"`
	Enabled        *bool  // 可选
	ManualDisabled *bool  // 可选
	Reason         string `validate:"required,max=500"`
	Actor          string `validate:"required,max=100"`
}

// UpdateCredentialAPI 凭据状态修改API
type UpdateCredentialAPI struct {
	CredentialID      int     `validate:"required,gt=0"`
	AvailabilityState *string `validate:"omitempty,oneof=ready degraded auth_failed rate_limited unreachable suspended"`
	ManualDisabled    *bool   // 可选
	QuotaState        *string `validate:"omitempty,oneof=ok periodic_exhausted balance_exhausted permanently_exhausted"`
	Reason            string  `validate:"required,max=500"`
	Actor             string  `validate:"required,max=100"`
}
