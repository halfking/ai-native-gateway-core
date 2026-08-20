package pluginruntime

import (
	"fmt"
	"strings"
)

// SandboxPolicy declares the isolation and resource limits applied to a plugin
// process before it receives any request data. Workflow B (execution sandbox and
// isolation) relies on this policy to refuse write capabilities, strip sensitive
// environment variables and gate the DTO exposure level until a real sandbox is
// established. A nil or invalid policy means "no sandbox": the enforcer must
// fail-closed for any privileged operation.
type SandboxPolicy struct {
	// Available reports whether the runtime actually provides isolation.
	// Until this is true, write capabilities and credential-bearing env are refused.
	Available bool

	// OSUser is the unprivileged account the plugin runs as. Empty = inherited (unsafe).
	OSUser string

	// MemoryBytes caps the plugin resident set. 0 = unset / unbounded.
	MemoryBytes int64

	// CPUShares is the relative CPU weight (cgroup semantics). 0 = unset.
	CPUShares int64

	// MaxConcurrent caps parallel adapter invocations for one plugin.
	MaxConcurrent int

	// NetworkEgressAllowlist is the set of host:port the plugin may dial.
	// Empty = no egress allowed (deny by default).
	NetworkEgressAllowlist []string

	// FilesystemAllowlist is the set of paths the plugin may read/write.
	// Empty = no filesystem access beyond its own sandbox root.
	FilesystemAllowlist []string

	// EnvAllowlist is the set of environment variable NAMES exposed to the plugin.
	// Only names present here are copied from the gateway environment. All
	// DSN/credential-bearing variables are dropped regardless of this list.
	EnvAllowlist []string
}

// DefaultSandboxPolicy returns a deny-by-default policy that provides no
// privilege. It is safe to use as the zero-trust baseline before any real
// sandbox is wired up.
func DefaultSandboxPolicy() SandboxPolicy {
	return SandboxPolicy{
		Available:              false,
		OSUser:                 "",
		MemoryBytes:            0,
		CPUShares:              0,
		MaxConcurrent:          1,
		NetworkEgressAllowlist: nil,
		FilesystemAllowlist:    nil,
		EnvAllowlist:           nil,
	}
}

// ValidateSandboxPolicy checks internal consistency. A policy that claims
// availability must at least run as a non-empty OS user and set a memory cap;
// otherwise it is not a real sandbox and must not be trusted.
func ValidateSandboxPolicy(p SandboxPolicy) error {
	if !p.Available {
		return nil
	}
	if strings.TrimSpace(p.OSUser) == "" {
		return fmt.Errorf("sandbox: available policy requires a non-empty os_user")
	}
	if p.MemoryBytes <= 0 {
		return fmt.Errorf("sandbox: available policy requires memory_bytes > 0")
	}
	if p.MaxConcurrent <= 0 {
		return fmt.Errorf("sandbox: max_concurrent must be >= 1")
	}
	return nil
}

// sensitiveEnvKey reports whether an environment variable name carries
// credentials, DSNs or cross-tenant connection secrets that must never reach a
// plugin process. Matching is case-insensitive on known suffixes.
func sensitiveEnvKey(name string) bool {
	upper := strings.ToUpper(name)
	for _, suffix := range []string{
		"PASSWORD", "SECRET", "TOKEN", "APIKEY", "API_KEY",
		"PRIVATEKEY", "PRIVATE_KEY", "DSN", "DATABASE_URL",
		"CREDENTIAL", "AUTH", "CERT", "KEY",
	} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}
