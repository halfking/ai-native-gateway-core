package store

import (
	"context"
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
	Success     bool
	ErrorKind   string
	NowMs       int64
	LatencyMs   int
	RequestID   string
	NodeTTL     time.Duration
	Window5mTTL time.Duration
	Window30mTTL time.Duration
	AdminHold   bool
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
		[]string{nodeKey, win1m, win5m, win30m},
		boolFlag(o.Success), o.ErrorKind, fmt.Sprintf("%d", o.NowMs),
		fmt.Sprintf("%d", o.LatencyMs), o.RequestID,
		fmt.Sprintf("%d", int(o.NodeTTL.Seconds())),
		fmt.Sprintf("%d", int(o.Window5mTTL.Seconds())),
		fmt.Sprintf("%d", int(o.Window30mTTL.Seconds())),
		boolFlag(o.AdminHold),
	).Slice()
	if err != nil {
		return RecordResult{}, fmt.Errorf("ursm.v2: record_request: %w", err)
	}
	if len(res) < 3 {
		return RecordResult{}, fmt.Errorf("ursm.v2: record_request: short reply")
	}
	return RecordResult{Status: asString(res[0]), FailNew: asInt64(res[1]), FailLast: asInt64(res[2])}, nil
}

func boolFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
