package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

func TestLargeIngressArchivesStreamingClientResponse(t *testing.T) {
	directory := t.TempDir()
	store, _ := largebody.NewStore(directory)
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	ingress := NewLargeIngress(h, store, LargeIngressConfig{})
	var id largebody.Identity
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ref, _ := LargeRequestArchive(r.Context())
		id = ref.Identity
		id.RequestID += "-response"
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "data: last\n\n")
	})
	w := httptest.NewRecorder()
	ingress.Wrap(next).ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`)))
	_ = store.Close()
	reopened, err := largebody.NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	ref, err := reopened.Recover(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	f, err := reopened.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, _ := io.ReadAll(f)
	if string(got) != w.Body.String() || !w.Flushed {
		t.Fatalf("archive=%q wire=%q flush=%v", got, w.Body.String(), w.Flushed)
	}
}

func TestArchiveResponsePreservesFlushError(t *testing.T) {
	base := &initialFlushErrorWriter{header: make(http.Header), flushErr: io.ErrClosedPipe}
	wrapped := &archiveResponseWriter{ResponseWriter: base, archive: io.Discard}
	if safeFlush(wrapped) {
		t.Fatal("archive wrapper hid client disconnect")
	}
}
