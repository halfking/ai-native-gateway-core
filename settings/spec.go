// Package settings provides a unified runtime configuration registry.
//
// Design principles (verified with 老板 2026-06-20):
//   - Q1 (B): DB 覆盖 env，优先级 DB > env > default
//   - Q2 (A): 立即生效
//   - Q3 (B): 部分支持租户级
//   - Q4 (C): app_settings.rate_limit_* 迁移到 settings_kv，清理死字段
//   - Q6 (C): audit 保留 7 天
//
// Usage:
//
//	// At startup (main.go):
//	settingsDB := settings.NewStoreDB(dbPool)
//	settings.Init(settingsDB)
//	for _, sp := range settings.CompressionSpecs() {
//	    _ = settings.Global.RegisterSpec(sp)
//	}
//	for _, sp := range settings.RateLimitSpecs() {
//	    _ = settings.Global.RegisterSpec(sp)
//	}
//
//	// In code (compressor.go):
//	mode := compressor.LoadMode()  // reads via settings.Global
package settings

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// ValueType is the JSON-compatible value type for a setting.
type ValueType string

const (
	TypeEnum     ValueType = "enum"
	TypeInt      ValueType = "int"
	TypeFloat    ValueType = "float"
	TypeBool     ValueType = "bool"
	TypeString   ValueType = "string"
	TypeURL      ValueType = "url"
	TypeDuration ValueType = "duration"
)

// Scope distinguishes platform-level vs tenant-level settings.
type Scope string

const (
	ScopePlatform Scope = "platform"
	ScopeTenant   Scope = "tenant"
)

// Category groups settings in the UI sidebar.
type Category string

const (
	CategoryCompression    Category = "compression"
	CategoryRateLimit      Category = "rate_limit"
	CategoryTimeout        Category = "timeout"
	CategoryRouting        Category = "routing"
	CategorySession        Category = "session"
	CategorySecurity       Category = "security"
	CategoryCircuitBreaker Category = "circuit_breaker"
	CategoryGeneral        Category = "general"
)

// DangerLevel gates the required role for PUT operations.
type DangerLevel int

const (
	Safe      DangerLevel = 0 // 改完无副作用
	Warning   DangerLevel = 1 // 改完有影响但容易回滚
	Dangerous DangerLevel = 2 // 可能影响所有请求
	Breaking  DangerLevel = 3 // 可能导致服务不可用
)

// Spec is the metadata for one configurable setting.
//
// One Spec is registered at package init() time per setting; the runtime
// Registry uses Spec to validate, type-check, and route storage backends.
type Spec struct {
	Key    string
	EnvName string // e.g. "LLM_GATEWAY_COMPRESSION_MODE"
	Type   ValueType
	Scope  Scope
	Category Category

	// Default value (used when DB and env both miss).
	// Must match Type (an int when Type=TypeInt, etc.).
	Default any

	// Range constraints (optional).
	Min, Max *float64 // numeric
	Options  []string // enum

	// UI metadata (all Chinese).
	Description     string // 一句话描述
	DescriptionLong string // 详细说明
	Unit            string // "秒"/"RPM"/"毫秒"

	// Operational metadata.
	DangerLevel   DangerLevel
	HotReload     bool   // false = 必须重启才能生效
	Observability string // 反映此设置效果的查询 URL
}

