package settings

// timezone.go — 系统时区设置（2026-11-20 时区设置轮）。
//
// owner 需求：系统设置增加时区设置，所有时间范围的检查/查询/展示统一按
// 系统时区转换与操作，默认东8区；须有公共函数承担时区自动转换，保证
// 显示、查询、数据库三方时区一致。
//
// 设计（与仓内既有口径兼容）：
//   - 数据库 timestamptz 一律 UTC 存储（仓内既有事实，本轮不动）；
//   - 「日切」与「日期串解析」从硬编码 UTC 改为按本设置解析出的 Location
//     执行 ⇒ 用户在日期选择器里表达的「2026-11-20」在系统时区下语义稳定；
//   - 展示转换由前端承担（/api/admin/system-timezone 下发 IANA 名）。
//
// 关键决策：**进程级缓存 + 5 分钟 TTL，而非每次 EffectiveValue**。
// getPlatformString 每次 EffectiveValue 走 DB；时区读取在热路径
// （每个时间范围解析调用一次）+ 每次 LoadLocation 有 tzdata 解析成本，
// 不适合每查询一次 DB 往返。HotReload 语义保留：改设置后最多 TTL 窗口
// （5 分钟）生效，运维语义与 reports.daily_rollup.hour 的 worker 重读同档。
// 测试通道 SystemTimezoneLocationForTest 允许单测注入并立即生效。

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// SystemTimezoneKey 平台设置键；IANA 时区名（如 Asia/Shanghai）。
const SystemTimezoneKey = "system.timezone"

// DefaultSystemTimezone owner 指定默认东8区。
const DefaultSystemTimezone = "Asia/Shanghai"

const systemTimezoneCacheTTL = 5 * time.Minute

var (
	tzMu       sync.RWMutex
	tzCache    *time.Location
	tzCacheAt  time.Time
	tzTestHook func() string // 非 nil 时短路缓存与 DB（仅测试注入）。
)

// TimezoneSpecs returns the platform specs for system timezone.
func TimezoneSpecs() []*Spec {
	return []*Spec{
		{
			Key:         SystemTimezoneKey,
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryGeneral,
			Default:     DefaultSystemTimezone,
			Description: "系统时区（时间范围/日切口径）",
			DescriptionLong: "IANA 时区名。所有管理面时间范围的「日切」（今天/近N天/自定义日期）、" +
				"日期串解析与按日聚合都按此时区执行；数据库时间戳仍以 UTC 存储（timestamptz），" +
				"查询边界在入 SQL 前按时区换算，显示由前端按同一时区渲染。" +
				"默认 Asia/Shanghai（东8区）。改后最多 5 分钟内生效（热加载窗口）。",
			Unit:          "IANA",
			DangerLevel:   Safe,
			HotReload:     true,
			Observability: "/api/admin/system-timezone",
		},
	}
}

// validateTimezone 拒绝空串与不可加载的时区名（写入面闸门）。
func validateTimezone(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("timezone must not be empty")
	}
	if _, err := time.LoadLocation(strings.TrimSpace(name)); err != nil {
		return fmt.Errorf("unknown IANA timezone %q: %w", name, err)
	}
	return nil
}

// ValidateTimezoneExported 是 validateTimezone 的导出形态，供 admin 便捷写
// 端点作为第二道闸门使用（settings 通用通道走 Spec.Validate，string 类型
// 不校验内容——IANA 合法性由本函数把关）。
func ValidateTimezoneExported(name string) error {
	return validateTimezone(name)
}

// SystemTimezoneName returns the configured IANA timezone name (with cache + test hook).
func SystemTimezoneName() string {
	if tzTestHook != nil {
		return tzTestHook()
	}
	tzMu.RLock()
	if tzCache != nil && time.Since(tzCacheAt) < systemTimezoneCacheTTL {
		name := tzCache.String()
		tzMu.RUnlock()
		return name
	}
	tzMu.RUnlock()
	name := GetPlatformString(SystemTimezoneKey, DefaultSystemTimezone)
	if err := validateTimezone(name); err != nil {
		slog.Warn("settings: configured timezone invalid, falling back to default",
			"key", SystemTimezoneKey, "value", name, "error", err)
		name = DefaultSystemTimezone
	}
	loc, err := time.LoadLocation(strings.TrimSpace(name))
	if err != nil {
		loc = time.UTC
	}
	tzMu.Lock()
	tzCache = loc
	tzCacheAt = time.Now()
	tzMu.Unlock()
	return name
}

