package db

import (
	"os"
	"strings"
	"testing"
)

func TestApplyMigrationsIncludesMigration646Ensure(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		"ensureProxyManagementCanonicalSchema(migCtx)",
		"func (d *DB) ensureProxyManagementCanonicalSchema",
		"CREATE TABLE IF NOT EXISTS public.proxy_subscriptions",
		"CREATE TABLE IF NOT EXISTS public.proxy_nodes",
		"CREATE TABLE IF NOT EXISTS public.provider_domains",
		"WHEN subscribe_url IS NULL OR btrim(subscribe_url) = '' THEN 'disabled'",
		"DELETE FROM public.proxy_nodes n WHERE n.subscription_id IS NULL",
		"ALTER TABLE public.proxy_nodes ALTER COLUMN subscription_id SET NOT NULL",
		"status NOT IN ('active', 'disabled', 'unhealthy')",
		"ALTER TABLE public.proxy_nodes ALTER COLUMN health_check_url SET NOT NULL",
		"DROP CONSTRAINT proxy_nodes_subscription_id_fkey",
		"DROP CONSTRAINT providers_proxy_subscription_id_fkey",
		"left(d.domain, 255 - length('#legacy-' || d.id::text))",
		"UPDATE public.providers SET egress_profile = 'direct'",
		"INSERT INTO public.schema_migrations (version, description)",
		"VALUES ('646', 'canonical proxy management schema')",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("db.go missing migration 646 contract %q", want)
		}
	}
}
