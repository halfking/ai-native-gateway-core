package config

import "fmt"

const (
	RuntimeRoleActive      = "active"
	RuntimeRoleTrafficOnly = "traffic-only"
)

// NormalizeRuntimeRole validates the process role used by blue-green deploys.
// Empty values retain the active default; unknown values fail closed so a
// candidate can never accidentally start with ambiguous worker ownership.
func NormalizeRuntimeRole(role string) (string, error) {
	if role == "" {
		return RuntimeRoleActive, nil
	}
	switch role {
	case RuntimeRoleActive, RuntimeRoleTrafficOnly:
		return role, nil
	default:
		return "", fmt.Errorf("invalid runtime role %q (want %s or %s)", role, RuntimeRoleActive, RuntimeRoleTrafficOnly)
	}
}

func (c *Config) IsTrafficOnly() bool {
	return c != nil && c.RuntimeRole == RuntimeRoleTrafficOnly
}

// ValidateRuntimeRole applies the fail-closed role contract after YAML/env
// precedence has been resolved.
func (c *Config) ValidateRuntimeRole() error {
	role, err := NormalizeRuntimeRole(c.RuntimeRole)
	if err != nil {
		return err
	}
	c.RuntimeRole = role
	return nil
}
