package store

import (
	"context"
	"fmt"
)

// HSetFields is a small helper for tests and seed paths to write multiple
// hash fields in a single round trip.
func (s *Store) HSetFields(ctx context.Context, key string, fields map[string]any) error {
	if s == nil || s.rdb == nil {
		return ErrRedisUnavailable
	}
	args := make([]any, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	return s.rdb.HSet(ctx, key, args...).Err()
}

// ApplyDecision runs the CAS reducer Lua. It compares (generation, source_priority)
// of the incoming decision against the current hash and applies it only if the
// incoming tuple is not strictly older. Manual admin hold short-circuits.
//
// Returns a RecordResult whose Status is one of:
//   - "applied"
//   - "ignored_stale"
//   - "ignored_manual_hold"
func (s *Store) ApplyDecision(ctx context.Context, key string, gen int64, pri int, avail bool, streak int, reason string, adminHold bool) (RecordResult, error) {
	if s == nil || s.rdb == nil {
		return RecordResult{}, ErrRedisUnavailable
	}
	res, err := ApplyDecisionScript.Run(ctx, s.rdb,
		[]string{key},
		fmt.Sprintf("%d", gen), fmt.Sprintf("%d", pri), BoolFlag(avail),
		fmt.Sprintf("%d", streak), reason, BoolFlag(adminHold),
	).Slice()
	if err != nil {
		return RecordResult{}, fmt.Errorf("ursm.v2: apply_decision: %w", err)
	}
	if len(res) < 1 {
		return RecordResult{}, fmt.Errorf("ursm.v2: apply_decision: short reply")
	}
	return RecordResult{Status: asString(res[0])}, nil
}
