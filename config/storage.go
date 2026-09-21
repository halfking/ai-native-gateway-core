package config

// 双模式存储架构（Task 4.1）：storage 配置的类型定义、校验、默认值与加载。
//
// 网关支持两种存储后端：
//   - full：PostgreSQL + Redis，面向生产/多实例部署（YAML full_storage 段）；
//   - lite：SQLite + 本地文件目录，面向单机/轻量部署（YAML lite_storage 段）。
//
// 加载入口为 LoadStorageConfigFromYAML：先解析 YAML，再用 LLM_GATEWAY_* 环境
// 变量填充 YAML 未配置（空字符串/零值）的字段——覆盖风格与本包 config.go 的
// 手写 env 处理（applyPositiveIntEnv 等）保持一致。
//
// StorageConfig 是独立结构体，不挂载到 Config；与主程序启动流程的接线由后续
// 波次的集成任务完成，因此本文件不修改 config.go。

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// StorageMode 标识存储模式。
type StorageMode string

const (
	// StorageModeFull：PostgreSQL + Redis 全量存储（生产/多实例）。
	StorageModeFull StorageMode = "full"
	// StorageModeLite：SQLite + 本地目录轻量存储（单机/个人部署）。
	StorageModeLite StorageMode = "lite"
)

// StorageConfig 是双模式存储的顶层配置。
// SQLitePragmasConfig SQLite PRAGMA 调优项（具名类型：主程序装配层直接引用，
// 避免 cmd 侧用匿名 struct 复制导致加字段时静默漂移——审计 P2）。
type SQLitePragmasConfig struct {
	JournalMode   string `yaml:"journal_mode"`
	CacheSizeKB   int    `yaml:"cache_size_kb"`
	Synchronous   string `yaml:"synchronous"`
	BusyTimeoutMS int    `yaml:"busy_timeout_ms"`
}

type StorageConfig struct {
	// Mode 存储模式："full" 或 "lite"。空值非法（见 Validate）。
	Mode string `yaml:"storage_mode" env:"LLM_GATEWAY_STORAGE_MODE"`
	// Full full 模式的存储明细；mode=full 时必填。
	Full *FullStorageConfig `yaml:"full_storage"`
	// Lite lite 模式的存储明细；mode=lite 时必填。
	Lite *LiteStorageConfig `yaml:"lite_storage"`
}

// FullStorageConfig full 模式：外部 PostgreSQL + Redis。
type FullStorageConfig struct {
	PostgresURL    string `yaml:"postgres_url" env:"LLM_GATEWAY_POSTGRES_URL"`
	RedisURL       string `yaml:"redis_url" env:"LLM_GATEWAY_REDIS_URL"`
	MaxConnections int    `yaml:"max_connections" env:"LLM_GATEWAY_STORAGE_MAX_CONNECTIONS"`
}

// LiteConsistencyConfig lite 跨介质一致性对账（后台 worker）配置。
// 默认语义恒安全：worker 默认开启但 report-only（只对账 + 结构化日志，
// 不删任何数据）；仅当 delete_orphans 显式开启时才删除孤儿，且删除路径
// 仍受「删除前 meta 复检 + mtime 宽限」双保险保护（storage.RepairTurnArtifacts）。
type LiteConsistencyConfig struct {
	// Enabled 是否启用周期对账。指针类型区分「未配置（nil）→ 默认 true」
	// 与 YAML/env 显式 false（关闭对账 worker）。
	Enabled *bool `yaml:"consistency_check_enabled"`
	// IntervalHours 对账周期（小时），默认 24（每日一次，低频）。
	IntervalHours int `yaml:"interval_hours"`
	// IdleThresholdMin 空闲阈值（分钟）：只对账最后活动早于该阈值的会话，
	// 把「body 先落盘、meta 后提交」的在途写入挡在对账窗外，默认 10。
	IdleThresholdMin int `yaml:"idle_threshold_min"`
	// DeleteOrphanBodies 是否在对账之外删除已确认的孤儿 body 文件。
	// 默认 false（report-only）；零值即安全默认，无需显式配置。
	DeleteOrphanBodies bool `yaml:"delete_orphans"`
	// MaxSessionsPerRun 单轮对账的会话数上限（bounded，防止首跑扫全库），
	// 默认 500；空闲会话超过上限时 worker 按轮转偏移（OFFSET）跨轮分页，
	// 全部空闲会话在 ceil(N/上限) 轮内覆盖（2026-09-05 round2 复审 F1）。
	MaxSessionsPerRun int `yaml:"max_sessions_per_run"`
}

