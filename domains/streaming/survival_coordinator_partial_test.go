package streaming

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

type successfulPartialExecutor struct {
	partial string
}

func (e *successfulPartialExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	if _, err := params.W.Write([]byte(e.partial)); err != nil {
		return nil, err
	}
	return &executors.ExecuteResult{}, nil
}

func TestSurvivalCoordinatorFlushesSuccessfulTrailingPartial(t *testing.T) {
	const partial = `data: {"choices":[{"delta":{"content":"tail"}}]}`
	h := newCoordHarness(nil)
	c := h.coordinator()
	c.Exec = &successfulPartialExecutor{partial: partial}

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{IsStream: true})
	if !res.Succeed {
		t.Fatalf("expected success, decision=%v reason=%s", res.Decision.Action, res.Decision.Reason)
	}
	if got := h.flusher.buf.String(); !strings.Contains(got, partial) {
		t.Fatalf("successful trailing partial missing from wire: %q", got)
	}
}
