package streaming

import (
	"net/http"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"
	"github.com/kaixuan/llm-gateway-go/domains/authentication"
)

type recordingProvisionalExtractor struct {
	mu    sync.Mutex
	calls []provisionalExtractCall
}

type provisionalExtractCall struct {
	tenantID  string
	sessionID string
	taskID    string
	in        sessionmeta.Input
}

func (r *recordingProvisionalExtractor) MaybeGenerateProvisionalMetadata(tenantID, sessionID, taskID string, in sessionmeta.Input) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, provisionalExtractCall{
		tenantID:  tenantID,
		sessionID: sessionID,
		taskID:    taskID,
		in:        in,
	})
}

func (r *recordingProvisionalExtractor) lenCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func TestInvokeProvisionalMetadataOnArrival_CallsExtractor(t *testing.T) {
	rec := &recordingProvisionalExtractor{}
	h := &ChatHandler{}
	h.SetProvisionalMetadataExtractor(rec)

	body := []byte(`{"messages":[{"role":"user","content":"修复 session summary bug"}]}`)
	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Gw-Task-Id", "task-42")
	req.Header.Set("X-Gw-Client-Type", "cursor")

	logCtx := &RequestLogContext{
		meta: requestAttemptMeta{
			AgentName:      "cursor",
			AgentType:      "coding_agent",
			ClientProtocol: "openai",
			ProjectID:      "acc-42",
		},
		WorkType: "debugging",
	}

	h.invokeProvisionalMetadataOnArrival(req, "gw_sessionmeta_test", &authentication.KeyInfo{TenantID: "tenant-a"}, logCtx, body)
	if rec.lenCalls() != 1 {
		t.Fatalf("calls = %d, want 1", rec.lenCalls())
	}
	call := rec.calls[0]
	if call.tenantID != "tenant-a" || call.sessionID != "gw_sessionmeta_test" || call.taskID != "task-42" {
		t.Fatalf("unexpected call identity: %+v", call)
	}
	if string(call.in.RequestBody) != string(body) {
		t.Fatalf("RequestBody not forwarded")
	}
	if call.in.AgentName != "cursor" || call.in.WorkType != "debugging" || call.in.ProjectRef != "acc-42" {
		t.Fatalf("metadata not forwarded: %+v", call.in)
	}
}

func TestInvokeProvisionalMetadataOnArrival_SkipsBranchSession(t *testing.T) {
	rec := &recordingProvisionalExtractor{}
	h := &ChatHandler{}
	h.SetProvisionalMetadataExtractor(rec)

	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	logCtx := &RequestLogContext{}

	for _, sessionID := range []string{"gt_branch", "gs_branch"} {
		h.invokeProvisionalMetadataOnArrival(req, sessionID, &authentication.KeyInfo{TenantID: "tenant-a"}, logCtx, []byte(`{"messages":[]}`))
	}
	if rec.lenCalls() != 0 {
		t.Fatalf("branch sessions should not invoke extractor, calls = %d", rec.lenCalls())
	}
}

func TestInvokeProvisionalMetadataOnArrival_SkipsWhenExtractorNil(t *testing.T) {
	h := &ChatHandler{}
	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	h.invokeProvisionalMetadataOnArrival(req, "gw_ok", &authentication.KeyInfo{TenantID: "tenant-a"}, &RequestLogContext{}, []byte(`{"messages":[]}`))
}
