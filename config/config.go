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
	CursorHMACSecret        string `yaml:"-" env:"CURSOR_HMAC_SECRET"`

	// Redis
	RedisAddr     string `yaml:"redis_addr" env:"LLM_GATEWAY_REDIS_ADDR"`
	RedisPassword string `yaml:"redis_password" env:"LLM_GATEWAY_REDIS_PASSWORD"`
	RedisDB       int    `yaml:"redis_db" env:"LLM_GATEWAY_REDIS_DB"`

	// Sessions
	SessionTTLHours   int      `yaml:"session_ttl_hours" env:"LLM_GATEWAY_SESSION_TTL_HOURS"`
	PendingTTLSeconds int      `yaml:"pending_ttl_seconds" env:"LLM_GATEWAY_PENDING_TTL_SECONDS"`
	SessionIDBodyKeys []string `yaml:"session_id_body_keys" env:"LLM_GATEWAY_SESSION_ID_BODY_KEYS"`

	// TrustedProxyCIDRs (2026-08-29, HIGH security): allowlist of immediate
	// peer CIDRs allowed to set X-Forwarded-For / X-Real-IP. Requests from
	// peers outside this list fall back to RemoteAddr; this blocks spoof
	// attempts where a public client impersonates another tenant or bypasses
	// IP-based rate limits / audit trails. Default: loopback only —
	// production deployments behind an LB MUST extend this explicitly.
	// Accepts a comma-separated list via LLM_GATEWAY_TRUSTED_PROXY_CIDRS or
	// `trusted_proxy_cidrs` in YAML.
	TrustedProxyCIDRs []string `yaml:"trusted_proxy_cidrs" env:"LLM_GATEWAY_TRUSTED_PROXY_CIDRS"`

	// Server
	Listen      string `yaml:"listen" env:"LLM_GATEWAY_LISTEN"`
	LogLevel    string `yaml:"log_level" env:"LLM_GATEWAY_LOG_LEVEL"`
	APIKey      string `yaml:"api_key" env:"LLM_GATEWAY_API_KEY"`
	CORSOrigins string `yaml:"cors_origins" env:"LLM_GATEWAY_CORS_ORIGINS"`
	StaticDir   string `yaml:"static_dir" env:"LLM_GATEWAY_STATIC_DIR"`

	// Log rotation. File-based rotation is opt-in: an empty
	// LLM_GATEWAY_LOG_FILE keeps slog on stderr (default).
	// When set, the gateway writes JSON lines to the file under
	// the rotation policy below; rotated backups are gzipped
	// (LLM_GATEWAY_LOG_COMPRESS) and pruned by both count
	// (LLM_GATEWAY_LOG_MAX_BACKUPS) and age (LLM_GATEWAY_LOG_MAX_AGE_DAYS).
	// Defaults match the operator spec: 100 MB × 10 = ~1 GB
	// ceiling, 7-day rolling retention, gzip on rotate.
	LogFile       string `yaml:"log_file" env:"LLM_GATEWAY_LOG_FILE"`
	LogMaxSizeMB  int    `yaml:"log_max_size_mb" env:"LLM_GATEWAY_LOG_MAX_SIZE_MB"`
	LogMaxBackups int    `yaml:"log_max_backups" env:"LLM_GATEWAY_LOG_MAX_BACKUPS"`
	LogMaxAgeDays int    `yaml:"log_max_age_days" env:"LLM_GATEWAY_LOG_MAX_AGE_DAYS"`
	LogCompress   bool   `yaml:"log_compress" env:"LLM_GATEWAY_LOG_COMPRESS"`

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

	// StreamRetryEnabled (2026-08-12): when true, internal/streamretry wraps
	// the v1 chat handler at the mux layer so upstream 5xx / 429 / connection
	// drops BEFORE the first byte triggers exponential-backoff retry on the
	// server side. Client stays connected via ": thinking:" SSE comments;
	// once the upstream recovers the response continues transparently.
	// Disabled by default; enable via LLM_GATEWAY_STREAM_RETRY_ENABLED=true.
	//
	// Independent of StreamRetryThreshold (which controls per-request
	// candidate failover inside the executor).
	StreamRetryEnabled bool `yaml:"stream_retry_enabled" env:"LLM_GATEWAY_STREAM_RETRY_ENABLED"`

	// StreamRetryMaxRetries (2026-08-12): retry budget after the initial
	// attempt. Default 3.
	StreamRetryMaxRetries int `yaml:"stream_retry_max_retries" env:"LLM_GATEWAY_STREAM_RETRY_MAX_RETRIES"`

	// StreamRetryBaseDelayMs (2026-08-12): base delay for exponential
	// backoff. Default 200ms. Real delay = base * 2^attempt with ±20% jitter.
	StreamRetryBaseDelayMs int `yaml:"stream_retry_base_delay_ms" env:"LLM_GATEWAY_STREAM_RETRY_BASE_DELAY_MS"`

	// StreamRetryMaxDelayMs (2026-08-12): cap on per-attempt backoff.
	// Default 5000ms.
	StreamRetryMaxDelayMs int `yaml:"stream_retry_max_delay_ms" env:"LLM_GATEWAY_STREAM_RETRY_MAX_DELAY_MS"`

	// StreamRetryKeepaliveSecs (2026-08-12): how often to emit SSE thinking
	// comments during the retry sleep so the client connection doesn't
	// idle-out. Default 10s; must be < client-side read idle timeout.
	StreamRetryKeepaliveSecs int `yaml:"stream_retry_keepalive_secs" env:"LLM_GATEWAY_STREAM_RETRY_KEEPALIVE_SECS"`

	// ── Request survival (docs/修订0811/18, 2026-08-14) ──────────────────
	// RequestSurvival* controls the request-survival coordinator: protocol-
	// safe keepalive + recoverable-error retry inside the client connection
	// (Phase 1) and durable task takeover across disconnect/restart (Phase 2).
	// ALL defaults OFF; survival is the exclusive outer retry owner and is
	// mutually exclusive with StreamRetryEnabled at startup.
	RequestSurvivalEnabled bool `yaml:"request_survival_enabled" env:"LLM_GATEWAY_REQUEST_SURVIVAL_ENABLED"`

	// RequestSurvivalDurableEnabled (Phase 2): durable task creation,
	// recovery workers, write-ahead semantic checkpoint. Requires
	// RequestSurvivalEnabled and the durable crypto/schema prerequisites.
	RequestSurvivalDurableEnabled bool `yaml:"request_survival_durable_enabled" env:"LLM_GATEWAY_REQUEST_SURVIVAL_DURABLE_ENABLED"`

	// RequestSurvivalInteractiveDeadlineSeconds: max in-connection wait for
	// ordinary streaming requests. Default 7200 (2h, 会话优化 v4 T3 — 与请求
	// 缓存 TTL 2h 对齐；终止优先级 组合穷尽 > 2h 时限 > 100 次预算).
	RequestSurvivalInteractiveDeadlineSeconds int `yaml:"request_survival_interactive_deadline_seconds" env:"LLM_GATEWAY_REQUEST_SURVIVAL_INTERACTIVE_DEADLINE_SECONDS"`

	// RequestSurvivalDurableDeadlineSeconds: max total wait for durable
	// tasks (client disconnect / gateway restart included). Default 86400.
	RequestSurvivalDurableDeadlineSeconds int `yaml:"request_survival_durable_deadline_seconds" env:"LLM_GATEWAY_REQUEST_SURVIVAL_DURABLE_DEADLINE_SECONDS"`

	// RequestSurvivalStatusIntervalSeconds: min interval between visible
	// gateway-status emissions. Default 60.
	RequestSurvivalStatusIntervalSeconds int `yaml:"request_survival_status_interval_seconds" env:"LLM_GATEWAY_REQUEST_SURVIVAL_STATUS_INTERVAL_SECONDS"`

	// RequestSurvivalRetryBaseSeconds / RetryMaxSeconds: exponential backoff
	// base and cap for the fallback delay when no authoritative recovery time
	// exists. Authoritative Retry-After / recover_at is NOT truncated by the
	// cap. Defaults 2s / 120s.
	RequestSurvivalRetryBaseSeconds int `yaml:"request_survival_retry_base_seconds" env:"LLM_GATEWAY_REQUEST_SURVIVAL_RETRY_BASE_SECONDS"`
	RequestSurvivalRetryMaxSeconds  int `yaml:"request_survival_retry_max_seconds" env:"LLM_GATEWAY_REQUEST_SURVIVAL_RETRY_MAX_SECONDS"`

	// RequestSurvivalWorkerCount / WorkerLeaseSeconds: Phase 2 recovery
	// worker pool size and lease duration. Defaults 4 / 60.
	RequestSurvivalWorkerCount     int `yaml:"request_survival_worker_count" env:"LLM_GATEWAY_REQUEST_SURVIVAL_WORKER_COUNT"`
	RequestSurvivalWorkerLeaseSecs int `yaml:"request_survival_worker_lease_seconds" env:"LLM_GATEWAY_REQUEST_SURVIVAL_WORKER_LEASE_SECONDS"`

	// RequestSurvivalMaxAttempts: retries allowed after a task's initial
	// execution. Default 100, for at most 101 total executions.
	RequestSurvivalMaxAttempts int `yaml:"request_survival_max_attempts" env:"LLM_GATEWAY_REQUEST_SURVIVAL_MAX_ATTEMPTS"`

	// RequestSurvivalMaxActiveTasksPerTenant: tenant-level active durable
	// task cap. Default 100.
	RequestSurvivalMaxActiveTasksPerTenant int `yaml:"request_survival_max_active_tasks_per_tenant" env:"LLM_GATEWAY_REQUEST_SURVIVAL_MAX_ACTIVE_TASKS_PER_TENANT"`

	// RequestSurvivalTenantAllowlist: when non-empty, survival only applies
	// to these tenants (Phase 1/2 canary gate). Empty = all tenants (once
	// enabled).
	RequestSurvivalTenantAllowlist []string `yaml:"request_survival_tenant_allowlist" env:"LLM_GATEWAY_REQUEST_SURVIVAL_TENANT_ALLOWLIST"`

	// EnablePreStreamKeepalive (2026-06-28): when true, the gateway commits
	// the SSE response (200 + text/event-stream) and emits periodic
	// ": keep-alive\n\n" comments during upstream credential retries so
	// streaming clients do not trip their first-byte / read timeouts.
	// Disabled automatically for non-openai-completions protocols. Off by
	// default so production rollouts opt in via config.
	EnablePreStreamKeepalive bool `yaml:"enable_pre_stream_keepalive" env:"LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE"`

	// EnableEmptyStreamGate (2026-07-15): when true, the stream reader
	// buffers the first few upstream chunks without writing to the client,
	// and only flushes once a chunk carries real content. If [DONE] arrives
	// with no content ever seen, the reader returns Resumable=true so the
	// executor transparently fails over to the next candidate. This catches
	// the NIM (Provider 18) failure mode: ~13% of streams open, send
	// empty choices, then [DONE] — currently invisible to the retry/circuit
	// path. ON by default; set to false to disable per-deploy if it causes
	// regressions with a particular upstream.
	EnableEmptyStreamGate bool `yaml:"enable_empty_stream_gate" env:"LLM_GATEWAY_ENABLE_EMPTY_STREAM_GATE"`

	// EmptyStreamEarlyEmptyChunks (2026-08-20): valid empty OpenAI delta
	// frames seen consecutively before any semantic content trigger an
	// immediate KindEmptyResponse failover. Zero disables early detection;
	// the end-of-stream empty gate remains available independently.
	EmptyStreamEarlyEmptyChunks int `yaml:"empty_stream_early_empty_chunks" env:"LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS"`

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

	// DeployEnv is the deployment environment: "production"/"prod" enables
	// auth fail-closed mode (rule 20 §8). Empty or any other value keeps
	// dev/local fail-open behavior with slog.Warn.
	// Source: LLM_GATEWAY_ENV (also accepts GO_ENV / APP_ENV for parity).
	DeployEnv string `yaml:"deploy_env" env:"LLM_GATEWAY_ENV"`

	// DefaultLanguage is the BCP-47 language tag used when a request carries
	// no usable X-Lang or Accept-Language header. It is the middle tier of the
	// i18n locale resolution chain (see i18n.Detect): request headers first,
	// then this default, then English as the ultimate fallback.
	// Source: LLM_GATEWAY_DEFAULT_LANGUAGE. Examples: "en", "zh-CN", "ja".
	// Empty defaults to "en".
	DefaultLanguage string `yaml:"default_language" env:"LLM_GATEWAY_DEFAULT_LANGUAGE"`

	// ModelAliasPrefix is the client-facing model name prefix that gets stripped
	// before internal routing. When clients send "kx-gpt-5.6-terra", the gateway
	// strips this prefix and routes to "gpt-5.6-terra". Default "kx-" can be
	// overridden via LLM_GATEWAY_MODEL_ALIAS_PREFIX or yaml "model_alias_prefix".
	// Empty string disables alias stripping.
	ModelAliasPrefix string `yaml:"model_alias_prefix" env:"LLM_GATEWAY_MODEL_ALIAS_PREFIX"`

	// WeChat Work (企业微信) notification settings for approval workflow
	WeChatCorpID     string `yaml:"wechat_corp_id" env:"LLM_GATEWAY_WECHAT_CORP_ID"`
	WeChatCorpSecret string `yaml:"wechat_corp_secret" env:"LLM_GATEWAY_WECHAT_CORP_SECRET"`
	WeChatAgentID    int    `yaml:"wechat_agent_id" env:"LLM_GATEWAY_WECHAT_AGENT_ID"`
	WeChatToken      string `yaml:"wechat_token" env:"LLM_GATEWAY_WECHAT_TOKEN"`       // Callback verification token
	WeChatAESKey     string `yaml:"wechat_aes_key" env:"LLM_GATEWAY_WECHAT_AES_KEY"`   // Callback encryption key (optional)
	WeChatBaseURL    string `yaml:"wechat_base_url" env:"LLM_GATEWAY_WECHAT_BASE_URL"` // Frontend base URL for approval links

	// License crypto keys (RSA 4096 key pair + AES-256 key for offline activation)
	LicensePrivateKey string `yaml:"license_private_key" env:"LLM_GATEWAY_LICENSE_PRIVATE_KEY"`
	LicensePublicKey  string `yaml:"license_public_key" env:"LLM_GATEWAY_LICENSE_PUBLIC_KEY"`
	LicenseAESKey     string `yaml:"license_aes_key" env:"LLM_GATEWAY_LICENSE_AES_KEY"`

	// Config file path (internal, not serialized)
	configPath                 string `yaml:"-"`
	modelAliasPrefixConfigured bool   `yaml:"-"`
}

