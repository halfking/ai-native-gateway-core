package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

const dispatchJournalAPIPath = "/api/admin/dispatch/journal/"

type journalSnapshotConsumer interface {
	ConsumeSnapshot(context.Context, dispatch.JournalSnapshotQuery) (dispatch.JournalSnapshot, error)
}

// JournalSnapshotAPI exposes one tenant/request-scoped journal snapshot. The
// consumer owns the read model and authorization sentinel; this adapter only
// enforces path shape and maps errors to the public HTTP contract.
type JournalSnapshotAPI struct {
	consumer journalSnapshotConsumer
}

func NewJournalSnapshotAPI(consumer journalSnapshotConsumer) *JournalSnapshotAPI {
	return &JournalSnapshotAPI{consumer: consumer}
}

func (api *JournalSnapshotAPI) RegisterRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	if mux == nil || wrap == nil {
		return
	}
	mux.HandleFunc(dispatchJournalAPIPath, wrap(api.ServeHTTP))
}

func (api *JournalSnapshotAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeRequestJourneyError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if api == nil || api.consumer == nil {
		writeRequestJourneyError(w, http.StatusServiceUnavailable, "journal unavailable")
		return
	}

	tenantID, requestID, ok := parseDispatchJournalPath(r.URL.EscapedPath())
	if !ok {
		writeRequestJourneyError(w, http.StatusBadRequest, "invalid journal path")
		return
	}
	auth := GetAuthContext(r)
	if auth == nil || strings.TrimSpace(auth.TenantID) == "" {
		writeRequestJourneyError(w, http.StatusNotFound, "not found")
		return
	}
	callerTenant := strings.TrimSpace(auth.TenantID)
	privileged := auth.Role == "super_admin" || auth.Role == "admin_key"
	if !privileged && auth.Role != "tenant_admin" {
		writeRequestJourneyError(w, http.StatusNotFound, "not found")
		return
	}
	if !privileged && callerTenant != tenantID {
		writeRequestJourneyError(w, http.StatusNotFound, "not found")
		return
	}

	snapshot, err := api.consumer.ConsumeSnapshot(r.Context(), dispatch.JournalSnapshotQuery{
		CallerTenantID: callerTenant,
		TargetTenantID: tenantID,
		RequestID:      requestID,
		Privileged:     privileged,
	})
	if err != nil {
		if errors.Is(err, dispatch.ErrJournalNotFound) {
			writeRequestJourneyError(w, http.StatusNotFound, "not found")
			return
		}
		writeRequestJourneyError(w, http.StatusServiceUnavailable, "journal unavailable")
		return
	}
	if snapshot.TenantID != tenantID || snapshot.RequestID != requestID {
		writeRequestJourneyError(w, http.StatusNotFound, "not found")
		return
	}
	if snapshot.Entries == nil {
		snapshot.Entries = []dispatch.JournalEntry{}
	}
	writeRequestJourneyJSON(w, http.StatusOK, journalSnapshotResponse{
		TenantID: snapshot.TenantID, RequestID: snapshot.RequestID, Entries: snapshot.Entries,
		Truncated: snapshot.Truncated, TruncatedCount: snapshot.TruncatedCount,
		SnapshotVersion: snapshot.SnapshotVersion,
	})
}

func parseDispatchJournalPath(path string) (tenantID, requestID string, ok bool) {
	if !strings.HasPrefix(path, dispatchJournalAPIPath) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, dispatchJournalAPIPath)
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	tenantID, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", "", false
	}
	requestID, err = url.PathUnescape(parts[1])
	tenantID = strings.TrimSpace(tenantID)
	requestID = strings.TrimSpace(requestID)
	if err != nil || tenantID == "" || requestID == "" || strings.ContainsAny(tenantID, "/\\\x00") || strings.ContainsAny(requestID, "/\\\x00") || tenantID == "." || tenantID == ".." || requestID == "." || requestID == ".." {
		return "", "", false
	}
	return tenantID, requestID, true
}

type journalSnapshotResponse struct {
	TenantID        string                  `json:"tenant_id"`
	RequestID       string                  `json:"request_id"`
	Entries         []dispatch.JournalEntry `json:"entries"`
	Truncated       bool                    `json:"truncated"`
	TruncatedCount  int                     `json:"truncated_count"`
	SnapshotVersion int64                   `json:"snapshot_version"`
}
