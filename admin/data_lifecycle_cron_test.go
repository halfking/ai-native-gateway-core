package admin

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHotCronConfig_Defaults(t *testing.T) {
	// defaults() 只对「负数 / 越界」字段应用默认值；零值由 NewHotCronScheduler 显式转换。
	cfg := HotCronConfig{RunAtHour: -1, MaxRetries: -1}
	cfg.defaults()
	if cfg.RunAtHour != 2 {
		t.Errorf("expected RunAtHour=2, got %d", cfg.RunAtHour)
	}
	if cfg.RunAtMinute != 0 {
		t.Errorf("expected RunAtMinute=0, got %d", cfg.RunAtMinute)
	}
	if cfg.RetentionHours != 8 {
		t.Errorf("expected RetentionHours=8, got %d", cfg.RetentionHours)
	}
	if cfg.BatchSize != 500 {
		t.Errorf("expected BatchSize=500, got %d", cfg.BatchSize)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("expected MaxRetries=3, got %d", cfg.MaxRetries)
	}
	if cfg.RetryBackoff != 30*time.Second {
		t.Errorf("expected RetryBackoff=30s, got %v", cfg.RetryBackoff)
	}
}

func TestHotCronConfig_NewHotCronSchedulerAppliesDefaults(t *testing.T) {
	// NewHotCronScheduler 在 cfg={} 时应该应用所有默认值
	s := NewHotCronScheduler(&Handler{hotJobMgr: newHotJobManager()}, HotCronConfig{})
	if s.cfg.RunAtHour != 2 {
		t.Errorf("expected default RunAtHour=2, got %d", s.cfg.RunAtHour)
	}
	if s.cfg.RetentionHours != 8 {
		t.Errorf("expected default RetentionHours=8, got %d", s.cfg.RetentionHours)
	}
	if s.cfg.BatchSize != 500 {
		t.Errorf("expected default BatchSize=500, got %d", s.cfg.BatchSize)
	}
	if s.cfg.MaxRetries != 3 {
		t.Errorf("expected default MaxRetries=3, got %d", s.cfg.MaxRetries)
	}
	if s.cfg.RetryBackoff != 30*time.Second {
		t.Errorf("expected default RetryBackoff=30s, got %v", s.cfg.RetryBackoff)
	}
}

func TestHotCronConfig_DefaultsPreservesExplicit(t *testing.T) {
	cfg := HotCronConfig{
		Enabled:        true,
		RunAtHour:      4,
		RunAtMinute:    30,
		RetentionHours: 48,
		BatchSize:      1000,
		MaxRetries:     5,
		RetryBackoff:   10 * time.Second,
	}
	cfg.defaults()
	if cfg.RunAtHour != 4 {
		t.Errorf("explicit RunAtHour=4 should be preserved, got %d", cfg.RunAtHour)
	}
	if cfg.RetentionHours != 48 {
		t.Errorf("explicit RetentionHours=48 should be preserved, got %d", cfg.RetentionHours)
	}
}

func TestHotCronConfigFromEnv(t *testing.T) {
	t.Setenv("HOT_CRON_DISABLED", "1")
	t.Setenv("HOT_CRON_RUN_AT", "03:30")
	t.Setenv("HOT_CRON_RETENTION_HOURS", "12")
	t.Setenv("HOT_CRON_BATCH_SIZE", "200")
	t.Setenv("HOT_CRON_MAX_RETRIES", "5")
	t.Setenv("HOT_CRON_BACKOFF_SECONDS", "15")

	cfg := HotCronConfigFromEnv()
	if cfg.Enabled {
		t.Errorf("expected Enabled=false when HOT_CRON_DISABLED=1")
	}
	if cfg.RunAtHour != 3 || cfg.RunAtMinute != 30 {
		t.Errorf("expected 03:30, got %02d:%02d", cfg.RunAtHour, cfg.RunAtMinute)
	}
	if cfg.RetentionHours != 12 {
		t.Errorf("expected 12, got %d", cfg.RetentionHours)
	}
	if cfg.BatchSize != 200 {
		t.Errorf("expected 200, got %d", cfg.BatchSize)
	}
	if cfg.MaxRetries != 5 {
		t.Errorf("expected 5, got %d", cfg.MaxRetries)
	}
	if cfg.RetryBackoff != 15*time.Second {
		t.Errorf("expected 15s, got %v", cfg.RetryBackoff)
	}
}

