package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

const requestJourneyAPIPath = "/api/admin/request-journeys"

type requestJourneyReader interface {
	Detail(ctx context.Context, tenantID, requestID string) (requestjourney.DetailResult, error)
	RecentIngress(ctx context.Context) (requestjourney.IngressListResult, error)
	RecentTotal(ctx context.Context, tenantID string) (requestjourney.ListResult, error)
	RecentModel(ctx context.Context, tenantID, model string) (requestjourney.ListResult, error)
	RecentNode(ctx context.Context, tenantID string, key requestjourney.NodeKey) (requestjourney.ListResult, error)
	RecentModels(ctx context.Context, tenantID string) (requestjourney.ModelListResult, error)
	RecentNodes(ctx context.Context, tenantID string) (requestjourney.NodeListResult, error)
}

// RequestJourneyAPI exposes tenant-scoped RequestJourney reads. Route
// registration is intentionally left to the gateway composition root.
type RequestJourneyAPI struct {
	reader requestJourneyReader
}

func NewRequestJourneyAPI(reader requestJourneyReader) *RequestJourneyAPI {
	return &RequestJourneyAPI{reader: reader}
}

func (api *RequestJourneyAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeRequestJourneyError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.URL.Path == requestJourneyAPIPath+"/queues" && strings.TrimSpace(r.URL.Query().Get("scope")) == "all" && !IsSuperAdminOrLegacy(r) {
		writeRequestJourneyError(w, http.StatusForbidden, "scope=all requires super_admin")
		return
	}
	if api == nil || api.reader == nil {
		if r.URL.Path == requestJourneyAPIPath+"/queues" {
			writeRequestJourneyJSON(w, http.StatusOK, requestJourneyQueuesResponse{
				View: "total", ObservationStatus: requestjourney.ObservationDegraded,
				TotalSnapshot: &requestjourney.TotalRequestFIFOSnapshot{
					Capacity: requestjourney.DefaultTotalRequestCapacity,
					Requests: []requestjourney.RequestSnapshot{},
				},
			})
			return
		}
		writeRequestJourneyJSON(w, http.StatusOK, requestjourney.ListResult{
			ObservationStatus: requestjourney.ObservationDegraded,
			Capacity:          requestjourney.DefaultTotalRequestCapacity,
			Requests:          []requestjourney.RequestSnapshot{},
		})

		return
	}

	tenantID := requestJourneyTenant(r)
	if tenantID == "" {
		writeRequestJourneyError(w, http.StatusBadRequest, "tenant is required")
		return
	}

	switch {
	case r.URL.Path == requestJourneyAPIPath || r.URL.Path == requestJourneyAPIPath+"/":
		api.serveList(w, r, tenantID)
	case r.URL.Path == requestJourneyAPIPath+"/queues":
		api.serveQueues(w, r, tenantID)
	case strings.HasPrefix(r.URL.Path, requestJourneyAPIPath+"/"):
		api.serveDetail(w, r, tenantID)
	default:
		writeRequestJourneyError(w, http.StatusNotFound, "not found")
	}
}

func (api *RequestJourneyAPI) serveList(w http.ResponseWriter, r *http.Request, tenantID string) {
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	providerIDRaw := strings.TrimSpace(r.URL.Query().Get("provider_id"))
	credentialIDRaw := strings.TrimSpace(r.URL.Query().Get("credential_id"))

	var result requestjourney.ListResult
	var err error
	switch {
	case providerIDRaw != "" || credentialIDRaw != "":
		providerID, providerErr := strconv.ParseInt(providerIDRaw, 10, 64)
		credentialID, credentialErr := strconv.ParseInt(credentialIDRaw, 10, 64)
		if model == "" || providerErr != nil || credentialErr != nil || providerID <= 0 || credentialID <= 0 {
			writeRequestJourneyError(w, http.StatusBadRequest, "model, provider_id, and credential_id are required for node queries")
			return
		}
		result, err = api.reader.RecentNode(r.Context(), tenantID, requestjourney.NodeKey{
			Model: model, ProviderID: providerID, CredentialID: credentialID,
		})
	case model != "":
		result, err = api.reader.RecentModel(r.Context(), tenantID, model)
	default:
		result, err = api.reader.RecentTotal(r.Context(), tenantID)
	}
	if err != nil {
		result.ObservationStatus = requestjourney.ObservationDegraded
	}
	if result.Requests == nil {
		result.Requests = []requestjourney.RequestSnapshot{}
	}
	writeRequestJourneyJSON(w, http.StatusOK, result)
}

type requestJourneyQueuesResponse struct {
	View              string                                   `json:"view"`
	Scope             string                                   `json:"scope,omitempty"`
	ObservationStatus requestjourney.ObservationStatus         `json:"observation_status"`
	ObservationScope  string                                   `json:"observation_scope,omitempty"`
	TotalSnapshot     *requestjourney.TotalRequestFIFOSnapshot `json:"total_snapshot,omitempty"`
	ModelSnapshots    []requestjourney.ModelFIFOSnapshot       `json:"model_snapshots"`
	NodeSnapshots     []requestjourney.NodeFIFOSnapshot        `json:"node_snapshots"`
}

type requestJourneyIngressFIFOSnapshot struct {
	Capacity int                              `json:"capacity"`
	Requests []requestjourney.IngressSnapshot `json:"requests"`
}

