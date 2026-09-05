package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStorageValidateFull 覆盖 full 模式的合法与必填项校验。
func TestStorageValidateFull(t *testing.T) {
	valid := &StorageConfig{
		Mode: "full",
		Full: &FullStorageConfig{
			PostgresURL: "postgres://user:pass@127.0.0.1:5432/llm_gateway",
			RedisURL:    "redis://127.0.0.1:6379/2",
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() full with both URLs error = %v, want nil", err)
	}

	missingPostgres := &StorageConfig{
		Mode: "full",
		Full: &FullStorageConfig{RedisURL: "redis://127.0.0.1:6379/2"},
	}
	if err := missingPostgres.Validate(); err == nil || !strings.Contains(err.Error(), "postgres_url") {
		t.Fatalf("Validate() full missing postgres_url error = %v, want postgres_url error", err)
	}

	missingRedis := &StorageConfig{
		Mode: "full",
		Full: &FullStorageConfig{PostgresURL: "postgres://user:pass@127.0.0.1:5432/llm_gateway"},
	}
	if err := missingRedis.Validate(); err == nil || !strings.Contains(err.Error(), "redis_url") {
		t.Fatalf("Validate() full missing redis_url error = %v, want redis_url error", err)
	}

	missingSection := &StorageConfig{Mode: "full"}
	if err := missingSection.Validate(); err == nil || !strings.Contains(err.Error(), "full_storage") {
		t.Fatalf("Validate() full without full_storage error = %v, want full_storage error", err)
	}
}

// TestStorageValidateLite 覆盖 lite 模式的合法与必填项校验。
func TestStorageValidateLite(t *testing.T) {
	valid := &StorageConfig{
		Mode: "lite",
		Lite: &LiteStorageConfig{SQLitePath: "./data/llm-gateway.db"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() lite with sqlite_path error = %v, want nil", err)
	}

	missingPath := &StorageConfig{
		Mode: "lite",
		Lite: &LiteStorageConfig{},
	}
	if err := missingPath.Validate(); err == nil || !strings.Contains(err.Error(), "sqlite_path") {
		t.Fatalf("Validate() lite missing sqlite_path error = %v, want sqlite_path error", err)
	}

	missingSection := &StorageConfig{Mode: "lite"}
	if err := missingSection.Validate(); err == nil || !strings.Contains(err.Error(), "lite_storage") {
		t.Fatalf("Validate() lite without lite_storage error = %v, want lite_storage error", err)
	}
}

// TestStorageValidateMode 覆盖非法与空 mode 的明确报错。
func TestStorageValidateMode(t *testing.T) {
	invalid := &StorageConfig{Mode: "mongo"}
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), `invalid storage_mode "mongo"`) {
		t.Fatalf("Validate() invalid mode error = %v, want invalid storage_mode error", err)
	}

	empty := &StorageConfig{}
	if err := empty.Validate(); err == nil || !strings.Contains(err.Error(), "storage_mode is empty") {
		t.Fatalf("Validate() empty mode error = %v, want storage_mode is empty error", err)
	}
}

// TestNormalizeMode 验证模式归一化辅助函数。
func TestNormalizeMode(t *testing.T) {
	if got := (&StorageConfig{Mode: "full"}).NormalizeMode(); got != StorageModeFull {
		t.Fatalf("NormalizeMode() = %q, want %q", got, StorageModeFull)
	}
	if got := (&StorageConfig{Mode: "lite"}).NormalizeMode(); got != StorageModeLite {
		t.Fatalf("NormalizeMode() = %q, want %q", got, StorageModeLite)
	}
	if got := (&StorageConfig{}).NormalizeMode(); got != "" {
		t.Fatalf("NormalizeMode() empty = %q, want empty", got)
	}
}

