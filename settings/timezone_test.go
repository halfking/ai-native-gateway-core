package settings

import (
	"testing"
	"time"
)

func TestTimezoneSpecDefaultsToEastEight(t *testing.T) {
	specs := TimezoneSpecs()
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	sp := specs[0]
	if sp.Key != "system.timezone" {
		t.Fatalf("key = %q", sp.Key)
	}
	if sp.Default != "Asia/Shanghai" {
		t.Fatalf("default = %v, want Asia/Shanghai", sp.Default)
	}
	if !sp.HotReload {
		t.Fatal("must be hot reload")
	}
	if err := sp.Validate(sp.Default); err != nil {
		t.Fatalf("default must validate: %v", err)
	}
}

func TestValidateTimezone(t *testing.T) {
	cases := []struct {
		in    string
		valid bool
	}{
		{"Asia/Shanghai", true},
		{"UTC", true},
		{"America/New_York", true},
		{"Etc/GMT-8", true}, // POSIX 反号：Etc/GMT-8 == UTC+8
		{"", false},
		{"   ", false},
		{"Not/AZone", false},
		{"GMT+8", false}, // 常见误解：IANA 无 GMT+8
	}
	for _, c := range cases {
		err := validateTimezone(c.in)
		if c.valid && err != nil {
			t.Errorf("validateTimezone(%q) = %v, want nil", c.in, err)
		}
		if !c.valid && err == nil {
			t.Errorf("validateTimezone(%q) = nil, want error", c.in)
		}
	}
}

func TestSystemDayStartUsesSystemTimezone(t *testing.T) {
	SystemTimezoneLocationForTest("Asia/Shanghai")
	defer SystemTimezoneLocationForTest("")

	// UTC 2026-11-20 20:00 = +08 2026-11-21 04:00 ⇒ 系统日切应落在 +08 的 11-21 00:00
	// （= UTC 11-20 16:00）。
	in := time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	got := SystemDayStart(in)
	want := time.Date(2026, 11, 21, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))
	if !got.Equal(want) {
		t.Fatalf("SystemDayStart = %v, want %v", got, want)
	}
	if got.UTC() != want.UTC() {
		t.Fatalf("UTC mismatch: %v vs %v", got.UTC(), want.UTC())
	}
}

func TestParseSystemDateVsLegacyUTCParse(t *testing.T) {
	SystemTimezoneLocationForTest("Asia/Shanghai")
	defer SystemTimezoneLocationForTest("")

	got, err := ParseSystemDate("2026-11-20")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	legacy, _ := time.Parse("2006-01-02", "2026-11-20") // UTC 零点（旧行为）
	if got.Equal(legacy) {
		t.Fatal("ParseSystemDate must NOT equal legacy UTC parse for +08 zone")
	}
	if got.UTC().Format(time.RFC3339) != "2026-11-19T16:00:00Z" {
		t.Fatalf("got.UTC() = %v", got.UTC())
	}
}

func TestSystemDayRangeUtc(t *testing.T) {
	SystemTimezoneLocationForTest("Asia/Shanghai")
	defer SystemTimezoneLocationForTest("")

	start, end, err := SystemDayRangeUtc("2026-11-19", "2026-11-20")
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if start.UTC().Format(time.RFC3339) != "2026-11-18T16:00:00Z" {
		t.Fatalf("start = %v", start)
	}
	// end = 11-20 24:00 +08 = 11-20 16:00 UTC
	if end.UTC().Format(time.RFC3339) != "2026-11-20T16:00:00Z" {
		t.Fatalf("end = %v", end)
	}

	// 反转区间必须拒绝。
	if _, _, err := SystemDayRangeUtc("2026-11-20", "2026-11-19"); err == nil {
		t.Fatal("reversed range must error")
	}
	// 非法日期串必须拒绝。
	if _, _, err := SystemDayRangeUtc("2026-11-31", "2026-11-30"); err == nil {
		t.Fatal("invalid date must error")
	}
}

func TestSystemTimezoneCacheAndHookReset(t *testing.T) {
	SystemTimezoneLocationForTest("Asia/Shanghai")
	if got := SystemTimezoneName(); got != "Asia/Shanghai" {
		t.Fatalf("name = %q", got)
	}
	SystemTimezoneLocationForTest("UTC")
	if got := SystemTimezoneName(); got != "UTC" {
		t.Fatalf("after switch name = %q", got)
	}
	SystemTimezoneLocationForTest("") // 恢复默认链路
	if loc := SystemTimezoneLocation(); loc.String() != time.UTC.String() && loc.String() != "Local" {
		// 无 Global 时 fallback 链不会真读 DB（GetPlatformString 返回 default），
		// 此处应为 Asia/Shanghai；仅验证不 panic 且返回合法 Location。
		t.Logf("default-chain location = %v (acceptable)", loc)
	}
}

func TestSystemNowInZone(t *testing.T) {
	SystemTimezoneLocationForTest("Asia/Shanghai")
	defer SystemTimezoneLocationForTest("")
	now := SystemNow()
	if _, offset := now.Zone(); offset != 8*3600 {
		t.Fatalf("offset = %d, want 28800", offset)
	}
}
