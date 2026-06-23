package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// PostgreSQL
	DatabaseURL string `yaml:"database_url" env:"LLM_GATEWAY_DATABASE_URL"`

	// Secrets
	SecretKey               string `yaml:"secret_key" env:"LLM_GATEWAY_SECRET_KEY"`
	CredentialEncryptionKey string `yaml:"credential_encryption_key" env:"LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY"`

	// Redis
	RedisAddr     string `yaml:"redis_addr" env:"LLM_GATEWAY_REDIS_ADDR"`
	RedisPassword string `yaml:"redis_password" env:"LLM_GATEWAY_REDIS_PASSWORD"`
	RedisDB       int    `yaml:"redis_db" env:"LLM_GATEWAY_REDIS_DB"`

	// Sessions
	SessionTTLHours int `yaml:"session_ttl_hours" env:"LLM_GATEWAY_SESSION_TTL_HOURS"`

	// Server
	Listen      string `yaml:"listen" env:"LLM_GATEWAY_LISTEN"`
	LogLevel    string `yaml:"log_level" env:"LLM_GATEWAY_LOG_LEVEL"`
	APIKey      string `yaml:"api_key" env:"LLM_GATEWAY_API_KEY"`
	CORSOrigins string `yaml:"cors_origins" env:"LLM_GATEWAY_CORS_ORIGINS"`
	StaticDir   string `yaml:"static_dir" env:"LLM_GATEWAY_STATIC_DIR"`

	// Upstream
	PythonEndpoint  string `yaml:"python_endpoint" env:"LLM_GATEWAY_PYTHON_ENDPOINT"`
	AdminAPIKey     string `yaml:"admin_api_key" env:"LLM_GATEWAY_ADMIN_API_KEY"`
	UpstreamURL     string `yaml:"upstream_url" env:"LLM_GATEWAY_UPSTREAM"`
	DefaultProvider int    `yaml:"default_provider" env:"LLM_GATEWAY_DEFAULT_PROVIDER"`
	DefaultCred     int    `yaml:"default_credential" env:"LLM_GATEWAY_DEFAULT_CREDENTIAL"`

	// Timeouts (seconds)
	UpstreamTimeout    int `yaml:"upstream_timeout_seconds" env:"LLM_GATEWAY_UPSTREAM_TIMEOUT"`
	StreamTimeout      int `yaml:"stream_timeout_seconds" env:"LLM_GATEWAY_STREAM_TIMEOUT"`
	StreamChunkTimeout int `yaml:"stream_chunk_timeout_seconds" env:"LLM_GATEWAY_STREAM_CHUNK_TIMEOUT"`
	FirstByteTimeout   int `yaml:"first_byte_timeout_seconds" env:"LLM_GATEWAY_FIRST_BYTE_TIMEOUT"`
	KeepaliveInterval  int `yaml:"keepalive_interval_seconds" env:"LLM_GATEWAY_KEEPALIVE_INTERVAL"`

	// Stream failover
	StreamRetryThreshold int `yaml:"stream_retry_threshold" env:"LLM_GATEWAY_STREAM_RETRY_THRESHOLD"`

	// Pool grace period (seconds)
	PoolGracePeriod int `yaml:"pool_grace_period_seconds" env:"LLM_GATEWAY_POOL_GRACE_PERIOD"`

	// Identity
	IdentitySalt string `yaml:"identity_salt" env:"LLM_GATEWAY_IDENTITY_SALT"`

	// Background task mode: "full" (default) or "data-plane" (skip loops owned by Python 71)
	BGMode string `yaml:"bg_mode" env:"LLM_GATEWAY_BG_MODE"`

	// Per-credential virtual fingerprint slot pool (NULL DB limit → default).
	DefaultCredentialConcurrency int  `yaml:"default_credential_concurrency" env:"LLM_GATEWAY_DEFAULT_CREDENTIAL_CONCURRENCY"`
	EnableCredentialFpSlots      bool `yaml:"enable_credential_fp_slots" env:"LLM_GATEWAY_ENABLE_CREDENTIAL_FP_SLOTS"`

	// CredentialFpSlotActiveGateSeconds is the "active holder"
	// threshold (in seconds) for fp_slot preemption. 2026-06-24:
	// when ALL slot candidates fail (active holders), the in-line
	// Acquire path refuses to preempt any slot whose holder has
	// been active within the last ActiveGateSeconds. Per operator
	// spec "5分钟内的会话不允许抢的" — default 300. Set to 0 to
	// fall back to the package default (also 300).
	CredentialFpSlotActiveGateSeconds int `yaml:"credential_fp_slot_active_gate_seconds" env:"LLM_GATEWAY_CREDENTIAL_FP_SLOT_ACTIVE_GATE_SECONDS"`

	// CredentialFpSlotReclaimIdleSeconds is the idle threshold for
	// the BACKGROUND reclaim goroutine. 2026-06-24: when a slot
	// has been silent for at least this many seconds, the goroutine
	// proactively deletes it. Per operator spec "自动清除无活动的
	// 时长，放到30分钟，这个可以作成常量，由系统设置中来设置" —
	// default 1800 (30 min). Set to 0 to fall back to the package
	// default (also 1800). Independent from the active gate so
	// the two knobs can be tuned separately.
	CredentialFpSlotReclaimIdleSeconds int `yaml:"credential_fp_slot_reclaim_idle_seconds" env:"LLM_GATEWAY_CREDENTIAL_FP_SLOT_RECLAIM_IDLE_SECONDS"`

	// EnableDisguise rotates User-Agent, Accept-Language, and (optionally)
	// TLS ClientHello fingerprints. See docs/legal/disguise-compliance.md
	// for the legal/ToS implications.
	EnableDisguise bool `yaml:"enable_disguise" env:"LLM_GATEWAY_ENABLE_DISGUISE"`

	// Config file path (internal, not serialized)
	configPath string `yaml:"-"`
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load loads configuration from environment variables (and optionally a file).
func Load() *Config {
	cfg := &Config{
		DatabaseURL:                        firstNonEmpty(os.Getenv("LLM_GATEWAY_DATABASE_URL"), os.Getenv("DATABASE_URL")),
		SecretKey:                          firstNonEmpty(os.Getenv("LLM_GATEWAY_SECRET_KEY"), os.Getenv("SECRET_KEY")),
		CredentialEncryptionKey:            firstNonEmpty(os.Getenv("LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY"), os.Getenv("CREDENTIAL_ENCRYPTION_KEY")),
		RedisAddr:                          os.Getenv("LLM_GATEWAY_REDIS_ADDR"),
		RedisPassword:                      os.Getenv("LLM_GATEWAY_REDIS_PASSWORD"),
		Listen:                             envOrDefault("LLM_GATEWAY_LISTEN", ":8781"),
		LogLevel:                           envOrDefault("LLM_GATEWAY_LOG_LEVEL", "info"),
		APIKey:                             os.Getenv("LLM_GATEWAY_API_KEY"),
		CORSOrigins:                        os.Getenv("LLM_GATEWAY_CORS_ORIGINS"),
		StaticDir:                          envOrDefault("LLM_GATEWAY_STATIC_DIR", "web/dist"),
		PythonEndpoint:                     os.Getenv("LLM_GATEWAY_PYTHON_ENDPOINT"),
		AdminAPIKey:                        os.Getenv("LLM_GATEWAY_ADMIN_API_KEY"),
		UpstreamURL:                        envOrDefault("LLM_GATEWAY_UPSTREAM", "http://127.0.0.1:8780"),
		IdentitySalt:                       os.Getenv("LLM_GATEWAY_IDENTITY_SALT"),
		BGMode:                             envOrDefault("LLM_GATEWAY_BG_MODE", "full"),
		UpstreamTimeout:                    120,
		StreamTimeout:                      900,
		StreamChunkTimeout:                 300,
		FirstByteTimeout:                   30,
		KeepaliveInterval:                  15,
		SessionTTLHours:                    168,
		StreamRetryThreshold:               5,   // Default: allow stream failover if < 5 chunks sent
		PoolGracePeriod:                    180, // Default: 3 minutes grace period before marking pool as dead
		DefaultCredentialConcurrency:       20,  // 2026-06-24: 5 → 20. 每个凭据 20 个 fp_slot，更宽松避免争抢。
		EnableCredentialFpSlots:            true,
		CredentialFpSlotActiveGateSeconds:  300,   // 5 min — "5 min 内不允许抢的"
		CredentialFpSlotReclaimIdleSeconds: 1800,  // 30 min — 自动清除无活动的时长
		EnableDisguise:                     false, // off by default; opt-in
	}

	if dbStr := os.Getenv("LLM_GATEWAY_REDIS_DB"); dbStr != "" {
		if v, err := strconv.Atoi(dbStr); err == nil {
			cfg.RedisDB = v
		}
	}
	if ttlStr := os.Getenv("LLM_GATEWAY_SESSION_TTL_HOURS"); ttlStr != "" {
		if v, err := strconv.Atoi(ttlStr); err == nil {
			cfg.SessionTTLHours = v
		}
	}
	if pidStr := os.Getenv("LLM_GATEWAY_DEFAULT_PROVIDER"); pidStr != "" {
		if v, err := strconv.Atoi(pidStr); err == nil {
			cfg.DefaultProvider = v
		} else {
			cfg.DefaultProvider = 1
		}
	} else {
		cfg.DefaultProvider = 1
	}
	if cidStr := os.Getenv("LLM_GATEWAY_DEFAULT_CREDENTIAL"); cidStr != "" {
		if v, err := strconv.Atoi(cidStr); err == nil {
			cfg.DefaultCred = v
		} else {
			cfg.DefaultCred = 1
		}
	} else {
		cfg.DefaultCred = 1
	}

	// Boolean env-var overrides.
	if v := os.Getenv("LLM_GATEWAY_ENABLE_DISGUISE"); v != "" {
		cfg.EnableDisguise = v == "true" || v == "1"
	}
	if v := os.Getenv("LLM_GATEWAY_ENABLE_CREDENTIAL_FP_SLOTS"); v != "" {
		cfg.EnableCredentialFpSlots = v == "true" || v == "1"
	}
	if v := os.Getenv("LLM_GATEWAY_DEFAULT_CREDENTIAL_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.DefaultCredentialConcurrency = n
		}
	}
	if v := os.Getenv("LLM_GATEWAY_CREDENTIAL_FP_SLOT_ACTIVE_GATE_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.CredentialFpSlotActiveGateSeconds = n
		}
	}
	if v := os.Getenv("LLM_GATEWAY_CREDENTIAL_FP_SLOT_RECLAIM_IDLE_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.CredentialFpSlotReclaimIdleSeconds = n
		}
	}

	return cfg
}

