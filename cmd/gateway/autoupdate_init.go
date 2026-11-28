package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/autoupdate"
	"github.com/kaixuan/llm-gateway-go/center"
	"github.com/labstack/echo/v5"
)

// autoupdateConfig 是自更新面的运行配置。
//
// ★ 路径默认值对齐**真实部署布局**（deploy/llm-gateway-go.service 的
//
//	WorkingDirectory=/opt/llm-gateway-go、ExecStart=/opt/llm-gateway-go/llm-gateway-go）。
//	旧实现硬编码 /usr/local/bin + /var/backups + /var/lib —— 那套路径在本仓的
//	部署形态下**根本不存在**，即使把安装接上也只会替换一个没有在跑的文件。
type autoupdateConfig struct {
	Enabled             bool
	BinPath             string
	BackupDir           string
	DataDir             string
	ServiceName         string
	DownloadDir         string
	SourceURL           string
	SourceToken         string
	SourceRepoDir       string
	ArtifactURLTemplate string
	EnvFile             string // 主机配置文件（升级配置合同：新增/强制更新写这里）
	CheckInterval       time.Duration
}

// autoupdateEnvNames 是自更新面读取的环境变量清单（集中登记，便于运维查阅）。
var autoupdateEnvNames = []string{
	"LLM_GATEWAY_UPDATE_DISABLED",
	"LLM_GATEWAY_UPDATE_BIN_PATH",
	"LLM_GATEWAY_UPDATE_BACKUP_DIR",
	"LLM_GATEWAY_UPDATE_DATA_DIR",
	"LLM_GATEWAY_UPDATE_SERVICE_NAME",
	"LLM_GATEWAY_UPDATE_DOWNLOAD_DIR",
	"LLM_GATEWAY_UPDATE_SOURCE_URL",
	"LLM_GATEWAY_UPDATE_SOURCE_TOKEN",
	"LLM_GATEWAY_UPDATE_SOURCE_DIR",
	"LLM_GATEWAY_UPDATE_ARTIFACT_URL_TEMPLATE",
	"LLM_GATEWAY_UPDATE_CHECK_INTERVAL",
	"LLM_GATEWAY_UPDATE_ENV_FILE",
}

// loadAutoupdateConfig 从环境变量装配自更新配置。
func loadAutoupdateConfig() autoupdateConfig {
	cfg := autoupdateConfig{
		Enabled:             !envTruthy(os.Getenv("LLM_GATEWAY_UPDATE_DISABLED")),
		BinPath:             envOrDefault("LLM_GATEWAY_UPDATE_BIN_PATH", "/opt/llm-gateway-go/llm-gateway-go"),
		BackupDir:           envOrDefault("LLM_GATEWAY_UPDATE_BACKUP_DIR", "/opt/llm-gateway-go/backups"),
		DataDir:             envOrDefault("LLM_GATEWAY_UPDATE_DATA_DIR", "/opt/llm-gateway-go"),
		ServiceName:         envOrDefault("LLM_GATEWAY_UPDATE_SERVICE_NAME", "llm-gateway-go"),
		DownloadDir:         envOrDefault("LLM_GATEWAY_UPDATE_DOWNLOAD_DIR", "/opt/llm-gateway-go/downloads"),
		SourceURL:           strings.TrimSpace(os.Getenv("LLM_GATEWAY_UPDATE_SOURCE_URL")),
		SourceToken:         strings.TrimSpace(os.Getenv("LLM_GATEWAY_UPDATE_SOURCE_TOKEN")),
		SourceRepoDir:       strings.TrimSpace(os.Getenv("LLM_GATEWAY_UPDATE_SOURCE_DIR")),
		ArtifactURLTemplate: envOrDefault("LLM_GATEWAY_UPDATE_ARTIFACT_URL_TEMPLATE", "https://llm.kxpms.cn/releases/{version}/llm-gateway-go"),
		EnvFile:             envOrDefault("LLM_GATEWAY_UPDATE_ENV_FILE", filepath.Join(envOrDefault("LLM_GATEWAY_UPDATE_DATA_DIR", "/opt/llm-gateway-go"), ".env")),
		CheckInterval:       6 * time.Hour,
	}
	if cfg.SourceURL == "" {
		// 默认用 license-authority（与 token 刷新守护进程同一个源）。
		cfg.SourceURL = strings.TrimSpace(os.Getenv("LICENSE_AUTHORITY_URL"))
	}
	if raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_UPDATE_CHECK_INTERVAL")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			cfg.CheckInterval = d
		} else if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			cfg.CheckInterval = time.Duration(secs) * time.Second
		} else {
			slog.Warn("autoupdate: invalid check interval, using default", "value", raw, "default", cfg.CheckInterval)
		}
	}
	return cfg
}

