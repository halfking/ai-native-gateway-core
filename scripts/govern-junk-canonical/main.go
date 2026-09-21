// Command govern-junk-canonical remediates the junk standard-model rows the
// pre-2026-09-10 discovery seeder left in models_canonical: raw names had
// their vendor prefix stripped ("claude/opus-5" → canonical "opus-5",
// "grok/4.6" → "4.6") even though the proper rows ("claude-opus-5",
// "grok-4.6") already existed.
//
// Two phases, mirroring sql/fixes/ house style (dry-run first, explicit
// apply second):
//
//	# phase 1 — diagnosis only, writes nothing
//	go run ./scripts/govern-junk-canonical
//
//	# phase 2 — actually remediate (single transaction)
//	go run ./scripts/govern-junk-canonical -apply
//
// Diagnosis lists every suspect with its references, the aliases landing on
// it, the match evidence and the suggested target standard row (scored with
// modelname.BestStandardModelMatch against the catalog minus the suspects —
// with the junk rows left in, the matcher's Rule 2 prefers the junk base row
// itself). Apply then, per fixable suspect and inside ONE transaction:
//
//  1. redirects provider_models rows (canonical_id and/or standardized_name)
//     to their own best standard row, gated at -min-score (default
//     modelname.AutoLinkThreshold = 0.85); gate failures are skipped and
//     block deprecation so no live reference is stranded;
//  2. re-points routing: upserts model_aliases so the junk name and every
//     alias that landed on the junk row resolve to the target, then
//     deprecates the aliases pointing at the junk row;
//  3. sets the junk canonical row's status to 'deprecated' (never DELETE)
//     once no provider_models row references it anymore.
//
// Every step is a no-op on an already-remediated database, so the tool is
// safe to re-run.
//
// Connection: -dsn flag, else $LLM_GATEWAY_DATABASE_URL, else $DATABASE_URL.
// Unlike cmd/fetch-standard-iq this opens a plain pgx pool WITHOUT running
// schema migrations — a governance tool must not mutate schema as a side
// effect of a diagnosis read.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

func main() {
	dsn := flag.String("dsn", "", "postgres DSN (default: $LLM_GATEWAY_DATABASE_URL, then $DATABASE_URL)")
	apply := flag.Bool("apply", false, "execute the remediation (default: diagnosis only)")
	minScore := flag.Float64("min-score", modelname.AutoLinkThreshold, "minimum BestStandardModelMatch score to accept a target/redirect")
	sources := flag.String("sources", strings.Join(CandidateSources, ","), "models_canonical.source values treated as auto-seeded (junk candidates)")
	asJSON := flag.Bool("json", false, "print the diagnosis as JSON")
	timeout := flag.Duration("timeout", 5*time.Minute, "overall context timeout")
	flag.Parse()

	if *minScore <= 0 || *minScore > 1 {
		fmt.Fprintln(os.Stderr, "-min-score must be in (0,1]")
		os.Exit(2)
	}
	CandidateSources = nil
	for _, s := range strings.Split(*sources, ",") {
		if s = strings.TrimSpace(s); s != "" {
			CandidateSources = append(CandidateSources, s)
		}
	}
	if len(CandidateSources) == 0 {
		fmt.Fprintln(os.Stderr, "-sources must name at least one models_canonical.source value")
		os.Exit(2)
	}
	dbURL := *dsn
	if dbURL == "" {
		dbURL = os.Getenv("LLM_GATEWAY_DATABASE_URL")
	}
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
	}
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "no DSN: pass -dsn or set LLM_GATEWAY_DATABASE_URL/DATABASE_URL")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		fatal("parse DSN: %v", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		fatal("connect: %v", err)
	}
	defer pool.Close()

	diag, err := diagnose(ctx, pool, *minScore)
	if err != nil {
		fatal("diagnose: %v", err)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(diag); err != nil {
			fatal("encode: %v", err)
		}
	} else {
		printReport(diag)
	}

	if !*apply {
		fmt.Printf("\nmode: diagnosis only, nothing written. Re-run with -apply to remediate the %d fixable row(s).\n", countVerdict(diag, VerdictFixable))
		return
	}

	plans := BuildApplyPlan(diag, *minScore)
	if len(plans) == 0 {
		fmt.Println("\nnothing fixable to apply.")
		return
	}
	applied, err := applyPlans(ctx, pool, plans)
	if err != nil {
		fatal("apply: %v", err)
	}

	// Post-apply verification: a second diagnosis pass must find no fixable
	// rows left (rows we deliberately withheld or marked review may remain).
	verify, err := diagnose(ctx, pool, *minScore)
	if err != nil {
		fmt.Fprintf(os.Stderr, "post-apply verification failed to run: %v\n", err)
		os.Exit(1)
	}
	n := countVerdict(verify, VerdictFixable)
	fmt.Printf("\napply finished: %d row(s) remediated. post-apply diagnosis: %d fixable row(s) remaining (review/withheld/whitelisted rows are reported, never auto-fixed).\n",
		applied, n)
	if n > 0 {
		os.Exit(1)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "govern-junk-canonical: "+format+"\n", args...)
	os.Exit(2)
}

