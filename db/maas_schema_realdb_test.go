package db

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestEnsureMaasSchemaModelRateKey_RealDB(t *testing.T) {
	const baseline = `
		CREATE TABLE public.models_canonical (id INT PRIMARY KEY);
		INSERT INTO public.models_canonical VALUES (1);
		CREATE TABLE public.model_credit_rates (
			canonical_id INT NOT NULL,
			credits_per_1m_in BIGINT,
			credits_per_1m_out BIGINT,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		INSERT INTO public.model_credit_rates (canonical_id, credits_per_1m_in) VALUES (1, 17);
	`
	for _, existingIndex := range []bool{false, true} {
		name := "repair_baseline"
		if existingIndex {
			name = "reuse_existing_key"
		}
		t.Run(name, func(t *testing.T) {
			env := newGuardEnv(t)
			env.exec(t, baseline)
			if existingIndex {
				env.exec(t, `CREATE UNIQUE INDEX existing_model_rate_key ON public.model_credit_rates (canonical_id) INCLUDE (credits_per_1m_in)`)
			}
			for range 2 {
				if err := env.d.EnsureMaasSchema(env.ctx); err != nil {
					t.Fatal(err)
				}
			}
			var rows, credits, indexes int
			if err := env.pool.QueryRow(env.ctx, `
				SELECT count(*), min(credits_per_1m_in), (
					SELECT count(*) FROM pg_index
					WHERE indrelid = 'public.model_credit_rates'::regclass AND indisunique
				) FROM public.model_credit_rates
			`).Scan(&rows, &credits, &indexes); err != nil {
				t.Fatal(err)
			}
			if rows != 1 || credits != 17 || indexes != 1 {
				t.Fatalf("rates rows=%d credits=%d unique indexes=%d", rows, credits, indexes)
			}
			env.exec(t, `
				INSERT INTO public.model_credit_rates (canonical_id, credits_per_1m_in)
				VALUES (1, 23)
				ON CONFLICT (canonical_id) DO UPDATE SET credits_per_1m_in = EXCLUDED.credits_per_1m_in
			`)
			if err := env.pool.QueryRow(env.ctx, `
				SELECT count(*), min(credits_per_1m_in) FROM public.model_credit_rates
			`).Scan(&rows, &credits); err != nil {
				t.Fatal(err)
			}
			if rows != 1 || credits != 23 {
				t.Fatalf("rate upsert rows=%d credits=%d", rows, credits)
			}
		})
	}

	t.Run("preserve_conflicting_prices", func(t *testing.T) {
		env := newGuardEnv(t)
		env.exec(t, baseline)
		env.exec(t, `INSERT INTO public.model_credit_rates (canonical_id, credits_per_1m_in) VALUES (1, 23)`)
		var pgErr *pgconn.PgError
		if err := env.d.EnsureMaasSchema(env.ctx); !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Fatalf("expected a unique violation for conflicting rates, got %v", err)
		}
		var rows, sum int
		if err := env.pool.QueryRow(env.ctx, `
			SELECT count(*), sum(credits_per_1m_in) FROM public.model_credit_rates
		`).Scan(&rows, &sum); err != nil {
			t.Fatal(err)
		}
		if rows != 2 || sum != 40 {
			t.Fatalf("conflicting prices changed: rows=%d credits_sum=%d", rows, sum)
		}
	})
}
