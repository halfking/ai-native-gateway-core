// plan.go — pure (DB-free) detection and remediation-planning logic for the
// junk-canonical governance tool.
//
// Background (2026-09-10/11): discovery and the manual provider-refresh
// handler used to seed standard models by stripping the vendor prefix off
// raw names, so "claude/opus-5" seeded a canonical row "opus-5" and
// "grok/4.6" seeded "4.6" even though "claude-opus-5" / "grok-4.6" already
// existed. (The junk rows carry source 'discovery' OR 'provider_refresh' —
// EnsureCanonicalAndAliases tags the manual handler path differently.)
// MatchStandardModels fixed the forward path; this planner classifies the
// rows the old code left behind and builds the idempotent remediation.
//
// Detection is a PAIR TEST, not a shape heuristic. A canonical row c is a
// junk suspect when there is a prefixed raw name r (its stripped base
// equals c, or c is a strict token suffix of the base — the truncation
// shape) and another standard row t with:
//
//	score(r → t | c removed)   ≥  minScore   (the re-joined name lands on t confidently)
//	isStrictTokenSuffix(c, t)              (t strictly CONTAINS c: the prefix carried family info)
//	t.Score > score(r → c)                 (t beats c's own hold on the raw)
//	normalize(t) != normalize(c)           (not just a punctuation twin: 'claude-opus-4-6' vs 'claude-opus-4.6')
//
// Scoring t with c REMOVED from the catalog is what defeats the matcher's
// Rule 2 ("exact slug-prefixed row yields to its base row"): with the junk
// row present, 'claude/opus-5' resolves to junk 'opus-5'; with it removed,
// 'claude-opus-5' scores its honest 1.0. The strict-suffix + beats-self
// conditions are what keep LEGITIMATE rows out: for raw
// 'anthropic/claude-opus-5' the junk row 'opus-5' scores 0.9 by containment,
// but 'claude-opus-5' itself scores 0.99 base-exact and is not a suffix of
// 'opus-5' — so the real row wins its own name back.
package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// CanonicalRow is one models_canonical row.
type CanonicalRow struct {
	ID     int64
	Name   string
	Status string
	Source string
}

// AliasRow is one model_aliases row pointing at a canonical row.
type AliasRow struct {
	ID          int64
	CanonicalID int64
	RawName     string
	Status      string
}

// ProviderModelRow is one provider_models row referencing a suspect
// canonical row via canonical_id or standardized_name.
type ProviderModelRow struct {
	ID               int64
	ProviderID       int64
	RawModelName     string
	CanonicalID      *int64 // nil = not linked yet
	StandardizedName string
	Available        bool
}

// Verdict classifies what the tool will do with a suspect.
type Verdict string

const (
	// VerdictFixable — confident target exists; -apply redirects references,
	// re-points aliases and deprecates the row.
	VerdictFixable Verdict = "fixable"
	// VerdictReview — junk evidence exists but no target survived the gate;
	// never touched by -apply, listed for manual review.
	VerdictReview Verdict = "review"
	// VerdictWithheld — junk evidence exists, but an ACTIVE alias spelling
	// something other than the row's own name points here; manual review
	// first.
	VerdictWithheld Verdict = "withheld-foreign-alias"
	// VerdictWhitelisted — the row matches the junk shape but is on
	// OperatorWhitelist: an operator examined it and decided to KEEP the row
	// (re-pointing it at any standard row would be semantically wrong).
	// Whitelist overrides every other verdict, so -apply never touches the
	// row even if a unique target would clear the gate.
	VerdictWhitelisted Verdict = "whitelisted"
)