// IsProduction reports whether the process is running in a production-like
// environment. Auth secret validation (rule 20 §8) treats this as the
// fail-closed switch: production + missing secret → startup panic.
func (cfg *Config) IsProduction() bool {
	env := strings.ToLower(strings.TrimSpace(cfg.DeployEnv))
	return env == "production" || env == "prod"
}

// NormalizeRequestSurvival applies safe defaults to the request-survival
// knobs after config load (zero values fall back to the documented defaults
// from docs/修订0811/18 §14). Durable implies survival: enabling durable
// alone also enables the in-connection coordinator, which is its Phase 1
// prerequisite.
func (cfg *Config) NormalizeRequestSurvival() {
	if cfg.RequestSurvivalInteractiveDeadlineSeconds <= 0 {
		cfg.RequestSurvivalInteractiveDeadlineSeconds = 7200
	}
	if cfg.RequestSurvivalDurableDeadlineSeconds <= 0 {
		cfg.RequestSurvivalDurableDeadlineSeconds = 86400
	}
	if cfg.RequestSurvivalStatusIntervalSeconds <= 0 {
		cfg.RequestSurvivalStatusIntervalSeconds = 60
	}
	if cfg.RequestSurvivalRetryBaseSeconds <= 0 {
		cfg.RequestSurvivalRetryBaseSeconds = 2
	}
	if cfg.RequestSurvivalRetryMaxSeconds <= 0 || cfg.RequestSurvivalRetryMaxSeconds > 120 {
		cfg.RequestSurvivalRetryMaxSeconds = 120
	}
	if cfg.RequestSurvivalWorkerCount <= 0 {
		cfg.RequestSurvivalWorkerCount = 4
	}
	if cfg.RequestSurvivalWorkerLeaseSecs <= 0 {
		cfg.RequestSurvivalWorkerLeaseSecs = 60
	}
	if cfg.RequestSurvivalMaxAttempts <= 0 || cfg.RequestSurvivalMaxAttempts > 100 {
		cfg.RequestSurvivalMaxAttempts = 100
	}
	if cfg.RequestSurvivalMaxActiveTasksPerTenant <= 0 {
		cfg.RequestSurvivalMaxActiveTasksPerTenant = 100
	}
	if cfg.RequestSurvivalDurableEnabled {
		cfg.RequestSurvivalEnabled = true
	}
}

