package maas

// 峰谷倍率时段解析（Wave 3 B1，2026-09-22 设计差距审计）。
//
// 单一事实：本文件的解析规则与迁移 736 的 SQL 函数
// maas_resolve_rate_multiplier() 逐条对应（半开区间 [start,end)、
// start>end 视为跨午夜、multiplier<=0 视为 1.0、配置缺失/禁用/异常
// 一律 1.0 fail-open），配置同读 settings_kv 键 maas.rate_periods。
// 两侧规则矩阵由 rate_periods_test.go 精确钉住；SQL 函数的行为对拍
// 在 deploy 冒烟以固定时刻验证。改动规则必须双侧同步。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// RatePeriod is one billable time window with its multiplier.
type RatePeriod struct {
	Name       string  `json:"name"`
	Start      string  `json:"start"`      // "HH:MM" local-to-Timezone
	End        string  `json:"end"`        // "HH:MM"; Start > End ⇒ crosses midnight
	Multiplier float64 `json:"multiplier"` // <=0 treated as 1.0
}

// RatePeriodConfig is the decoded maas.rate_periods value.
type RatePeriodConfig struct {
	Enabled  bool         `json:"enabled"`
	Timezone string       `json:"timezone"`
	Periods  []RatePeriod `json:"periods"`
}

// parseTimeOfDay parses "HH:MM" (seconds tolerated) into minutes since
// midnight.
func parseTimeOfDay(s string) (int, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid time-of-day %q", s)
	}
	var h, m int
	if _, err := fmt.Sscanf(parts[0], "%d", &h); err != nil {
		return 0, fmt.Errorf("invalid hour in %q", s)
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &m); err != nil {
		return 0, fmt.Errorf("invalid minute in %q", s)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("time-of-day out of range %q", s)
	}
	return h*60 + m, nil
}

// ParseRatePeriodConfig decodes the settings value. The admin TypeString
// channel may persist either a JSON object or its stringified form (same
// constraint as the keyword dictionaries) — both accepted. Parse errors
// return the disabled config so callers resolve to 1.0 (fail-open, same
// posture as the SQL resolver).
func ParseRatePeriodConfig(raw []byte) RatePeriodConfig {
	cfg := RatePeriodConfig{}
	if len(raw) == 0 {
		return cfg
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		var str string
		if json.Unmarshal(raw, &str) == nil && len(str) > 0 {
			if err2 := json.Unmarshal([]byte(str), &cfg); err2 == nil {
				return normalizeRatePeriodConfig(cfg)
			}
		}
		slog.Warn("maas.rate_periods unparsable; treating as disabled", "error", err)
		return RatePeriodConfig{}
	}
	return normalizeRatePeriodConfig(cfg)
}

func normalizeRatePeriodConfig(cfg RatePeriodConfig) RatePeriodConfig {
	if cfg.Timezone == "" {
		cfg.Timezone = "Asia/Shanghai"
	}
	return cfg
}

// ResolveRateMultiplier returns the billable multiplier at `at` (local wall
// clock taken in cfg.Timezone, falling back to Asia/Shanghai, then UTC).
// First matching period wins; no match / disabled / broken config → 1.0.
func ResolveRateMultiplier(cfg RatePeriodConfig, at time.Time) float64 {
	if !cfg.Enabled {
		return 1.0
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		loc, err = time.LoadLocation("Asia/Shanghai")
	}
	if err != nil {
		loc = time.UTC
	}
	local := at.In(loc)
	nowMin := local.Hour()*60 + local.Minute()

	for _, p := range cfg.Periods {
		startMin, err := parseTimeOfDay(p.Start)
		if err != nil {
			continue
		}
		endMin, err := parseTimeOfDay(p.End)
		if err != nil {
			continue
		}
		mult := p.Multiplier
		if mult <= 0 {
			mult = 1.0
		}
		if startMin <= endMin {
			// Half-open [start, end).
			if nowMin >= startMin && nowMin < endMin {
				return mult
			}
		} else if nowMin >= startMin || nowMin < endMin {
			// Crosses midnight: [start, 24:00) ∪ [00:00, end).
			return mult
		}
	}
	return 1.0
}

// LoadRatePeriods reads the current config from settings_kv. DB errors
// return the disabled config (resolve → 1.0) rather than failing the
// charge — a config outage must not block billing.
func LoadRatePeriods(ctx context.Context, pool *pgxpool.Pool) RatePeriodConfig {
	if pool == nil {
		return RatePeriodConfig{}
	}
	if settings.Global != nil {
		raw, _, err := settings.Global.EffectiveValue(settings.ScopePlatform, settings.KeyMaasRatePeriods, "")
		if err == nil && len(raw) > 0 {
			return ParseRatePeriodConfig(raw)
		}
	}
	var raw []byte
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := pool.QueryRow(queryCtx, `SELECT value::text FROM settings_kv WHERE key = $1`, settings.KeyMaasRatePeriods).Scan(&raw); err != nil {
		return RatePeriodConfig{}
	}
	return ParseRatePeriodConfig(raw)
}

// ResolveCurrentMultiplier resolves the multiplier for a request that
// started at startedAt. Exported for the charge call sites: the handler
// resolves once, stamps the telemetry row, and passes the same value into
// ChargeRequestMultimodalWithMultiplier so the charge and the audit trail
// cannot disagree.
func (s *Service) ResolveCurrentMultiplier(ctx context.Context, startedAt time.Time) float64 {
	return ResolveRateMultiplier(LoadRatePeriods(ctx, s.pool), startedAt)
}