func countVerdict(d *Diagnosis, v Verdict) int {
	n := 0
	for _, s := range d.Suspects {
		if s.Verdict == v {
			n++
		}
	}
	return n
}

// diagnose loads the four inputs from the DB and runs the pure classifier.
func diagnose(ctx context.Context, pool *pgxpool.Pool, minScore float64) (*Diagnosis, error) {
	var canonical []CanonicalRow
	rows, err := pool.Query(ctx, `SELECT id, canonical_name, status, COALESCE(source,'') FROM models_canonical WHERE status = 'active'`)
	if err != nil {
		return nil, fmt.Errorf("query models_canonical: %w", err)
	}
	for rows.Next() {
		var c CanonicalRow
		if err := rows.Scan(&c.ID, &c.Name, &c.Status, &c.Source); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan models_canonical: %w", err)
		}
		canonical = append(canonical, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var aliases []AliasRow
	rows, err = pool.Query(ctx, `
		SELECT ma.id, ma.canonical_id, ma.raw_name, ma.status
		FROM model_aliases ma
		JOIN models_canonical mc ON mc.id = ma.canonical_id AND mc.status = 'active'
	`)
	if err != nil {
		return nil, fmt.Errorf("query model_aliases: %w", err)
	}
	for rows.Next() {
		var a AliasRow
		if err := rows.Scan(&a.ID, &a.CanonicalID, &a.RawName, &a.Status); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan model_aliases: %w", err)
		}
		aliases = append(aliases, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Corpus: distinct vendor-prefixed raw names — the evidence pool for the
	// structural shape check and for target scoring.
	var corpus []string
	rows, err = pool.Query(ctx, `
		SELECT DISTINCT raw_model_name FROM provider_models WHERE raw_model_name LIKE '%/%'
	`)
	if err != nil {
		return nil, fmt.Errorf("query provider_models corpus: %w", err)
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan corpus: %w", err)
		}
		corpus = append(corpus, strings.TrimSpace(raw))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(corpus)

	// Structural pass first (pure) so the heavy provider_models reference
	// query only pulls rows that can actually be suspects.
	pre := Diagnose(canonical, nil, nil, corpus, minScore)
	var suspectIDs []int64
	var suspectNames []string
	for _, s := range pre.Suspects {
		suspectIDs = append(suspectIDs, s.Row.ID)
		suspectNames = append(suspectNames, s.Row.Name)
	}

	var refs []ProviderModelRow
	if len(suspectIDs) > 0 || len(suspectNames) > 0 {
		rows, err = pool.Query(ctx, `
			SELECT id, provider_id, raw_model_name, canonical_id, COALESCE(standardized_name,''), available
			FROM provider_models
			WHERE canonical_id = ANY($1::bigint[]) OR lower(standardized_name) = ANY($2::text[])
		`, suspectIDs, suspectNames)
		if err != nil {
			return nil, fmt.Errorf("query provider_models refs: %w", err)
		}
		for rows.Next() {
			var pm ProviderModelRow
			if err := rows.Scan(&pm.ID, &pm.ProviderID, &pm.RawModelName, &pm.CanonicalID, &pm.StandardizedName, &pm.Available); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan provider_models refs: %w", err)
			}
			refs = append(refs, pm)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	return Diagnose(canonical, aliases, refs, corpus, minScore), nil
}

func printReport(d *Diagnosis) {
	counts := map[Verdict]int{}
	for _, s := range d.Suspects {
		counts[s.Verdict]++
	}
	fmt.Printf("=== junk canonical diagnosis ===\n")
	fmt.Printf("active canonical rows: %d | prefixed raw names: %d | min score: %.2f\n",
		d.ActiveCanonicalRows, d.PrefixedRawNames, d.MinScore)
	fmt.Printf("suspects: %d (fixable %d, review %d, withheld %d, whitelisted %d)\n",
		len(d.Suspects), counts[VerdictFixable], counts[VerdictReview], counts[VerdictWithheld], counts[VerdictWhitelisted])

	for _, s := range d.Suspects {
		fmt.Printf("\n--- [%s] %q id=%d source=%q ---\n", s.Verdict, s.Row.Name, s.Row.ID, s.Row.Source)
		fmt.Printf("  provider_models refs: %d\n", len(s.Refs))
		for _, ref := range s.Refs {
			var canonID any
			if ref.CanonicalID != nil {
				canonID = *ref.CanonicalID
			}
			fmt.Printf("    pm=%d provider=%d raw=%q canonical_id=%v standardized_name=%q available=%v\n",
				ref.ID, ref.ProviderID, ref.RawModelName, canonID, ref.StandardizedName, ref.Available)
		}
		active, other := 0, 0
		for _, a := range s.Aliases {
			if a.Status == "active" {
				active++
			} else {
				other++
			}
		}
		fmt.Printf("  aliases pointing here: %d active, %d inactive/deprecated\n", active, other)
		if len(s.Evidence) > 0 {
			fmt.Printf("  evidence (target scored with this row removed from the catalog):\n")
			for _, ev := range s.Evidence {
				fmt.Printf("    raw=%q base=%q → %q (%.3f) — self score %.3f\n",
					ev.RawName, ev.Base, ev.Target.Name, ev.Target.Score, ev.ScoreSelf)
			}
		} else {
			fmt.Printf("  evidence: none\n")
		}
		if s.Target != nil && s.Verdict != VerdictWhitelisted {
			fmt.Printf("  suggested target: %q (score %.3f)\n", s.Target.Name, s.Target.Score)
		} else if s.Target == nil && s.Verdict != VerdictWithheld && s.Verdict != VerdictWhitelisted {
			fmt.Printf("  suggested target: none ≥ %.2f — needs manual review\n", d.MinScore)
		}
		if s.WhitelistReason != "" {
			fmt.Printf("  whitelisted: %s\n", s.WhitelistReason)
		}
	}

	if counts[VerdictReview] > 0 || counts[VerdictWithheld] > 0 {
		fmt.Printf("\nnote: review/withheld rows are listed above but -apply never touches them.\n")
	}
	if counts[VerdictWhitelisted] > 0 {
		fmt.Printf("note: %d whitelisted row(s) are kept by operator decision (see scripts/govern-junk-canonical/plan.go OperatorWhitelist).\n", counts[VerdictWhitelisted])
	}
}

// applyPlans runs every plan inside ONE transaction. Statement order per
// suspect: redirect references → upsert replacement aliases → deprecate the
// aliases landing on the junk row → deprecate the junk row (guarded by a
// zero-references re-check). Each statement is idempotent, so a re-run (or a
// crash between statements of different suspects — one tx means that cannot
// happen) converges to the same state.
func applyPlans(ctx context.Context, pool *pgxpool.Pool, plans []RowPlan) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	applied := 0
	for _, plan := range plans {
		fmt.Printf("\n=== applying %q id=%d → %q id=%d ===\n", plan.Suspect.Row.Name, plan.Suspect.Row.ID, plan.TargetName, plan.TargetID)

		for _, r := range plan.RefRedirects {
			var tag pgconn.CommandTag
			if r.KeepCanonicalID {
				// canonical_id is already correct; only the stale
				// standardized_name references the junk name.
				tag, err = tx.Exec(ctx, `
					UPDATE provider_models
					   SET standardized_name = $1, updated_at = now()
					 WHERE id = $2 AND standardized_name = $3
				`, r.ToName, r.ProviderModelID, plan.Suspect.Row.Name)
			} else {
				tag, err = tx.Exec(ctx, `
					UPDATE provider_models
					   SET canonical_id = $1, standardized_name = $2, updated_at = now()
					 WHERE id = $3
				`, r.ToCanonicalID, r.ToName, r.ProviderModelID)
			}
			if err != nil {
				return applied, fmt.Errorf("redirect provider_model %d: %w", r.ProviderModelID, err)
			}
			fmt.Printf("  redirected pm=%d raw=%q → %q (score %.3f, rows=%d)\n",
				r.ProviderModelID, r.RawModelName, r.ToName, r.Score, tag.RowsAffected())
		}
		for _, skip := range plan.RefSkips {
			fmt.Printf("  SKIPPED     pm=%d raw=%q (%s) — deprecation blocked\n",
				skip.ProviderModelID, skip.RawModelName, skip.Reason)
		}

		for _, a := range plan.AliasUpserts {
			if _, err := tx.Exec(ctx, `
				INSERT INTO model_aliases (canonical_id, raw_name, status)
				VALUES ($1, $2, 'active')
				ON CONFLICT (canonical_id, raw_name) DO UPDATE
					SET status = 'active', updated_at = now()
			`, a.ToCanonicalID, a.RawName); err != nil {
				return applied, fmt.Errorf("upsert alias %q: %w", a.RawName, err)
			}
			fmt.Printf("  alias %q → %q upserted\n", a.RawName, plan.TargetName)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE model_aliases SET status = 'deprecated', updated_at = now()
			 WHERE canonical_id = $1 AND status = 'active'
		`, plan.Suspect.Row.ID)
		if err != nil {
			return applied, fmt.Errorf("deprecate aliases of canonical %d: %w", plan.Suspect.Row.ID, err)
		}
		fmt.Printf("  deprecated %d alias row(s) pointing at the junk row\n", tag.RowsAffected())

		// Deprecate only when nothing references the row anymore — re-check
		// inside the transaction rather than trusting the plan.
		var remaining int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM provider_models WHERE canonical_id = $1`, plan.Suspect.Row.ID,
		).Scan(&remaining); err != nil {
			return applied, fmt.Errorf("count remaining refs for canonical %d: %w", plan.Suspect.Row.ID, err)
		}
		if remaining > 0 || !plan.CanDeprecate {
			fmt.Printf("  deprecation SKIPPED (%d provider_models still reference the row)\n", remaining)
			continue
		}
		tag, err = tx.Exec(ctx, `
			UPDATE models_canonical SET status = 'deprecated', updated_at = now()
			 WHERE id = $1 AND status = 'active'
		`, plan.Suspect.Row.ID)
		if err != nil {
			return applied, fmt.Errorf("deprecate canonical %d: %w", plan.Suspect.Row.ID, err)
		}
		fmt.Printf("  canonical row deprecated (rows=%d)\n", tag.RowsAffected())
		applied++
	}

	if err := tx.Commit(ctx); err != nil {
		return applied, err
	}
	return applied, nil
}
