// Package reducer holds the pure StateReducer for URSM v2.
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

func Apply(ev api.RequestOutcome, cur api.NodeView) Decision {
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