type requestJourneyAllQueuesResponse struct {
	View              string                             `json:"view"`
	Scope             string                             `json:"scope"`
	ObservationStatus requestjourney.ObservationStatus   `json:"observation_status"`
	ObservationScope  string                             `json:"observation_scope"`
	TotalSnapshot     *requestJourneyIngressFIFOSnapshot `json:"total_snapshot"`
	ModelSnapshots    []requestjourney.ModelFIFOSnapshot `json:"model_snapshots"`
	NodeSnapshots     []requestjourney.NodeFIFOSnapshot  `json:"node_snapshots"`
}

func (api *RequestJourneyAPI) serveQueues(w http.ResponseWriter, r *http.Request, tenantID string) {
	view := strings.TrimSpace(r.URL.Query().Get("view"))
	if view == "" {
		view = "total"
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "all" {
		if view != "total" {
			writeRequestJourneyError(w, http.StatusBadRequest, "scope=all is only valid for view=total")
			return
		}
		result, err := api.reader.RecentIngress(r.Context())
		if err != nil {
			result.ObservationStatus = requestjourney.ObservationDegraded
			result.ObservationScope = "instance_local"
		}
		if result.Requests == nil {
			result.Requests = []requestjourney.IngressSnapshot{}
		}
		writeRequestJourneyJSON(w, http.StatusOK, requestJourneyAllQueuesResponse{
			View: "total", Scope: "all", ObservationStatus: result.ObservationStatus,
			ObservationScope: result.ObservationScope,
			TotalSnapshot:    &requestJourneyIngressFIFOSnapshot{Capacity: result.Capacity, Requests: result.Requests},
			ModelSnapshots:   []requestjourney.ModelFIFOSnapshot{}, NodeSnapshots: []requestjourney.NodeFIFOSnapshot{},
		})
		return
	}
	if scope != "" {
		writeRequestJourneyError(w, http.StatusBadRequest, "scope must be all or omitted")
		return
	}
	response := requestJourneyQueuesResponse{
		View:           view,
		ModelSnapshots: []requestjourney.ModelFIFOSnapshot{},
		NodeSnapshots:  []requestjourney.NodeFIFOSnapshot{},
	}
	switch view {
	case "total":
		result, err := api.reader.RecentTotal(r.Context(), tenantID)
		if err != nil {
			result.ObservationStatus = requestjourney.ObservationDegraded
		}
		response.ObservationStatus = result.ObservationStatus
		response.TotalSnapshot = &requestjourney.TotalRequestFIFOSnapshot{Capacity: result.Capacity, Requests: nonNilJourneyRequests(result.Requests)}
	case "models":
		result, err := api.reader.RecentModels(r.Context(), tenantID)
		if err != nil {
			result.ObservationStatus = requestjourney.ObservationDegraded
		}
		response.ObservationStatus = result.ObservationStatus
		response.ModelSnapshots = result.Models
		if response.ModelSnapshots == nil {
			response.ModelSnapshots = []requestjourney.ModelFIFOSnapshot{}
		}
	case "nodes":
		result, err := api.reader.RecentNodes(r.Context(), tenantID)
		if err != nil {
			result.ObservationStatus = requestjourney.ObservationDegraded
		}
		response.ObservationStatus = result.ObservationStatus
		response.NodeSnapshots = result.Nodes
		if response.NodeSnapshots == nil {
			response.NodeSnapshots = []requestjourney.NodeFIFOSnapshot{}
		}
	default:
		writeRequestJourneyError(w, http.StatusBadRequest, "view must be total, models, or nodes")
		return
	}
	writeRequestJourneyJSON(w, http.StatusOK, response)
}

func nonNilJourneyRequests(requests []requestjourney.RequestSnapshot) []requestjourney.RequestSnapshot {
	if requests == nil {
		return []requestjourney.RequestSnapshot{}
	}
	return requests
}

func (api *RequestJourneyAPI) serveDetail(w http.ResponseWriter, r *http.Request, tenantID string) {
	escapedID := strings.TrimPrefix(r.URL.EscapedPath(), requestJourneyAPIPath+"/")
	requestID, err := url.PathUnescape(escapedID)
	if err != nil || strings.TrimSpace(requestID) == "" || strings.Contains(requestID, "/") {
		writeRequestJourneyError(w, http.StatusBadRequest, "invalid request_id")
		return
	}
	result, readErr := api.reader.Detail(r.Context(), tenantID, requestID)
	if readErr != nil {
		result.ObservationStatus = requestjourney.ObservationDegraded
	}
	if result.Journey == nil && result.ObservationStatus != requestjourney.ObservationDegraded {
		writeRequestJourneyError(w, http.StatusNotFound, "request journey not found")
		return
	}
	writeRequestJourneyJSON(w, http.StatusOK, result)
}

func requestJourneyTenant(r *http.Request) string {
	if IsSuperAdminOrLegacy(r) {
		if tenantID := strings.TrimSpace(r.URL.Query().Get("tenant")); tenantID != "" {
			return tenantID
		}
	}
	return strings.TrimSpace(GetTenantID(r))
}

func writeRequestJourneyJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeRequestJourneyError(w http.ResponseWriter, status int, message string) {
	writeRequestJourneyJSON(w, status, map[string]any{
		"error": map[string]string{"message": message, "type": "admin_error"},
	})
}
