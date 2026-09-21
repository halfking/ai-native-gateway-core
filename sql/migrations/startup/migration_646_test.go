package startup

import (
	"strings"
	"testing"
)

func TestMigration646ProxyManagementCanonicalContract(t *testing.T) {
	up := string(migrationFile(t, "646_proxy_management_canonical.sql"))

	for _, required := range []string{
		"BEGIN;",
		"CREATE TABLE IF NOT EXISTS public.proxy_subscriptions",
		"CREATE TABLE IF NOT EXISTS public.proxy_nodes",
		"CREATE TABLE IF NOT EXISTS public.provider_domains",
		"ADD COLUMN IF NOT EXISTS egress_profile TEXT NOT NULL DEFAULT 'direct'",
		"ALTER TABLE public.proxy_subscriptions ALTER COLUMN name SET NOT NULL",
		"WHEN subscribe_url IS NULL OR btrim(subscribe_url) = '' THEN 'disabled'",
		"DELETE FROM public.proxy_nodes n",
		"WHEN protocol IS NULL OR btrim(protocol) = ''",
		"ALTER TABLE public.proxy_nodes ALTER COLUMN subscription_id SET NOT NULL",
		"status NOT IN ('active', 'disabled', 'unhealthy')",
		"ALTER TABLE public.proxy_nodes ALTER COLUMN health_check_url SET NOT NULL",
		"DROP CONSTRAINT proxy_nodes_subscription_id_fkey",
		"DROP CONSTRAINT providers_proxy_subscription_id_fkey",
		"ALTER TABLE public.proxy_nodes ALTER COLUMN name SET NOT NULL",
		"UPDATE public.provider_domains",
		"left(d.domain, 255 - length('#legacy-' || d.id::text))",
		"ALTER TABLE public.provider_domains ALTER COLUMN domain SET NOT NULL",
		"UPDATE public.providers SET egress_profile = 'direct'",
		"ALTER TABLE public.providers ALTER COLUMN egress_profile SET NOT NULL",
		"provider_domains_domain_key UNIQUE (domain)",
		"legacy-domain-",

		"ON DELETE CASCADE",
		"FOREIGN KEY (proxy_subscription_id)",
		"ON DELETE SET NULL",
		"INSERT INTO public.schema_migrations (version, description)",
		"'646'",
		"ON CONFLICT (version) DO NOTHING",
		"COMMIT;",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("migration 646 up missing %q", required)
		}
	}

	if strings.Contains(up, "INSERT INTO provider_domains") || strings.Contains(up, "INSERT INTO public.provider_domains") {
		t.Error("migration 646 must not seed environment-specific provider-domain policy")
	}
}

func TestMigration646RollbackIsNonDestructive(t *testing.T) {
	down := string(migrationFile(t, "646_proxy_management_canonical.down.sql"))

	if !strings.Contains(down, "DELETE FROM public.schema_migrations WHERE version = '646'") {
		t.Error("migration 646 down must remove its ledger entry")
	}
	for _, forbidden := range []string{
		"DROP TABLE",
		"DROP COLUMN",
		"DROP INDEX",
		"DROP CONSTRAINT",
		"TRUNCATE",
	} {
		if strings.Contains(strings.ToUpper(down), forbidden) {
			t.Errorf("migration 646 down must remain non-destructive; found %q", forbidden)
		}
	}
}
