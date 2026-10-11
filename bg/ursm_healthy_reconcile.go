package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	ursmv2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// ursmHealthyKeyReconciler closes the healthy-pair availability hole observed
// on 154 on 2026-10-11 (glm-5.3 / gpt-6-astra / claude-opus-5 lockouts).
//
// Background, in dependency order:
//
//   - 2026-09-20 probe-volume policy (MarkNodeProbeHealthy): every successful
//     business request parks its (credential, model) row in node_probe_state
//     for 30 days ("park, don't re-arm") — healthy pairs are intentionally
//     invisible to every probe scheduler.
//   - Their URSM v2 node key is then kept alive solely by request telemetry
//     (record_request.lua, source_priority=10, traffic-oriented NodeTTL —
//     minutes). The 7h probeWriteTTLFloor only applies to probe writes, and
//     after the park policy no probe write ever happens for a healthy pair.
//   - Any traffic gap longer than that TTL expires the key. The URSM v2 T4
//     read contract treats a missing key as Available=false, so the router
//     filters the candidate out ("URSM v2 authoritative view filtered out
//     every candidate"). Parked requests (30s × up to 5h) generate no
//     telemetry and re-arm no probe — nothing restores the key until the
//     daily integrity_verify sweep happens to cover the pair.
//   - A Redis turbulence event (the 2026-10-11 15:45:58 redis_unavailable
//     recovery restored 769 snapshot keys over live state) can expire or
//     stale many pairs at once; that is what turned a per-model edge case
//     into a fleet-wide latency incident.
//
// The probeWriteTTLFloor comment (domains/ursm/v2/probe.go) already states
// the intended contract: "the node probe worker re-verifies a healthy node
// at most every 1h". This reconciler implements that contract for the
// park-don't-re-arm world: on each tick it reads the URSM v2 view for every
// DB-verified healthy routable pair and re-writes missing or unavailable
// entries through the SAME probe-grade path (ApplyProbeForTenant,
// success=true, probe priority, 7h TTL floor).
//
// Safety invariants:
//
//   - only DB-verified pairs are ever written (bindings available, credential
//     active/ready, provider enabled, probe state not failing) — a write can
//     only restore a candidate the DB already routes;
//   - T4 fail-closed is untouched: pairs WITHOUT DB-verified health are never
//     written, so protection-rejection semantics are preserved;
//   - manual_hold wins: apply_probe.lua ignores writes on manually held nodes;
//   - the read is one FilterAndScoreReady pipeline (no SCAN) and the writes
//     are bounded by the unavailable subset (normally zero).
type ursmHealthyState interface {
	Mode() ursmv2api.RolloutMode
	Ready(ctx context.Context) bool
	FilterAndScoreReady(ctx context.Context, seeds []ursmv2.CandidateSeed, ready bool) ([]ursmv2api.NodeView, error)
	ApplyProbeForTenant(ctx context.Context, tenant string, p ursmv2api.ProbeOutcome) error
}

// ursmHealthyReconcileDB is the minimal DB contract. *pgxpool.Pool satisfies
// it; tests inject a fake.
type ursmHealthyReconcileDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const (
	// ursmHealthyReconcileInterval is the default tick. After a recovery wipe
	// the worst-case lockout is one tick + the router's 30s retry interval,
	// which keeps parked requests well inside their holdback window.
	ursmHealthyReconcileInterval = 60 * time.Second
	// ursmHealthyReconcileLimit bounds one pass. The healthy routable set is
	// ~hundreds today; the cap is a runaway guard, not a working-set size.
	ursmHealthyReconcileLimit = 4000
	// ursmHealthyReconcileSettingsKey is the settings_kv integer key. A value
	// <= 0 disables the reconciler (operator kill switch).
	ursmHealthyReconcileSettingsKey = "ursm.healthy_reconcile_interval_seconds"
)

// UrsmlHealthyKeyReconciler periodically restores URSM v2 node state for
// DB-verified healthy pairs.
type UrsmlHealthyKeyReconciler struct {
	state    ursmHealthyState
	db       ursmHealthyReconcileDB
	interval time.Duration
}

// NewUrsmlHealthyKeyReconciler constructs the reconciler. A non-positive
// interval resolved from settings disables it (Start logs and no-ops).
func NewUrsmlHealthyKeyReconciler(state ursmHealthyState, db ursmHealthyReconcileDB) *UrsmlHealthyKeyReconciler {
	return newUrsmlHealthyKeyReconcilerWithInterval(state, db,
		time.Duration(settings.GetPlatformInt(ursmHealthyReconcileSettingsKey, int(ursmHealthyReconcileInterval/time.Second)))*time.Second)
}

// newUrsmlHealthyKeyReconcilerWithInterval pins the tick explicitly; the
// settings-driven constructor delegates here so tests can exercise the
// disabled (<= 0) path without initializing settings.Global.
func newUrsmlHealthyKeyReconcilerWithInterval(state ursmHealthyState, db ursmHealthyReconcileDB, interval time.Duration) *UrsmlHealthyKeyReconciler {
	if interval < 0 {
		interval = 0
	}
	return &UrsmlHealthyKeyReconciler{state: state, db: db, interval: interval}
}

