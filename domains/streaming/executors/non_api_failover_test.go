package executors

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

func TestNonAPIReplyFailsOverBeforeCommit(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	var badKey string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Authorization"))
		if badKey == "" {
			badKey = r.Header.Get("Authorization")
		}
		bad := r.Header.Get("Authorization") == badKey
		mu.Unlock()
		if bad {
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, "<html>private challenge page</html>")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer s.Close()
	e := newOverloadTestExecutor()
	e.Upstream = upstreampkg.NewWithRetries(2)
	defer e.Upstream.Stop()
	defer e.dispatchPipeline.Stop()
	a := overloadTestCandidate(s.URL)
	b := a
	b.CredentialID, b.ProviderID, b.APIKey = 3, 315, "sk-other"
	rec := httptest.NewRecorder()
	result, err := e.Execute(&ExecParams{
		W: rec, R: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:      []byte(`{"model":"gpt-5.6-luna","messages":[],"stream":true}`),
		ClientProtocol: "openai-completions", ClientModel: "gpt-5.6-luna", IsStream: true,
		Candidates:                  []provider.Candidate{a, b},
		Policy:                      &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		DispatchAllowProviderChange: true,
	})
	if err != nil || result == nil {
		t.Fatalf("Execute result/error=%v/%v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) < 2 || keys[0] == keys[len(keys)-1] {
		t.Fatalf("attempt keys=%v, want failure on the first candidate and recovery on another", keys)
	}
	if body := rec.Body.String(); !strings.Contains(body, "recovered") || strings.Contains(body, "challenge") {
		t.Fatalf("client saw an invalid candidate response: %q", body)
	}
}
