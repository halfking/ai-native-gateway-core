package store

import "fmt"

// KeySchemaMode selects which key grammar(s) the URSM v2 Redis state uses
// (doc 14 §3). It is fixed at process start, is independent of
// URSM_V2_MODE (which keeps defining the legacy/URSM routing authority),
// and its zero value is legacy so an unset configuration never silently
// changes key bytes. Any mode transition must close the ready gate first.
type KeySchemaMode int

const (
	// KeySchemaModeLegacy reads and writes only the frozen legacy grammar.
	// It is the rollback target; no canonical fallback is allowed in its
	// name.
	KeySchemaModeLegacy KeySchemaMode = iota
	// KeySchemaModeDual writes both grammars atomically as one key set and
	// reads canonical first with exact-tuple legacy fallback for ledger
	// migratable keys only.
	KeySchemaModeDual
	// KeySchemaModeCanonical is the only authoritative read source; legacy
	// stays as a read/dual-write shadow until the observation window and
	// rollback checkpoint close.
	KeySchemaModeCanonical
)

// ParseKeySchemaMode decodes the frozen mode names. Unknown values are an
// error rather than a default so a typo cannot flip the schema silently.
func ParseKeySchemaMode(s string) (KeySchemaMode, error) {
	switch s {
	case "legacy":
		return KeySchemaModeLegacy, nil
	case "dual":
		return KeySchemaModeDual, nil
	case "canonical":
		return KeySchemaModeCanonical, nil
	}
	return KeySchemaModeLegacy, fmt.Errorf("ursm.v2.store: unknown key schema mode %q (frozen set: legacy/dual/canonical)", s)
}

func (m KeySchemaMode) String() string {
	switch m {
	case KeySchemaModeDual:
		return "dual"
	case KeySchemaModeCanonical:
		return "canonical"
	default:
		return "legacy"
	}
}