// OperatorWhitelist lists canonical names that match the junk detection
// shape but which an operator examined and confirmed to KEEP, with the
// rationale. Whitelisted rows are still reported (verdict "whitelisted",
// with the reason) so future diagnosis runs stay transparent, but they are
// never remediated and never eligible as redirect targets.
//
// Entries must never be removed without re-running the diagnosis and
// re-examining the row.
var OperatorWhitelist = map[string]string{
	"free": "OpenRouter free-pool pseudo-model (raw 'openrouter/free', provider 21): " +
		"it has no single model identity — OpenRouter's per-model free variants " +
		"('z-ai/glm-5.2:free', 'minimax/minimax-m3:free', …) are canonical rows of their own — " +
		"so re-pointing it at any one tied target (0.99 ×5) would be wrong. " +
		"Operator decision 2026-09-12: keep the row; zero requests ever routed here " +
		"(request_logs: 0 rows for canonical_id=2664333 and 0 for provider_id=21, all time).",
}

// CandidateSources lists the models_canonical.source values written by the
// auto-seeding paths that used the prefix-stripping logic. Curated sources
// ('seed', 'db', migration-*) are never candidates.
var CandidateSources = []string{"discovery", "provider_refresh", "auto_discovered"}

// Evidence is one corpus raw name pairing the suspect against the row that
// beats it. Best is scored with the suspect removed from the catalog;
// ScoreSelf is the suspect's own score for the same raw with everyone in.
type Evidence struct {
	RawName   string
	Base      string
	Target    modelname.StandardModelMatch
	ScoreSelf float64
}

// Suspect is one classified canonical row plus everything the report and
// the apply plan need about it.
type Suspect struct {
	Row      CanonicalRow
	Verdict  Verdict
	Refs     []ProviderModelRow
	Aliases  []AliasRow                    // every alias row pointing at Row, any status
	Evidence []Evidence                    // top matches only, best first
	Target   *modelname.StandardModelMatch // aggregated suggestion, nil = none ≥ min score
	// WhitelistReason is set iff Verdict == VerdictWhitelisted: the
	// OperatorWhitelist rationale for keeping the row.
	WhitelistReason string
}

// Diagnosis is the full phase-1 result.
type Diagnosis struct {
	Suspects            []Suspect
	ActiveCanonicalRows int
	PrefixedRawNames    int
	MinScore            float64
	Sources             []string

	// gateCatalog is every active canonical row EXCEPT the suspects: the
	// candidate set BuildApplyPlan re-scores reference raw names against,
	// so a gated redirect can never land back on a suspect row.
	gateCatalogNames []string
	catalogByID      map[string]CanonicalRow
	suspectIDs       map[int64]bool
}

// maxEvidence limits how many evidence entries are kept per suspect for the
// report. The apply plan re-scores each reference raw name independently,
// so capping only trims report noise.
const maxEvidence = 5

// normalizeSelfVariant folds the alias spellings the old discovery pipeline
// generated alongside a junk row (case flips, '_'/'.' vs '-', e.g.
// "opus_5", "4-6") onto one key, so those aliases don't disqualify the row
// from diagnosis. Also used for the punctuation-twin guard.
func normalizeSelfVariant(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	return strings.ReplaceAll(s, ".", "-")
}

// betterMatch mirrors MatchStandardModels' tie-break: higher score wins,
// then the shorter name (base model beats its variants), then alphabetical.
func betterMatch(a, b modelname.StandardModelMatch) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if len(a.Name) != len(b.Name) {
		return len(a.Name) < len(b.Name)
	}
	return a.Name < b.Name
}

func isCandidateSource(source string) bool {
	for _, s := range CandidateSources {
		if s == source {
			return true
		}
	}
	return false
}

