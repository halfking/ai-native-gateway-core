package store

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed record_request.lua
var recordRequestSrc string

var RecordRequestScript = redis.NewScript(recordRequestSrc)

//go:embed record_request_dual.lua
var recordRequestDualSrc string

// RecordRequestDualScript maintains the legacy and canonical key sets as
// one atomic unit (doc 14 §5.2). See record_request_dual.lua for the
// KEYS/ARGV layout.
var RecordRequestDualScript = redis.NewScript(recordRequestDualSrc)

type RecordOutcome struct {
	Success      bool
	ErrorKind    string
	NowMs        int64
	LatencyMs    int
	RequestID    string
	DedupKey     string
	NodeTTL      time.Duration
	Window5mTTL  time.Duration
	Window30mTTL time.Duration
	AdminHold    bool
	// Cooling parameters (P1: unify with credentialfpslot/node_state.go)
	CoolSeconds     int // Seconds to cool when disabled (default 300 = 5min)
	FailStreakLimit int // Failures before disabling (default 3)
	// BillingMode (2026-08-10): when "free", record_request.lua tolerates
	// transient error kinds (rate_limit/timeout/stream_timeout/
	// upstream_down/empty_response/transient) — fail_streak still
	// accumulates but the node is not hard-disabled. (The old
	// soft-demote twin in domains/ursm/v2/reducer was deleted — zero
	// importers, AUDIT_24H B2a; this Lua script is the authoritative
	// implementation.) Empty/non-"free" values keep the pre-existing
	// hard-disable behavior for all other billing modes.
	BillingMode string
	// HealthStatus (会话优化 v4 T5 / P1-5): optional rich health enum
	// (api.HealthStatus*). Empty or out-of-vocabulary values leave the
	// stored "health" field untouched. Display-only — see
	// record_request.lua's bridge block for the eligibility boundary.
	HealthStatus string
	// BackoffCapSeconds (UT-UR-05): caps cool_seconds × 2^disable_count
	// in record_request.lua. <=0 falls back to 1800 (the T5 target; the
	// value was hard-coded 3600 in the Lua before parameterization).
	BackoffCapSeconds int
}

type RecordResult struct {
	Status   string
	FailNew  int64
	FailLast int64
}

var ErrRedisUnavailable = errors.New("ursm.v2: redis unavailable")

func (s *Store) RecordRequest(ctx context.Context, nodeKey, win1m, win5m, win30m string, o RecordOutcome) (RecordResult, error) {
	if s == nil || s.rdb == nil {
		return RecordResult{}, ErrRedisUnavailable
	}
	res, err := RecordRequestScript.Run(ctx, s.rdb,
		[]string{nodeKey, win1m, win5m, win30m, requestDedupKey(nodeKey, o.DedupKey)},
		recordRequestArgv(o)...,
	).Slice()
	if err != nil {
		return RecordResult{}, fmt.Errorf("ursm.v2: record_request: %w", err)
	}
	if len(res) < 3 {
		return RecordResult{}, fmt.Errorf("ursm.v2: record_request: short reply")
	}
	return RecordResult{Status: asString(res[0]), FailNew: asInt64(res[1]), FailLast: asInt64(res[2])}, nil
}

// recordRequestArgv marshals one outcome into the ARGV layout shared by
// record_request.lua and record_request_dual.lua, applying the same
// defaults the legacy entry point always applied.
func recordRequestArgv(o RecordOutcome) []interface{} {
	coolSeconds := o.CoolSeconds
	if coolSeconds <= 0 {
		coolSeconds = 300 // 5 minutes default
	}
	failStreakLimit := o.FailStreakLimit
	if failStreakLimit <= 0 {
		failStreakLimit = 3
	}
	backoffCapSeconds := o.BackoffCapSeconds
	if backoffCapSeconds <= 0 {
		backoffCapSeconds = 1800 // 会话优化 v4 T5 target (was hard-coded 3600 in the Lua)
	}
	return []interface{}{
		BoolFlag(o.Success), o.ErrorKind, fmt.Sprintf("%d", o.NowMs),
		fmt.Sprintf("%d", o.LatencyMs), o.RequestID,
		fmt.Sprintf("%d", int64(o.NodeTTL/time.Second)),
		fmt.Sprintf("%d", int64(o.Window5mTTL/time.Second)),
		fmt.Sprintf("%d", int64(o.Window30mTTL/time.Second)),
		BoolFlag(o.AdminHold),
		fmt.Sprintf("%d", coolSeconds),
		fmt.Sprintf("%d", failStreakLimit),
		BoolFlag(o.DedupKey != ""),
		o.BillingMode,
		o.HealthStatus,
		fmt.Sprintf("%d", backoffCapSeconds),
	}
}

// recordRequestDual applies one outcome to both grammars atomically. A
// tuple the canonical grammar cannot represent (empty tenant) keeps the
// legacy set only (doc 14 §2); a representable tuple that still fails to
// derive is an error rather than a silent canonical-side drop.
func (s *Store) recordRequestDual(ctx context.Context, ks NodeKeySet, o RecordOutcome) (RecordResult, error) {
	keys := []string{
		ks.Node, ks.Win1m, ks.Win5m, ks.Win30m, requestDedupKey(ks.Node, o.DedupKey),
		"", "", "", "", "",
	}
	k2, err := K2KeySetForTenant(ks.Prefix, ks.TenantID, ks.CredentialID, ks.RawModel)
	if err == nil {
		keys[5], keys[6], keys[7], keys[8] = k2.Node, k2.Win1m, k2.Win5m, k2.Win30m
		keys[9] = requestDedupKey(k2.Node, o.DedupKey)
	} else if ks.TenantID != "" {
		return RecordResult{}, fmt.Errorf("ursm.v2: derive k2 key set: %w", err)
	}
	res, err := RecordRequestDualScript.Run(ctx, s.rdb, keys, recordRequestArgv(o)...).Slice()
	if err != nil {
		return RecordResult{}, fmt.Errorf("ursm.v2: record_request_dual: %w", err)
	}
	if len(res) < 3 {
		return RecordResult{}, fmt.Errorf("ursm.v2: record_request_dual: short reply")
	}
	return RecordResult{Status: asString(res[0]), FailNew: asInt64(res[1]), FailLast: asInt64(res[2])}, nil
}

func requestDedupKey(nodeKey, dedupKey string) string {
	sum := sha256.Sum256([]byte(dedupKey))
	return fmt.Sprintf("%s:request_dedup:%x", nodeKey, sum)
}

// RecordRequestKeySet records one request outcome against a complete
// logical key set (node + windows + derived dedup). In legacy mode (the
// default) it is byte-identical to RecordRequest with the individual keys;
// dual and canonical modes additionally maintain the canonical key set as
// one atomic unit.
func (s *Store) RecordRequestKeySet(ctx context.Context, ks NodeKeySet, o RecordOutcome) (RecordResult, error) {
	if s == nil || s.rdb == nil {
		return RecordResult{}, ErrRedisUnavailable
	}
	if s.schemaMode == KeySchemaModeLegacy {
		return s.RecordRequest(ctx, ks.Node, ks.Win1m, ks.Win5m, ks.Win30m, o)
	}
	return s.recordRequestDual(ctx, ks, o)
}

func BoolFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
