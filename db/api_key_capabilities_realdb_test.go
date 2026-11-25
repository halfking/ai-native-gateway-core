package db

import (
	"strings"
	"testing"
)

func TestEnsureAPIKeyCapabilities_RealDB(t *testing.T) {
	env := newGuardEnv(t)
	env.exec(t, `CREATE TABLE public.api_keys (id INT PRIMARY KEY);
		INSERT INTO public.api_keys VALUES (1)`)
	if err := env.d.ensureAPIKeyCapabilities(env.ctx); err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO public.api_keys (id, capabilities) VALUES (2, '["session.audit"]')`)
	env.exec(t, `INSERT INTO public.api_keys (id) VALUES (3)`)
	if err := env.d.ensureAPIKeyCapabilities(env.ctx); err != nil {
		t.Fatal(err)
	}
	var legacy, granted, newDefault, method string
	if err := env.pool.QueryRow(env.ctx, `
		SELECT (SELECT capabilities::text FROM public.api_keys WHERE id = 1),
		       (SELECT capabilities::text FROM public.api_keys WHERE id = 2),
		       (SELECT capabilities::text FROM public.api_keys WHERE id = 3),
		       (SELECT am.amname FROM pg_index i
		        JOIN pg_class c ON c.oid = i.indexrelid
		        JOIN pg_am am ON am.oid = c.relam
		        WHERE i.indexrelid = 'public.idx_api_keys_capabilities'::regclass
		          AND i.indisvalid)`).Scan(&legacy, &granted, &newDefault, &method); err != nil {
		t.Fatal(err)
	}
	if legacy != "[]" || granted != `["session.audit"]` || newDefault != "[]" || method != "gin" {
		t.Fatalf("legacy=%s granted=%s new=%s index=%s", legacy, granted, newDefault, method)
	}
}

func TestEnsureAPIKeyCapabilitiesRejectsIncompatibleObjects_RealDB(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drift string
	}{
		{name: "same_name_btree", drift: `CREATE INDEX idx_api_keys_capabilities ON public.api_keys (id)`},
		{name: "wrong_default", drift: `ALTER TABLE public.api_keys ALTER COLUMN capabilities SET DEFAULT '["session.audit"]'::jsonb`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newGuardEnv(t)
			env.exec(t, `CREATE TABLE public.api_keys (id INT PRIMARY KEY, capabilities JSONB NOT NULL DEFAULT '[]'::jsonb)`)
			env.exec(t, tc.drift)
			err := env.d.ensureAPIKeyCapabilities(env.ctx)
			if err == nil || !strings.Contains(err.Error(), "API key capabilities schema requires") {
				t.Fatalf("expected incompatible schema error, got %v", err)
			}
		})
	}
}