// Diagnose classifies active auto-seeded canonical rows and suggests
// remediation targets. prefixedRawNames must be the DISTINCT raw model
// names carrying a vendor prefix (containing '/'); minScore is the
// confidence gate (use modelname.AutoLinkThreshold).
func Diagnose(canonical []CanonicalRow, aliases []AliasRow, providerModels []ProviderModelRow, prefixedRawNames []string, minScore float64) *Diagnosis {
	d := &Diagnosis{
		ActiveCanonicalRows: len(canonical),
		PrefixedRawNames:    len(prefixedRawNames),
		MinScore:            minScore,
		Sources:             CandidateSources,
	}

	aliasByCanonical := map[int64][]AliasRow{}
	for _, a := range aliases {
		aliasByCanonical[a.CanonicalID] = append(aliasByCanonical[a.CanonicalID], a)
	}
	refsByCanonical := map[int64][]ProviderModelRow{}
	refsByStdName := map[string][]ProviderModelRow{}
	for _, pm := range providerModels {
		if pm.CanonicalID != nil {
			refsByCanonical[*pm.CanonicalID] = append(refsByCanonical[*pm.CanonicalID], pm)
		}
		if pm.StandardizedName != "" {
			refsByStdName[strings.ToLower(pm.StandardizedName)] = append(refsByStdName[strings.ToLower(pm.StandardizedName)], pm)
		}
	}

	// Full active catalog (all sources): everything is a legal remediation
	// target and every candidate is scored against it.
	var activeNames []string
	d.catalogByID = map[string]CanonicalRow{}
	for _, c := range canonical {
		if c.Status != "active" {
			continue
		}
		activeNames = append(activeNames, c.Name)
		d.catalogByID[strings.ToLower(c.Name)] = c
	}

	// Corpus cache: raw name, stripped base, tokenized base.
	type rawEntry struct {
		raw, base string
		baseTok   []string
	}
	entries := make([]rawEntry, 0, len(prefixedRawNames))
	for _, raw := range prefixedRawNames {
		_, base := modelname.SplitVendorPrefix(raw)
		entries = append(entries, rawEntry{raw: raw, base: base, baseTok: tokenizeName(base)})
	}

	for _, c := range canonical {
		if c.Status != "active" || !isCandidateSource(c.Source) {
			continue
		}
		cTok := tokenizeName(c.Name)
		if len(cTok) == 0 {
			continue
		}

		var evidence []Evidence
		for _, re := range entries {
			// Truncation shape: the row IS the prefix-stripped base, or a
			// strict token suffix of it.
			if !strings.EqualFold(re.base, c.Name) && !isTokenSuffix(cTok, re.baseTok) {
				continue
			}
			scoreSelf := 0.0
			for _, m := range modelname.MatchStandardModels(re.raw, activeNames) {
				if strings.EqualFold(m.Name, c.Name) {
					scoreSelf = m.Score
					break
				}
				if m.Score < modelname.AutoLinkThreshold {
					break // ranked descending; nothing at/above the gate left
				}
			}
			minusC := make([]string, 0, len(activeNames))
			for _, n := range activeNames {
				if strings.EqualFold(n, c.Name) {
					continue
				}
				// Whitelisted rows are never remediation targets: excluding
				// them from the rival catalog means a suspect whose ONLY
				// confident landing is a whitelisted row produces no
				// evidence (nothing to suggest), instead of a plan that
				// would redirect into a row the operator decided to keep.
				if _, whitelisted := OperatorWhitelist[strings.ToLower(n)]; whitelisted {
					continue
				}
				minusC = append(minusC, n)
			}
			for _, m := range modelname.MatchStandardModels(re.raw, minusC) {
				if m.Score < minScore {
					break
				}
				if m.Score <= scoreSelf ||
					normalizeSelfVariant(m.Name) == normalizeSelfVariant(c.Name) ||
					!modelname.IsStrictTokenSuffix(c.Name, m.Name) {
					continue
				}
				evidence = append(evidence, Evidence{RawName: re.raw, Base: re.base, Target: m, ScoreSelf: scoreSelf})
			}
		}
		if len(evidence) == 0 {
			continue
		}

		s := Suspect{Row: c, Aliases: aliasByCanonical[c.ID]}
		seenRef := map[int64]bool{}
		for _, pm := range refsByCanonical[c.ID] {
			s.Refs = append(s.Refs, pm)
			seenRef[pm.ID] = true
		}
		for _, pm := range refsByStdName[strings.ToLower(c.Name)] {
			if !seenRef[pm.ID] {
				s.Refs = append(s.Refs, pm)
			}
		}

		sort.SliceStable(evidence, func(i, j int) bool {
			return betterMatch(evidence[i].Target, evidence[j].Target)
		})
		// Consensus guard: when the top score is shared by DIFFERENT target
		// rows the raw corpus disagrees about where the name belongs
		// (e.g. junk "free" tying at 0.99 with "glm-5.2:free",
		// "inkling:free", …) — no confident single target, so the row is
		// reported as review instead of being auto-remediated.
		best := evidence[0].Target
		tiedNames := map[string]bool{best.Name: true}
		for _, ev := range evidence {
			if best.Score-ev.Target.Score < 1e-9 {
				tiedNames[ev.Target.Name] = true
			}
		}
		if len(evidence) > maxEvidence {
			evidence = evidence[:maxEvidence]
		}
		s.Evidence = evidence
		s.Target = &best

		s.Verdict = VerdictFixable
		switch {
		case len(tiedNames) > 1:
			s.Verdict = VerdictReview
		default:
			for _, a := range s.Aliases {
				if a.Status == "active" && normalizeSelfVariant(a.RawName) != normalizeSelfVariant(c.Name) {
					s.Verdict = VerdictWithheld
					break
				}
			}
		}
		// Operator whitelist has the final say: a row an operator confirmed
		// to keep is reported as whitelisted (with the reason) no matter
		// what the evidence would otherwise suggest.
		if reason, ok := OperatorWhitelist[strings.ToLower(c.Name)]; ok {
			s.Verdict = VerdictWhitelisted
			s.WhitelistReason = reason
		}
		d.Suspects = append(d.Suspects, s)
	}

	// Gate catalog for the apply-time per-reference scoring: everything
	// active except the suspects, so a redirect can never land on a row the
	// tool is about to deprecate — and never on a whitelisted row either,
	// even when the whitelist kept it out of the suspects.
	d.suspectIDs = map[int64]bool{}
	for _, s := range d.Suspects {
		d.suspectIDs[s.Row.ID] = true
	}
	for _, c := range canonical {
		if c.Status != "active" || d.suspectIDs[c.ID] {
			continue
		}
		if _, whitelisted := OperatorWhitelist[strings.ToLower(c.Name)]; whitelisted {
			continue
		}
		d.gateCatalogNames = append(d.gateCatalogNames, c.Name)
	}

	sort.Slice(d.Suspects, func(i, j int) bool {
		if d.Suspects[i].Verdict != d.Suspects[j].Verdict {
			order := map[Verdict]int{VerdictFixable: 0, VerdictReview: 1, VerdictWithheld: 2, VerdictWhitelisted: 3}
			return order[d.Suspects[i].Verdict] < order[d.Suspects[j].Verdict]
		}
		return d.Suspects[i].Row.Name < d.Suspects[j].Row.Name
	})
	return d
}

