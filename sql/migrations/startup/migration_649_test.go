package startup

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration649RoutingAnalyticsProbeFilter(t *testing.T) {
	up, err := os.ReadFile("649_routing_analytics_probe_filter.sql")
	require.NoError(t, err)
	sql := string(up)

	require.Contains(t, sql, "DROP VIEW IF EXISTS public.routing_analytics_source")
	require.Contains(t, sql, "CREATE VIEW public.routing_analytics_source AS")
	require.Contains(t, sql, "FROM public.request_logs_hot")
	require.Contains(t, sql, "FROM public.request_logs")
	require.Contains(t, sql, "origin_stage::text AS origin_stage")
	require.NotContains(t, sql, "request_logs_with_current_month_without_customer_id")
	require.Contains(t, sql, "FROM public.routing_analytics_source")
	require.Equal(t, 2, strings.Count(sql, "FROM public.routing_analytics_source"))
	require.Contains(t, sql, "COALESCE(origin_stage, '') NOT IN")
	require.Contains(t, sql, "BEGIN;")
	require.Contains(t, sql, "COMMIT;")
}
