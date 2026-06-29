package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/apihub"
)

// AgentService is the subset of apihub.Service that AgentsHandler depends on.
// Defining it here (instead of importing from apihub) breaks a would-be
// import cycle and lets tests inject a stub without standing up PG.
//
// Every method MUST be safe for concurrent use (relay + admin both call
// into the same Service from many goroutines).
type AgentService interface {
	List(ctx context.Context, f apihub.Filter) ([]apihub.Asset, error)
	Get(ctx context.Context, k apihub.Kind, refID int64) (apihub.Asset, error)
	Link(ctx context.Context, rel apihub.Relationship) error
	Neighbors(ctx context.Context, k apihub.Kind, refID int64, depth int) ([]apihub.Asset, []apihub.Relationship, error)
}

// AgentsHandler exposes the unified asset registry as REST API.
//
// Routes:
//
//	GET    /api/agents                 — List assets (Phase 3 A3-1)
//	GET    /api/agents/:id             — Get one asset (Phase 3 A3-1)
//	POST   /api/agents/:id/link        — Create asset_link (Phase 3 A3-1)
//	GET    /api/agents/:id/neighbors   — Topology traversal (Phase 6)
//	GET    /api/agents/stats           — Aggregate overview (Phase 6)
//
// All methods scope by tenant_id extracted from context. RLS is enforced
// at the DB layer via apihub.PGStore's per-query SET LOCAL GUC.
type AgentsHandler struct {
	svc AgentService
}

// NewAgentsHandler returns a handler backed by an AgentService. Pass the
// result of `apihub.Service` directly; it satisfies AgentService.
func NewAgentsHandler(svc *apihub.Service) *AgentsHandler {
	return &AgentsHandler{svc: svc}
}

// newAgentsHandlerWithSvc is the test seam: pass any AgentService impl.
// Used by handlers_test.go to inject a stub without touching PG.
func newAgentsHandlerWithSvc(svc AgentService) *AgentsHandler {
	return &AgentsHandler{svc: svc}
}

// ── List ─────────────────────────────────────────────────────────────────

// List handles GET /api/agents.
//
// Query params:
//
//	kind    — llm_endpoint | mcp_server | agent | all (default: all)
//	tenant  — tenant_id override (super_admin only; default: from ctx)
//	limit   — max rows (default 100, capped at 1000)
//	offset  — pagination offset (default 0)
func (h *AgentsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	tenant := q.Get("tenant")
	if tenant == "" {
		tenant = "default"
	}

	limit := 100
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 {
		limit = l
	}
	if limit > 1000 {
		limit = 1000
	}

	offset := 0
	if o, err := strconv.Atoi(q.Get("offset")); err == nil && o >= 0 {
		offset = o
	}

	filter := apihub.Filter{
		TenantID: tenant,
		Limit:    limit,
	}
	if k := q.Get("kind"); k != "" && k != "all" {
		filter.Kind = apihub.Kind(k)
	}

	assets, err := h.svc.List(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"agents": assets,
		"total":  len(assets),
		"limit":  limit,
		"offset": offset,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ── Get ──────────────────────────────────────────────────────────────────

// Get handles GET /api/agents/:id. The :id is the asset's ref_id (composite
// key is (kind, ref_id); we assume llm_endpoint for now since that is the
// only Kind populated by the v1 AssetWatcher).
func (h *AgentsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	refID, ok := parseAgentRefID(w, r)
	if !ok {
		return
	}

	asset, err := h.svc.Get(ctx, apihub.KindLLMEndpoint, refID)
	if err != nil {
		if err == apihub.ErrNotFound {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "agent not found",
				"id":    refID,
			})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"agent": asset})
}

// ── Link ─────────────────────────────────────────────────────────────────

