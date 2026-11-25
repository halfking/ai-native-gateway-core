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
func ReadFinancialCalendar(ctx context.Context, reader financialCalendarReader) (*settings.ReconciliationCalendar, time.Time, error) {
	var raw *string
	var readAt time.Time
	err := reader.QueryRow(ctx, `SELECT (
		SELECT value::text FROM public.settings_kv WHERE key = $1
	), transaction_timestamp()`, settings.ReconciliationCalendarKey).Scan(&raw, &readAt)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read financial calendar: %w", err)
	}
	value := settings.AutomaticReconciliationDays
	encoded := os.Getenv(settings.EnvNameAuto(settings.ReconciliationCalendarKey))
	if raw != nil {
		encoded = *raw
	}
	if encoded != "" {
		var decoded *string
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
			return nil, time.Time{}, fmt.Errorf("financial calendar must be a string: %w", err)
		}
		if decoded == nil {
			return nil, time.Time{}, fmt.Errorf("financial calendar cannot be null")
		}
		value = *decoded
	}
	calendar, err := settings.ParseReconciliationCalendar(value)
	return calendar, readAt, err
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