// envOrDefault 读环境变量，空则用默认值。
func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// envTruthy 判断环境变量是否为真值。
func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// autoupdateRuntime 持有自更新面的运行时对象。
type autoupdateRuntime struct {
	api         *autoupdate.AdminAPI
	customerAPI *autoupdate.CustomerAPI
	checker     *autoupdate.Checker
	executor    *autoupdate.Executor
}

// setupAutoupdate 装配并注册自更新面（检索 + 提醒 + 安装执行）。
//
// 返回的 runtime 里 checker 可能为 nil（未配置远端源）——调用方要能区分
// 「没配」与「配了但检索失败」，这两种情况在运维面上的处置完全不同。
func setupAutoupdate(pool *pgxpool.Pool, adminGroup *echo.Group, version autoupdate.VersionProvider) *autoupdateRuntime {
	cfg := loadAutoupdateConfig()
	if !cfg.Enabled {
		slog.Info("autoupdate: disabled (LLM_GATEWAY_UPDATE_DISABLED)")
		return nil
	}

	store := autoupdate.NewPgxStore(pool)
	downloader := autoupdate.NewDownloader(cfg.DownloadDir)
	installer := autoupdate.NewInstaller(cfg.BinPath, cfg.BackupDir, cfg.DataDir)
	installer.SetServiceName(cfg.ServiceName)
	// 迁移用**新二进制**执行（此刻 binPath 已被替换成新版本），
	// 保证迁移逻辑与新代码同版本 —— 这与 r1052 的「先迁移后切流」原则一致。
	installer.SetMigration(cfg.BinPath, "migrate")

	rollback := autoupdate.NewRollback(cfg.BinPath, cfg.BackupDir, cfg.DataDir)
	rollback.SetServiceName(cfg.ServiceName)
	// 升级配置合同：发布物的配置变更（新增/强制更新）写主机配置文件，
	// 默认 <dataDir>/.env；systemd EnvironmentFile 另有位置时用 env 覆盖。
	// 安装器改前备份、失败还原；回滚器还原最新配置备份。
	installer.SetEnvFile(cfg.EnvFile)
	rollback.SetEnvFile(cfg.EnvFile)

	api := autoupdate.NewAdminAPI(store, downloader, installer, rollback)

	instanceID := localInstanceID()

	customerAPI := autoupdate.NewCustomerAPI(store, version, autoupdate.ChannelStable)

	execOpts := []autoupdate.ExecutorOption{
		autoupdate.WithArtifactURLTemplate(cfg.ArtifactURLTemplate),
		autoupdate.WithExecutorInstanceID(instanceID),
	}
	if cfg.SourceRepoDir != "" {
		execOpts = append(execOpts, autoupdate.WithSourceBuilder(
			autoupdate.NewSourceBuilder(
				autoupdate.WithSourceRepoDir(cfg.SourceRepoDir),
				autoupdate.WithGoBin(envOrDefault("LLM_GATEWAY_UPDATE_GO_BIN", "go")),
			),
		))
	}
	executor := autoupdate.NewExecutor(store, downloader, installer, version, execOpts...)
	api.SetExecutor(executor)

	if cfg.SourceURL != "" {
		source := autoupdate.NewHTTPRemoteSource(cfg.SourceURL,
			autoupdate.WithTokenFunc(func() string { return resolveUpdateSourceToken(cfg.SourceToken) }),
		)
		checker := autoupdate.NewChecker(store, source, version, autoupdate.ChannelStable,
			autoupdate.WithCheckInterval(cfg.CheckInterval),
			autoupdate.WithInstanceID(instanceID),
			autoupdate.WithAlertSink(centerAlertSink{store: center.NewPgxStore(pool)}),
		)
		api.SetChecker(checker)
		customerAPI.SetChecker(checker)
		slog.Info("autoupdate: version source configured",
			"source_url", cfg.SourceURL,
			"interval", cfg.CheckInterval.String())
		return &autoupdateRuntime{
			api:         api,
			customerAPI: customerAPI,
			checker:     checker,
			executor:    executor,
		}
	}

	slog.Warn("autoupdate: no remote version source configured — automatic version check and reminders are disabled",
		"hint", "set LLM_GATEWAY_UPDATE_SOURCE_URL or LICENSE_AUTHORITY_URL")
	return &autoupdateRuntime{
		api:         api,
		customerAPI: customerAPI,
		executor:    executor,
	}
}

