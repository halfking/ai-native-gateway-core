package credentialquota

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

type Config struct {
	Mode       Mode
	Resolver   PolicyResolver
	Redis      redis.UniversalClient
	LeaseID    func() string
	Clock      func() time.Time
	LeaseTTL   time.Duration
	RenewAfter time.Duration
}

type Service struct {
	cfg    Config
	mode   atomic.Value // Mode
	active atomic.Int64
}

func New(cfg Config) *Service {
	if cfg.LeaseID == nil {
		cfg.LeaseID = defaultLeaseID
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = LeaseTTL
	}
	if cfg.RenewAfter <= 0 {
		cfg.RenewAfter = RenewalInterval
	}
	s := &Service{cfg: cfg}
	s.setMode(cfg.Mode)
	return s
}

func defaultLeaseID() string {
	return fmt.Sprintf("lease-%d", time.Now().UnixNano())
}

func (s *Service) Mode() Mode {
	m, _ := s.mode.Load().(Mode)
	return m
}

func (s *Service) SetMode(m Mode) { s.setMode(m) }

func (s *Service) setMode(m Mode) {
	if m == ModeOff || m == ModeShadow || m == ModeEnforce {
		s.mode.Store(m)
		return
	}
	s.mode.Store(ModeOff)
}

func (s *Service) Acquire(ctx context.Context, request Request) (Decision, error) {
	credentialID := request.CredentialID
	clientType := normalizeClientType(request.ClientType)
	policy, found := s.cfg.Resolver.Resolve(ctx, credentialID, clientType)

	if !found || !hasLimit(policy) {
		return Decision{Allowed: true, Outcome: OutcomeUnlimited}, nil
	}

	if s.cfg.Redis == nil {
		return Decision{Allowed: true, Outcome: OutcomeUnlimited, RedisErr: ErrRedis}, nil
	}

	key := concurrentKey(credentialID, clientType)
	limit := policy.MaxConcurrent
	now := s.cfg.Clock()
	leaseID := s.cfg.LeaseID()
	enforce := s.Mode() == ModeEnforce && policyEnforced(policy, now)

	acquired, expiry, err := acquireLease(ctx, s.cfg.Redis, key, leaseID, limit, s.cfg.LeaseTTL)
	if err != nil {
		s.observe(OutcomeRedisError)
		return Decision{Allowed: true, Outcome: OutcomeRedisError, RedisErr: err}, nil
	}
	if !acquired {
		if enforce {
			s.observe(OutcomeExceeded)
			return Decision{Allowed: false, Outcome: OutcomeExceeded, RedisErr: ErrLimitExceeded}, ErrLimitExceeded
		}
		s.observe(OutcomeShadow)
		return Decision{Allowed: true, Outcome: OutcomeShadow}, nil
	}
	s.active.Add(1)
	s.observe(OutcomeAllowed)
	return Decision{
		Allowed: true,
		Outcome: OutcomeAllowed,
		Lease: &Lease{
			ID:           leaseID,
			CredentialID: credentialID,
			ClientType:   clientType,
			ExpiresAt:    expiry,
		},
	}, nil
}

func (s *Service) Renew(ctx context.Context, lease Lease) (Lease, error) {
	if s.cfg.Redis == nil {
		return lease, ErrRedis
	}
	key := concurrentKey(lease.CredentialID, lease.ClientType)
	renewed, err := renewLease(ctx, s.cfg.Redis, key, lease.ID, s.cfg.LeaseTTL)
	if err != nil {
		s.observeRenew(OutcomeRedisError)
		return lease, err
	}
	if renewed.ID == "" {
		s.observeRenew(OutcomeExceeded)
		return lease, ErrLeaseNotFound
	}
	lease.ExpiresAt = renewed.ExpiresAt
	s.observeRenew(OutcomeAllowed)
	return lease, nil
}

func (s *Service) Release(ctx context.Context, lease Lease) error {
	if s.cfg.Redis == nil {
		return nil
	}
	key := concurrentKey(lease.CredentialID, lease.ClientType)
	if err := releaseLease(ctx, s.cfg.Redis, key, lease.ID); err != nil {
		s.observeRelease(OutcomeRedisError)
		return err
	}
	s.active.Add(-1)
	s.observeRelease(OutcomeAllowed)
	return nil
}

func (s *Service) Stats(ctx context.Context, request Request) (Stats, error) {
	if s.cfg.Redis == nil {
		return Stats{}, ErrRedis
	}
	active, err := countActive(ctx, s.cfg.Redis, concurrentKey(request.CredentialID, normalizeClientType(request.ClientType)))
	if err != nil {
		return Stats{}, err
	}
	return Stats{Active: active}, nil
}

func (s *Service) Reset(ctx context.Context, request Request) error {
	if s.cfg.Redis == nil {
		return ErrRedis
	}
	return resetLeaseKey(ctx, s.cfg.Redis, concurrentKey(request.CredentialID, normalizeClientType(request.ClientType)))
}

func (s *Service) observe(outcome Outcome)  { metricAcquire.WithLabelValues(string(outcome)).Inc() }
func (s *Service) observeRelease(o Outcome) { metricRelease.WithLabelValues(string(o)).Inc() }
func (s *Service) observeRenew(o Outcome)   { metricRenew.WithLabelValues(string(o)).Inc() }

func hasLimit(p Policy) bool {
	return p.MaxConcurrent > 0 || p.MaxFPSlots > 0
}

func policyEnforced(p Policy, now time.Time) bool {
	if p.FPEnforceAfter == nil {
		return true
	}
	return !now.Before(*p.FPEnforceAfter)
}

// ensureErrIs exposes redis error helpers without polluting API surface.
var ensureErrIs = func(err error) error {
	if errors.Is(err, ErrRedis) || errors.Is(err, ErrLimitExceeded) {
		return err
	}
	return nil
}
