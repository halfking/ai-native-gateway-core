package licensing

import "time"

type License struct {
	ID               int64      `json:"id"`
	LicenseKey       string     `json:"license_key"`
	CustomerName     string     `json:"customer_name"`
	CustomerEmail    string     `json:"customer_email"`
	MaxDevices       int        `json:"max_devices"`
	SubscriptionTier string     `json:"subscription_tier"`
	Features         []string   `json:"features"`
	ExpiresAt        time.Time  `json:"expires_at"`
	CreatedAt        time.Time  `json:"created_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
}

type SignedLicense struct {
	Data      []byte `json:"data"`
	Signature []byte `json:"signature"`
}

type Device struct {
	ID               int64      `json:"id"`
	LicenseID        int64      `json:"license_id"`
	InstanceID       string     `json:"instance_id"`
	HardwareHash     string     `json:"hardware_hash"`
	DeviceName       string     `json:"device_name"`
	ActivatedAt      time.Time  `json:"activated_at"`
	LastHeartbeat    *time.Time `json:"last_heartbeat,omitempty"`
	Status           string     `json:"status"`
	DeactivatedAt    *time.Time `json:"deactivated_at,omitempty"`
	DeactivateReason string     `json:"deactivate_reason,omitempty"`
}

type ActivationRequest struct {
	LicenseKey   string `json:"license_key"`
	HardwareHash string `json:"hardware_hash"`
	InstanceID   string `json:"instance_id"`
	DeviceName   string `json:"device_name"`
	Version      string `json:"version"`
}

type ActivationResponse struct {
	Success        bool           `json:"success"`
	SignedLicense  *SignedLicense `json:"signed_license,omitempty"`
	ExpiresAt      *time.Time     `json:"expires_at,omitempty"`
	ActiveDevices  []Device       `json:"active_devices"`
	Message        string         `json:"message,omitempty"`
	NeedDeactivate bool           `json:"need_deactivate,omitempty"`
}

type DeactivateRequest struct {
	LicenseKey   string `json:"license_key"`
	HardwareHash string `json:"hardware_hash"`
	Reason       string `json:"reason"`
}

type OfflineRequest struct {
	LicenseKey     string     `json:"license_key"`
	HardwareHash   string     `json:"hardware_hash"`
	InstanceID     string     `json:"instance_id"`
	DeviceName     string     `json:"device_name"`
	RequestID      string     `json:"request_id"`
	Timestamp      time.Time  `json:"timestamp"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty"`
	ActivationCode string     `json:"activation_code,omitempty"`
	Status         string     `json:"status"`
	RejectReason   string     `json:"reject_reason,omitempty"`
}

type ProductModule struct {
	ID          int       `json:"id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	Icon        *string   `json:"icon,omitempty"`
	SettingKey  *string   `json:"setting_key,omitempty"`
	IsBase      bool      `json:"is_base"`
	SortOrder   int       `json:"sort_order"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ProductModuleFeature struct {
	ID          int       `json:"id"`
	ModuleKey   string    `json:"module_key"`
	FeatureKey  string    `json:"feature_key"`
	FeatureName string    `json:"feature_name"`
	Description string    `json:"description"`
	SettingKey  *string   `json:"setting_key,omitempty"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
}

type LicenseModule struct {
	LicenseID int64      `json:"license_id"`
	ModuleKey string     `json:"module_key"`
	Enabled   bool       `json:"enabled"`
	Config    []byte     `json:"config,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type SubscriptionTier struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceCents  int    `json:"price_cents"`
	SortOrder   int    `json:"sort_order"`
}

type TierModuleMap struct {
	TierCode    string `json:"tier_code"`
	ModuleKey   string `json:"module_key"`
	MaxFeatures string `json:"max_features,omitempty"`
}