// TestApplyLiteDefaultsFillsAllDefaults 验证全空配置补齐仓库约定默认值。
func TestApplyLiteDefaultsFillsAllDefaults(t *testing.T) {
	cfg := &StorageConfig{Mode: "lite"}
	cfg.ApplyLiteDefaults()

	l := cfg.Lite
	if l == nil {
		t.Fatal("ApplyLiteDefaults() left Lite nil, want populated section")
	}
	if l.SQLitePath != "./data/llm-gateway.db" {
		t.Fatalf("SQLitePath = %q, want ./data/llm-gateway.db", l.SQLitePath)
	}
	if l.BodiesDir != "./data/session_bodies" {
		t.Fatalf("BodiesDir = %q, want ./data/session_bodies", l.BodiesDir)
	}
	if l.CacheDir != "./data/cache" {
		t.Fatalf("CacheDir = %q, want ./data/cache", l.CacheDir)
	}
	if l.LogsDir != "./data/request_logs" {
		t.Fatalf("LogsDir = %q, want ./data/request_logs", l.LogsDir)
	}
	if l.AsyncWriters != 4 {
		t.Fatalf("AsyncWriters = %d, want 4", l.AsyncWriters)
	}
	if l.CacheTTLHours != 24 {
		t.Fatalf("CacheTTLHours = %d, want 24", l.CacheTTLHours)
	}
	if l.CacheMaxSizeGB != 10 {
		t.Fatalf("CacheMaxSizeGB = %d, want 10", l.CacheMaxSizeGB)
	}
	if l.SQLitePragmas.JournalMode != "WAL" {
		t.Fatalf("JournalMode = %q, want WAL", l.SQLitePragmas.JournalMode)
	}
	if l.SQLitePragmas.CacheSizeKB != 64000 {
		t.Fatalf("CacheSizeKB = %d, want 64000", l.SQLitePragmas.CacheSizeKB)
	}
	if l.SQLitePragmas.Synchronous != "NORMAL" {
		t.Fatalf("Synchronous = %q, want NORMAL", l.SQLitePragmas.Synchronous)
	}
	if l.SQLitePragmas.BusyTimeoutMS != 5000 {
		t.Fatalf("BusyTimeoutMS = %d, want 5000", l.SQLitePragmas.BusyTimeoutMS)
	}
	if l.Retention.SessionBodiesDays != 30 {
		t.Fatalf("SessionBodiesDays = %d, want 30", l.Retention.SessionBodiesDays)
	}
	if l.Retention.RequestLogsDays != 7 {
		t.Fatalf("RequestLogsDays = %d, want 7", l.Retention.RequestLogsDays)
	}
	if l.Retention.CacheHours != 24 {
		t.Fatalf("CacheHours = %d, want 24", l.Retention.CacheHours)
	}

	// 补齐默认值后必须通过校验。
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() after ApplyLiteDefaults error = %v, want nil", err)
	}

	// 显式配置的值不允许被默认值覆盖。
	custom := &StorageConfig{
		Mode: "lite",
		Lite: &LiteStorageConfig{SQLitePath: "/custom/gw.db", AsyncWriters: 8},
	}
	custom.ApplyLiteDefaults()
	if custom.Lite.SQLitePath != "/custom/gw.db" {
		t.Fatalf("SQLitePath = %q, want explicit /custom/gw.db preserved", custom.Lite.SQLitePath)
	}
	if custom.Lite.AsyncWriters != 8 {
		t.Fatalf("AsyncWriters = %d, want explicit 8 preserved", custom.Lite.AsyncWriters)
	}
}

// TestApplyDefaultsFullMaxConnections 验证 full 模式 max_connections 默认 100。
func TestApplyDefaultsFullMaxConnections(t *testing.T) {
	cfg := &StorageConfig{
		Mode: "full",
		Full: &FullStorageConfig{
			PostgresURL: "postgres://user:pass@127.0.0.1:5432/llm_gateway",
			RedisURL:    "redis://127.0.0.1:6379/2",
		},
	}
	cfg.ApplyDefaults()
	if cfg.Full.MaxConnections != 100 {
		t.Fatalf("MaxConnections = %d, want default 100", cfg.Full.MaxConnections)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() after ApplyDefaults error = %v, want nil", err)
	}

	explicit := &StorageConfig{
		Mode: "full",
		Full: &FullStorageConfig{MaxConnections: 42},
	}
	explicit.ApplyDefaults()
	if explicit.Full.MaxConnections != 42 {
		t.Fatalf("MaxConnections = %d, want explicit 42 preserved", explicit.Full.MaxConnections)
	}
}