// tokenizeName mirrors modelname's tokenize: lowercase split on anything
// that is not a-z / 0-9.
func tokenizeName(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
}

// isTokenSuffix reports whether tail is a contiguous token suffix of full
// and strictly shorter ("opus-5" ⊂ "claude-opus-5"). Operates on
// pre-tokenized input; semantics match modelname.IsStrictTokenSuffix.
func isTokenSuffix(tail, full []string) bool {
	if len(tail) == 0 || len(tail) >= len(full) {
		return false
	}
	offset := len(full) - len(tail)
	for i := range tail {
		if tail[i] != full[offset+i] {
			return false
		}
	}
	return true
}

// ---- apply planning ----

// RefRedirect moves one provider_models row off a junk canonical row.
type RefRedirect struct {
	ProviderModelID int64
	ProviderID      int64
	RawModelName    string
	FromCanonicalID int64 // suspect row the reference currently points at
	ToCanonicalID   int64
	ToName          string
	Score           float64
	// KeepCanonicalID marks a row whose canonical_id is already correct and
	// whose stale standardized_name is the only junk reference — only the
	// name column is rewritten.
	KeepCanonicalID bool
}

// RefSkip is a reference the gate refused to move; its existence blocks
// deprecation of the suspect row.
type RefSkip struct {
	ProviderModelID int64
	RawModelName    string
	Reason          string
}

