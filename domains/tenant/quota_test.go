package tenant

import (
	"context"
	"errors"
	"testing"
	"time"
)

type statsSource struct {
	stats HistoricalStats
	err   error
	since time.Time
}

func (s *statsSource) Stats(_ context.Context, _ string, since time.Time) (HistoricalStats, error) {
	s.since = since
	return s.stats, s.err
}

func TestQuotaCheckerLimitsQPSAndTokens(t *testing.T) {
	c := NewQuotaChecker()
	c.SetQuota(&TenantQuota{TenantID: "t", MaxQPS: 2, MaxTokensPerMin: 10, BurstMultiplier: 1})
	if err := c.CheckQuota(context.Background(), "t", 4); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckQuota(context.Background(), "t", 4); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckQuota(context.Background(), "t", 1); err == nil {
		t.Fatal("expected qps limit")
	}
}

func TestQuotaCheckerAcquireRelease(t *testing.T) {
	c := NewQuotaChecker()
	c.SetQuota(&TenantQuota{TenantID: "t", MaxConcurrent: 1})
	release, err := c.Acquire(context.Background(), "t", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Acquire(context.Background(), "t", 0); err == nil {
		t.Fatal("expected concurrent limit")
	}
	release()
	if _, err = c.Acquire(context.Background(), "t", 0); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaCheckerAcquireDoesNotConsumeWhenConcurrentIsFull(t *testing.T) {
	c := NewQuotaChecker()
	c.SetQuota(&TenantQuota{TenantID: "t", MaxConcurrent: 1, MaxQPS: 2, MaxTokensPerMin: 10})
	release, err := c.Acquire(context.Background(), "t", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := c.Acquire(context.Background(), "t", 4); err == nil {
		t.Fatal("expected concurrent limit")
	}
	release()
	if _, err := c.Acquire(context.Background(), "t", 6); err != nil {
		t.Fatalf("concurrency rejection consumed quota: %v", err)
	}
}

func TestQuotaCheckerRejectsNegativeTokens(t *testing.T) {
	c := NewQuotaChecker()
	c.SetQuota(&TenantQuota{TenantID: "t", MaxTokensPerMin: 10})
	if err := c.CheckQuota(context.Background(), "t", -1); err == nil {
		t.Fatal("expected negative token count to be rejected")
	}
}
func TestQuotaCheckerAdjustsFromSevenDayStats(t *testing.T) {
	s := &statsSource{stats: HistoricalStats{ConcurrentP95: 4, QPSP95: 10}}
	c := NewQuotaChecker()
	c.SetQuota(&TenantQuota{TenantID: "t", MaxConcurrent: 1, MaxQPS: 1})
	c.SetHistoricalStatsSource(s)
	if err := c.AdjustQuota(context.Background(), "t"); err != nil {
		t.Fatal(err)
	}
	q, _ := c.GetQuota("t")
	if q.MaxConcurrent != 6 || q.MaxQPS != 15 {
		t.Fatalf("quota=%+v", q)
	}
	if time.Since(s.since) < 6*24*time.Hour {
		t.Fatal("stats window was not seven days")
	}
}

func TestQuotaCheckerStatsError(t *testing.T) {
	c := NewQuotaChecker()
	c.SetQuota(&TenantQuota{TenantID: "t"})
	c.SetHistoricalStatsSource(&statsSource{err: errors.New("db down")})
	if err := c.AdjustQuota(context.Background(), "t"); err == nil {
		t.Fatal("expected stats error")
	}
}
