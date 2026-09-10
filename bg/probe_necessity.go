// bg/probe_necessity.go — self-check necessity gate (自检必要性检查).
//
// 需求 (2026-09-11): for queued self-check (node_probe) tasks, run one
// necessity check BEFORE executing the probe. When either condition holds the
// probe is unnecessary: skip it, do not execute, and remove the probe request
// from the queue AND the database.
//
//	① 当前模型状态没有出错，且同一凭据下其它节点在 redis 缓存中的状态
//	  全部正常（有当前请求的节点请求正常，没有请求的节点视为正常）；
//	② 前一个探测周期的探测结果显示节点状态正常，且此后没有错误请求。
//
// Data sources:
//   - Redis node hashes via NodeHealthEvidenceSource (production adapter:
//     URSM v2 Manager.ProbeHealthEvidence — the same node hash that routing
//     and record_request.lua maintain, so "redis 缓存中的状态" is authoritative).
//   - node_probe_runs for the previous probe cycle outcome (condition ②).
//   - credential_model_bindings × provider_models to enumerate the sibling
//     nodes under the same credential (node = (credential_id, raw_model)).
//
// Removal semantics (从队列及数据库中移除):
//   - credential_probe_queue row: hard DELETE via ProbeQueue.Remove
//     (lease-guarded) — this frees the dedup_key for a future real failure;
//   - node_probe_state mirror row: DELETE via deleteNodeProbeState — otherwise
//     pumpDueStatesToQueue would re-enqueue the same node within 30s;
//   - the 自检 SSE tile gets a terminal "skipped" transition instead of
//     hanging in-flight.
//
// Failure posture: the gate is an optimization on top of a probe that is
// already scheduled. Any evidence error (redis down, DB down) fails OPEN —
// the probe runs as before. Only provable "not necessary" skips a probe.
package bg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// probeNecessitySiblingLimit bounds the sibling enumeration per credential.
// Truncation has a real cost: an errored sibling beyond the limit is not seen,
// which can flip condition ① from "must probe" to "skip". 500 is far above any
// realistic per-credential binding count, and the residual risk is bounded:
// the skip only happens when the CURRENT node is provably healthy in Redis, so
// the probe being skipped was for a node that already looks fine.
const probeNecessitySiblingLimit = 500

// probeNecessityGateTimeout bounds the whole gate (sibling query + evidence
// reads + previous-probe lookup). The gate runs on the queue worker's loop
// before the lease heartbeat starts; without a deadline a slow Redis/DB would
// stall the single-worker probe pipeline and burn the claim lease. An
// over-budget gate fails open exactly like an evidence error.
const probeNecessityGateTimeout = 3 * time.Second

// Skip reason codes (ReasonCode in ProbeQueueResult / metric label).
const (
	SkipReasonAllNodesHealthy  = "not_necessary_all_nodes_healthy"
	SkipReasonLastProbeHealthy = "not_necessary_last_probe_healthy"
)

// skipReasonSSEPrefix marks a terminal SSE transition as a necessity skip.
const skipReasonSSEPrefix = "skipped_not_necessary"

var probeNecessitySkipTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llmgw_node_probe_necessity_skip_total",
		Help: "Self-check probes skipped by the necessity gate before execution, by skip reason.",
	},
	[]string{"reason"},
)

// NodeHealthEvidence is the bg-local view of one node's Redis cache state.
// It mirrors the fields of the URSM node hash that the necessity gate needs;
// the production adapter (cmd/gateway ursmV2EvidenceSource) fills it from
// v2.Manager.ProbeHealthEvidence so bg stays decoupled from the URSM package.
type NodeHealthEvidence struct {
	// Known is true when the persisted Redis state is complete enough to
	// prove a health decision. false = no state / undecodable state (treated
	// as "no requests, no error" for condition ①, but NOT usable to prove
	// condition ②).
	Known bool
	// Healthy is the composite routing-health verdict: available, not
	// disabled, not on manual hold, cool window elapsed, fail streak 0, no
	// last error, health status healthy/absent.
	Healthy bool
	// LastRequestAt is the timestamp of the most recent request event.
	// LastRequestFailed mirrors the latest request outcome. Both are carried
	// for observability/debugging; the gate's conditions derive normality
	// from Healthy + the LastRequestErrorAt watermark, which already subsume
	// them (any failed request bumps fail_streak, making Healthy false).
	LastRequestAt     time.Time
	LastRequestFailed bool
	// LastRequestErrorAt is the error watermark: the most recent request
	// failure timestamp (zero when no failed request was ever recorded).
	LastRequestErrorAt time.Time
}