// centerAlertSink 把「有新版」提醒投递到运维告警面（runtime_alert_events）。
//
// 告警与提醒行同源生成、不同面消费：提醒行是安装任务的日志，告警是给运维
// 中心看的（可确认 / 消解 / 抑制）。
type centerAlertSink struct {
	store *center.PgxStore
}

// RaiseUpdateAvailableAlert 实现 autoupdate.AlertSink。
func (s centerAlertSink) RaiseUpdateAvailableAlert(ctx context.Context, instanceID, version, notes, severity string) (int64, bool, error) {
	return s.store.RaiseUpdateAvailableAlert(ctx, instanceID, version, notes, severity)
}

// startAutoupdateChecker 启动后台周期检索（自动提醒的驱动源）。//
// 检索失败不会中断循环，也不会影响网关主流程 —— 它是「锦上添花」的能力。
func startAutoupdateChecker(ctx context.Context, rt *autoupdateRuntime) {
	if rt == nil || rt.checker == nil {
		return
	}
	go rt.checker.Run(ctx)
}

// resolveUpdateSourceToken 取访问远端发布源用的令牌。
//
// 优先用显式配置；否则读 token 刷新守护进程写的 instance_token 文件
// （守护进程会轮换令牌，所以每次请求时读，而不是启动时固化）。
func resolveUpdateSourceToken(configured string) string {
	if v := strings.TrimSpace(configured); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	path := filepath.Join(home, ".kx-gateway", "instance_token")
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// localInstanceID 读取本机实例标识（与 licensing 的落盘位置一致）。
//
// 读不到时返回 "local"：提醒行仍然可写，只是跨机器聚合时区分度低。
func localInstanceID() string {
	candidates := []string{}
	if dir := strings.TrimSpace(os.Getenv("LLM_GATEWAY_DATA_DIR")); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "instance.id"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, ".local", "share", "kx-gateway", "instance.id"))
	}
	candidates = append(candidates, "/var/lib/kx-gateway/instance.id")
	for _, path := range candidates {
		if raw, err := os.ReadFile(path); err == nil {
			if id := strings.TrimSpace(string(raw)); id != "" {
				return id
			}
		}
	}
	return "local"
}

// describeAutoupdateEnv 输出自更新面的生效配置（供启动日志与排障使用）。
func describeAutoupdateEnv(cfg autoupdateConfig) string {
	return fmt.Sprintf("bin=%s backup=%s data=%s service=%s source=%s interval=%s",
		cfg.BinPath, cfg.BackupDir, cfg.DataDir, cfg.ServiceName, cfg.SourceURL, cfg.CheckInterval)
}
