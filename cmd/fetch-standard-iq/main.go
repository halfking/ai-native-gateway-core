// Command fetch-standard-iq populates models_canonical.standard_iq from the
// embedded reference table, with an optional live refresh from the Artificial
// Analysis Data API when AA_API_KEY is set.
//
// Usage:
//
//	# from embedded reference (no network)
//	go run ./cmd/fetch-standard-iq
//
//	# live refresh from Artificial Analysis (needs a key)
//	AA_API_KEY=xxxx go run ./cmd/fetch-standard-iq -live
//
//	# dry-run (report only, no DB writes)
//	go run ./cmd/fetch-standard-iq -dry-run
//
// The DB connection comes from LLM_GATEWAY_DATABASE_URL / DATABASE_URL via
// config.Load + db.Open (which also runs pending migrations on boot).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/modeliqdata"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "report matches only, do not write to the DB")
	live := flag.Bool("live", false, "refresh from the Artificial Analysis Data API using $AA_API_KEY (overrides the embedded reference)")
	overwrite := flag.Bool("overwrite", false, "overwrite models that already have a standard_iq; default only fills NULL/missing")
	flag.Parse()

	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dbConn, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil || dbConn == nil || !dbConn.Enabled() {
		fmt.Fprintf(os.Stderr, "db connect failed: %v\n", err)
		os.Exit(2)
	}
	defer dbConn.Close()
	pool := dbConn.Pool()

	// 1. Load embedded reference.
	ref := modeliqdata.ReferenceEntries()
	sourceLabel, snapshot := modeliqdata.ReferenceVersion()
	fmt.Fprintf(os.Stderr, "reference: %s (%s), %d entries\n", sourceLabel, snapshot, len(ref))

	// 2. Optionally pull live values from AA and build an override map keyed by
	// the same lookup the package uses.
	liveCount := 0
	if *live {
		apiKey := os.Getenv("AA_API_KEY")
		models, err := modeliqdata.FetchAAIntelligenceIndex(ctx, apiKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "live refresh skipped: %v (falling back to embedded reference)\n", err)
		} else {
			// Rebuild ref from live payload so LookupStandardIQ alignment still
			// applies — we just repopulate the slice.
			ref = ref[:0]
			for _, m := range models {
				if m.IntelligenceIndex == 0 && m.Score != 0 {
					m.IntelligenceIndex = m.Score
				}
				if m.IntelligenceIndex == 0 {
					continue
				}
				ref = append(ref, modeliqdata.ReferenceEntry{
					CanonicalName: m.Slug,
					StandardIQ:    m.IntelligenceIndex,
					Family:        m.Provider,
				})
				liveCount++
			}
			sourceLabel = modeliqdata.SourceArtificialAnalysis + "-live"
			fmt.Fprintf(os.Stderr, "live refresh: %d models from AA\n", liveCount)
		}
	}

	// 3. Read canonical models from DB.
	rows, err := pool.Query(ctx, `
		SELECT id, canonical_name, COALESCE(standard_iq, -1)
		FROM models_canonical
		WHERE status IN ('active','disabled','deprecated')
	`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query models_canonical: %v\n", err)
		os.Exit(2)
	}
	type canon struct {
		id            int64
		name          string
		existingValue float64 // -1 = NULL
	}
	var all []canon
	for rows.Next() {
		var c canon
		if err := rows.Scan(&c.id, &c.name, &c.existingValue); err != nil {
			rows.Close()
			fmt.Fprintf(os.Stderr, "scan: %v\n", err)
			os.Exit(2)
		}
		all = append(all, c)
	}
	rows.Close()

	// 4. Match + (optionally) write.
	var matched, written, skippedExisting, unmatched int
	var unmatchedSamples []string
	for _, c := range all {
		// The reference is keyed by canonical_name; try the reference lookup
		// (which itself normalizes) for robustness against raw names.
		iq, found, key := matchReference(c.name, ref)
		if !found {
			unmatched++
			if len(unmatchedSamples) < 20 {
				unmatchedSamples = append(unmatchedSamples, c.name)
			}
			continue
		}
		matched++
		if !*overwrite && c.existingValue >= 0 {
			skippedExisting++
			continue
		}
		if *dryRun {
			fmt.Fprintf(os.Stderr, "[dry-run] %-32s id=%-6d iq=%.1f (matched %q)\n", c.name, c.id, iq, key)
			continue
		}
		_, err := pool.Exec(ctx, `
			UPDATE models_canonical
			   SET standard_iq = $1,
			       standard_iq_source = $2,
			       standard_iq_updated_at = now()
			 WHERE id = $3
		`, iq, sourceLabel, c.id)
		if err != nil {
			slog.Warn("update standard_iq failed", "model", c.name, "id", c.id, "error", err)
			continue
		}
		written++
	}

	fmt.Fprintf(os.Stderr, "\n=== summary ===\n")
	fmt.Fprintf(os.Stderr, "canonical models scanned: %d\n", len(all))
	fmt.Fprintf(os.Stderr, "matched:    %d\n", matched)
	fmt.Fprintf(os.Stderr, "written:    %d\n", written)
	fmt.Fprintf(os.Stderr, "skipped (already set, no -overwrite): %d\n", skippedExisting)
	fmt.Fprintf(os.Stderr, "unmatched:  %d\n", unmatched)
	if len(unmatchedSamples) > 0 {
		fmt.Fprintf(os.Stderr, "unmatched examples (consider adding to modeliqdata/data/standard_iq.json or model_aliases):\n")
		for _, s := range unmatchedSamples {
			fmt.Fprintf(os.Stderr, "  - %s\n", s)
		}
	}
}

// matchReference mirrors modeliqdata.LookupStandardIQ but operates on a
// caller-supplied slice (so the live-refresh path can reuse the alignment
// without mutating package state). It prefers the package lookup (which uses
// the embedded + normalized index) and only falls back to a raw scan of the
// provided slice for live data.
func matchReference(name string, ref []modeliqdata.ReferenceEntry) (float64, bool, string) {
	// Prefer the embedded/normalized lookup first — it handles variant keys.
	if v, ok, key := modeliqdata.LookupStandardIQ(name); ok {
		return v, true, key
	}
	// Fall back to a raw scan of the (possibly live) slice using the same
	// normalization the package applies.
	for _, e := range ref {
		if v, ok, key := modeliqdata.LookupStandardIQ(e.CanonicalName); ok {
			_ = v
			_ = key
		}
		// Direct slug/name compare (lowercased) for live AA data that didn't
		// normalize onto an embedded canonical key.
		if eq(name, e.CanonicalName) {
			return e.StandardIQ, true, e.CanonicalName
		}
	}
	return 0, false, ""
}

func eq(a, b string) bool {
	la := lowerTrim(a)
	lb := lowerTrim(b)
	return la != "" && la == lb
}

func lowerTrim(s string) string {
	var sb []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c == ' ' || c == '\t' {
			continue
		}
		sb = append(sb, c)
	}
	return string(sb)
}