// TestLoadStorageConfigFromYAML 验证临时 YAML 文件的完整解析。
func TestLoadStorageConfigFromYAML(t *testing.T) {
	for _, key := range []string{
		"LLM_GATEWAY_STORAGE_MODE", "LLM_GATEWAY_POSTGRES_URL", "LLM_GATEWAY_REDIS_URL",
		"LLM_GATEWAY_STORAGE_MAX_CONNECTIONS", "LLM_GATEWAY_SQLITE_PATH",
		"LLM_GATEWAY_BODIES_DIR", "LLM_GATEWAY_CACHE_DIR", "LLM_GATEWAY_LOGS_DIR",
	} {
		t.Setenv(key, "")
	}

	path := filepath.Join(t.TempDir(), "storage.yaml")
	contents := `storage_mode: lite
lite_storage:
  sqlite_path: /tmp/gateway/gw.db
  bodies_dir: /tmp/gateway/bodies
  cache_dir: /tmp/gateway/cache
  logs_dir: /tmp/gateway/logs
  sqlite_pragmas:
    journal_mode: WAL
    cache_size_kb: 32000
    synchronous: FULL
    busy_timeout_ms: 3000
  cache_ttl_hours: 12
  cache_max_size_gb: 5
  async_writers: 2
  retention:
    session_bodies_days: 14
    request_logs_days: 3
    cache_hours: 48
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadStorageConfigFromYAML(path)
	if err != nil {
		t.Fatalf("LoadStorageConfigFromYAML() error = %v", err)
	}
	if cfg.Mode != "lite" {
		t.Fatalf("Mode = %q, want lite", cfg.Mode)
	}
	l := cfg.Lite
	if l == nil {
		t.Fatal("Lite = nil, want parsed section")
	}
	if l.SQLitePath != "/tmp/gateway/gw.db" || l.BodiesDir != "/tmp/gateway/bodies" ||
		l.CacheDir != "/tmp/gateway/cache" || l.LogsDir != "/tmp/gateway/logs" {
		t.Fatalf("lite dirs = %q/%q/%q/%q, want parsed values", l.SQLitePath, l.BodiesDir, l.CacheDir, l.LogsDir)
	}
	if l.SQLitePragmas.JournalMode != "WAL" || l.SQLitePragmas.CacheSizeKB != 32000 ||
		l.SQLitePragmas.Synchronous != "FULL" || l.SQLitePragmas.BusyTimeoutMS != 3000 {
		t.Fatalf("pragmas = %#v, want parsed values", l.SQLitePragmas)
	}
	if l.CacheTTLHours != 12 || l.CacheMaxSizeGB != 5 || l.AsyncWriters != 2 {
		t.Fatalf("cache/writer knobs = %d/%d/%d, want 12/5/2", l.CacheTTLHours, l.CacheMaxSizeGB, l.AsyncWriters)
	}
	if l.Retention.SessionBodiesDays != 14 || l.Retention.RequestLogsDays != 3 || l.Retention.CacheHours != 48 {
		t.Fatalf("retention = %#v, want 14/3/48", l.Retention)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() parsed lite config error = %v", err)
	}

	// full 段解析。
	fullPath := filepath.Join(t.TempDir(), "storage-full.yaml")
	fullContents := `storage_mode: full
full_storage:
  postgres_url: postgres://user:pass@127.0.0.1:5432/llm_gateway
  redis_url: redis://127.0.0.1:6379/2
  max_connections: 50
`
	if err := os.WriteFile(fullPath, []byte(fullContents), 0o600); err != nil {
		t.Fatal(err)
	}
	fullCfg, err := LoadStorageConfigFromYAML(fullPath)
	if err != nil {
		t.Fatalf("LoadStorageConfigFromYAML(full) error = %v", err)
	}
	if fullCfg.Full == nil || fullCfg.Full.PostgresURL != "postgres://user:pass@127.0.0.1:5432/llm_gateway" ||
		fullCfg.Full.RedisURL != "redis://127.0.0.1:6379/2" || fullCfg.Full.MaxConnections != 50 {
		t.Fatalf("full section = %#v, want parsed values", fullCfg.Full)
	}
	if err := fullCfg.Validate(); err != nil {
		t.Fatalf("Validate() parsed full config error = %v", err)
	}
}

// TestLoadStorageConfigFromYAMLEnvOverrides 验证环境变量填充 YAML 未配置字段。
func TestLoadStorageConfigFromYAMLEnvOverrides(t *testing.T) {
	for _, key := range []string{
		"LLM_GATEWAY_STORAGE_MODE", "LLM_GATEWAY_POSTGRES_URL", "LLM_GATEWAY_REDIS_URL",
		"LLM_GATEWAY_STORAGE_MAX_CONNECTIONS", "LLM_GATEWAY_SQLITE_PATH",
		"LLM_GATEWAY_BODIES_DIR", "LLM_GATEWAY_CACHE_DIR", "LLM_GATEWAY_LOGS_DIR",
	} {
		t.Setenv(key, "")
	}

	// env-only lite 部署：YAML 只给出 mode，其余字段全部来自环境变量。
	path := filepath.Join(t.TempDir(), "storage.yaml")
	if err := os.WriteFile(path, []byte("storage_mode: lite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_GATEWAY_SQLITE_PATH", "/env/gw.db")
	t.Setenv("LLM_GATEWAY_BODIES_DIR", "/env/bodies")
	t.Setenv("LLM_GATEWAY_CACHE_DIR", "/env/cache")
	t.Setenv("LLM_GATEWAY_LOGS_DIR", "/env/logs")

	cfg, err := LoadStorageConfigFromYAML(path)
	if err != nil {
		t.Fatalf("LoadStorageConfigFromYAML() error = %v", err)
	}
	if cfg.Lite == nil {
		t.Fatal("Lite = nil, want env-created section")
	}
	if cfg.Lite.SQLitePath != "/env/gw.db" || cfg.Lite.BodiesDir != "/env/bodies" ||
		cfg.Lite.CacheDir != "/env/cache" || cfg.Lite.LogsDir != "/env/logs" {
		t.Fatalf("lite fields = %#v, want env values", cfg.Lite)
	}

	// YAML 未配置 mode 时由 LLM_GATEWAY_STORAGE_MODE 决定。
	modeEnvPath := filepath.Join(t.TempDir(), "storage-mode-env.yaml")
	if err := os.WriteFile(modeEnvPath, []byte("lite_storage:\n  sqlite_path: /env/gw.db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_GATEWAY_STORAGE_MODE", "lite")
	modeCfg, err := LoadStorageConfigFromYAML(modeEnvPath)
	if err != nil {
		t.Fatalf("LoadStorageConfigFromYAML() error = %v", err)
	}
	if modeCfg.Mode != "lite" {
		t.Fatalf("Mode = %q, want lite from env", modeCfg.Mode)
	}

	// full 模式：postgres/redis/max_connections 均可由 env 填充。
	fullPath := filepath.Join(t.TempDir(), "storage-full-env.yaml")
	if err := os.WriteFile(fullPath, []byte("storage_mode: full\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_GATEWAY_STORAGE_MODE", "")
	t.Setenv("LLM_GATEWAY_POSTGRES_URL", "postgres://env:env@127.0.0.1:5432/envdb")
	t.Setenv("LLM_GATEWAY_REDIS_URL", "redis://127.0.0.1:6379/3")
	t.Setenv("LLM_GATEWAY_STORAGE_MAX_CONNECTIONS", "77")
	fullCfg, err := LoadStorageConfigFromYAML(fullPath)
	if err != nil {
		t.Fatalf("LoadStorageConfigFromYAML(full) error = %v", err)
	}
	if fullCfg.Full == nil {
		t.Fatal("Full = nil, want env-created section")
	}
	if fullCfg.Full.PostgresURL != "postgres://env:env@127.0.0.1:5432/envdb" ||
		fullCfg.Full.RedisURL != "redis://127.0.0.1:6379/3" || fullCfg.Full.MaxConnections != 77 {
		t.Fatalf("full fields = %#v, want env values", fullCfg.Full)
	}
	if err := fullCfg.Validate(); err != nil {
		t.Fatalf("Validate() env-only full config error = %v", err)
	}

	// YAML 已显式配置的字段不被 env 覆盖。
	t.Setenv("LLM_GATEWAY_SQLITE_PATH", "/other/should-not-win.db")
	explicitPath := filepath.Join(t.TempDir(), "storage-explicit.yaml")
	if err := os.WriteFile(explicitPath, []byte("storage_mode: lite\nlite_storage:\n  sqlite_path: /yaml/gw.db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	explicitCfg, err := LoadStorageConfigFromYAML(explicitPath)
	if err != nil {
		t.Fatalf("LoadStorageConfigFromYAML() error = %v", err)
	}
	if explicitCfg.Lite == nil || explicitCfg.Lite.SQLitePath != "/yaml/gw.db" {
		t.Fatalf("SQLitePath = %#v, want explicit YAML value preserved", explicitCfg.Lite)
	}
}

// TestLoadStorageConfigFromYAMLEnvironmentModeSwitch 验证 yaml 声明 lite 时
// 环境变量可将模式切换为 full（空字段填充语义）。
func TestLoadStorageConfigFromYAMLEnvironmentModeSwitch(t *testing.T) {
	t.Setenv("LLM_GATEWAY_STORAGE_MODE", "full")
	t.Setenv("LLM_GATEWAY_POSTGRES_URL", "postgres://switch:switch@127.0.0.1:5432/switchdb")
	t.Setenv("LLM_GATEWAY_REDIS_URL", "redis://127.0.0.1:6379/4")

	path := filepath.Join(t.TempDir(), "storage.yaml")
	if err := os.WriteFile(path, []byte("storage_mode:\nlite_storage:\n  sqlite_path: ./data/llm-gateway.db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadStorageConfigFromYAML(path)
	if err != nil {
		t.Fatalf("LoadStorageConfigFromYAML() error = %v", err)
	}
	if cfg.Mode != "full" {
		t.Fatalf("Mode = %q, want full from env", cfg.Mode)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() switched full config error = %v", err)
	}
}

// TestLoadStorageConfigFromEnv 验证 env-only 加载入口：仅凭 LLM_GATEWAY_*
// 环境变量即可构造完整可校验的 lite 配置；无任何相关环境变量时返回零值
// 配置（Mode 为空，Validate 给出明确报错）。
func TestLoadStorageConfigFromEnv(t *testing.T) {
	t.Setenv("LLM_GATEWAY_STORAGE_MODE", "lite")
	t.Setenv("LLM_GATEWAY_SQLITE_PATH", "/tmp/env-only/gateway.db")
	t.Setenv("LLM_GATEWAY_BODIES_DIR", "")
	t.Setenv("LLM_GATEWAY_CACHE_DIR", "")
	t.Setenv("LLM_GATEWAY_LOGS_DIR", "")

	cfg := LoadStorageConfigFromEnv()
	if cfg.Mode != "lite" {
		t.Fatalf("Mode = %q, want lite from env", cfg.Mode)
	}
	if cfg.Lite == nil || cfg.Lite.SQLitePath != "/tmp/env-only/gateway.db" {
		t.Fatalf("Lite = %#v, want env-created section with sqlite path", cfg.Lite)
	}

	// env-only 配置经默认值补齐后应可直接通过校验。
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() env-only lite config error = %v", err)
	}
	if cfg.Lite.BodiesDir == "" || cfg.Lite.CacheDir == "" || cfg.Lite.LogsDir == "" {
		t.Fatalf("ApplyLiteDefaults 未补齐目录默认值: %#v", cfg.Lite)
	}

	// 完全无环境变量：零值配置，Validate 报 mode 为空。
	t.Setenv("LLM_GATEWAY_STORAGE_MODE", "")
	t.Setenv("LLM_GATEWAY_SQLITE_PATH", "")
	empty := LoadStorageConfigFromEnv()
	if empty.Mode != "" {
		t.Fatalf("Mode = %q, want empty when no env set", empty.Mode)
	}
	if err := empty.Validate(); err == nil {
		t.Fatal("Validate() on empty config should fail with explicit error")
	}
}

// TestNormalizeModeTrimsWhitespace 回归：env 值可能带首尾空白（审计 P2），
// NormalizeMode 必须 trim，否则 " lite " 落入 full 分支且无提示。
func TestNormalizeModeTrimsWhitespace(t *testing.T) {
	c := &StorageConfig{Mode: " lite "}
	if got := c.NormalizeMode(); got != StorageModeLite {
		t.Fatalf("NormalizeMode() = %q, want %q", got, StorageModeLite)
	}
}

// consistencyEnvKeys 列出一致性对账相关的全部环境变量，测试开头统一清空，
// 避免宿主机环境污染。
var consistencyEnvKeys = []string{
	"LLM_GATEWAY_CONSISTENCY_CHECK_ENABLED",
	"LLM_GATEWAY_CONSISTENCY_INTERVAL_HOURS",
	"LLM_GATEWAY_CONSISTENCY_IDLE_THRESHOLD_MIN",
	"LLM_GATEWAY_CONSISTENCY_DELETE_ORPHANS",
	"LLM_GATEWAY_CONSISTENCY_MAX_SESSIONS_PER_RUN",
}

// TestLiteConsistencyDefaults 验证一致性对账配置的默认值与显式覆盖：
// 默认开启（Enabled 指针 nil → true）、report-only（delete_orphans=false）、
// 24h 周期 / 10min 空闲阈值 / 单轮 500；显式配置一律保留。
func TestLiteConsistencyDefaults(t *testing.T) {
	cfg := &StorageConfig{Mode: "lite"}
	cfg.ApplyLiteDefaults()

	c := cfg.Lite.Consistency
	if c.Enabled == nil || !*c.Enabled {
		t.Fatalf("Consistency.Enabled = %v, want default true", c.Enabled)
	}
	if c.IntervalHours != 24 {
		t.Fatalf("IntervalHours = %d, want 24", c.IntervalHours)
	}
	if c.IdleThresholdMin != 10 {
		t.Fatalf("IdleThresholdMin = %d, want 10", c.IdleThresholdMin)
	}
	if c.DeleteOrphanBodies {
		t.Fatal("DeleteOrphanBodies = true, want default false (report-only 恒安全)")
	}
	if c.MaxSessionsPerRun != 500 {
		t.Fatalf("MaxSessionsPerRun = %d, want 500", c.MaxSessionsPerRun)
	}

	// 显式关闭不被默认值翻转；显式数值不被覆盖。
	explicit := &StorageConfig{
		Mode: "lite",
		Lite: &LiteStorageConfig{SQLitePath: "/x/gw.db"},
	}
	f := false
	explicit.Lite.Consistency.Enabled = &f
	explicit.Lite.Consistency.IntervalHours = 6
	explicit.Lite.Consistency.IdleThresholdMin = 30
	explicit.Lite.Consistency.DeleteOrphanBodies = true
	explicit.Lite.Consistency.MaxSessionsPerRun = 50
	explicit.ApplyLiteDefaults()
	c = explicit.Lite.Consistency
	if c.Enabled == nil || *c.Enabled {
		t.Fatalf("显式 false 被默认值覆盖: %v", c.Enabled)
	}
	if c.IntervalHours != 6 || c.IdleThresholdMin != 30 || c.MaxSessionsPerRun != 50 || !c.DeleteOrphanBodies {
		t.Fatalf("显式配置被覆盖: %+v", c)
	}
}

// TestLoadStorageConfigYAMLConsistency 验证 YAML consistency 段解析
// （含显式 consistency_check_enabled: false 的区分能力）。
func TestLoadStorageConfigYAMLConsistency(t *testing.T) {
	for _, key := range consistencyEnvKeys {
		t.Setenv(key, "")
	}

	path := filepath.Join(t.TempDir(), "storage.yaml")
	contents := `storage_mode: lite
lite_storage:
  sqlite_path: /tmp/gateway/gw.db
  consistency:
    consistency_check_enabled: false
    interval_hours: 12
    idle_threshold_min: 30
    delete_orphans: true
    max_sessions_per_run: 100
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadStorageConfigFromYAML(path)
	if err != nil {
		t.Fatalf("LoadStorageConfigFromYAML() error = %v", err)
	}
	c := cfg.Lite.Consistency
	if c.Enabled == nil || *c.Enabled {
		t.Fatalf("Enabled = %v, want explicit false", c.Enabled)
	}
	if c.IntervalHours != 12 || c.IdleThresholdMin != 30 || c.MaxSessionsPerRun != 100 || !c.DeleteOrphanBodies {
		t.Fatalf("consistency 段 = %+v, want parsed values", c)
	}

	// ApplyLiteDefaults 后显式 false 必须保留（默认值不得翻转）。
	cfg.ApplyLiteDefaults()
	if cfg.Lite.Consistency.Enabled == nil || *cfg.Lite.Consistency.Enabled {
		t.Fatalf("ApplyLiteDefaults 翻转了显式 false: %v", cfg.Lite.Consistency.Enabled)
	}
}

