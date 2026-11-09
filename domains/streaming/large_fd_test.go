//go:build !windows

package streaming

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

func TestLargeIngressReleasesFileDescriptors(t *testing.T) {
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		if _, err := io.WriteString(w, `{"ok":true}`); err != nil {
			t.Error(err)
		}
	})
	wrapped := NewLargeIngress(h, store, LargeIngressConfig{}).Wrap(next)
	request := func() {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"file descriptor proof"}]}`))
		w := httptest.NewRecorder()
		wrapped.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d: %s", w.Code, w.Body.String())
		}
	}
	request() // Warm shared policy/runtime state before measuring descriptors.
	path := "/proc/self/fd"
	if runtime.GOOS == "darwin" {
		path = "/dev/fd"
	}
	count := func() int {
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	before := count()
	for i := 0; i < 100; i++ {
		request()
	}
	if after := count(); after > before+2 {
		t.Fatalf("request/response archive leaked descriptors: before=%d after=%d", before, after)
	}
}
