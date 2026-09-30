package executors

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// D12 出口代理 —— 真实请求构建点的 egress 盖章回归钉桩。
//
// 为什么这条必须存在：R28-P-1 把 WithEgressMeta 打在
// ChatExecutor.BuildRequest 上，而该方法**没有生产调用方**；直到 R30 e2e
// 实测 glm-5.3 静默直连，才改到真实构建点 executor_chat.go 的请求构造处。
// 修复之后**没有任何测试钉住它**——下次任何人搬动/重构请求构造，stamp 会
// 再次悄悄消失，症状是「静默直连」，恰是 D12 最严的禁止项。
//
// 本测试不看源码文本，而是在**真实消费点**取值：upstream.Client.Do 只有在
// 请求 context 携带 EgressMeta 且 ProviderID > 0 时才会走 egress transport，
// 否则回落到默认 transport（即直连）。所以「egress transport 被问到正确的
// ProviderID」等价于「真实构建点盖了章」。

// recordingEgressTransport records which provider IDs asked for an egress
// transport, and short-circuits the dial so the test never touches a network.
type recordingEgressTransport struct {
	mu    sync.Mutex
	asked []int
	reply string
}

func (r *recordingEgressTransport) TransportFor(_ context.Context, providerID int) (http.RoundTripper, error) {
	r.mu.Lock()
	r.asked = append(r.asked, providerID)
	r.mu.Unlock()
	return roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(r.reply)),
		}, nil
	}), nil
}

func (r *recordingEgressTransport) askedIDs() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int, len(r.asked))
	copy(out, r.asked)
	return out
}

const chatOKBody = `{"id":"chatcmpl-egress","object":"chat.completion","model":"glm-5.3",` +
	`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":1,"completion_tokens":1}}`

// TestLiveChatBuildPathStampsEgressMeta is the regression pin for the R29→R30
// incident: a provider that requires the egress proxy must be routed through
// the egress transport, not the default one.
func TestLiveChatBuildPathStampsEgressMeta(t *testing.T) {
	exec, _ := newF04ExecutorWithSlots(t)

	egress := &recordingEgressTransport{reply: chatOKBody}
	up := upstreampkg.NewWithRetries(0)
	up.SetEgressProvider(egress)
	exec.Upstream = up

	candidate := provider.Candidate{
		ProviderID: 4321, // distinctive so a wrong stamp is unmistakable
		BaseURL:    "http://egress-pin.invalid",
		Protocol:   "openai-completions",
		RawModel:   "glm-5.3",
		APIKey:     "test-key",
	}
	params := f04ExecParams(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)
	params.Model = "glm-5.3"
	params.ClientModel = "glm-5.3"

	if _, err := exec.executeOpenAI(params, candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	asked := egress.askedIDs()
	if len(asked) == 0 {
		t.Fatal("live chat build path did NOT stamp egress meta: the request would " +
			"silently fall back to the default transport (direct) for an " +
			"egress_profile='proxy' provider — this is the R29→R30 incident shape")
	}
	for _, id := range asked {
		if id != 4321 {
			t.Fatalf("egress transport asked with ProviderID %d, want 4321", id)
		}
	}
}

// context / roundTripperFunc / stringsReader shims kept local so the test file
// has no dependency on helper names that may be reused elsewhere.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestEveryRequestBuildSiteStampsEgressMeta is the complementary source pin.
//
// The behavioural test above proves ONE live path stamps. This one keeps every
// current request-build site honest, which is the actual R29 failure shape: a
// stamp exists somewhere plausible while the path in production does not carry
// it. Scanning the source is the cheap half; the behavioural test is the half
// that proves the live path really is one of the stamped ones.
//
// A NEW request-construction site that forgets the stamp is the regression this
// catches: it shows up as a build site with no WithEgressMeta nearby, and the
// failure mode is silent direct connect.
func TestEveryRequestBuildSiteStampsEgressMeta(t *testing.T) {
	root := repoRootForTest(t)
	files := []string{
		"domains/streaming/executors/executor_chat.go",
		"domains/streaming/executors/executor_anthropic.go",
		"domains/streaming/executors/executor_ollama.go",
		"domains/streaming/executors/context_summarize.go",
	}
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			// Every place that constructs an outbound request with a context
			// must be followed by an egress stamp BEFORE the next build site.
			if !strings.Contains(line, "http.NewRequestWithContext(") {
				continue
			}
			// The bound is a function of the boilerplate between the two:
			// sites measured 3/6/6/6/15 lines apart (err-check + comments).
			// 24 clears the widest with headroom. The second bound is the
			// important one: stopping at the NEXT build site stops a wide
			// window from being "credited" with a later stamp, which is
			// exactly the R29 shape (stamp somewhere plausible, not here).
			end := min(i+24, len(lines))
			for j := i + 1; j < end; j++ {
				if strings.Contains(lines[j], "http.NewRequestWithContext(") {
					end = j
					break
				}
			}
			window := strings.Join(lines[i:end], "\n")
			if !strings.Contains(window, "WithEgressMeta(") {
				t.Errorf("%s:%d builds an outbound request with no egress stamp before the next build site:\n%s\n"+
					"an egress_profile='proxy' provider would silently connect direct (R29->R30 incident shape)",
					rel, i+1, line)
			}
		}
	}
}

// repoRootForTest walks up to the module root using this file's own location.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}
