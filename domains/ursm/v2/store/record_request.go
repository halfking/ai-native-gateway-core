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
	// Defaults for cooling parameters
	coolSeconds := o.CoolSeconds
	if coolSeconds <= 0 {
		coolSeconds = 300 // 5 minutes default
	}
	failStreakLimit := o.FailStreakLimit
	if failStreakLimit <= 0 {
		failStreakLimit = 3
	}
	res, err := RecordRequestScript.Run(ctx, s.rdb,
		[]string{nodeKey, win1m, win5m, win30m, requestDedupKey(nodeKey, o.DedupKey)},
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
	).Slice()
	if err != nil {
		return RecordResult{}, fmt.Errorf("ursm.v2: record_request: %w", err)
	}
	if len(res) < 3 {
		return RecordResult{}, fmt.Errorf("ursm.v2: record_request: short reply")
	}
	return RecordResult{Status: asString(res[0]), FailNew: asInt64(res[1]), FailLast: asInt64(res[2])}, nil
}

func requestDedupKey(nodeKey, dedupKey string) string {
	sum := sha256.Sum256([]byte(dedupKey))
	return fmt.Sprintf("%s:request_dedup:%x", nodeKey, sum)
}

func BoolFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
