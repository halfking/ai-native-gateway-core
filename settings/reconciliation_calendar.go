package settings

import (
	"fmt"
	"strings"
	"time"
)

const (
	ReconciliationCalendarKey   = "reports.reconciliation.financial_calendar"
	AutomaticReconciliationDays = "automatic"
)

// ReconciliationCalendar pins the time zone of a manually closed reporting
// period. An empty closed-through date leaves every day open.
type ReconciliationCalendar struct {
	Timezone      string
	ClosedThrough string
	location      *time.Location
}

// ParseReconciliationCalendar returns nil for the legacy natural-day policy.
func ParseReconciliationCalendar(value string) (*ReconciliationCalendar, error) {
	if value == AutomaticReconciliationDays {
		return nil, nil
	}
	day, zone, ok := strings.Cut(value, "@")
	if !ok || zone == "" || zone == "Local" {
		return nil, fmt.Errorf("use automatic, open@IANA-timezone, or YYYY-MM-DD@IANA-timezone")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, fmt.Errorf("invalid financial calendar timezone")
	}
	calendar := &ReconciliationCalendar{Timezone: zone, location: loc}
	if day == "open" {
		return calendar, nil
	}
	label, err := time.Parse(time.DateOnly, day)
	if err != nil || label.Format(time.DateOnly) != day || label.Year() < 1 || label.Year() > 9998 {
		return nil, fmt.Errorf("invalid financial closing date")
	}
	local := time.Date(label.Year(), label.Month(), label.Day(), 12, 0, 0, 0, loc)
	if local.Format(time.DateOnly) != day {
		return nil, fmt.Errorf("closing date does not exist in the financial calendar timezone")
	}
	calendar.ClosedThrough = day
	return calendar, nil
}

func ReconciliationCalendarSpec() *Spec {
	return &Spec{
		Key:             ReconciliationCalendarKey,
		Type:            TypeString,
		Scope:           ScopePlatform,
		Category:        CategoryGeneral,
		Default:         AutomaticReconciliationDays,
		Description:     "日对账财务日历（关账截止日@时区，或 open@时区）",
		DescriptionLong: "YYYY-MM-DD@IANA时区 表示关账至该日（含当日），open@IANA时区 表示所有日期开放。automatic 按原自然日结束规则。仅控制 V2 日对账的正式读取，修改留痕。",
		DangerLevel:     Dangerous,
		HotReload:       true,
		validateString: func(value string) error {
			calendar, err := ParseReconciliationCalendar(value)
			if err != nil || calendar == nil || calendar.ClosedThrough == "" {
				return err
			}
			if calendar.ClosedThrough >= time.Now().In(calendar.location).Format(time.DateOnly) {
				return fmt.Errorf("closing date must be a completed local calendar day")
			}
			return nil
		},
	}
}