// Start launches the reconcile loop. Idempotent with Start semantics matching
// AssetWatcher: performs an initial pass immediately so a restart heals a
// wiped namespace without waiting a full tick.
func (r *UrsmlHealthyKeyReconciler) Start(ctx context.Context) {
	if r == nil || r.state == nil || r.db == nil {
		return
	}
	if r.interval <= 0 {
		slog.Info("ursm.v2 healthy-key reconciler disabled",
			"settings_key", ursmHealthyReconcileSettingsKey)
		return
	}
	if r.state.Mode() == ursmv2api.ModeOff {
		slog.Info("ursm.v2 healthy-key reconciler not started (mode off)")
		return
	}
	go r.run(ctx)
	slog.Info("ursm.v2 healthy-key reconciler started", "interval", r.interval.String())
}

func (r *UrsmlHealthyKeyReconciler) run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.reconcileOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reconcileOnce(ctx)
		}
	}
}

// healthyCandidateSQL lists DB-verified healthy routable (tenant, credential,
// model) pairs. Every predicate is a safety gate: the reconciler may only
// re-arm state for a pair the DB itself currently routes.
func healthyCandidateSQL() string {
	return `
		SELECT DISTINCT COALESCE(c.tenant_id, ''), cmb.credential_id, pm.raw_model_name, c.provider_id
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN credentials c      ON c.id = cmb.credential_id
		JOIN providers p        ON p.id = c.provider_id
		LEFT JOIN node_probe_state nps
		       ON nps.credential_id = cmb.credential_id
		      AND nps.raw_model_name = pm.raw_model_name
		WHERE cmb.available = TRUE
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(c.availability_state, 'ready') = 'ready'
		  AND COALESCE(p.enabled, TRUE) = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND (nps.credential_id IS NULL
		       OR (COALESCE(nps.paused, FALSE) = FALSE
		           AND COALESCE(nps.consecutive_failures, 0) = 0))
		LIMIT $1
	`
}

// reconcileOnce runs one read-then-repair pass. Errors on individual writes
// are logged and do not abort the remaining repairs.
func (r *UrsmlHealthyKeyReconciler) reconcileOnce(ctx context.Context) {
	if !r.state.Ready(ctx) {
		// Gate closed: the recovery flow owns the namespace until it reopens;
		// writing behind a closed gate would fight the bootstrap.
		return
	}
	rows, err := r.db.Query(ctx, healthyCandidateSQL(), ursmHealthyReconcileLimit)
	if err != nil {
		slog.Warn("ursm.v2 healthy-key reconciler: candidate query failed", "error", err)
		return
	}
	type pair struct {
		tenant  string
		credID  int
		model   string
		provID  int
		written bool
	}
	pairs := make([]pair, 0, 256)
	seeds := make([]ursmv2.CandidateSeed, 0, 256)
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.tenant, &p.credID, &p.model, &p.provID); err != nil {
			rows.Close()
			slog.Warn("ursm.v2 healthy-key reconciler: candidate scan failed", "error", err)
			return
		}
		// Authoritative mode validates seed.TenantID != "" (ursm.v2 contract);
		// a credential with an empty tenant_id cannot be matched to the
		// request-side tenant key form, so skip it rather than fail the pass.
		if p.tenant == "" {
			continue
		}
		pairs = append(pairs, p)
		seeds = append(seeds, ursmv2.CandidateSeed{
			ProviderID:   p.provID,
			CredentialID: p.credID,
			RawModel:     p.model,
			TenantID:     p.tenant,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("ursm.v2 healthy-key reconciler: candidate rows error", "error", err)
		return
	}
	if len(seeds) == 0 {
		return
	}
	views, err := r.state.FilterAndScoreReady(ctx, seeds, true)
	if err != nil {
		slog.Warn("ursm.v2 healthy-key reconciler: view read failed", "error", err, "pairs", len(seeds))
		return
	}
	n := len(views)
	if n > len(pairs) {
		n = len(pairs)
	}
	repaired := 0
	for i := 0; i < n; i++ {
		if views[i].Available {
			continue
		}
		p := pairs[i]
		err := r.state.ApplyProbeForTenant(ctx, p.tenant, ursmv2api.ProbeOutcome{
			CredentialID: p.credID,
			RawModel:     p.model,
			Success:      true,
		})
		if err != nil {
			slog.Warn("ursm.v2 healthy-key reconciler: probe-grade write failed",
				"credential_id", p.credID, "model", p.model, "tenant", p.tenant, "error", err)
			continue
		}
		pairs[i].written = true
		repaired++
	}
	if repaired > 0 {
		sample := make([]string, 0, 5)
		for i := 0; i < n && len(sample) < 5; i++ {
			if pairs[i].written {
				sample = append(sample, pairs[i].model)
			}
		}
		slog.Info("ursm.v2 healthy-key reconciler: repaired node state",
			"repaired", repaired, "checked", len(pairs), "sample_models", sample)
	}
}

// compile-time check that the production manager satisfies the interface.
var _ ursmHealthyState = (*ursmv2.Manager)(nil)