func TestHotCronConfigFromEnv_DefaultsWhenEmpty(t *testing.T) {
	// 确保环境变量未设置时使用默认值
	t.Setenv("HOT_CRON_DISABLED", "")
	t.Setenv("HOT_CRON_RUN_AT", "")
	t.Setenv("HOT_CRON_RETENTION_HOURS", "")
	t.Setenv("HOT_CRON_BATCH_SIZE", "")
	t.Setenv("HOT_CRON_MAX_RETRIES", "")
	t.Setenv("HOT_CRON_BACKOFF_SECONDS", "")

	cfg := HotCronConfigFromEnv()
	if !cfg.Enabled {
		t.Errorf("default Enabled should be true when no env var set")
	}
	if cfg.RunAtHour != 2 {
		t.Errorf("expected default RunAtHour=2, got %d", cfg.RunAtHour)
	}
	if cfg.RetentionHours != 8 {
		t.Errorf("expected default RetentionHours=8, got %d", cfg.RetentionHours)
	}
}

func TestHotCronStats(t *testing.T) {
	s := NewHotCronScheduler(&Handler{hotJobMgr: newHotJobManager()}, HotCronConfig{
		Enabled:        true,
		RunAtHour:      2,
		RunAtMinute:    0,
		RetentionHours: 24,
		BatchSize:      500,
		MaxRetries:     3,
		RetryBackoff:   30 * time.Second,
	})
	stats := s.Stats()
	if !stats.Enabled {
		t.Errorf("expected Enabled=true")
	}
	if stats.RunAt != "02:00" {
		t.Errorf("expected RunAt=02:00, got %s", stats.RunAt)
	}
	if stats.RetentionHrs != 24 {
		t.Errorf("explicit RetentionHrs=24 should be preserved, got %d", stats.RetentionHrs)
	}
	if stats.RunningNow {
		t.Errorf("expected RunningNow=false initially")
	}
	if stats.RunCount != 0 {
		t.Errorf("expected RunCount=0, got %d", stats.RunCount)
	}
}

func TestMaybeRunAt(t *testing.T) {
	s := &HotCronScheduler{
		cfg: HotCronConfig{
			RunAtHour:   2,
			RunAtMinute: 0,
		},
	}
	// 不在 cron 时间 → 不应触发
	notRunTime := time.Date(2026, 7, 13, 3, 0, 0, 0, time.UTC)
	s.maybeRunAt(context.Background(), notRunTime)
	if s.runCount != 0 {
		t.Errorf("expected runCount=0, got %d", s.runCount)
	}
}

func TestMaybeRunAt_MatchTime(t *testing.T) {
	// ★ 本用例在 2026-10-21（§19.20.24）重写过，原因三条：
	// ① 旧版的注释前提是**假的**（写着「用 mock handler 让 runOnce 不会因为
	//    db=nil 而 panic」，而下一行注释又承认「runOnce 内部会 panic」）⇒
	//    它其实**靠 panic 在跑**；
	// ② 旧版调的是 maybeRun，而用例名叫 TestMaybeRunAt_MatchTime ⇒
	//    「命中时间 ⇒ maybeRunAt 会触发」这条**根本没被测**；
	// ③ 靠 panic 在跑 ⇒ 每跑一次 admin 包就往证据通道里塞一条
	//    ERROR data-lifecycle: hot cron panic（假故障噪声）。
	//
	// ⇒ 现在注入假 DB（窄接口 hotCronDB）⇒ 全程无 panic、无真库、无噪声。
	logs := captureHotCronLogs(t)
	db := &fakeHotCronDB{probePresent: false}
	s := &HotCronScheduler{
		h:   &Handler{hotJobMgr: newHotJobManager()},
		db:  db,
		cfg: HotCronConfig{RunAtHour: 2, RunAtMinute: 0, RetentionHours: 24, BatchSize: 100},
	}

	// ★ 真正走 maybeRunAt 且命中时间（2026-07-13 02:00 UTC）。
	s.maybeRunAt(context.Background(), time.Date(2026, 7, 13, 2, 0, 0, 0, time.UTC))

	if s.runCount != 1 {
		t.Errorf("expected runCount=1, got %d", s.runCount)
	}
	// maybeRun 是**异步**的（detached goroutine）⇒ 等 runOnce 跑完再读日志。
	hotCronWaitFor(t, "hot cron run 结束", func() bool {
		return s.Stats().LastRunAt != nil
	})
	// ★ 关键不变量：命中时间这条路径**一条 ERROR 都不该有**。
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("hot cron 正常路径不该产生 ERROR；日志：\n%s", logs.String())
	}
}
