// Package reducer holds the pure StateReducer for URSM v2.
//
// Deprecated: this package is NOT wired into any production write path.
// The actual hot-path state machine lives in record_request.lua (executed
// atomically in Redis via domains/ursm/v2/store.Store.RecordRequest), which
// is the sole authority for node availability under authoritative/canary
// mode. Apply exists only as documentation of the intended pure-function
// semantics and is exercised solely by this package's own tests — grep the
// repo for "ursm/v2/reducer" and the only importer is this directory.
//
// 2026-08-10: verified zero callers outside reducer_test.go. Do not wire
// this into new code; extend record_request.lua instead (and keep this
// comment's transientErrors set in sync with the Lua transient_kinds table
// if the policy changes, since the two are not code-shared). Kept in-repo
// rather than deleted in case a future refactor migrates the Lua logic
// into a Go-side reducer for testability — at that point this file would
// become the real implementation instead of a spec-only artifact.
//
// The reducer is a side-effect-free function: it takes a RequestOutcome and
// the current NodeView snapshot, then returns a Decision containing the new
// NodeView and whether the write is accepted. Source-priority semantics
// (Seed < Request < Probe < Recover < Admin) ensure that higher-priority
// writes (e.g. admin manual disable) cannot be overridden by lower-priority
// ones (e.g. live request traffic).
package reducer

import (
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

type Decision struct {
	Accepted bool
	NodeView api.NodeView
	Reason   string
}

var permanentErrors = map[string]struct{}{
	"auth":            {},
	"auth_revoked":    {},
	"model_not_found": {},
	"quota_permanent": {},
}

var transientErrors = map[string]struct{}{
	"rate_limit":     {},
	"timeout":        {},
	"stream_timeout": {},
	"upstream_down":  {},
	"empty_response": {},
	"transient":      {},
}

// Apply is exported so reducer_test.go can exercise it, but it is not
// called by any runtime code path.
//
// Deprecated: use record_request.lua (via store.Store.RecordRequest) —
// see the package doc comment above for details.
func Apply(ev api.RequestOutcome, cur api.NodeView) Decision { //nolint:unused // spec-only, exercised by reducer_test.go
	if cur.SrcPriority >= api.SourcePriorityAdmin && !cur.Available {
		return Decision{Accepted: false, NodeView: cur, Reason: "manual_hold"}
	}

	nv := cur
	if ev.RequestID != "" {
		nv.Generation = cur.Generation + 1
	}
	nv.SrcPriority = api.SourcePriorityRequest
	nv.UpdatedAt = nowUTC()

	if ev.Success {
		nv.FailStreak = 0
		nv.Available = true
		nv.Reason = ""
		mergeLatency(&nv, ev.LatencyMs)
		return Decision{Accepted: true, NodeView: nv}
	}

	if _, permanent := permanentErrors[ev.ErrorKind]; permanent {
		nv.Available = false
		nv.FailStreak = cur.FailStreak + 1
		nv.Reason = ev.ErrorKind
		return Decision{Accepted: true, NodeView: nv}
	}

	if _, transient := transientErrors[ev.ErrorKind]; transient && ev.BillingMode == "free" {
		nv.Available = cur.Available
		nv.FailStreak = cur.FailStreak + 1
		nv.Reason = ev.ErrorKind
		slog.Info("ursm.v2.reducer: free transient soft-demote", "cid", ev.CredentialID, "model", ev.RawModel)
		return Decision{Accepted: true, NodeView: nv}
	}

	nv.Available = false
	nv.FailStreak = cur.FailStreak + 1
	nv.Reason = ev.ErrorKind
	return Decision{Accepted: true, NodeView: nv}
}

func mergeLatency(nv *api.NodeView, ms int) {
	if ms <= 0 {
		return
	}
	if nv.LatEWMA == 0 {
		nv.LatEWMA = ms
		return
	}
	nv.LatEWMA = (nv.LatEWMA*3 + ms) / 4
}