// NodeHealthEvidenceSource supplies fresh Redis-backed node health evidence.
// Implemented in production by a thin adapter over URSM v2's
// Manager.ProbeHealthEvidence; keys of the returned map are raw model names.
type NodeHealthEvidenceSource interface {
	NodeHealthEvidence(ctx context.Context, tenant string, credentialID int, models []string) (map[string]NodeHealthEvidence, error)
}

// nodeProbeRunSummary is the slice of the previous probe-cycle audit row that
// condition ② needs.
type nodeProbeRunSummary struct {
	Success     bool
	CompletedAt time.Time
}

// SetHealthEvidenceSource wires the Redis-backed evidence reader used by the
// necessity gate. nil (default) disables the gate entirely — every probe runs.
func (s *ProbeService) SetHealthEvidenceSource(src NodeHealthEvidenceSource) {
	if s != nil {
		s.healthEvidence = src
	}
}

// SetSiblingModelsFn overrides how the gate enumerates the other nodes
// (raw model names) bound to a credential, excluding the probed model itself.
// Production leaves it nil and uses the credential_model_bindings ×
// provider_models query.
func (s *ProbeService) SetSiblingModelsFn(fn func(ctx context.Context, credID int, model string) ([]string, error)) {
	if s != nil {
		s.siblingModelsFn = fn
	}
}

// SetLastProbeRunFn overrides how the gate reads the previous probe-cycle
// outcome. Production leaves it nil and uses the node_probe_runs query.
func (s *ProbeService) SetLastProbeRunFn(fn func(ctx context.Context, credID int, model string) (*nodeProbeRunSummary, error)) {
	if s != nil {
		s.lastProbeRunFn = fn
	}
}

// SetRemoveSkippedFn overrides the skip-path removal (queue row + state
// mirror). Production leaves it nil and uses removeSkippedProbe.
func (s *ProbeService) SetRemoveSkippedFn(fn func(ctx context.Context, task ProbeQueueTask, reason string)) {
	if s != nil {
		s.removeSkippedFn = fn
	}
}

// probeUnnecessary evaluates the two skip conditions. Returns ("", "", nil)
// when the probe must run, (reasonCode, detail, nil) when it must be skipped,
// or an error when the evidence could not be read (callers fail open and run
// the probe).
func (s *ProbeService) probeUnnecessary(ctx context.Context, task ProbeQueueTask) (string, string, error) {
	if s.healthEvidence == nil {
		return "", "", fmt.Errorf("necessity gate unavailable: no health evidence source")
	}
	if code, detail, err := s.conditionAllNodesHealthy(ctx, task); err != nil {
		return "", "", err
	} else if code != "" {
		return code, detail, nil
	}
	return s.conditionLastProbeHealthy(ctx, task)
}