// RequestSurvivalEnabledForTenant evaluates the request-survival canary
// gate: survival must be globally enabled AND, when a tenant allowlist is
// configured, the tenant must be listed. Empty tenantID never passes an
// allowlist gate (fail closed).
func (cfg *Config) RequestSurvivalEnabledForTenant(tenantID string) bool {
	if cfg == nil || !cfg.RequestSurvivalEnabled {
		return false
	}
	if len(cfg.RequestSurvivalTenantAllowlist) == 0 {
		return true
	}
	if tenantID == "" {
		return false
	}
	for _, t := range cfg.RequestSurvivalTenantAllowlist {
		if t == tenantID {
			return true
		}
	}
	return false
}

// ValidateAuthSecrets enforces production secrets for the data plane, ops plane,
// session JWTs, and signed pagination cursors. A missing secret must prevent
// production startup rather than silently enabling a forgeable fallback.
//
// JWT secret (SSOT: admin/auth_params.go EnvJWTSecret, EnvSecretKey):
//
//	LLM_GATEWAY_JWT_SECRET → SecretKey fallback
//
// (same precedence as admin/jwt.go.jwtSecret). A JWT secret is "present"
// if either is set.
//
// Returns the first missing secret name, or "" if all present.
func (cfg *Config) ValidateAuthSecrets() (missing string) {
	if cfg.APIKey == "" {
		return "LLM_GATEWAY_API_KEY"
	}
	if cfg.AdminAPIKey == "" {
		return "LLM_GATEWAY_ADMIN_API_KEY"
	}
	jwtSecret := firstNonEmpty(os.Getenv("LLM_GATEWAY_JWT_SECRET"), cfg.SecretKey)
	if jwtSecret == "" {
		return "LLM_GATEWAY_JWT_SECRET (or LLM_GATEWAY_SECRET_KEY fallback)"
	}
	if len([]byte(strings.TrimSpace(cfg.CursorHMACSecret))) < 32 {
		return "CURSOR_HMAC_SECRET (must be at least 32 bytes)"
	}
	return ""
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

func modelAliasPrefixFromEnv() string {
	if value, ok := os.LookupEnv("LLM_GATEWAY_MODEL_ALIAS_PREFIX"); ok {
		return value
	}
	return "kx-"
}

func parseCommaList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// defaultTrustedProxyCIDRs returns the caller-supplied list unchanged when
// non-empty, otherwise falls back to the loopback-only default. The default
// is the safest baseline (public peers can never spoof XFF); deployments
// behind a load balancer MUST extend it explicitly via
// LLM_GATEWAY_TRUSTED_PROXY_CIDRS or `trusted_proxy_cidrs` in YAML.
func defaultTrustedProxyCIDRs(supplied []string) []string {
	if len(supplied) > 0 {
		return supplied
	}
	return []string{"127.0.0.1/32", "::1/128"}
}

// Load loads configuration from environment variables (and optionally a file).
func Load() *Config {
	cfg := &Config{
		DatabaseURL:             firstNonEmpty(os.Getenv("LLM_GATEWAY_DATABASE_URL"), os.Getenv("DATABASE_URL")),
		SecretKey:               firstNonEmpty(os.Getenv("LLM_GATEWAY_SECRET_KEY"), os.Getenv("SECRET_KEY")),
		CredentialEncryptionKey: firstNonEmpty(os.Getenv("LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY"), os.Getenv("CREDENTIAL_ENCRYPTION_KEY")),
		CursorHMACSecret:        os.Getenv("CURSOR_HMAC_SECRET"),
		RedisAddr:               os.Getenv("LLM_GATEWAY_REDIS_ADDR"),
		RedisPassword:           os.Getenv("LLM_GATEWAY_REDIS_PASSWORD"),
		Listen:                  envOrDefault("LLM_GATEWAY_LISTEN", ":8781"),
		LogLevel:                envOrDefault("LLM_GATEWAY_LOG_LEVEL", "info"),
		APIKey:                  os.Getenv("LLM_GATEWAY_API_KEY"),
		CORSOrigins:             os.Getenv("LLM_GATEWAY_CORS_ORIGINS"),
		StaticDir:               envOrDefault("LLM_GATEWAY_STATIC_DIR", "web/dist"),
		PythonEndpoint:          os.Getenv("LLM_GATEWAY_PYTHON_ENDPOINT"),
		AdminAPIKey:             os.Getenv("LLM_GATEWAY_ADMIN_API_KEY"),
		UpstreamURL:             envOrDefault("LLM_GATEWAY_UPSTREAM", "http://127.0.0.1:8780"),
		IdentitySalt:            os.Getenv("LLM_GATEWAY_IDENTITY_SALT"),
		BGMode:                  envOrDefault("LLM_GATEWAY_BG_MODE", "full"),
		UpstreamTimeout:         150,
		StreamTimeout:           900,
		StreamChunkTimeout:      600,
		// 2026-08-04: 120→180s. Reasoning models (Claude thinking, o-series)
		// with large tool-call contexts regularly exceed 120s to first byte.
		// Combined with all-protocol pre-stream keepalive (now on by default),
		// the client connection stays alive during the wait, so a 180s budget
		// no longer risks client-side idle disconnects.
		FirstByteTimeout:  180,
		KeepaliveInterval: 15,
		// 2026-07-23: Session TTL 从 168h (7d) 改为 72h (3d)。
		// 7 天累计 23.6 万个 session hash keys 占用 91% 的 Redis 内存。
		// 3 天足以覆盖 OpenCode/Cursor 用户的连续编辑场景。
		// 如果需要更长可设 LLM_GATEWAY_SESSION_TTL_HOURS 环境变量。
		SessionTTLHours:      72,
		PendingTTLSeconds:    300,
		SessionIDBodyKeys:    parseCommaList(os.Getenv("LLM_GATEWAY_SESSION_ID_BODY_KEYS")),
		// TrustedProxyCIDRs (HIGH security, 2026-08-29): loopback-only default.
		// Operators behind an LB / reverse proxy MUST extend this list,
		// otherwise X-Forwarded-For / X-Real-IP will be ignored and every
		// request will appear to come from the LB's IP.
		TrustedProxyCIDRs:    defaultTrustedProxyCIDRs(parseCommaList(os.Getenv("LLM_GATEWAY_TRUSTED_PROXY_CIDRS"))),
		StreamRetryThreshold: 50, // Default: allow stream failover if < 50 chunks sent
		// 2026-08-12: streamretry 默认关闭。开启后 internal/streamretry
		// 会在 mux 入口包一层重试 executor，掩盖 pre-stream 5xx/429/连接中断。
		// 通过 LLM_GATEWAY_STREAM_RETRY_ENABLED=true 启用；建议先在
		// staging/245 跑一周观察 metrics，再上 154 生产。
		StreamRetryEnabled:       false,
		StreamRetryMaxRetries:    3,
		StreamRetryBaseDelayMs:   200,
		StreamRetryMaxDelayMs:    5000,
		StreamRetryKeepaliveSecs: 10,
		// Request survival (docs/修订0811/18): all OFF by default. Enabling
		// makes the survival coordinator the exclusive outer retry owner;
		// mutually exclusive with StreamRetryEnabled (startup check).
		RequestSurvivalEnabled:                    false,
		RequestSurvivalDurableEnabled:             false,
		RequestSurvivalInteractiveDeadlineSeconds: 7200,
		RequestSurvivalDurableDeadlineSeconds:     86400,
		RequestSurvivalStatusIntervalSeconds:      60,
		RequestSurvivalRetryBaseSeconds:           2,
		RequestSurvivalRetryMaxSeconds:            120,
		RequestSurvivalWorkerCount:                4,
		RequestSurvivalWorkerLeaseSecs:            60,
		RequestSurvivalMaxAttempts:                100,
		RequestSurvivalMaxActiveTasksPerTenant:    100,
		RequestSurvivalTenantAllowlist:            nil,
		// 2026-08-04: ON by default for ALL streaming protocols. Previously
		// opt-in and limited to openai-completions, which left Anthropic Messages
		// and Responses clients with no heartbeat before the first upstream
		// chunk — reasoning models' long thinking window then tripped client/
		// proxy idle timeouts, causing the "agent task interrupted through the
		// gateway" symptom. Disable via LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE=false.
		EnablePreStreamKeepalive:           true,
		PoolGracePeriod:                    180, // Default: 3 minutes grace period before marking pool as dead
		DefaultCredentialConcurrency:       20,  // 2026-06-24: 5 → 20. 每个凭据 20 个 fp_slot，更宽松避免争抢。
		EnableCredentialFpSlots:            true,
		CredentialFpSlotActiveGateSeconds:  300,   // 5 min — 5 min 内不允许抢的"
		CredentialFpSlotReclaimIdleSeconds: 1800,  // 30 min — 自动清除无活动的时长
		EnableDisguise:                     false, // off by default; opt-in
		EnableEmptyStreamGate:              true,  // 2026-07-15: ON by default; kills the 13% NIM empty-stream failure mode.
		EmptyStreamEarlyEmptyChunks:        3,     // 2026-08-20: fail over after three valid empty deltas.
		DeployEnv:                          firstNonEmpty(os.Getenv("LLM_GATEWAY_ENV"), os.Getenv("GO_ENV"), os.Getenv("APP_ENV")),
		DefaultLanguage:                    envOrDefault("LLM_GATEWAY_DEFAULT_LANGUAGE", "en"),
		ModelAliasPrefix:                   modelAliasPrefixFromEnv(),
		// WeChat Work notification settings
		WeChatCorpID:     os.Getenv("LLM_GATEWAY_WECHAT_CORP_ID"),
		WeChatCorpSecret: os.Getenv("LLM_GATEWAY_WECHAT_CORP_SECRET"),
		WeChatToken:      os.Getenv("LLM_GATEWAY_WECHAT_TOKEN"),
		WeChatAESKey:     os.Getenv("LLM_GATEWAY_WECHAT_AES_KEY"),
		WeChatBaseURL:    envOrDefault("LLM_GATEWAY_WECHAT_BASE_URL", "https://llm-gateway.example.com"),
		// Log rotation: opt-in via LLM_GATEWAY_LOG_FILE. Defaults
		// match the operator spec when file logging IS enabled:
		// 100 MB × 10 files ≈ 1 GB ceiling, 7-day rolling
		// retention, gzip on rotate. See internal/logging.
		LogFile:       os.Getenv("LLM_GATEWAY_LOG_FILE"),
		LogMaxSizeMB:  100,
		LogMaxBackups: 10,
		LogMaxAgeDays: 7,
		LogCompress:   true,
	}

	if dbStr := os.Getenv("LLM_GATEWAY_REDIS_DB"); dbStr != "" {
		if v, err := strconv.Atoi(dbStr); err == nil {
			cfg.RedisDB = v
		}
	} else {
		// 2026-08-04: 默认 db=2，避免与共享 Redis 上其他系统（pms session
		// 等 12 万 keys）混在 db=0。llmgw 的所有 Redis 客户端都通过 cfg.RedisDB
		// 拿这个值。覆盖方式：env LLM_GATEWAY_REDIS_DB=<n>。
		cfg.RedisDB = 2
	}
	applyPositiveIntEnv("LLM_GATEWAY_SESSION_TTL_HOURS", &cfg.SessionTTLHours)
	applyPositiveIntEnv("LLM_GATEWAY_PENDING_TTL_SECONDS", &cfg.PendingTTLSeconds)
	applyPositiveIntEnv("LLM_GATEWAY_UPSTREAM_TIMEOUT", &cfg.UpstreamTimeout)
	applyPositiveIntEnv("LLM_GATEWAY_STREAM_TIMEOUT", &cfg.StreamTimeout)
	applyPositiveIntEnv("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", &cfg.StreamChunkTimeout)
	applyPositiveIntEnv("LLM_GATEWAY_FIRST_BYTE_TIMEOUT", &cfg.FirstByteTimeout)
	applyPositiveIntEnv("LLM_GATEWAY_KEEPALIVE_INTERVAL", &cfg.KeepaliveInterval)
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

	// License crypto (RSA / AES) — env tag is documentation only; read explicitly.
	if v := os.Getenv("LLM_GATEWAY_LICENSE_PRIVATE_KEY"); v != "" {
		cfg.LicensePrivateKey = v
	}
	if v := os.Getenv("LLM_GATEWAY_LICENSE_PUBLIC_KEY"); v != "" {
		cfg.LicensePublicKey = v
	}
	if v := os.Getenv("LLM_GATEWAY_LICENSE_AES_KEY"); v != "" {
		cfg.LicenseAESKey = v
	}

	// WeChat Agent ID parsing
	if agentIDStr := os.Getenv("LLM_GATEWAY_WECHAT_AGENT_ID"); agentIDStr != "" {
		if v, err := strconv.Atoi(agentIDStr); err == nil {
			cfg.WeChatAgentID = v
		}
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
	if v := os.Getenv("LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE"); v != "" {
		cfg.EnablePreStreamKeepalive = v == "true" || v == "1"
	}
	applyNonNegativeIntEnv("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS", &cfg.EmptyStreamEarlyEmptyChunks)
	// streamretry flag: the struct tag declares the env var but the parse was
	// historically missing, so env-only deployments silently ran with the
	// wrapper off. Parsed here so the survival mutual-exclusion check in
	// cmd/gateway sees the same value the wrapper wiring does.
	if v := os.Getenv("LLM_GATEWAY_STREAM_RETRY_ENABLED"); v != "" {
		cfg.StreamRetryEnabled = v == "true" || v == "1"
	}
	// Request survival env overrides.
	if v := os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_ENABLED"); v != "" {
		cfg.RequestSurvivalEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_DURABLE_ENABLED"); v != "" {
		cfg.RequestSurvivalDurableEnabled = v == "true" || v == "1"
	}
	applyPositiveIntEnv("LLM_GATEWAY_REQUEST_SURVIVAL_INTERACTIVE_DEADLINE_SECONDS", &cfg.RequestSurvivalInteractiveDeadlineSeconds)
	applyPositiveIntEnv("LLM_GATEWAY_REQUEST_SURVIVAL_DURABLE_DEADLINE_SECONDS", &cfg.RequestSurvivalDurableDeadlineSeconds)
	applyPositiveIntEnv("LLM_GATEWAY_REQUEST_SURVIVAL_STATUS_INTERVAL_SECONDS", &cfg.RequestSurvivalStatusIntervalSeconds)
	applyPositiveIntEnv("LLM_GATEWAY_REQUEST_SURVIVAL_RETRY_BASE_SECONDS", &cfg.RequestSurvivalRetryBaseSeconds)
	applyPositiveIntEnv("LLM_GATEWAY_REQUEST_SURVIVAL_RETRY_MAX_SECONDS", &cfg.RequestSurvivalRetryMaxSeconds)
	applyPositiveIntEnv("LLM_GATEWAY_REQUEST_SURVIVAL_WORKER_COUNT", &cfg.RequestSurvivalWorkerCount)
	applyPositiveIntEnv("LLM_GATEWAY_REQUEST_SURVIVAL_WORKER_LEASE_SECONDS", &cfg.RequestSurvivalWorkerLeaseSecs)
	applyPositiveIntEnv("LLM_GATEWAY_REQUEST_SURVIVAL_MAX_ATTEMPTS", &cfg.RequestSurvivalMaxAttempts)
	applyPositiveIntEnv("LLM_GATEWAY_REQUEST_SURVIVAL_MAX_ACTIVE_TASKS_PER_TENANT", &cfg.RequestSurvivalMaxActiveTasksPerTenant)
	if v := os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_TENANT_ALLOWLIST"); v != "" {
		cfg.RequestSurvivalTenantAllowlist = parseCommaList(v)
	}

	// Log rotation overrides (only honoured when LLM_GATEWAY_LOG_FILE
	// is non-empty; the package-level defaults in internal/logging
	// already match the operator spec).
	if v := os.Getenv("LLM_GATEWAY_LOG_MAX_SIZE_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.LogMaxSizeMB = n
		}
	}
	if v := os.Getenv("LLM_GATEWAY_LOG_MAX_BACKUPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.LogMaxBackups = n
		}
	}
	if v := os.Getenv("LLM_GATEWAY_LOG_MAX_AGE_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.LogMaxAgeDays = n
		}
	}
	if v := os.Getenv("LLM_GATEWAY_LOG_COMPRESS"); v != "" {
		cfg.LogCompress = v == "true" || v == "1"
	}

	return cfg
}