// LoadFile merges config from a YAML file on top of the current config.
// File values are overridden by environment variables for security.
func (cfg *Config) LoadFile(path string) error {
	cfg.configPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var fileCfg Config
	if err := yaml.Unmarshal(data, &fileCfg); err != nil {
		return err
	}
	cfg.mergeFrom(&fileCfg)
	return nil
}

func (cfg *Config) mergeFrom(other *Config) {
	if other.DatabaseURL != "" && os.Getenv("LLM_GATEWAY_DATABASE_URL") == "" && os.Getenv("DATABASE_URL") == "" {
		cfg.DatabaseURL = other.DatabaseURL
	}
	if other.SecretKey != "" && os.Getenv("LLM_GATEWAY_SECRET_KEY") == "" && os.Getenv("SECRET_KEY") == "" {
		cfg.SecretKey = other.SecretKey
	}
	if other.CredentialEncryptionKey != "" && os.Getenv("LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY") == "" && os.Getenv("CREDENTIAL_ENCRYPTION_KEY") == "" {
		cfg.CredentialEncryptionKey = other.CredentialEncryptionKey
	}
	if other.RedisAddr != "" && os.Getenv("LLM_GATEWAY_REDIS_ADDR") == "" {
		cfg.RedisAddr = other.RedisAddr
	}
	if other.RedisPassword != "" && os.Getenv("LLM_GATEWAY_REDIS_PASSWORD") == "" {
		cfg.RedisPassword = other.RedisPassword
	}
	if other.RedisDB != 0 && os.Getenv("LLM_GATEWAY_REDIS_DB") == "" {
		cfg.RedisDB = other.RedisDB
	}
	if other.Listen != "" && os.Getenv("LLM_GATEWAY_LISTEN") == "" {
		cfg.Listen = other.Listen
	}
	if other.LogLevel != "" && os.Getenv("LLM_GATEWAY_LOG_LEVEL") == "" {
		cfg.LogLevel = other.LogLevel
	}
	if other.APIKey != "" && os.Getenv("LLM_GATEWAY_API_KEY") == "" {
		cfg.APIKey = other.APIKey
	}
	if other.CORSOrigins != "" && os.Getenv("LLM_GATEWAY_CORS_ORIGINS") == "" {
		cfg.CORSOrigins = other.CORSOrigins
	}
	if other.StaticDir != "" && os.Getenv("LLM_GATEWAY_STATIC_DIR") == "" {
		cfg.StaticDir = other.StaticDir
	}
	if other.PythonEndpoint != "" && os.Getenv("LLM_GATEWAY_PYTHON_ENDPOINT") == "" {
		cfg.PythonEndpoint = other.PythonEndpoint
	}
	if other.AdminAPIKey != "" && os.Getenv("LLM_GATEWAY_ADMIN_API_KEY") == "" {
		cfg.AdminAPIKey = other.AdminAPIKey
	}
	if other.UpstreamURL != "" && os.Getenv("LLM_GATEWAY_UPSTREAM") == "" {
		cfg.UpstreamURL = other.UpstreamURL
	}
	if other.IdentitySalt != "" && os.Getenv("LLM_GATEWAY_IDENTITY_SALT") == "" {
		cfg.IdentitySalt = other.IdentitySalt
	}
	if other.DefaultProvider != 0 && os.Getenv("LLM_GATEWAY_DEFAULT_PROVIDER") == "" {
		cfg.DefaultProvider = other.DefaultProvider
	}
	if other.DefaultCred != 0 && os.Getenv("LLM_GATEWAY_DEFAULT_CREDENTIAL") == "" {
		cfg.DefaultCred = other.DefaultCred
	}
	if other.UpstreamTimeout != 0 && os.Getenv("LLM_GATEWAY_UPSTREAM_TIMEOUT") == "" {
		cfg.UpstreamTimeout = other.UpstreamTimeout
	}
	if other.StreamTimeout != 0 && os.Getenv("LLM_GATEWAY_STREAM_TIMEOUT") == "" {
		cfg.StreamTimeout = other.StreamTimeout
	}
	if other.StreamChunkTimeout != 0 && os.Getenv("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT") == "" {
		cfg.StreamChunkTimeout = other.StreamChunkTimeout
	}
	if other.FirstByteTimeout != 0 && os.Getenv("LLM_GATEWAY_FIRST_BYTE_TIMEOUT") == "" {
		cfg.FirstByteTimeout = other.FirstByteTimeout
	}
	if other.KeepaliveInterval != 0 && os.Getenv("LLM_GATEWAY_KEEPALIVE_INTERVAL") == "" {
		cfg.KeepaliveInterval = other.KeepaliveInterval
	}
	if other.SessionTTLHours != 0 && os.Getenv("LLM_GATEWAY_SESSION_TTL_HOURS") == "" {
		cfg.SessionTTLHours = other.SessionTTLHours
	}
}

// Store provides atomic access to a Config pointer for hot-reload.
type Store struct {
	ptr atomic.Pointer[Config]
}

func NewStore(cfg *Config) *Store {
	s := &Store{}
	s.ptr.Store(cfg)
	return s
}

func (s *Store) Get() *Config {
	return s.ptr.Load()
}

func (s *Store) Swap(cfg *Config) {
	s.ptr.Store(cfg)
}

func (s *Store) ReloadFile(path string) error {
	old := s.Get()
	cfg := &Config{}
	*cfg = *old
	if err := cfg.LoadFile(path); err != nil {
		return err
	}
	s.Swap(cfg)
	slog.Info("config: hot-reloaded from file", "path", path)
	return nil
}
