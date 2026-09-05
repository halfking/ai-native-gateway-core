package v2

import (
	"errors"
	"fmt"
	"strings"
)

// ErrOutOfScope marks an intentional strict-canary rejection. Callers should
// not retry it or write any side effects.
var ErrOutOfScope = errors.New("ursm.v2: identity is outside strict canary scope")

// Scope is the immutable identity boundary for strict URSM v2 canaries.
// A strict scope admits a node only when its tenant, credential, and raw model
// all belong to the configured allowlists.
type Scope struct {
	strict      bool
	tenants     map[string]struct{}
	credentials map[int]struct{}
	models      map[string]struct{}
}

func newScope(strict bool, tenants []string, credentials []int, models []string) Scope {
	s := Scope{strict: strict}
	if !strict {
		return s
	}
	s.tenants = make(map[string]struct{}, len(tenants))
	for _, tenant := range tenants {
		s.tenants[tenant] = struct{}{}
	}
	s.credentials = make(map[int]struct{}, len(credentials))
	for _, credentialID := range credentials {
		s.credentials[credentialID] = struct{}{}
	}
	s.models = make(map[string]struct{}, len(models))
	for _, model := range models {
		s.models[model] = struct{}{}
	}
	return s
}

// Strict reports whether the scope is enforcing exact canary identity.
func (s Scope) Strict() bool { return s.strict }

// Allows returns true for every identity outside strict-canary mode. In strict
// mode all identity dimensions must be present and explicitly allowlisted.
func (s Scope) Allows(tenant string, credentialID int, rawModel string) bool {
	if !s.strict {
		return true
	}
	if strings.TrimSpace(tenant) == "" || credentialID <= 0 || strings.TrimSpace(rawModel) == "" {
		return false
	}
	_, tenantAllowed := s.tenants[tenant]
	_, credentialAllowed := s.credentials[credentialID]
	_, modelAllowed := s.models[rawModel]
	return tenantAllowed && credentialAllowed && modelAllowed
}

func validateStrictCanaryScope(c Config) error {
	if !c.StrictCanary {
		return nil
	}
	if c.Mode != "canary" {
		return fmt.Errorf("URSM_V2_STRICT_CANARY requires URSM_V2_MODE=canary")
	}
	if c.CanaryPercent != 0 {
		return fmt.Errorf("URSM_V2_STRICT_CANARY requires URSM_V2_CANARY_PERCENT=0")
	}
	if len(c.CanaryTenants) == 0 || len(c.CanaryCredentials) == 0 || len(c.CanaryModels) == 0 {
		return fmt.Errorf("URSM_V2_STRICT_CANARY requires non-empty tenant, credential, and model allowlists")
	}
	if strings.TrimSpace(c.RedisKeyPrefix) == "" || c.RedisKeyPrefix == DefaultConfig().RedisKeyPrefix {
		return fmt.Errorf("URSM_V2_STRICT_CANARY requires a non-default URSM_V2_REDIS_KEY_PREFIX")
	}
	if !strings.HasSuffix(c.RedisKeyPrefix, ":") {
		return fmt.Errorf("URSM_V2_REDIS_KEY_PREFIX must end with ':'")
	}
	for _, credentialID := range c.CanaryCredentials {
		if credentialID <= 0 {
			return fmt.Errorf("URSM_V2_CANARY_CREDENTIALS must contain positive integer IDs")
		}
	}
	return nil
}