// LiteStorageConfig lite 模式：SQLite 数据库 + 本地文件目录。
type LiteStorageConfig struct {
	SQLitePath string `yaml:"sqlite_path" env:"LLM_GATEWAY_SQLITE_PATH"`
	BodiesDir  string `yaml:"bodies_dir" env:"LLM_GATEWAY_BODIES_DIR"`
	// BodiesCodec 会话体落盘压缩编码："zstd"（默认）或 "gzip"（回退）。
	// 读路径按文件后缀双格式兼容，切换不影响存量数据。
	BodiesCodec string `yaml:"bodies_codec" env:"LLM_GATEWAY_BODIES_CODEC"`
	CacheDir    string `yaml:"cache_dir" env:"LLM_GATEWAY_CACHE_DIR"`
	LogsDir     string `yaml:"logs_dir" env:"LLM_GATEWAY_LOGS_DIR"`

	// SQLitePragmas SQLite 连接初始化时执行的 PRAGMA 调优项。
	SQLitePragmas SQLitePragmasConfig `yaml:"sqlite_pragmas"`

	CacheTTLHours  int `yaml:"cache_ttl_hours"`
	CacheMaxSizeGB int `yaml:"cache_max_size_gb"`
	AsyncWriters   int `yaml:"async_writers"`

	// Retention 各类数据的保留周期。
	Retention struct {
		SessionBodiesDays int `yaml:"session_bodies_days"`
		RequestLogsDays   int `yaml:"request_logs_days"`
		CacheHours        int `yaml:"cache_hours"`
	} `yaml:"retention"`

	// Consistency lite 跨介质一致性对账（后台 worker，审计 B-#2 接线）配置。
	Consistency LiteConsistencyConfig `yaml:"consistency"`
}

// NormalizeMode 返回当前存储模式（原样转换，不做 trim 或归一化）。
func (c *StorageConfig) NormalizeMode() StorageMode {
	if c == nil {
		return ""
	}
	return StorageMode(strings.TrimSpace(c.Mode))
}

// Validate 校验存储配置：
//   - mode 必须为 "full" 或 "lite"，空字符串给出明确报错；
//   - full 模式要求 full_storage 段存在且 postgres_url / redis_url 非空；
//   - lite 模式要求 lite_storage 段存在且 sqlite_path 非空。
//
// 建议先调用 ApplyDefaults 补齐默认值再校验。
func (c *StorageConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("storage config is nil")
	}
	switch c.NormalizeMode() {
	case StorageModeFull:
		if c.Full == nil {
			return fmt.Errorf(`storage_mode "full" requires a full_storage section in yaml`)
		}
		if strings.TrimSpace(c.Full.PostgresURL) == "" {
			return fmt.Errorf(`storage_mode "full" requires full_storage.postgres_url (env LLM_GATEWAY_POSTGRES_URL)`)
		}
		if strings.TrimSpace(c.Full.RedisURL) == "" {
			return fmt.Errorf(`storage_mode "full" requires full_storage.redis_url (env LLM_GATEWAY_REDIS_URL)`)
		}
	case StorageModeLite:
		if c.Lite == nil {
			return fmt.Errorf(`storage_mode "lite" requires a lite_storage section in yaml`)
		}
		if strings.TrimSpace(c.Lite.SQLitePath) == "" {
			return fmt.Errorf(`storage_mode "lite" requires lite_storage.sqlite_path (env LLM_GATEWAY_SQLITE_PATH)`)
		}
		if codec := strings.ToLower(strings.TrimSpace(c.Lite.BodiesCodec)); codec != "" && codec != "zstd" && codec != "gzip" {
			return fmt.Errorf(`lite_storage.bodies_codec %q: must be "zstd" or "gzip"`, c.Lite.BodiesCodec)
		}
	default:
		if strings.TrimSpace(c.Mode) == "" {
			return fmt.Errorf(`storage_mode is empty: must be "full" or "lite" (yaml storage_mode or env LLM_GATEWAY_STORAGE_MODE)`)
		}
		return fmt.Errorf(`invalid storage_mode %q: must be "full" or "lite"`, c.Mode)
	}
	return nil
}

