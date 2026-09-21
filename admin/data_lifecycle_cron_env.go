package admin

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// HotCronConfigFromEnv 从环境变量构造 HotCronConfig（导出供 cmd/gateway/main.go 调用）。
//
// 环境变量：
//
//	HOT_CRON_DISABLED=1         完全禁用夜间 cron
//	HOT_CRON_RUN_AT=HH:MM       触发时间（默认 02:00）
//	HOT_CRON_RETENTION_HOURS=N  迁移超过 N 小时的数据（默认 8）
//	HOT_CRON_BATCH_SIZE=N       单批迁移行数（默认 500）
//	HOT_CRON_MAX_RETRIES=N      单表失败重试次数（默认 3）
//	HOT_CRON_BACKOFF_SECONDS=N  首次重试等待秒数（默认 30）
func HotCronConfigFromEnv() HotCronConfig {
	cfg := HotCronConfig{
		Enabled:        true,
		RunAtHour:      2,
		RunAtMinute:    0,
		RetentionHours: defaultHotRetentionHours,
		BatchSize:      500,
		MaxRetries:     3,
		RetryBackoff:   30 * time.Second,
	}
	if v := os.Getenv("HOT_CRON_DISABLED"); v == "1" || strings.EqualFold(v, "true") {
		cfg.Enabled = false
	}
	if v := os.Getenv("HOT_CRON_RUN_AT"); v != "" {
		parts := strings.SplitN(v, ":", 2)
		if len(parts) == 2 {
			h, errH := strconv.Atoi(parts[0])
			m, errM := strconv.Atoi(parts[1])
			if errH == nil && errM == nil && h >= 0 && h < 24 && m >= 0 && m < 60 {
				cfg.RunAtHour = h
				cfg.RunAtMinute = m
			}
		}
	}
	if v := os.Getenv("HOT_CRON_RETENTION_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.RetentionHours = n
		}
	}
	if v := os.Getenv("HOT_CRON_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.BatchSize = n
		}
	}
	if v := os.Getenv("HOT_CRON_MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.MaxRetries = n
		}
	}
	if v := os.Getenv("HOT_CRON_BACKOFF_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.RetryBackoff = time.Duration(n) * time.Second
		}
	}
	return cfg
}

// SetHotCron 设置夜间 cron 调度器引用，供 /cron/stats 端点访问。
// 主程序在启动 goroutine 之后调用。
func (h *Handler) SetHotCron(c *HotCronScheduler) {
	h.hotCron = c
}
