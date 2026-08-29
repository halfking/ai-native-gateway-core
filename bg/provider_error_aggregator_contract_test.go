package bg

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestProviderErrorAggregatorSQLIsTenantScopedAndBucketIdempotent(t *testing.T) {
	data, err := os.ReadFile("provider_error_aggregator.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"set_config('app.current_role', 'super_admin', true)",
		"set_config('app.bypass_rls', 'true', true)",
		"PARTITION BY tenant_id, provider_id",
		"tenant_id, provider_id, model_name",

		"AS aggregation_bucket",
		"occurrences = EXCLUDED.occurrences",
		"aggregation_bucket",
		"COALESCE(tenant_id, '')",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("provider error aggregator missing %q", want)
		}
	}
	if strings.Contains(s, "occurrences = provider_error_details.occurrences + EXCLUDED.occurrences") {
		t.Fatal("aggregator must replace bucket counts, not repeatedly add overlapping windows")
	}
}

func TestProviderErrorAggregatorStopIsSafeBeforeAndAfterStart(t *testing.T) {
	agg := NewProviderErrorAggregator(nil, 0)
	agg.Stop()
	agg.Stop()
	agg.Start(context.Background())
	agg.Stop()
}
