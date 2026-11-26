package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/settings"
)

var (
	ErrFinancialDayOpen  = errors.New("reconciliation day is open in the financial calendar")
	ErrFinancialTimezone = errors.New("reconciliation requires the configured financial calendar timezone")
)

type FinancialPeriod struct {
	Status        string `json:"status"`
	Timezone      string `json:"timezone"`
	ClosedThrough string `json:"closed_through,omitempty"`
}

type financialCalendarReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// ReadFinancialCalendar uses the reader's snapshot. Report callers pass their
// existing transaction so the financial policy and evidence agree.
//
// ★ 2026-11-26 12h 审计轮（P1 根修）：env 通道（`LLM_GATEWAY_REPORTS_…`）原本
//   绕过注册表的 `Spec.Validate`（那只把关 HTTP 写路径）⇒ 部署面塞一个坏时区
//   （`open@No/Such/Zone`）会让**所有 V2 对账报表 500**，且报错文案是泛化的
//   "invalid financial calendar timezone"——无键名、无来源 ⇒ 排查困难。
//   修法：解析失败一律包装出 **key 与取值来源（settings_kv/env）**，错误落点
//   从「泛化文案」升级为「哪把配置、哪个通道、什么值」。
func ReadFinancialCalendar(ctx context.Context, reader financialCalendarReader) (*settings.ReconciliationCalendar, time.Time, error) {
	var raw *string
	var readAt time.Time
	err := reader.QueryRow(ctx, `SELECT (
		SELECT value::text FROM public.settings_kv WHERE key = $1
	), transaction_timestamp()`, settings.ReconciliationCalendarKey).Scan(&raw, &readAt)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read financial calendar: %w", err)
	}
	source := "default"
	value := settings.AutomaticReconciliationDays
	envName := settings.EnvNameAuto(settings.ReconciliationCalendarKey)
	encoded := os.Getenv(envName)
	if encoded != "" {
		source = "env:" + envName
	}
	if raw != nil {
		encoded = *raw
		source = "settings_kv"
	}
	if encoded != "" {
		var decoded *string
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
			return nil, time.Time{}, fmt.Errorf("financial calendar (key=%s source=%s) must be a JSON string, got %q: %w",
				settings.ReconciliationCalendarKey, source, encoded, err)
		}
		if decoded == nil {
			return nil, time.Time{}, fmt.Errorf("financial calendar (key=%s source=%s) cannot be null",
				settings.ReconciliationCalendarKey, source)
		}
		value = *decoded
	}
	calendar, err := settings.ParseReconciliationCalendar(value)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("financial calendar (key=%s source=%s) rejected value %q: %w",
			settings.ReconciliationCalendarKey, source, value, err)
	}
	return calendar, readAt, nil
}

func financialPeriod(calendar *settings.ReconciliationCalendar, day, zone string, end, readAt time.Time) *FinancialPeriod {
	if calendar == nil {
		return nil
	}
	period := &FinancialPeriod{Status: "open", Timezone: calendar.Timezone, ClosedThrough: calendar.ClosedThrough}
	switch {
	case zone != calendar.Timezone:
		period.Status = "outside_calendar"
	case day <= calendar.ClosedThrough && !end.After(readAt):
		period.Status = "closed"
	}
	return period
}
