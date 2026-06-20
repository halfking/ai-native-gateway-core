package settings

import "os"

// StoreEnv reads values from process environment.
// It honours the value of `key` AS the env-var name directly (so the
// caller must pass Spec.EnvName, not Spec.Key, when consulting env).
// This preserves back-compat with existing LLM_GATEWAY_* env-vars.
type StoreEnv struct{}

// NewStoreEnv builds a new env backend.
func NewStoreEnv() *StoreEnv { return &StoreEnv{} }

// Get returns the env value for the env-var name, or (nil, nil) if unset.
func (s *StoreEnv) Get(_ Scope, envName string) (jsonRawMessage, error) {
	raw := os.Getenv(envName)
	if raw == "" {
		return nil, nil
	}
	return []byte(raw), nil
}

// Set is a no-op — env vars are read-only at runtime.
func (s *StoreEnv) Set(_ Scope, _ string, _ any) (jsonRawMessage, error) {
	return nil, nil
}

// GetTenant is a no-op (env vars are process-global, not per-tenant).
func (s *StoreEnv) GetTenant(_ string, _ string) (jsonRawMessage, error) {
	return nil, nil
}

// SetTenant is a no-op.
func (s *StoreEnv) SetTenant(_ string, _ string, _ any) (jsonRawMessage, error) {
	return nil, nil
}