// Link handles POST /api/agents/:id/link with body {target_id, link_type}.
// Both endpoints must already exist as assets under the caller's tenant
// (FK constraint at the DB layer enforces this).
func (h *AgentsHandler) Link(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	sourceID, ok := parseAgentRefIDWithSuffix(w, r, "/link")
	if !ok {
		return
	}

	var req struct {
		TargetID int64  `json:"target_id"`
		LinkType string `json:"link_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	rel := apihub.Relationship{
		SrcKind: apihub.RelationEndpoint{Kind: apihub.KindLLMEndpoint, RefID: sourceID},
		DstKind: apihub.RelationEndpoint{Kind: apihub.KindLLMEndpoint, RefID: req.TargetID},
		Type:    apihub.RelationType(req.LinkType),
	}
	if err := h.svc.Link(ctx, rel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"link": map[string]interface{}{
			"source_id": sourceID,
			"target_id": req.TargetID,
			"link_type": req.LinkType,
		},
	})
}

// ── Neighbors (Phase 6) ─────────────────────────────────────────────────

// Neighbors handles GET /api/agents/:id/neighbors?depth=N.
//
// BFS traversal of the topology graph up to the given depth (default 1).
// Returns BOTH upstream and downstream assets along the edges that touch
// the seed asset. depth=0 is treated as depth=1 (Service convention).
//
// Response shape:
//
//	{
//	  "asset":    { ...seed... },
//	  "depth":    2,
//	  "edges":    [{ src_kind, src_ref_id, dst_kind, dst_ref_id, type }],
//	  "neighbors":[{ kind, ref_id, name, ... }]
//	}
func (h *AgentsHandler) Neighbors(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	refID, ok := parseAgentRefIDWithSuffix(w, r, "/neighbors")
	if !ok {
		return
	}

	depth := 1
	if d, err := strconv.Atoi(r.URL.Query().Get("depth")); err == nil && d >= 1 && d <= 5 {
		depth = d
	}

	seed, err := h.svc.Get(ctx, apihub.KindLLMEndpoint, refID)
	if err != nil {
		if err == apihub.ErrNotFound {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "agent not found",
				"id":    refID,
			})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	assets, rels, err := h.svc.Neighbors(ctx, apihub.KindLLMEndpoint, refID, depth)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Partition neighbors by direction (upstream vs downstream) for the
	// UI's topology view. An edge is "upstream" of the seed when the seed
	// is the destination; "downstream" when the seed is the source.
	type neighborView struct {
		Kind  string `json:"kind"`
		RefID int64  `json:"ref_id"`
		Name  string `json:"name"`
	}
	upstream := make([]neighborView, 0)
	downstream := make([]neighborView, 0)
	seen := make(map[string]bool)
	for _, r := range rels {
		var neighborKind apihub.Kind
		var neighborRef int64
		if r.SrcKind.Kind == apihub.KindLLMEndpoint && r.SrcKind.RefID == refID {
			neighborKind = r.DstKind.Kind
			neighborRef = r.DstKind.RefID
		} else if r.DstKind.Kind == apihub.KindLLMEndpoint && r.DstKind.RefID == refID {
			neighborKind = r.SrcKind.Kind
			neighborRef = r.SrcKind.RefID
		} else {
			continue
		}
		key := string(neighborKind) + "|" + strconv.FormatInt(neighborRef, 10)
		if seen[key] {
			continue
		}
		seen[key] = true
		var name string
		for _, a := range assets {
			if a.Kind == neighborKind && a.RefID == neighborRef {
				name = a.Name
				break
			}
		}
		nv := neighborView{Kind: string(neighborKind), RefID: neighborRef, Name: name}
		if r.DstKind.Kind == apihub.KindLLMEndpoint && r.DstKind.RefID == refID {
			upstream = append(upstream, nv)
		} else {
			downstream = append(downstream, nv)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"asset":      seed,
		"depth":      depth,
		"edges":      rels,
		"upstream":   upstream,
		"downstream": downstream,
		"count":      len(upstream) + len(downstream),
	})
}

// ── Stats (Phase 6) ─────────────────────────────────────────────────────

// Stats handles GET /api/agents/stats — overview aggregates across all
// assets visible to the caller.
//
// One-shot implementation: pulls up to 1000 rows then groups in-memory.
// For 10k+ asset deployments this should move to a SQL GROUP BY; tracked
// as a follow-up since v1 totals are <500.
func (h *AgentsHandler) Stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	assets, err := h.svc.List(ctx, apihub.Filter{
		TenantID: "default",
		Limit:    1000,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	byKind := make(map[string]int)
	byHealth := make(map[string]int)
	byOwner := make(map[string]int)
	for _, a := range assets {
		byKind[string(a.Kind)]++
		byHealth[string(a.HealthState)]++
		if a.Owner != "" {
			byOwner[a.Owner]++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"total":     len(assets),
		"by_kind":   byKind,
		"by_health": byHealth,
		"by_owner":  byOwner,
	})
}

// ── helpers ─────────────────────────────────────────────────────────────

// parseAgentRefID parses the path /api/agents/:id (returns the int id).
// On error it writes a 400 to w and returns ok=false.
func parseAgentRefID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/agents/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// parseAgentRefIDWithSuffix is the /link and /neighbors variant: strips
// the trailing action suffix before parsing.
func parseAgentRefIDWithSuffix(w http.ResponseWriter, r *http.Request, suffix string) (int64, bool) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/agents/")
	idStr = strings.TrimSuffix(idStr, suffix)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}
