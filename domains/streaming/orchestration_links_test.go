package streaming

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/internal/largebody"
	"github.com/kaixuan/llm-gateway-go/middleware"
)

type recordingOrchestrationLinks struct {
	mu               sync.Mutex
	parentID         string
	registeredTenant string
	registeredKey    int
	registrations    int
	claimed          map[string]string
	bodyFingerprints map[string]string
}

func (l *recordingOrchestrationLinks) Register(_ context.Context, tenant string, key int, parentID, bodySHA string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.registeredTenant, l.registeredKey, l.parentID = tenant, key, parentID
	l.registrations++
	token := fmt.Sprintf("%064x", l.registrations)
	l.claimed[token] = ""
	l.bodyFingerprints[token] = bodySHA
	return token, nil
}

func (l *recordingOrchestrationLinks) Claim(_ context.Context, token, tenant string, key int, childID, bodySHA string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	previous, exists := l.claimed[token]
	if !exists || previous != "" || tenant != l.registeredTenant || key != l.registeredKey ||
		len(bodySHA) != 64 || bodySHA != l.bodyFingerprints[token] {
		return false, nil
	}
	l.claimed[token] = childID
	return true, nil
}

func TestTextOrchestrationLinksServerIssuedIDsThroughHTTP(t *testing.T) {
	key := &authentication.KeyInfo{ID: 21, TenantID: "actual-tenant", Status: "active"}
	verifier := &stubKeyVerifier{info: key}
	svc := NewSummarizeService()
	svc.keyVerifier = verifier
	chat := NewChatHandler(nil, nil, nil, nil, nil, nil)
	chat.keyVerifier = verifier
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	links := &recordingOrchestrationLinks{claimed: make(map[string]string), bodyFingerprints: make(map[string]string)}
	wal := &memoryModalityWAL{}
	var childCalls atomic.Int64
	mux := http.NewServeMux()
	mux.Handle("/v1/summarize/title", ObserveModalityIngress(NewSummarizeHandler(svc), wal, nil))
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(middleware.OrchestrationLinkHeader) != "" {
			t.Error("link capability reached the protocol handler")
		}
		if childCalls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"malformed candidates"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"[\"日对账覆盖核验\"]"}}]}`))
	})
	h := middleware.Chain{middleware.NewRequestIDMiddleware(), gatewayAdmissionObserver{wal: wal, links: links}}
	srv := httptest.NewServer(h.Then(NewLargeIngress(chat, store, LargeIngressConfig{}).Wrap(mux)))
	defer srv.Close()
	svc.baseURL = srv.URL
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/v1/summarize/title",
		strings.NewReader(`{"model":"m","text":"客户请求的日对账覆盖核验"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sk-caller")
	req.Header.Set("X-Request-Id", "caller-chosen")
	req.Header.Set(middleware.OrchestrationLinkHeader, "caller-supplied")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("parent status=%d", resp.StatusCode)
	}
	parentID := resp.Header.Get("X-Request-Id")
	links.mu.Lock()
	defer links.mu.Unlock()
	if parentID == "" || parentID == "caller-chosen" || links.parentID != parentID || links.registeredTenant != key.TenantID ||
		links.registeredKey != key.ID || links.registrations != 2 || childCalls.Load() != 2 {
		t.Fatalf("unexpected parent=%q tenant=%s key=%d registrations=%d calls=%d", links.parentID,
			links.registeredTenant, links.registeredKey, links.registrations, childCalls.Load())
	}
	childIDs := make(map[string]bool)
	for _, id := range links.claimed {
		if id == "" || id == parentID || childIDs[id] {
			t.Fatalf("invalid or repeated child ID %q", id)
		}
		childIDs[id] = true
	}
}
