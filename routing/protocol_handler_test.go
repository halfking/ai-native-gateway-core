package routing

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// stubProtocolHandler captures what an executor would call
// so we can assert the interface contract.
type stubProtocolHandler struct {
	buildReqCalled       bool
	writeNonStreamCalled bool
	streamRespCalled     bool
	extractUsageCalled   bool
	softMismatchCalled   bool
	reqModel, respModel  string
}

func (s *stubProtocolHandler) BuildRequest(cand provider.Candidate, body []byte, isStream bool) (*http.Request, error) {
	s.buildReqCalled = true
	req := httptest.NewRequest("POST", "http://upstream/v1/messages", strings.NewReader(string(body)))
	req.Header.Set("x-api-key", cand.APIKey)
	return req, nil
}

func (s *stubProtocolHandler) WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel, qualityFixMode string, qualitySignals *QualitySignals) error {
	s.writeNonStreamCalled = true
	return nil
}

func (s *stubProtocolHandler) StreamResponse(w http.ResponseWriter, resp *http.Response) StreamOutcome {
	s.streamRespCalled = true
	return StreamOutcome{}
}

func (s *stubProtocolHandler) ExtractUsage(resp *http.Response, body []byte) (inputTokens, outputTokens *int) {
	s.extractUsageCalled = true
	return nil, nil
}

func (s *stubProtocolHandler) CheckSoftMismatch(reqModel, respModel string) (mismatched bool, reason string) {
	s.softMismatchCalled = true
	s.reqModel = reqModel
	s.respModel = respModel
	return false, ""
}

// Verify ProtocolHandler interface is the union of 5 methods.
func TestProtocolHandler_InterfaceContract(t *testing.T) {
	var _ ProtocolHandler = (*stubProtocolHandler)(nil)
}
