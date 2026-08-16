package credentialquota

import (
	"context"
	"errors"
	"time"
)

type Mode string

const (
	ModeOff     Mode = "off"
	ModeShadow  Mode = "shadow"
	ModeEnforce Mode = "enforce"
)

const (
	LeaseTTL        = 5 * time.Minute
	RenewalInterval = 100 * time.Second
)

var (
	ErrLimitExceeded = errors.New("credential client quota exceeded")
	ErrRedis         = errors.New("credential client quota redis error")
	ErrLeaseNotFound = errors.New("credential client quota lease not found")
)

type Policy struct {
	CredentialID   int64
	OwnerTenantID  string
	ClientType     string
	MaxConcurrent  int
	MaxFPSlots     int
	FPEnforceAfter *time.Time
}

type Request struct {
	CredentialID int64
	ClientType   string
}

type Lease struct {
	ID           string
	CredentialID int64
	ClientType   string
	ExpiresAt    time.Time
}

type Outcome string

const (
	OutcomeAllowed    Outcome = "allowed"
	OutcomeExceeded   Outcome = "exceeded"
	OutcomeUnlimited  Outcome = "unlimited"
	OutcomeRedisError Outcome = "redis_error"
	OutcomeShadow     Outcome = "shadow_exceeded"
)

type Decision struct {
	Allowed  bool
	Outcome  Outcome
	Lease    *Lease
	RedisErr error
}

type Stats struct {
	Active int64 `json:"active"`
}

type PolicyResolver interface {
	Resolve(ctx context.Context, credentialID int64, clientType string) (Policy, bool)
}

type Enforcer interface {
	Acquire(ctx context.Context, request Request) (Decision, error)
	Renew(ctx context.Context, lease Lease) (Lease, error)
	Release(ctx context.Context, lease Lease) error
	Mode() Mode
}

type Administrator interface {
	Stats(ctx context.Context, request Request) (Stats, error)
	Reset(ctx context.Context, request Request) error
}

type Reloader interface {
	Reload(ctx context.Context) error
}