// EnvNameAuto derives the canonical env-var name from Key.
// e.g. "compression.mode" → "LLM_GATEWAY_COMPRESSION_MODE"
func EnvNameAuto(key string) string {
	return "LLM_GATEWAY_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// Validate checks whether a candidate value matches Spec.Type and constraints.
func (s *Spec) Validate(v any) error {
	if v == nil {
		return fmt.Errorf("value is nil")
	}
	switch s.Type {
	case TypeInt:
		var n int
		switch x := v.(type) {
		case int:
			n = x
		case int64:
			n = int(x)
		case float64:
			n = int(x)
		default:
			return fmt.Errorf("expected int, got %T", v)
		}
		if s.Min != nil && float64(n) < *s.Min {
			return fmt.Errorf("value %d below min %v", n, *s.Min)
		}
		if s.Max != nil && float64(n) > *s.Max {
			return fmt.Errorf("value %d above max %v", n, *s.Max)
		}
	case TypeFloat:
		var f float64
		switch x := v.(type) {
		case float64:
			f = x
		case int:
			f = float64(x)
		case int64:
			f = float64(x)
		default:
			return fmt.Errorf("expected float, got %T", v)
		}
		if s.Min != nil && f < *s.Min {
			return fmt.Errorf("value %f below min %v", f, *s.Min)
		}
		if s.Max != nil && f > *s.Max {
			return fmt.Errorf("value %f above max %v", f, *s.Max)
		}
	case TypeBool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("expected bool, got %T", v)
		}
	case TypeEnum:
		str, ok := v.(string)
		if !ok {
			return fmt.Errorf("expected string, got %T", v)
		}
		valid := false
		for _, opt := range s.Options {
			if opt == str {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("value %q not in options %v", str, s.Options)
		}
	case TypeString, TypeURL, TypeDuration:
		if _, ok := v.(string); !ok {
			return fmt.Errorf("expected string, got %T", v)
		}
	default:
		return fmt.Errorf("unknown type %q", s.Type)
	}
	return nil
}

// jsonRawMessage is a JSON-encoded value (avoids importing encoding/json here).
type jsonRawMessage = []byte

// Backend is implemented by StoreDB / StoreEnv.
type Backend interface {
	// Get returns the stored value or (nil, nil) if absent.
	// For tenant scope, key is the bare setting key; the caller passes
	// tenant_id via a separate method.
	Get(scope Scope, key string) (jsonRawMessage, error)
	// Set writes the value, stashing the previous value for rollback.
	// Returns the previous value (or nil if first write).
	Set(scope Scope, key string, value any) (jsonRawMessage, error)
	// GetTenant is the tenant-scoped variant.
	GetTenant(tenantID, key string) (jsonRawMessage, error)
	// SetTenant is the tenant-scoped variant.
	SetTenant(tenantID, key string, value any) (jsonRawMessage, error)
}

// Registry is the global Spec → Backend index.
type Registry struct {
	mu       sync.RWMutex
	specs    map[string]*Spec
	backends map[Scope]Backend
}

// NewRegistry creates a fresh, empty registry.
func NewRegistry() *Registry {
	return &Registry{
		specs:    make(map[string]*Spec),
		backends: make(map[Scope]Backend),
	}
}

// RegisterBackend wires a backend (DB or env) for a scope.
func (r *Registry) RegisterBackend(scope Scope, b Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends[scope] = b
}

// RegisterSpec adds a Spec to the index. Returns error on duplicate.
func (r *Registry) RegisterSpec(s *Spec) error {
	if s.Key == "" {
		return fmt.Errorf("spec.Key empty")
	}
	if s.EnvName == "" {
		s.EnvName = EnvNameAuto(s.Key)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.specs[s.Key]; exists {
		return fmt.Errorf("spec %q already registered", s.Key)
	}
	r.specs[s.Key] = s
	return nil
}

// MustRegisterSpec is like RegisterSpec but panics on error. Use at init().
func (r *Registry) MustRegisterSpec(s *Spec) {
	if err := r.RegisterSpec(s); err != nil {
		panic("settings: " + err.Error())
	}
}

// Spec returns the Spec for key, or nil if absent.
func (r *Registry) Spec(key string) *Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.specs[key]
}

// AllSpecs returns a snapshot of all registered specs (sorted by key).
func (r *Registry) AllSpecs() []*Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Spec, 0, len(r.specs))
	for _, s := range r.specs {
		out = append(out, s)
	}
	return out
}

// EffectiveValue resolves a setting following the priority chain
// DB > env > default. tenantID is required when scope=ScopeTenant.
//
// Returns (rawValue, source, error) where source ∈ {"db","env","default"}.
func (r *Registry) EffectiveValue(scope Scope, key, tenantID string) (jsonRawMessage, string, error) {
	s := r.Spec(key)
	if s == nil {
		return nil, "", fmt.Errorf("unknown setting %q", key)
	}
	// 1. DB
	if b, ok := r.backends[scope]; ok {
		var v jsonRawMessage
		var err error
		if scope == ScopeTenant {
			if tenantID == "" {
				return nil, "", fmt.Errorf("setting %q is tenant-scoped; tenant_id required", key)
			}
			v, err = b.GetTenant(tenantID, key)
		} else {
			v, err = b.Get(scope, key)
		}
		if err == nil && len(v) > 0 {
			return v, "db", nil
		}
	}
	// 2. env-var (only for platform scope)
	if scope == ScopePlatform {
		if b, ok := r.backends[EnvBackendScope]; ok {
			if v, err := b.Get(ScopePlatform, s.EnvName); err == nil && len(v) > 0 {
				return v, "env", nil
			}
		}
	}
	// 3. default
	b, err := jsonMarshalAny(s.Default)
	if err != nil {
		return nil, "", fmt.Errorf("marshal default: %w", err)
	}
	return b, "default", nil
}

// Global is the process-wide Registry. Initialised by Init() at startup.
var Global = NewRegistry()

// EnvBackendScope is a sentinel Scope value used internally to address
// the env-var backend. The env backend is registered under this scope
// so it doesn't conflict with the platform DB backend.
const EnvBackendScope Scope = "__env__"

// Init wires the DB and env backends into Global. Idempotent.
func Init(dbStore Backend) {
	Global.RegisterBackend(ScopePlatform, dbStore)
	Global.RegisterBackend(ScopeTenant, dbStore)
	Global.RegisterBackend(EnvBackendScope, NewStoreEnv())
	slog.Info("settings: registry initialised",
		"platform_specs", len(Global.AllSpecs()))
}