// TestLoadStorageConfigEnvConsistency 验证一致性对账的 env-only 配置路径。
func TestLoadStorageConfigEnvConsistency(t *testing.T) {
	for _, key := range consistencyEnvKeys {
		t.Setenv(key, "")
	}
	t.Setenv("LLM_GATEWAY_CONSISTENCY_CHECK_ENABLED", "false")
	t.Setenv("LLM_GATEWAY_CONSISTENCY_INTERVAL_HOURS", "6")
	t.Setenv("LLM_GATEWAY_CONSISTENCY_IDLE_THRESHOLD_MIN", "30")
	t.Setenv("LLM_GATEWAY_CONSISTENCY_DELETE_ORPHANS", "true")
	t.Setenv("LLM_GATEWAY_CONSISTENCY_MAX_SESSIONS_PER_RUN", "50")

	cfg := LoadStorageConfigFromEnv()
	if cfg.Lite == nil {
		t.Fatal("Lite = nil, want env-created section")
	}
	c := cfg.Lite.Consistency
	if c.Enabled == nil || *c.Enabled {
		t.Fatalf("Enabled = %v, want env false", c.Enabled)
	}
	if c.IntervalHours != 6 || c.IdleThresholdMin != 30 || c.MaxSessionsPerRun != 50 || !c.DeleteOrphanBodies {
		t.Fatalf("env consistency = %+v, want env values", c)
	}

	// 非法数值不覆盖（保持零值 → ApplyLiteDefaults 兜底）。
	t.Setenv("LLM_GATEWAY_CONSISTENCY_INTERVAL_HOURS", "-3")
	t.Setenv("LLM_GATEWAY_CONSISTENCY_DELETE_ORPHANS", "not-a-bool")
	cfg = LoadStorageConfigFromEnv()
	if cfg.Lite.Consistency.IntervalHours != 0 {
		t.Fatalf("非法 IntervalHours 被采纳: %d", cfg.Lite.Consistency.IntervalHours)
	}
	if cfg.Lite.Consistency.DeleteOrphanBodies {
		t.Fatal("非法 delete_orphans 值不应置位")
	}
}

