package boardcache

import (
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

func foldUnit() string {
	raw := readSettingString("dashboard.stats.fold_unit", "second")
	if raw == "minute" {
		return "minute"
	}
	return "second"
}

func foldInterval() time.Duration {
	n := readIntSetting("dashboard.stats.fold_interval", 1)
	if n < 1 {
		n = 1
	}
	if foldUnit() == "minute" {
		return time.Duration(n) * time.Minute
	}
	return time.Duration(n) * time.Second
}

func rebuildInterval() time.Duration {
	h := readIntSetting("dashboard.stats.rebuild_interval_hours", 4)
	if h < 1 {
		h = 4
	}
	return time.Duration(h) * time.Hour
}

func rebuildIdleQPS() int64 {
	return readIntSetting("dashboard.stats.rebuild_idle_qps", 10)
}

func rebuildMaxDelay() time.Duration {
	m := readIntSetting("dashboard.stats.rebuild_max_delay_minutes", 120)
	if m < 15 {
		m = 120
	}
	return time.Duration(m) * time.Minute
}

func deltaTTL() time.Duration {
	h := readIntSetting("dashboard.stats.delta_ttl_hours", 6)
	if h < 2 {
		h = 6
	}
	return time.Duration(h) * time.Hour
}

func readIntSetting(key string, def int) int64 {
	raw := readSettingRaw(key)
	if raw == "" {
		return int64(def)
	}
	s := strings.Trim(raw, `"`)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return int64(def)
	}
	return n
}

func readSettingString(key, def string) string {
	raw := readSettingRaw(key)
	if raw == "" {
		return def
	}
	return strings.Trim(raw, `"`)
}

func readSettingRaw(key string) string {
	if settings.Global == nil {
		return ""
	}
	v, _, err := settings.Global.EffectiveValue(settings.ScopePlatform, key, "")
	if err != nil || len(v) == 0 {
		return ""
	}
	return string(v)
}
