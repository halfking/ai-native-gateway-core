//go:build !windows

package streaming

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

type memoryEvidence struct {
	Bytes        int64  `json:"bytes"`
	Concurrency  int    `json:"concurrency"`
	Accepted     int64  `json:"accepted"`
	Rejected     int64  `json:"rejected"`
	HeapDelta    uint64 `json:"heap_delta"`
	RSS          uint64 `json:"rss_bytes"`
	TotalAlloc   uint64 `json:"total_alloc"`
	Milliseconds int64  `json:"milliseconds"`
}

func TestLargeIngressMemoryScaling(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int64{1_000_000, 10_000_000, 100_000_000} {
		for _, parallel := range []int{1, 4, 16} {
			cmd := exec.Command(exe, "-test.run=^TestLargeIngressMemoryChild$", "-test.count=1")
			cmd.Env = append(os.Environ(), "LGW_MEMORY_BYTES="+strconv.FormatInt(size, 10), "LGW_MEMORY_CONCURRENCY="+strconv.Itoa(parallel))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("memory child: %v\n%s", err, out)
			}
			var result memoryEvidence
			found := false
			for _, line := range bytes.Split(out, []byte("\n")) {
				if bytes.HasPrefix(line, []byte("MEMORY_RESULT ")) {
					if err = json.Unmarshal(line[len("MEMORY_RESULT "):], &result); err != nil {
						t.Fatal(err)
					}
					found = true
				}
			}
			if !found {
				t.Fatalf("no evidence: %s", out)
			}
			allowance := uint64((16 + 32*min(parallel, 4)) << 20)
			if result.HeapDelta > allowance {
				t.Errorf("RAW-sized heap allocation: %+v limit=%d", result, allowance)
			}
			if result.Accepted < 1 || result.Accepted+result.Rejected != int64(parallel) {
				t.Errorf("bad admission result: %+v", result)
			}
			b, _ := json.Marshal(result)
			t.Log(string(b))
		}
	}
}
func TestLargeIngressMemoryChild(t *testing.T) {
	text := os.Getenv("LGW_MEMORY_BYTES")
	if text == "" {
		return
	} // Invoked only by the process-isolated scaling test.
	size, _ := strconv.ParseInt(text, 10, 64)
	parallel, _ := strconv.Atoi(os.Getenv("LGW_MEMORY_CONCURRENCY"))
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	ingress := NewLargeIngress(h, store, LargeIngressConfig{})
	var accepted, rejected atomic.Int64
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(r.Body)
		if e != nil || len(body) > 1<<20 || !json.Valid(body) {
			t.Errorf("unbounded downstream: %d %v", len(body), e)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
		accepted.Add(1)
	})
	handler := ingress.Wrap(next)
	runtime.GC()
	var initial runtime.MemStats
	runtime.ReadMemStats(&initial)
	var peak atomic.Uint64
	peak.Store(initial.HeapAlloc)
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				for old := peak.Load(); m.HeapAlloc > old; old = peak.Load() {
					if peak.CompareAndSwap(old, m.HeapAlloc) {
						break
					}
				}
			}
		}
	}()
	start := time.Now()
	barrier := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-barrier
			pre := `{"messages":[{"role":"user","content":"Maintain billing"},{"role":"assistant","content":"`
			post := `"},{"role":"user","content":"Find billing port"}],"model":"m"}`
			reader := io.MultiReader(strings.NewReader(pre), io.LimitReader(&fillIngress{}, size-int64(len(pre)+len(post))), strings.NewReader(post))
			r := httptest.NewRequest("POST", "/v1/chat/completions", reader)
			r.ContentLength = -1
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code == 429 {
				rejected.Add(1)
			} else if w.Code != 200 {
				t.Errorf("unexpected %d: %s", w.Code, w.Body.String())
			}
		}()
	}
	close(barrier)
	wg.Wait()
	close(stop)
	<-stopped
	var final runtime.MemStats
	runtime.ReadMemStats(&final)
	var usage syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
	rss := uint64(usage.Maxrss)
	if runtime.GOOS != "darwin" {
		rss *= 1024
	}
	delta := uint64(0)
	if peak.Load() > initial.HeapAlloc {
		delta = peak.Load() - initial.HeapAlloc
	}
	evidence := memoryEvidence{size, parallel, accepted.Load(), rejected.Load(), delta, rss, final.TotalAlloc - initial.TotalAlloc, time.Since(start).Milliseconds()}
	data, _ := json.Marshal(evidence)
	fmt.Printf("MEMORY_RESULT %s\n", data)
}
