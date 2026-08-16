// Package shadow implements the diff protocol used to compare the
// candidate order produced by the URSM v1 (production) plan against the
// order produced by a v2 plan running in shadow or canary mode. The
// diff is purely observational: it never influences the production
// decision.
//
// Note: the value type is named Diff per the plan spec, so the
// package-level constructor is exported as Compute(...) to avoid
// colliding with the type symbol within the same package.
package shadow

// Result captures the inputs that produced a diff. It is preserved on
// the Diff value so callers can attach metadata (request id,
// tenant, canonical model) when the diff is recorded as a metric or log
// line.
type Result struct {
	RequestID     string
	TenantID      string
	Canonical     string
	OldOrderedIDs []string
	NewOrderedIDs []string
}

// Diff is the outcome of comparing two candidate orderings. It
// exposes only the two questions operators care about during a v2
// rollout:
//
//	HasAvailabilityMismatch — do the two orderings see a different
//	  availability set (a candidate appears in one ordering but not
//	  the other)?
//	HasOrderMismatch — same set, but a different priority order?
//
// The flags are mutually exclusive. Ordering is compared only when both
// planners returned the same availability set.
type Diff struct {
	r     Result
	avail bool
	order bool
}

// HasAvailabilityMismatch reports whether the two orderings disagree
// on which candidates are available.
func (d Diff) HasAvailabilityMismatch() bool { return d.avail }

// HasOrderMismatch reports whether the two orderings disagree on
// priority, given the same availability set.
func (d Diff) HasOrderMismatch() bool { return d.order }

func (d Diff) HasTop1Mismatch() bool {
	return len(d.r.OldOrderedIDs) > 0 && len(d.r.NewOrderedIDs) > 0 && d.r.OldOrderedIDs[0] != d.r.NewOrderedIDs[0]
}

// Result returns the captured inputs (request id, tenant, canonical,
// both orderings) for logging / metric labels.
func (d Diff) Result() Result {
	r := d.r
	r.OldOrderedIDs = append([]string(nil), r.OldOrderedIDs...)
	r.NewOrderedIDs = append([]string(nil), r.NewOrderedIDs...)
	return r
}

func (d Diff) Outcome() Outcome {
	if d.avail {
		return OutcomeAvailabilityMismatch
	}
	if d.order {
		return OutcomeOrderMismatch
	}
	return OutcomeIdentical
}

// Compute compares two candidate orderings identified by credential
// id strings. The orderings are expected to be already-stable sequences
// as produced by the respective planners; this function does not
// resort them.
//
// Availability mismatch is reported when the two orderings' underlying
// sets of ids differ. Otherwise, when the sets are equal, an order
// mismatch is reported whenever the two sequences disagree position by
// position.
func Compute(reqID, tenant, canonical string, oldOrder, newOrder []string) Diff {
	d := Diff{r: Result{
		RequestID:     reqID,
		TenantID:      tenant,
		Canonical:     canonical,
		OldOrderedIDs: append([]string(nil), oldOrder...),
		NewOrderedIDs: append([]string(nil), newOrder...),
	}}
	set := func(xs []string) map[string]struct{} {
		m := map[string]struct{}{}
		for _, x := range xs {
			m[x] = struct{}{}
		}
		return m
	}
	a, b := set(oldOrder), set(newOrder)
	if len(a) != len(b) {
		d.avail = true
	} else {
		for k := range a {
			if _, ok := b[k]; !ok {
				d.avail = true
				break
			}
		}
	}
	if !d.avail && (len(oldOrder) != len(newOrder)) {
		d.order = true
	}
	if !d.avail && len(oldOrder) == len(newOrder) {
		for i := range oldOrder {
			if oldOrder[i] != newOrder[i] {
				d.order = true
				break
			}
		}
	}
	return d
}