// ApplyLiteDefaults 为 lite 模式补齐默认值：任何空字符串/零值字段都采用
// 仓库约定的轻量部署默认值（显式配置的值不被覆盖）。Lite 段缺失时自动创建。
func (c *StorageConfig) ApplyLiteDefaults() {
	if c == nil {
		return
	}
	if c.Lite == nil {
		c.Lite = &LiteStorageConfig{}
	}
	l := c.Lite
	if strings.TrimSpace(l.SQLitePath) == "" {
		l.SQLitePath = "./data/llm-gateway.db"
	}
	if strings.TrimSpace(l.BodiesDir) == "" {
		l.BodiesDir = "./data/session_bodies"
	}
	if strings.TrimSpace(l.BodiesCodec) == "" {
		// 默认保持历史 gzip，避免新二进制写入 zstd 后旧二进制回滚不可读。
		// 经过灰度验证后再显式配置 bodies_codec=zstd。
		l.BodiesCodec = "gzip"
	}
	if strings.TrimSpace(l.CacheDir) == "" {
		l.CacheDir = "./data/cache"
	}
	if strings.TrimSpace(l.LogsDir) == "" {
		l.LogsDir = "./data/request_logs"
	}
	if l.AsyncWriters <= 0 {
		l.AsyncWriters = 4
	}
	if l.CacheTTLHours <= 0 {
		l.CacheTTLHours = 24
	}
	if l.CacheMaxSizeGB <= 0 {
		l.CacheMaxSizeGB = 10
	}
	if strings.TrimSpace(l.SQLitePragmas.JournalMode) == "" {
		l.SQLitePragmas.JournalMode = "WAL"
	}
	if l.SQLitePragmas.CacheSizeKB <= 0 {
		l.SQLitePragmas.CacheSizeKB = 64000
	}
	if strings.TrimSpace(l.SQLitePragmas.Synchronous) == "" {
		l.SQLitePragmas.Synchronous = "NORMAL"
	}
	if l.SQLitePragmas.BusyTimeoutMS <= 0 {
		l.SQLitePragmas.BusyTimeoutMS = 5000
	}
	if l.Retention.SessionBodiesDays <= 0 {
		l.Retention.SessionBodiesDays = 30
	}
	if l.Retention.RequestLogsDays <= 0 {
		l.Retention.RequestLogsDays = 7
	}
	if l.Retention.CacheHours <= 0 {
		l.Retention.CacheHours = 24
	}
	// 一致性对账（B-#2）：默认开启、每日一次、空闲阈值 10 分钟、单轮上限
	// 500。DeleteOrphanBodies 零值即安全默认（false = report-only），不兜底。
	if l.Consistency.Enabled == nil {
		enabled := true
		l.Consistency.Enabled = &enabled
	}
	if l.Consistency.IntervalHours <= 0 {
		l.Consistency.IntervalHours = 24
	}
	if l.Consistency.IdleThresholdMin <= 0 {
		l.Consistency.IdleThresholdMin = 10
	}
	if l.Consistency.MaxSessionsPerRun <= 0 {
		l.Consistency.MaxSessionsPerRun = 500
	}
}

// ApplyDefaults 按 mode 分派默认值：full 模式补 max_connections=200；
// lite 模式委托给 ApplyLiteDefaults。mode 为空/非法时不做任何事（由
// Validate 报错），方便调用方按 "Load → ApplyDefaults → Validate" 排序。
//
// ⚠️ 消费范围（2026-09-18 审计澄清，作废早期错误注释）：生产网关不消费
// 本默认值——main 从不调用 ApplyDefaults，业务 PG 池由 db.Open 构造，
// 上限旋钮是 LLM_GATEWAY_DB_MAX_CONNS（默认 32）。Full.MaxConnections 仅当
// 调用方显式走 NewStorageFactory(mode=full) 时才生效（当前生产无此路径：
// factory full 分支为桩，见 storage/factory/stubs.go；initStorageMode 仅
// lite 构造工厂）。早期注释"单 pod 总占 PG = 32 + 200 = 232 conn"因此作废：
// 生产 pool 只有 32，不要拿本字段或连接计数推断生产池容量，以启动日志
// "postgres connected max_conns=N" 为准。2026-09-18 默认 100→200 保留：
// 配合 PG max_connections=1000（252 + 本地 ALTER SYSTEM）为未来 full 接线
// 预留安全值，单 pod 演示环境（独占 PG）200-500 均安全。
func (c *StorageConfig) ApplyDefaults() {
	if c == nil {
		return
	}
	switch c.NormalizeMode() {
	case StorageModeFull:
		if c.Full == nil {
			c.Full = &FullStorageConfig{}
		}
		if c.Full.MaxConnections <= 0 {
			c.Full.MaxConnections = 200
		}
	case StorageModeLite:
		c.ApplyLiteDefaults()
	}
}

// PostgresURLAlignment 描述 AlignFullPostgresURL 的对齐结果。
type PostgresURLAlignment int

