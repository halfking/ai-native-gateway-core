package executors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/tooloffer"
)

func TestToolOfferReaderUsesActualUpstreamCancellationAndOriginalNames(t *testing.T) {
	caller, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()
	reader, cancelReader := context.WithCancel(context.Background())
	defer cancelReader()
	params := &ExecParams{R: httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(caller), BodyBytes: []byte(`{"tools":[{"name":"caller_function","input_schema":{}}]}`)}
	response := &http.Response{Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(reader)}
	ctx := streamReaderContext(params, response)
	if ctx.Err() != nil || !tooloffer.FromContext(ctx).Has("caller_function") {
		t.Fatalf("lost reader lifetime or caller names: %v", ctx.Err())
	}
	cancelReader()
	if ctx.Err() != context.Canceled {
		t.Fatal("reader cancellation not propagated")
	}
	params.BodyBytes = []byte(`{"tools":[{"type":"namespace","name":"caller_function"}]}`)
	if tooloffer.FromContext(streamReaderContext(params, response)).Len() != 0 {
		t.Fatal("namespace authorized executable name")
	}
}