// AliasUpsert re-points one client-facing name at the target: the junk
// name itself plus every active alias raw_name that currently lands on the
// junk row.
type AliasUpsert struct {
	RawName       string
	ToCanonicalID int64
}

// RowPlan is the apply-time work item for one fixable suspect.
type RowPlan struct {
	Suspect       Suspect
	TargetID      int64
	TargetName    string
	RefRedirects  []RefRedirect
	RefSkips      []RefSkip
	AliasUpserts  []AliasUpsert
	CanDeprecate  bool
	BlockedReason string
}

// BuildApplyPlan turns every fixable suspect into a concrete, idempotent
// work item. References whose raw name does not confidently match the gate
// catalog (score ≥ minScore) are skipped and block deprecation, so -apply
// can never strand a live provider model on a deprecated row.
func BuildApplyPlan(d *Diagnosis, minScore float64) []RowPlan {
	if d == nil {
		return nil
	}
	var plans []RowPlan
	for _, s := range d.Suspects {
		if s.Verdict != VerdictFixable || s.Target == nil {
			continue
		}
		target, ok := d.catalogByID[strings.ToLower(s.Target.Name)]
		if !ok {
			continue
		}
		plan := RowPlan{
			Suspect:      s,
			TargetID:     target.ID,
			TargetName:   target.Name,
			CanDeprecate: true,
		}

		for _, ref := range s.Refs {
			best := modelname.BestStandardModelMatch(ref.RawModelName, d.gateCatalogNames)
			if best == nil || best.Score < minScore || strings.EqualFold(best.Name, s.Row.Name) {
				plan.RefSkips = append(plan.RefSkips, RefSkip{
					ProviderModelID: ref.ID,
					RawModelName:    ref.RawModelName,
					Reason:          noConfidentTargetReason(best, minScore),
				})
				continue
			}
			redirectTarget, ok := d.catalogByID[strings.ToLower(best.Name)]
			if !ok {
				plan.RefSkips = append(plan.RefSkips, RefSkip{
					ProviderModelID: ref.ID,
					RawModelName:    ref.RawModelName,
					Reason:          "matched name missing from catalog",
				})
				continue
			}
			plan.RefRedirects = append(plan.RefRedirects, RefRedirect{
				ProviderModelID: ref.ID,
				ProviderID:      ref.ProviderID,
				RawModelName:    ref.RawModelName,
				FromCanonicalID: s.Row.ID,
				ToCanonicalID:   redirectTarget.ID,
				ToName:          redirectTarget.Name,
				Score:           best.Score,
				KeepCanonicalID: ref.CanonicalID != nil && *ref.CanonicalID != s.Row.ID,
			})
		}

		plan.AliasUpserts = append(plan.AliasUpserts, AliasUpsert{RawName: s.Row.Name, ToCanonicalID: target.ID})
		// Dedupe by the exact raw name — the DB upsert key is
		// (canonical_id, raw_name). Spelling variants ('opus_5' vs
		// 'opus-5') are DISTINCT rows here and each needs its own upsert,
		// otherwise the variant would resolve nowhere once the aliases
		// landing on the junk row are deprecated.
		seenAlias := map[string]bool{s.Row.Name: true}
		for _, a := range s.Aliases {
			if a.Status != "active" || seenAlias[a.RawName] {
				continue
			}
			seenAlias[a.RawName] = true
			plan.AliasUpserts = append(plan.AliasUpserts, AliasUpsert{RawName: a.RawName, ToCanonicalID: target.ID})
		}

		if len(plan.RefSkips) > 0 {
			plan.CanDeprecate = false
			plan.BlockedReason = "references failed the score gate and would be stranded on a deprecated row"
		}
		plans = append(plans, plan)
	}
	return plans
}

func noConfidentTargetReason(best *modelname.StandardModelMatch, minScore float64) string {
	if best == nil {
		return "raw name has no catalog match above the ranking floor"
	}
	return fmt.Sprintf("best match %q scores %.3f < gate %.2f", best.Name, best.Score, minScore)
}