// TestConsistencyYAMLExplicitBeatsEnv（2026-09-05 round2 复审 F5a）：YAML 显式
// consistency_check_enabled: true + env = false 时 YAML 必须胜出——env 只填充
// YAML 未配置（nil）的指针字段（applyOptionalBoolEnv），不覆盖显式配置。
func TestConsistencyYAMLExplicitBeatsEnv(t *testing.T) {
	for _, key := range consistencyEnvKeys {
		t.Setenv(key, "")
	}
	t.Setenv("LLM_GATEWAY_CONSISTENCY_CHECK_ENABLED", "false")

	path := filepath.Join(t.TempDir(), "storage.yaml")
	contents := `storage_mode: lite
lite_storage:
  sqlite_path: /tmp/gateway/gw.db
  consistency:
    consistency_check_enabled: true
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadStorageConfigFromYAML(path)
	if err != nil {
		t.Fatalf("LoadStorageConfigFromYAML() error = %v", err)
	}
	c := cfg.Lite.Consistency
	if c.Enabled == nil || !*c.Enabled {
		t.Fatalf("Enabled = %v, want YAML explicit true（env false 不得覆盖显式配置）", c.Enabled)
	}
}

// TestConsistencyEnvInvalidBoolFallsBackToDefault（2026-09-05 round2 复审
// F5b）：env CHECK_ENABLED 为非法值（非 1/true/yes/on 与 0/false/no/off）时
// 忽略不报错，指针保持 nil → ApplyLiteDefaults 兜底默认 true。
func TestConsistencyEnvInvalidBoolFallsBackToDefault(t *testing.T) {
	for _, key := range consistencyEnvKeys {
		t.Setenv(key, "")
	}
	t.Setenv("LLM_GATEWAY_CONSISTENCY_CHECK_ENABLED", "notabool")

	cfg := LoadStorageConfigFromEnv()
	if cfg.Lite == nil {
		t.Fatal("Lite = nil, want env-created section")
	}
	if cfg.Lite.Consistency.Enabled != nil {
		t.Fatalf("Enabled = %v, want nil（非法 env 值不写入）", cfg.Lite.Consistency.Enabled)
	}
	cfg.ApplyLiteDefaults()
	if cfg.Lite.Consistency.Enabled == nil || !*cfg.Lite.Consistency.Enabled {
		t.Fatalf("Enabled = %v, want 默认 true", cfg.Lite.Consistency.Enabled)
	}
}
