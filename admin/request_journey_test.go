package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

type requestJourneyReaderStub struct {
	lastScope     string
	lastTenant    string
	lastRequest   string
	lastModel     string
	lastNode      requestjourney.NodeKey
	listResult    requestjourney.ListResult
	ingressResult requestjourney.IngressListResult
	detail        requestjourney.DetailResult
	err           error
}

func (s *requestJourneyReaderStub) Detail(_ context.Context, tenantID, requestID string) (requestjourney.DetailResult, error) {
	s.lastTenant, s.lastRequest = tenantID, requestID
	return s.detail, s.err
}

func (s *requestJourneyReaderStub) RecentIngress(_ context.Context) (requestjourney.IngressListResult, error) {
	s.lastScope = "all"
	return s.ingressResult, s.err
}

func (s *requestJourneyReaderStub) RecentTotal(_ context.Context, tenantID string) (requestjourney.ListResult, error) {
	s.lastTenant = tenantID
	return s.listResult, s.err
}

func (s *requestJourneyReaderStub) RecentModel(_ context.Context, tenantID, model string) (requestjourney.ListResult, error) {
	s.lastTenant, s.lastModel = tenantID, model
	return s.listResult, s.err
}

func (s *requestJourneyReaderStub) RecentNode(_ context.Context, tenantID string, key requestjourney.NodeKey) (requestjourney.ListResult, error) {
	s.lastTenant, s.lastNode = tenantID, key
	return s.listResult, s.err
}

func (s *requestJourneyReaderStub) RecentModels(_ context.Context, tenantID string) (requestjourney.ModelListResult, error) {
	s.lastTenant = tenantID
	return requestjourney.ModelListResult{ObservationStatus: s.listResult.ObservationStatus, Models: []requestjourney.ModelFIFOSnapshot{}}, s.err
}

func (s *requestJourneyReaderStub) RecentNodes(_ context.Context, tenantID string) (requestjourney.NodeListResult, error) {
	s.lastTenant = tenantID
	return requestjourney.NodeListResult{ObservationStatus: s.listResult.ObservationStatus, Nodes: []requestjourney.NodeFIFOSnapshot{}}, s.err
}