// conditionAllNodesHealthy is skip condition ①: the current model is not in
// an errored state, and every other node under the same credential is also
// not errored — nodes with current requests in the redis cache show normal
// requests, nodes without requests (or without cache state) count as normal.
func (s *ProbeService) conditionAllNodesHealthy(ctx context.Context, task ProbeQueueTask) (string, string, error) {
	credID := int(task.CredentialID)
	model := task.RawModel
	siblings, err := s.siblingModels(ctx, credID, model)
	if err != nil {
		return "", "", fmt.Errorf("list sibling nodes: %w", err)
	}
	models := make([]string, 0, len(siblings)+1)
	models = append(models, model)
	models = append(models, siblings...)

	evidence, err := s.healthEvidenceOf(ctx, task.TenantID, credID, models)
	if err != nil {
		return "", "", err
	}
	// The current node MUST have a readable Redis state to prove "没有出错".
	// A missing/undecodable hash (TTL expiry ≠ no traffic — the glm-5.2
	// node-keys-expired outage shape recorded in main.go) must fail OPEN:
	// skipping here would hard-delete the recovery probe, the recovery
	// scanner would resubmit it every minute, and the credential could never
	// recover through a probe — an infinite skip loop.
	current, ok := evidence[model]
	if !ok || !current.Known {
		return "", "", nil
	}
	if !current.Healthy {
		return "", "", nil // 当前模型状态出错 → 必须探测
	}
	for _, sibling := range siblings {
		ev, ok := evidence[sibling]
		if !ok {
			continue // no cache entry = no requests = normal
		}
		if ev.Known && !ev.Healthy {
			slog.Debug("probe_necessity: sibling node errored, probe stays necessary",
				"credential_id", credID, "model", model, "sibling", sibling)
			return "", "", nil // 所有其他模型状态必须都正常
		}
	}
	return SkipReasonAllNodesHealthy,
		fmt.Sprintf("current node and %d sibling node(s) under credential %d healthy or idle in redis cache", len(siblings), credID),
		nil
}

// conditionLastProbeHealthy is skip condition ②: the previous probe cycle
// reported the node healthy AND no error request has been recorded since that
// probe completed (redis error watermark still at or before the probe time).
func (s *ProbeService) conditionLastProbeHealthy(ctx context.Context, task ProbeQueueTask) (string, string, error) {
	credID := int(task.CredentialID)
	model := task.RawModel
	last, err := s.lastProbeRun(ctx, credID, model)
	if err != nil {
		return "", "", fmt.Errorf("read previous probe cycle: %w", err)
	}
	if last == nil || !last.Success {
		return "", "", nil // 前一探测周期不正常（或无记录）→ 必须探测
	}

	// The "no error requests since" half needs redis evidence: an absent or
	// undecodable node hash cannot prove the absence of errors (TTL expiry is
	// not the same as no traffic), so fail open instead of skipping.
	evidence, err := s.healthEvidenceOf(ctx, task.TenantID, credID, []string{model})
	if err != nil {
		return "", "", err
	}
	current, ok := evidence[model]
	if !ok || !current.Known {
		return "", "", nil
	}
	if !current.Healthy {
		return "", "", nil // 缓存状态已不再健康 → 必须探测
	}
	if !current.LastRequestErrorAt.IsZero() && current.LastRequestErrorAt.After(last.CompletedAt) {
		return "", "", nil // 探测之后出现过错误请求 → 必须探测
	}
	return SkipReasonLastProbeHealthy,
		fmt.Sprintf("previous probe cycle at %s reported the node healthy and no error request recorded since",
			last.CompletedAt.Format(time.RFC3339)),
		nil
}

