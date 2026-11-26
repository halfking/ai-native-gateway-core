package settings

import (
	"testing"
	"time"
)

func TestReconciliationCalendarSetting(t *testing.T) {
	var spec *Spec
	for _, candidate := range PlatformSpecs() {
		if candidate.Key == ReconciliationCalendarKey {
			spec = candidate
			break
		}
	}
	if spec == nil || spec.DangerLevel != Dangerous || !spec.HotReload {
		t.Fatalf("financial calendar must be an audited platform setting: %+v", spec)
	}
	today := time.Now().UTC().Format(time.DateOnly)
	for _, value := range []string{"automatic", "open@UTC", "2020-01-01@Asia/Shanghai"} {
		if err := spec.Validate(value); err != nil {
			t.Errorf("valid calendar %q: %v", value, err)
		}
	}
	for _, value := range []any{"", "today", "open@Local", "open@No/Such/Timezone", "2020-02-30@UTC",
		"2011-12-30@Pacific/Apia", today + "@UTC", 20201123} {
		if err := spec.Validate(value); err == nil {
			t.Errorf("invalid calendar accepted: %v", value)
		}
	}
}