func TestRequestJourneyAPITenantAdminCannotOverrideTenant(t *testing.T) {
	stub := &requestJourneyReaderStub{listResult: requestjourney.ListResult{
		ObservationStatus: requestjourney.ObservationComplete,
		Requests:          []requestjourney.RequestSnapshot{{TenantID: "tenant-a", RequestID: "request-1"}},
	}}
	request := httptest.NewRequest(http.MethodGet, requestJourneyAPIPath+"?tenant=tenant-b", nil)
	request = SetAuthContext(request, &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	response := httptest.NewRecorder()

	NewRequestJourneyAPI(stub).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.lastTenant != "tenant-a" {
		t.Fatalf("queried tenant = %q", stub.lastTenant)
	}
	if strings.Contains(response.Body.String(), "tenant-b") {
		t.Fatalf("response leaked overridden tenant: %s", response.Body.String())
	}
}

func TestRequestJourneyAPISuperAdminSelectsOneTenantAndNode(t *testing.T) {
	stub := &requestJourneyReaderStub{listResult: requestjourney.ListResult{
		ObservationStatus: requestjourney.ObservationComplete,
		Requests:          []requestjourney.RequestSnapshot{},
	}}
	request := httptest.NewRequest(http.MethodGet,
		requestJourneyAPIPath+"?tenant=tenant-b&model=model-a&provider_id=7&credential_id=11", nil)
	request = SetAuthContext(request, &AuthContext{TenantID: "default", Role: "super_admin", IsJWT: true})
	response := httptest.NewRecorder()

	NewRequestJourneyAPI(stub).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.lastTenant != "tenant-b" || stub.lastNode != (requestjourney.NodeKey{Model: "model-a", ProviderID: 7, CredentialID: 11}) {
		t.Fatalf("tenant/node = %q/%#v", stub.lastTenant, stub.lastNode)
	}
}

func TestRequestJourneyAPITenantAdminCannotReadAllIngress(t *testing.T) {
	stub := &requestJourneyReaderStub{}
	request := httptest.NewRequest(http.MethodGet, requestJourneyAPIPath+"/queues?view=total&scope=all", nil)
	request = SetAuthContext(request, &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	response := httptest.NewRecorder()

	NewRequestJourneyAPI(stub).ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.lastScope != "" || stub.lastTenant != "" {
		t.Fatalf("reader was called: scope=%q tenant=%q", stub.lastScope, stub.lastTenant)
	}
}

func TestRequestJourneyAPISuperAdminReadsAllIngressWithoutTenant(t *testing.T) {
	stub := &requestJourneyReaderStub{ingressResult: requestjourney.IngressListResult{
		ObservationStatus: requestjourney.ObservationComplete,
		ObservationScope:  "shared_redis",
		Capacity:          100,
		Requests: []requestjourney.IngressSnapshot{{
			RequestID: "request-1", GatewayInstanceID: "gateway-1",
			Protocol: requestjourney.IngressProtocolChat, PathClass: requestjourney.IngressPathChatCompletions,
			ArrivedAt: time.Unix(1700000000, 0).UTC(), UpdatedAt: time.Unix(1700000001, 0).UTC(),
			Status: requestjourney.IngressStatusFailed, ErrorKind: "missing_key", HTTPStatus: 401,
		}},
	}}
	request := httptest.NewRequest(http.MethodGet, requestJourneyAPIPath+"/queues?view=total&scope=all", nil)
	request = SetAuthContext(request, &AuthContext{TenantID: "default", Role: "super_admin", IsJWT: true})
	response := httptest.NewRecorder()

	NewRequestJourneyAPI(stub).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if stub.lastScope != "all" || !strings.Contains(body, `"scope":"all"`) || !strings.Contains(body, `"observation_scope":"shared_redis"`) {
		t.Fatalf("scope/body = %q/%s", stub.lastScope, body)
	}
	if strings.Contains(body, `"tenant_id"`) {
		t.Fatalf("global ingress exposed tenant identity: %s", body)
	}
}

func TestRequestJourneyAPIQueuesMatchesWebContract(t *testing.T) {
	stub := &requestJourneyReaderStub{listResult: requestjourney.ListResult{ObservationStatus: requestjourney.ObservationComplete}}
	request := httptest.NewRequest(http.MethodGet, requestJourneyAPIPath+"/queues?view=models", nil)
	request = SetAuthContext(request, &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	response := httptest.NewRecorder()

	NewRequestJourneyAPI(stub).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"view":"models"`) || !strings.Contains(body, `"model_snapshots":[]`) {
		t.Fatalf("body = %s", body)
	}
	if stub.lastTenant != "tenant-a" {
		t.Fatalf("tenant = %q", stub.lastTenant)
	}
}

func TestRequestJourneyAPIDetailUsesScopedTenantAndEscapedID(t *testing.T) {
	stub := &requestJourneyReaderStub{detail: requestjourney.DetailResult{
		ObservationStatus: requestjourney.ObservationComplete,
		Journey:           &requestjourney.RequestJourney{TenantID: "tenant-a", RequestID: "request id"},
	}}
	request := httptest.NewRequest(http.MethodGet, requestJourneyAPIPath+"/request%20id", nil)
	request = SetAuthContext(request, &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	response := httptest.NewRecorder()

	NewRequestJourneyAPI(stub).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.lastTenant != "tenant-a" || stub.lastRequest != "request id" {
		t.Fatalf("detail lookup = %q/%q", stub.lastTenant, stub.lastRequest)
	}
}

func TestRequestJourneyAPIRedisDegradationDoesNotExposeError(t *testing.T) {
	stub := &requestJourneyReaderStub{
		listResult: requestjourney.ListResult{
			ObservationStatus: requestjourney.ObservationComplete,
			Requests:          []requestjourney.RequestSnapshot{{RequestID: "request-1"}},
		},
		err: errors.New("dial tcp redis.internal:6379: connection refused"),
	}
	request := httptest.NewRequest(http.MethodGet, requestJourneyAPIPath, nil)
	request = SetAuthContext(request, &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	response := httptest.NewRecorder()

	NewRequestJourneyAPI(stub).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"observation_status":"observation_degraded"`) {
		t.Fatalf("body = %s", body)
	}
	if strings.Contains(body, "redis.internal") || strings.Contains(body, "connection refused") {
		t.Fatalf("body exposed infrastructure error: %s", body)
	}
}

func TestRequestJourneyAPINilReaderGracefullyDegrades(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, requestJourneyAPIPath, nil)
	request = SetAuthContext(request, &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	response := httptest.NewRecorder()

	NewRequestJourneyAPI(nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"requests":[]`) {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
}

// TestRequestJourneyQueuesLifecycleFilter (v4 R1.1/T2): the queues view
// exposes the additive lifecycle_state / retry_at snapshot fields and the
// optional lifecycle_state filter; invalid states are rejected and an absent
// filter keeps every row (backward compatible).
func TestRequestJourneyQueuesLifecycleFilter(t *testing.T) {
	retryAt := time.Unix(1700000123, 0).UTC()
	stub := &requestJourneyReaderStub{listResult: requestjourney.ListResult{
		ObservationStatus: requestjourney.ObservationComplete,
		Requests: []requestjourney.RequestSnapshot{
			{RequestID: "pending-1", LifecycleState: requestjourney.LifecyclePending, RetryAt: &retryAt},
			{RequestID: "done-1", LifecycleState: requestjourney.LifecycleCompleted},
		},
	}}

	request := httptest.NewRequest(http.MethodGet, requestJourneyAPIPath+"/queues?view=total", nil)
	request = SetAuthContext(request, &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	response := httptest.NewRecorder()
	NewRequestJourneyAPI(stub).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{
		`"lifecycle_state":"pending"`,
		`"lifecycle_state":"completed"`,
		`"retry_at":"` + retryAt.Format(time.RFC3339Nano)[:10], // date prefix suffices
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("unfiltered view missing %s: %s", want, body)
		}
	}

	request = httptest.NewRequest(http.MethodGet, requestJourneyAPIPath+"/queues?view=total&lifecycle_state=pending", nil)
	request = SetAuthContext(request, &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	response = httptest.NewRecorder()
	NewRequestJourneyAPI(stub).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("filtered status = %d, body = %s", response.Code, response.Body.String())
	}
	body = response.Body.String()
	if !strings.Contains(body, "pending-1") || strings.Contains(body, "done-1") {
		t.Fatalf("lifecycle filter must keep only pending rows: %s", body)
	}

	request = httptest.NewRequest(http.MethodGet, requestJourneyAPIPath+"/queues?view=total&lifecycle_state=running", nil)
	request = SetAuthContext(request, &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	response = httptest.NewRecorder()
	NewRequestJourneyAPI(stub).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid lifecycle_state must 400, got %d: %s", response.Code, response.Body.String())
	}
}