const (
	// PostgresURLUnchanged 无需对齐：双方一致、均为空，或不适用（非 full
	// 模式 / Full 段缺失）。
	PostgresURLUnchanged PostgresURLAlignment = iota
	// PostgresURLPromoted 提升方向：full_storage.postgres_url 非空而
	// DATABASE_URL 为空，已把 postgres_url 写入 *databaseURL。
	PostgresURLPromoted
	// PostgresURLBackfilled 回填方向：DATABASE_URL 非空而
	// full_storage.postgres_url 为空，已把 *databaseURL 写回 YAML 段。
	PostgresURLBackfilled
	// PostgresURLConflict 双方均非空且不一致：未改动任何一方，DATABASE_URL
	// env 优先（与历史行为一致），由调用方决定是否告警。
	PostgresURLConflict
)

// AlignFullPostgresURL 双向对齐 full_storage.postgres_url 与主配置的
// DATABASE_URL（audit 2026-09-14 R28 #17）：full 模式下两个入口指向同一个
// PostgreSQL，此前 main 建池只读 DATABASE_URL，postgres_url 是"假字段"，
// 只配其一会导致配置漂移。仅 full 模式生效：
//
//   - postgres_url 非空且 *databaseURL 为空 → 提升：*databaseURL = postgres_url；
//   - *databaseURL 非空且 postgres_url 为空 → 回填：postgres_url = *databaseURL；
//   - 两者均非空且不同 → 不改任何一方，返回 PostgresURLConflict（DATABASE_URL
//     env 优先，历史行为不变）。
//
// 非 full 模式、Full 段为 nil，或双方 trim 后一致时返回 PostgresURLUnchanged。
func (c *StorageConfig) AlignFullPostgresURL(databaseURL *string) PostgresURLAlignment {
	if c == nil || databaseURL == nil || c.NormalizeMode() != StorageModeFull || c.Full == nil {
		return PostgresURLUnchanged
	}
	pg := strings.TrimSpace(c.Full.PostgresURL)
	db := strings.TrimSpace(*databaseURL)
	switch {
	case pg != "" && db == "":
		*databaseURL = c.Full.PostgresURL
		return PostgresURLPromoted
	case pg == "" && db != "":
		c.Full.PostgresURL = *databaseURL
		return PostgresURLBackfilled
	case pg != "" && db != "" && pg != db:
		return PostgresURLConflict
	default:
		return PostgresURLUnchanged
	}
}

// LoadStorageConfigFromYAML 从 YAML 文件加载 StorageConfig：解析后再用
// LLM_GATEWAY_* 环境变量填充未配置（零值）字段。调用方随后应执行
// ApplyDefaults（补默认值）与 Validate（校验必填项）。
func LoadStorageConfigFromYAML(path string) (*StorageConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &StorageConfig{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse storage config %s: %w", path, err)
	}
	applyEnvOverrides(cfg)
	return cfg, nil
}

// LoadStorageConfigFromEnv 构造仅来自 LLM_GATEWAY_* 环境变量的 StorageConfig，
// 供无 YAML 文件的部署（env-only）作为存储配置加载入口。返回的配置同样需要
// 调用方执行 ApplyDefaults 与 Validate。没有任何相关环境变量时返回零值配置
// （Mode 为空，Validate 会给出明确报错）。
func LoadStorageConfigFromEnv() *StorageConfig {
	cfg := &StorageConfig{}
	applyEnvOverrides(cfg)
	return cfg
}

