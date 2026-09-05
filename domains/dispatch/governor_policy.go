package dispatch

import "time"

// GovernorPolicy is an immutable, versioned batch of specs produced by
// the management layer (cmd/gateway main path, applying PG→Redis
// notifications, reading candidate snapshots, etc.). Producers MUST NOT
// mutate a GovernorPolicy after Publish; Stage A pins this rule via
// test contracts. Receivers that need to compose a new policy MUST
// allocate a fresh GovernorPolicy value.
//
// Revision is monotonically increasing across the cluster — Stage E will
// publish via a transactionally-bumped DB column plus pg_notify. Until
// then, Stage A producers are responsible for sourcing the next
// revision from whatever local sequence they own.
//
// Source is a free-form provenance tag for audit logs
// ("pg_notify", "admin.update", "ursm-shadow", "admission-test"). It is
// NEVER used as a metric label; the allowlist enforces closed-enum
// label values.
type GovernorPolicy struct {
	Revision    uint64
	GeneratedAt time.Time
	Specs       []GovernorSpec
	Source      string
}
