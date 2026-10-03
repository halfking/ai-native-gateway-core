// Package bg — active_probe_executor_gateway_probe_test.go
//
// R40（R39 §七.4 移交收口）钉测：队列模式 gateway 轮（RunGateway）必须与
// legacy 直连轮同源走 directProbeBody 的形态分发。旧实现硬编码文本 ping，
// 对音频模型必被上游按内容拒收，每次探测写一条 http_400 审计行——回归
// 形态：音频模型（-asr 族）的 gateway 探针体应携带 input_audio 桥接载荷，
// 文本模型仍是 {"content":"ping"}。
package bg

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunGatewayAudioModelProbeShape(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"probe","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	e := &ActiveProbeExecutor{}
	e.SetGateway(srv.URL, "test-key", srv.Client())

	// 音频模型（转写族）：探针体应含 input_audio 载荷，不再文本 ping。
	res := e.RunGateway(t.Context(), &ProbeTarget{
		CredentialID: 7, ProviderID: 1,
		RawModel: "mimo-v2.5-asr", Protocol: "openai-completions",
	})
	if res == nil || res.Status != ProbeStatusSuccess {
		t.Fatalf("RunGateway(audio) status = %+v, want success (res=%+v)", res != nil, res)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("gateway probe body is not JSON: %q", gotBody)
	}
	if !strings.Contains(gotBody, "input_audio") {
		t.Errorf("audio model gateway probe body lacks input_audio bridge payload: %q", gotBody)
	}
	if strings.Contains(gotBody, `"content":"ping"`) {
		t.Errorf("audio model gateway probe body still uses text ping: %q", gotBody)
	}

	// 文本模型：维持原文本 ping 形态（回归保护）。
	res = e.RunGateway(t.Context(), &ProbeTarget{
		CredentialID: 8, ProviderID: 1,
		RawModel: "gpt-5.6-terra", Protocol: "openai-completions",
	})
	if res == nil || res.Status != ProbeStatusSuccess {
		t.Fatalf("RunGateway(text) status = %+v, want success (res=%+v)", res != nil, res)
	}
	if !strings.Contains(gotBody, `"content":"ping"`) {
		t.Errorf("text model gateway probe body should stay text ping, got: %q", gotBody)
	}
}