// applyEnvOverrides 用环境变量填充 YAML 未提供的字段（空字符串/零值才被
// env 填充，显式配置不被覆盖），风格对齐 config.go 的手写覆盖逻辑。
// 仅处理带 env tag 的字段；嵌套段缺失但 env 有值时自动创建对应段，
// 保证 env-only 部署同样可用。
func applyEnvOverrides(cfg *StorageConfig) {
	if cfg == nil {
		return
	}
	if cfg.Mode == "" {
		if v := os.Getenv("LLM_GATEWAY_STORAGE_MODE"); v != "" {
			cfg.Mode = v
		}
	}

	// full 段。
	if v := os.Getenv("LLM_GATEWAY_POSTGRES_URL"); v != "" {
		if cfg.Full == nil {
			cfg.Full = &FullStorageConfig{}
		}
		if cfg.Full.PostgresURL == "" {
			cfg.Full.PostgresURL = v
		}
	}
	if v := os.Getenv("LLM_GATEWAY_REDIS_URL"); v != "" {
		if cfg.Full == nil {
			cfg.Full = &FullStorageConfig{}
		}
		if cfg.Full.RedisURL == "" {
			cfg.Full.RedisURL = v
		}
	}
	if cfg.Full != nil {
		// 复用 config.go 的正整数 env 解析：非法/非正值不覆盖，保持零值。
		applyPositiveIntEnv("LLM_GATEWAY_STORAGE_MAX_CONNECTIONS", &cfg.Full.MaxConnections)
	}

	// lite 段。
	if v := os.Getenv("LLM_GATEWAY_SQLITE_PATH"); v != "" {
		if cfg.Lite == nil {
			cfg.Lite = &LiteStorageConfig{}
		}
		if cfg.Lite.SQLitePath == "" {
			cfg.Lite.SQLitePath = v
		}
	}
	if v := os.Getenv("LLM_GATEWAY_BODIES_DIR"); v != "" {
		if cfg.Lite == nil {
			cfg.Lite = &LiteStorageConfig{}
		}
		if cfg.Lite.BodiesDir == "" {
			cfg.Lite.BodiesDir = v
		}
	}
	if v := os.Getenv("LLM_GATEWAY_BODIES_CODEC"); v != "" {
		if cfg.Lite == nil {
			cfg.Lite = &LiteStorageConfig{}
		}
		if cfg.Lite.BodiesCodec == "" {
			cfg.Lite.BodiesCodec = v
		}
	}
	if v := os.Getenv("LLM_GATEWAY_CACHE_DIR"); v != "" {
		if cfg.Lite == nil {
			cfg.Lite = &LiteStorageConfig{}
		}
		if cfg.Lite.CacheDir == "" {
			cfg.Lite.CacheDir = v
		}
	}
	if v := os.Getenv("LLM_GATEWAY_LOGS_DIR"); v != "" {
		if cfg.Lite == nil {
			cfg.Lite = &LiteStorageConfig{}
		}
		if cfg.Lite.LogsDir == "" {
			cfg.Lite.LogsDir = v
		}
	}

	// lite 一致性对账段（B-#2）：段缺失但 env 有值时自动创建，保证 env-only
	// 部署同样可配置。
	if v := os.Getenv("LLM_GATEWAY_CONSISTENCY_CHECK_ENABLED"); v != "" {
		if cfg.Lite == nil {
			cfg.Lite = &LiteStorageConfig{}
		}
		applyOptionalBoolEnv(v, &cfg.Lite.Consistency.Enabled)
	}
	if v := os.Getenv("LLM_GATEWAY_CONSISTENCY_INTERVAL_HOURS"); v != "" {
		if cfg.Lite == nil {
			cfg.Lite = &LiteStorageConfig{}
		}
		applyPositiveIntEnv("LLM_GATEWAY_CONSISTENCY_INTERVAL_HOURS", &cfg.Lite.Consistency.IntervalHours)
	}
	if v := os.Getenv("LLM_GATEWAY_CONSISTENCY_IDLE_THRESHOLD_MIN"); v != "" {
		if cfg.Lite == nil {
			cfg.Lite = &LiteStorageConfig{}
		}
		applyPositiveIntEnv("LLM_GATEWAY_CONSISTENCY_IDLE_THRESHOLD_MIN", &cfg.Lite.Consistency.IdleThresholdMin)
	}
	if v := os.Getenv("LLM_GATEWAY_CONSISTENCY_DELETE_ORPHANS"); v != "" {
		if cfg.Lite == nil {
			cfg.Lite = &LiteStorageConfig{}
		}
		if parseBoolEnv(v) {
			// 仅真值置位：假值与零值语义相同（report-only 恒安全默认）。
			cfg.Lite.Consistency.DeleteOrphanBodies = true
		}
	}
	if v := os.Getenv("LLM_GATEWAY_CONSISTENCY_MAX_SESSIONS_PER_RUN"); v != "" {
		if cfg.Lite == nil {
			cfg.Lite = &LiteStorageConfig{}
		}
		applyPositiveIntEnv("LLM_GATEWAY_CONSISTENCY_MAX_SESSIONS_PER_RUN", &cfg.Lite.Consistency.MaxSessionsPerRun)
	}
}

// parseBoolEnv 解析布尔环境变量值：1/true/yes/on（大小写不敏感）为真，
// 其余（0/false/no/off 或非法值）为假。
func parseBoolEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// applyOptionalBoolEnv 把布尔 env 值写入「可选布尔」指针：仅在字段未配置
// （nil）时写入，保留 YAML 显式配置不被覆盖；env 值非法时忽略保持 nil。
func applyOptionalBoolEnv(v string, target **bool) {
	if target == nil || *target != nil {
		return
	}
	if parseBoolEnv(v) {
		t := true
		*target = &t
		return
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		f := false
		*target = &f
	}
}
