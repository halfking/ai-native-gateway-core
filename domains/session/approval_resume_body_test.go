package session

import (
	"context"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestApprovalResumeKeepsExistingOutboundBody(t *testing.T) {
	ctx := context.Background()
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"keep exact session history"}]}`)
	cache := compression.NewSessionCache(nil, nil)
	require.NoError(t, cache.Set(ctx, "tenant-1", "sess-1", &compression.SessionState{SchemaVersion: 1, ApprovalStatus: compression.ApprovalStatePending, AuditScore: 9}, body))
	record := makeRecord(sessionaudit.ApprovalRejected, true, "declined")
	handler := NewApprovalResumeHandler(cache, &fakeApprovalMgr{record: record}, nil, &fakeResponder{}, nil)
	require.NoError(t, handler.ResumeAfterApproval(ctx, "appr-1", "tenant-1"))
	state, got, err := cache.GetOrLoad(ctx, "tenant-1", "sess-1")
	require.NoError(t, err)
	require.Equal(t, string(body), string(got))
	require.Equal(t, compression.ApprovalStateRejected, state.ApprovalStatus)
	require.Equal(t, 9, state.AuditScore)
}
