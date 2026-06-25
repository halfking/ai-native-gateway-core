package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/stretchr/testify/assert"
)

type routeNodeRecorderSpy struct {
	successCalls []string
	failureCalls []string
}

func (s *routeNodeRecorderSpy) RecordNodeSuccess(_ context.Context, credentialID int, model, requestID string) error {
	s.successCalls = append(s.successCalls, model)
	_ = credentialID
	_ = requestID
	return nil
}

func (s *routeNodeRecorderSpy) RecordNodeFailure(_ context.Context, credentialID int, model, requestID, errorKind string) error {
	s.failureCalls = append(s.failureCalls, errorKind+":"+model)
	_ = credentialID
	_ = requestID
	return nil
}

func TestLiveRouteNodeRecorder_SuccessAndFailure(t *testing.T) {
	spy := &routeNodeRecorderSpy{}
	rec := NewRouteNodeRecorder(spy)
	ctx := context.Background()

	rec.RecordSuccess(ctx, 100, "gpt-4")
	rec.RecordFailure(ctx, 100, "gpt-4", errorsx.KindRateLimit)
	rec.RecordFailure(ctx, 100, "gpt-4", errorsx.KindNetwork)

	assert.Equal(t, []string{"gpt-4"}, spy.successCalls)
	assert.Equal(t, []string{"rate_limit:gpt-4"}, spy.failureCalls)
}