func applyPositiveIntEnv(key string, target *int) {
	if target == nil {
		return
	}
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			*target = parsed
		}
	}
}

func applyNonNegativeIntEnv(key string, target *int) {
	if target == nil {
		return
	}
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 0 {
			*target = parsed
		}
	}
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
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	_, fileCfg.modelAliasPrefixConfigured = raw["model_alias_prefix"]
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
	if len(other.SessionIDBodyKeys) > 0 && os.Getenv("LLM_GATEWAY_SESSION_ID_BODY_KEYS") == "" {
		cfg.SessionIDBodyKeys = other.SessionIDBodyKeys
	}
	if len(other.TrustedProxyCIDRs) > 0 && os.Getenv("LLM_GATEWAY_TRUSTED_PROXY_CIDRS") == "" {
		cfg.TrustedProxyCIDRs = other.TrustedProxyCIDRs
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
	if other.PendingTTLSeconds != 0 && os.Getenv("LLM_GATEWAY_PENDING_TTL_SECONDS") == "" {
		cfg.PendingTTLSeconds = other.PendingTTLSeconds
	}
	if other.EnablePreStreamKeepalive && os.Getenv("LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE") == "" {
		cfg.EnablePreStreamKeepalive = true
	}
	if other.EmptyStreamEarlyEmptyChunks != 0 && os.Getenv("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS") == "" {
		cfg.EmptyStreamEarlyEmptyChunks = other.EmptyStreamEarlyEmptyChunks
	}
	// Request survival file overrides (env wins, same pattern as above).

	if other.RequestSurvivalEnabled && os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_ENABLED") == "" {
		cfg.RequestSurvivalEnabled = true
	}
	if other.RequestSurvivalDurableEnabled && os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_DURABLE_ENABLED") == "" {
		cfg.RequestSurvivalDurableEnabled = true
	}
	if other.RequestSurvivalInteractiveDeadlineSeconds != 0 && os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_INTERACTIVE_DEADLINE_SECONDS") == "" {
		cfg.RequestSurvivalInteractiveDeadlineSeconds = other.RequestSurvivalInteractiveDeadlineSeconds
	}
	if other.RequestSurvivalDurableDeadlineSeconds != 0 && os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_DURABLE_DEADLINE_SECONDS") == "" {
		cfg.RequestSurvivalDurableDeadlineSeconds = other.RequestSurvivalDurableDeadlineSeconds
	}
	if other.RequestSurvivalStatusIntervalSeconds != 0 && os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_STATUS_INTERVAL_SECONDS") == "" {
		cfg.RequestSurvivalStatusIntervalSeconds = other.RequestSurvivalStatusIntervalSeconds
	}
	if other.RequestSurvivalRetryBaseSeconds != 0 && os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_RETRY_BASE_SECONDS") == "" {
		cfg.RequestSurvivalRetryBaseSeconds = other.RequestSurvivalRetryBaseSeconds
	}
	if other.RequestSurvivalRetryMaxSeconds != 0 && os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_RETRY_MAX_SECONDS") == "" {
		cfg.RequestSurvivalRetryMaxSeconds = other.RequestSurvivalRetryMaxSeconds
	}
	if other.RequestSurvivalWorkerCount != 0 && os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_WORKER_COUNT") == "" {
		cfg.RequestSurvivalWorkerCount = other.RequestSurvivalWorkerCount
	}
	if other.RequestSurvivalWorkerLeaseSecs != 0 && os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_WORKER_LEASE_SECONDS") == "" {
		cfg.RequestSurvivalWorkerLeaseSecs = other.RequestSurvivalWorkerLeaseSecs
	}
	if other.RequestSurvivalMaxAttempts != 0 && os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_MAX_ATTEMPTS") == "" {
		cfg.RequestSurvivalMaxAttempts = other.RequestSurvivalMaxAttempts
	}
	if other.RequestSurvivalMaxActiveTasksPerTenant != 0 && os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_MAX_ACTIVE_TASKS_PER_TENANT") == "" {
		cfg.RequestSurvivalMaxActiveTasksPerTenant = other.RequestSurvivalMaxActiveTasksPerTenant
	}
	if len(other.RequestSurvivalTenantAllowlist) > 0 && os.Getenv("LLM_GATEWAY_REQUEST_SURVIVAL_TENANT_ALLOWLIST") == "" {
		cfg.RequestSurvivalTenantAllowlist = other.RequestSurvivalTenantAllowlist
	}
	if other.LogFile != "" && os.Getenv("LLM_GATEWAY_LOG_FILE") == "" {
		cfg.LogFile = other.LogFile
	}
	if other.LogMaxSizeMB != 0 && os.Getenv("LLM_GATEWAY_LOG_MAX_SIZE_MB") == "" {
		cfg.LogMaxSizeMB = other.LogMaxSizeMB
	}
	if other.LogMaxBackups != 0 && os.Getenv("LLM_GATEWAY_LOG_MAX_BACKUPS") == "" {
		cfg.LogMaxBackups = other.LogMaxBackups
	}
	if other.LogMaxAgeDays != 0 && os.Getenv("LLM_GATEWAY_LOG_MAX_AGE_DAYS") == "" {
		cfg.LogMaxAgeDays = other.LogMaxAgeDays
	}
	if other.LogCompress && !cfg.LogCompress && os.Getenv("LLM_GATEWAY_LOG_COMPRESS") == "" {
		// YAML `log_compress: true` wins when the env var is unset;
		// explicit `false` in YAML cannot be overridden because
		// Go's zero-value bool is ambiguous (this matches how
		// EnablePreStreamKeepalive is handled above).
		cfg.LogCompress = true
	}
	if other.DefaultLanguage != "" && os.Getenv("LLM_GATEWAY_DEFAULT_LANGUAGE") == "" {
		cfg.DefaultLanguage = other.DefaultLanguage
	}
	// ModelAliasPrefix: an explicit YAML value, including empty string, wins
	// when the environment variable is not configured.
	if other.modelAliasPrefixConfigured {
		if _, envConfigured := os.LookupEnv("LLM_GATEWAY_MODEL_ALIAS_PREFIX"); !envConfigured {
			cfg.ModelAliasPrefix = other.ModelAliasPrefix
		}
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