// SystemTimezoneLocation returns the *time.Location for the configured system
// timezone. Fallback chain: invalid/unknown → DefaultSystemTimezone; default
// unloadable (tzdata 缺失的极端环境) → UTC。
func SystemTimezoneLocation() *time.Location {
	if tzTestHook != nil {
		if loc, err := time.LoadLocation(tzTestHook()); err == nil {
			return loc
		}
		return time.UTC
	}
	tzMu.RLock()
	if tzCache != nil && time.Since(tzCacheAt) < systemTimezoneCacheTTL {
		loc := tzCache
		tzMu.RUnlock()
		return loc
	}
	tzMu.RUnlock()
	name := SystemTimezoneName()
	loc, err := time.LoadLocation(strings.TrimSpace(name))
	if err != nil {
		slog.Warn("settings: LoadLocation failed, falling back to default timezone",
			"name", name, "error", err)
		loc, err = time.LoadLocation(DefaultSystemTimezone)
		if err != nil {
			loc = time.UTC
		}
	}
	tzMu.Lock()
	tzCache = loc
	tzCacheAt = time.Now()
	tzMu.Unlock()
	return loc
}

// SystemNow returns current wall time in the system timezone.
func SystemNow() time.Time {
	return time.Now().In(SystemTimezoneLocation())
}

// SystemDayStart truncates t to midnight in the system timezone.
// 用 time.Date 构造而非 t.In(loc) 后取字段：对 DST 日也正确。
func SystemDayStart(t time.Time) time.Time {
	loc := SystemTimezoneLocation()
	u := t.In(loc)
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, loc)
}

// ParseSystemDate parses a 'YYYY-MM-DD' date string as midnight in the
// system timezone. This is THE single entry for date-picker strings entering
// the backend: the user's picked date means a calendar day in the system
// timezone, not UTC.
func ParseSystemDate(s string) (time.Time, error) {
	d, err := time.ParseInLocation("2006-01-02", s, SystemTimezoneLocation())
	if err != nil {
		return time.Time{}, err
	}
	return d, nil
}

// SystemDayRangeUtc converts a [startDate, endDate] inclusive picker range
// (YYYY-MM-DD strings in the system timezone) to the UTC instant pair used in
// SQL. end is exclusive = endDate+1 day 00:00 system time. Returns the pair
// in UTC so timestamptz comparisons stay index-friendly and consistent with
// the DB's storage convention.
func SystemDayRangeUtc(startStr, endStr string) (startUtc, endUtc time.Time, err error) {
	startLoc, err := ParseSystemDate(startStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start date: %w", err)
	}
	endLoc, err := ParseSystemDate(endStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid end date: %w", err)
	}
	if endLoc.Before(startLoc) {
		return time.Time{}, time.Time{}, fmt.Errorf("end must be on or after start")
	}
	return startLoc.UTC(), endLoc.AddDate(0, 0, 1).UTC(), nil
}

// SystemTimezoneLocationForTest injects a timezone for tests and clears the
// cache so the injection takes effect immediately. 还原请 defer 复位并再次调用
// 本函数传回原值（或空串恢复默认链路）。
func SystemTimezoneLocationForTest(name string) {
	tzMu.Lock()
	defer tzMu.Unlock()
	if name == "" {
		tzTestHook = nil
	} else {
		tzTestHook = func() string { return name }
	}
	tzCache = nil
	tzCacheAt = time.Time{}
}

// resetSystemTimezoneCache clears the cached location (used after settings
// writes in the same process via hot-reload window semantics; exported for
// admin handler use so a successful PUT takes effect promptly).
func ResetSystemTimezoneCacheForAdminWrite() {
	tzMu.Lock()
	tzCache = nil
	tzCacheAt = time.Time{}
	tzMu.Unlock()
}