// siblingModels enumerates the other nodes (raw model names) bound to the
// credential, excluding the probed model itself — "同一凭据下其它节点".
// DISTINCT guards against a binding graph that joins out duplicated names.
func (s *ProbeService) siblingModels(ctx context.Context, credID int, model string) ([]string, error) {
	if s.siblingModelsFn != nil {
		return s.siblingModelsFn(ctx, credID, model)
	}
	if s.worker == nil || s.worker.db == nil {
		return nil, fmt.Errorf("no worker db wired for sibling lookup")
	}
	rows, err := s.worker.db.Query(ctx, `
		SELECT DISTINCT pm.raw_model_name
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE cmb.credential_id = $1
		  AND pm.raw_model_name <> $2
		ORDER BY pm.raw_model_name
		LIMIT $3`, credID, model, probeNecessitySiblingLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// lastProbeRun reads the previous probe-cycle outcome for the node from the
// node_probe_runs audit (最新一条已完成记录，无论成功与否).
func (s *ProbeService) lastProbeRun(ctx context.Context, credID int, model string) (*nodeProbeRunSummary, error) {
	if s.lastProbeRunFn != nil {
		return s.lastProbeRunFn(ctx, credID, model)
	}
	if s.worker == nil || s.worker.db == nil {
		return nil, fmt.Errorf("no worker db wired for previous-probe lookup")
	}
	var (
		success     bool
		completedAt time.Time
	)
	err := s.worker.db.QueryRow(ctx, `
		SELECT success, completed_at
		FROM node_probe_runs
		WHERE credential_id = $1 AND raw_model_name = $2
		  AND completed_at IS NOT NULL
		ORDER BY started_at DESC
		LIMIT 1`, credID, model).Scan(&success, &completedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &nodeProbeRunSummary{Success: success, CompletedAt: completedAt}, nil
}

// healthEvidenceOf fetches redis evidence, treating a nil evidence map as an
// empty (all-unknown) set.
func (s *ProbeService) healthEvidenceOf(ctx context.Context, tenant string, credID int, models []string) (map[string]NodeHealthEvidence, error) {
	evidence, err := s.healthEvidence.NodeHealthEvidence(ctx, tenant, credID, models)
	if err != nil {
		return nil, fmt.Errorf("read node health evidence: %w", err)
	}
	if evidence == nil {
		evidence = map[string]NodeHealthEvidence{}
	}
	return evidence, nil
}

// removeSkippedProbe is the "从队列及数据库中移除" half of the skip path.
// The queue row is hard-deleted (lease-guarded), the node_probe_state mirror
// row is deleted so the 30s pump cannot re-enqueue the same node, and the
// 自检 SSE tile gets a terminal transition. Best-effort: failures are logged,
// never panic, and never turn the skip into an execution.
//
// Ordering matters: the lease-guarded queue-row DELETE is the ownership
// proof. Only when it actually removed the row do we touch node_probe_state
// and publish the SSE transition — if the lease was lost, another worker owns
// the task and may be legitimately executing it, so its state mirror and
// dashboard tile must be left alone.
func (s *ProbeService) removeSkippedProbe(ctx context.Context, task ProbeQueueTask, reason string) {
	if s.removeSkippedFn != nil {
		s.removeSkippedFn(ctx, task, reason)
		return
	}
	credID := int(task.CredentialID)
	if s.queue == nil || task.LeaseToken == "" {
		// No queue wired (or no lease): nothing ownership-guarded to do; the
		// caller only reaches this with a claimed task in production.
		slog.Warn("probe_necessity: skip removal skipped — queue or lease token missing",
			"queue_id", task.ID, "credential_id", credID, "model", task.RawModel)
		return
	}
	// 1. Queue row (credential_probe_queue) — the queue itself lives in the
	//    database, so the hard DELETE removes the request from both.
	removed, err := s.queue.Remove(ctx, task)
	if err != nil {
		slog.Warn("probe_necessity: removing skipped queue row failed",
			"queue_id", task.ID, "credential_id", credID, "model", task.RawModel, "error", err)
		return
	}
	if !removed {
		slog.Info("probe_necessity: skipped queue row already gone (lease lost or completed elsewhere), leaving state to the new owner",
			"queue_id", task.ID, "credential_id", credID, "model", task.RawModel)
		return
	}
	// 2. Database mirror row — must go before returning: pumpDueStatesToQueue
	//    scans it every 30s and would re-enqueue the node probe we just
	//    decided to drop.
	s.worker.deleteNodeProbeState(ctx, credID, task.RawModel)
	// 3. SSE terminal transition so the dashboard tile does not hang in-flight.
	s.queue.publishRemovedTransition(task, skipReasonSSEPrefix+": "+reason)
	slog.Info("probe_necessity: self-check skipped as unnecessary, removed from queue and database",
		"queue_id", task.ID, "credential_id", credID, "model", task.RawModel,
		"source", task.Source, "reason", reason)
}
