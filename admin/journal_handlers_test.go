package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

type journalConsumerStub struct {
	snapshot dispatch.JournalSnapshot
	err      error
	query    dispatch.JournalSnapshotQuery
}

func (s *journalConsumerStub) ConsumeSnapshot(_ context.Context, query dispatch.JournalSnapshotQuery) (dispatch.JournalSnapshot, error) {
	s.query = query
	return s.snapshot, s.err
}

func journalRequest(path, role, tenant string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	return SetAuthContext(r, &AuthContext{Role: role, TenantID: tenant, IsJWT: true})
}

func TestJournalSnapshotAPIServesScopedSnapshot(t *testing.T) {
	consumer := &journalConsumerStub{snapshot: dispatch.JournalSnapshot{
		TenantID: "tenant-a", RequestID: "request id", Entries: []dispatch.JournalEntry{},
		Truncated: true, TruncatedCount: 3, SnapshotVersion: 9,
	}}
	api := NewJournalSnapshotAPI(consumer)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, journalRequest(dispatchJournalAPIPath+"tenant-a/request%20id", "tenant_admin", "tenant-a"))

	if response.Code != http.StatusOK || consumer.query.CallerTenantID != "tenant-a" || consumer.query.RequestID != "request id" {
		t.Fatalf("status=%d caller/request=%q/%q body=%s", response.Code, consumer.query.CallerTenantID, consumer.query.RequestID, response.Body.String())
	}
	body := response.Body.String()
	for _, field := range []string{`"tenant_id":"tenant-a"`, `"request_id":"request id"`, `"entries":[]`, `"truncated":true`, `"truncated_count":3`, `"snapshot_version":9`} {
		if !strings.Contains(body, field) {
			t.Errorf("body missing %s: %s", field, body)
		}
	}
}

func TestJournalSnapshotAPIUnifiesMissingAndCrossTenantAsNotFound(t *testing.T) {
	for name, consumer := range map[string]*journalConsumerStub{
		"missing":        {err: dispatch.ErrJournalNotFound},
		"infrastructure": {err: errors.New("database details must not escape")},
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			NewJournalSnapshotAPI(consumer).ServeHTTP(response, journalRequest(dispatchJournalAPIPath+"tenant-a/request-1", "tenant_admin", "tenant-a"))
			want := http.StatusNotFound
			if name == "infrastructure" {
				want = http.StatusServiceUnavailable
			}
			if response.Code != want || strings.Contains(response.Body.String(), "database details") || strings.Contains(response.Body.String(), "tenant-b") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestJournalSnapshotAPISuperAdminCanSelectTenant(t *testing.T) {
	consumer := &journalConsumerStub{snapshot: dispatch.JournalSnapshot{TenantID: "tenant-b", RequestID: "request-1", Entries: []dispatch.JournalEntry{}}}
	response := httptest.NewRecorder()
	NewJournalSnapshotAPI(consumer).ServeHTTP(response, journalRequest(dispatchJournalAPIPath+"tenant-b/request-1", "super_admin", "default"))
	if response.Code != http.StatusOK || consumer.query.TargetTenantID != "tenant-b" {
		t.Fatalf("status=%d target_tenant=%q body=%s", response.Code, consumer.query.TargetTenantID, response.Body.String())
	}
}

func TestJournalSnapshotAPIRejectsMalformedPathAndMethod(t *testing.T) {
	api := NewJournalSnapshotAPI(&journalConsumerStub{})
	cases := []struct {
		name, method, path string
		status             int
	}{
		{"extra segment", http.MethodGet, dispatchJournalAPIPath + "tenant/request/extra", http.StatusBadRequest},
		{"method", http.MethodPost, dispatchJournalAPIPath + "tenant/request", http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r = SetAuthContext(r, &AuthContext{Role: "tenant_admin", TenantID: "tenant"})
			response := httptest.NewRecorder()
			api.ServeHTTP(response, r)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
		})
	}
}

func TestJournalSnapshotAPINilConsumerReturnsUnavailable(t *testing.T) {
	response := httptest.NewRecorder()
	NewJournalSnapshotAPI(nil).ServeHTTP(response, journalRequest(dispatchJournalAPIPath+"tenant/request", "tenant_admin", "tenant"))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
