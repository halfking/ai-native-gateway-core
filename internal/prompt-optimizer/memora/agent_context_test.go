package memora

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeBody(t *testing.T, r *http.Request, out *map[string]any) {
	t.Helper()
	_ = json.NewDecoder(r.Body).Decode(out)
}

func newAgentContextTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := Config{
		BaseURL:   srv.URL,
		JWTSecret: "test-secret",
		ProjectID: "proj-1",
	}
	c, err := New(cfg)
	require.NoError(t, err)
	return c, srv
}

func TestAgentContext_Success(t *testing.T) {
	var gotPath, gotAuth, gotProject, gotAgentID, gotUserID string
	c, _ := newAgentContextTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotProject = r.Header.Get("X-Project-ID")
		var body map[string]any
		decodeBody(t, r, &body)
		gotAgentID, _ = body["agent_id"].(string)
		gotUserID, _ = body["user_id"].(string)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"context": {
				"L1_memories":  [{"id":"m1","content":"L1 fact","layer":"L1"}],
				"L2_episodic":  [{"id":"m2","content":"L2 fact","layer":"L2","tags":["a"]}],
				"L3_consolidated": [{"id":"m3","content":"L3 fact","layer":"L3"}],
				"knowledge":    [{"id":"k1","title":"KB title","summary":"kb summary"}]
			},
			"tokens_used": 42,
			"manifest_id": "mf-1",
			"truncated": true
		}`))
	})

	resp, err := c.AgentContext(context.Background(), AgentContextRequest{UserID: "user-9", ProjectID: "proj-1"})
	require.NoError(t, err)

	assert.Equal(t, "/api/v2/agent-context", gotPath)
	assert.Contains(t, gotAuth, "Bearer ")
	assert.Equal(t, "proj-1", gotProject)
	assert.Equal(t, "llm-gateway-contextagent", gotAgentID, "缺省 agent_id 应自动填网关标识")
	assert.Equal(t, "user-9", gotUserID)

	assert.Len(t, resp.Context.L1Memories, 1)
	assert.Len(t, resp.Context.L2Episodic, 1)
	assert.Len(t, resp.Context.L3Consolidated, 1)
	assert.Len(t, resp.Context.Knowledge, 1)
	assert.Equal(t, 42, resp.TokensUsed)
	assert.True(t, resp.Truncated)
}

func TestAgentContext_ACLForbidden(t *testing.T) {
	c, _ := newAgentContextTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden","message":"project authorization failed: principal_not_owner"}`))
	})

	_, err := c.AgentContext(context.Background(), AgentContextRequest{UserID: "u", ProjectID: "proj-1"})
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr), "应透出 APIError")
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
}

func TestAgentContext_FailClosed503(t *testing.T) {
	c, _ := newAgentContextTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"service_unavailable"}`))
	})

	_, err := c.AgentContext(context.Background(), AgentContextRequest{UserID: "u", ProjectID: "proj-1"})
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
}

func TestAgentContextAsMemories_OrderAndSkipEmpty(t *testing.T) {
	resp := &AgentContextResponse{}
	resp.Context.L1Memories = []AgentContextMemory{{ID: "l1", Content: ""}, {ID: "l1b", Content: "L1 内容"}}
	resp.Context.L2Episodic = []AgentContextMemory{{ID: "l2", Content: "L2 内容"}}
	resp.Context.L3Consolidated = []AgentContextMemory{{ID: "l3", Content: "L3 内容", Score: 0.9}}

	got := AgentContextAsMemories(resp)
	require.Len(t, got, 3, "空内容条目应跳过")
	// 顺序 L2 → L3 → L1（长期层语义优先）
	assert.Equal(t, "l2", got[0].ID)
	assert.Equal(t, "l3", got[1].ID)
	assert.Equal(t, "l1b", got[2].ID)
	assert.Equal(t, 0.9, got[1].Score)
}

func TestAgentContextAsMemories_Nil(t *testing.T) {
	assert.Nil(t, AgentContextAsMemories(nil))
}

func TestAgentContextAsMemories_MatchesMemoryShape(t *testing.T) {
	// 压平结果必须可直接当 memory.Memory 用（LongTermLoader 契约）。
	resp := &AgentContextResponse{}
	resp.Context.L2Episodic = []AgentContextMemory{{ID: "m2", Content: "内容", Tags: []string{"t"}}}
	got := AgentContextAsMemories(resp)
	require.Len(t, got, 1)
	var _ []memory.Memory = got // 编译期形状校验
}
