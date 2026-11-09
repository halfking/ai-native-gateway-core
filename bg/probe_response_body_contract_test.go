package bg

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProbeResponseBodyContract(t *testing.T) {
	body := `{"choices":[{"message":{"content":"` + strings.Repeat("pong ", 160) + `"}}],"usage":{"prompt_tokens":17,"completion_tokens":3}}`
	for _, mode := range []string{"direct", "gateway", "models", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			response := body
			if mode == "models" {
				response = `{"data":[{"id":"minimax-m3","description":"` + strings.Repeat("model ", 120) + `"}]}`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "fallback" && strings.HasSuffix(r.URL.Path, "/responses") {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"message":"provider does not support the Responses API"}}`)
					return
				}
				_, _ = io.WriteString(w, response)
			}))
			defer srv.Close()
			executor := &ActiveProbeExecutor{httpClient: srv.Client()}
			target := &ProbeTarget{BaseURL: srv.URL, RawModel: "minimax-m3", Protocol: "openai-completions"}
			var result *ProbeResult
			switch mode {
			case "gateway":
				executor.SetGateway(srv.URL, "test-key", srv.Client())
				result = executor.RunGateway(t.Context(), target)
			case "models":
				result = executor.runModelsList(t.Context(), target)
			default:
				if mode == "fallback" {
					target.Protocol = "openai-responses"
				}
				result = executor.Run(t.Context(), target)
			}
			require.Equal(t, ProbeStatusSuccess, result.Status)
			require.JSONEq(t, response, result.ResponseBody, "body must survive beyond the 500-byte preview")
			require.LessOrEqual(t, len(result.RespPreview), 500)
			entry := buildProbeRequestLogEntry(7, 1, "default", target.RawModel, target.RawModel, "direct", "", 1, result)
			require.NotNil(t, entry.ResponsePreview)
			require.Equal(t, result.RespPreview, *entry.ResponsePreview)
			require.NotNil(t, entry.ResponseBody)
			require.True(t, json.Valid([]byte(*entry.ResponseBody)))
			if mode != "models" {
				pt, ct, ok := parseProbeUsage(*entry.ResponseBody)
				require.True(t, ok)
				require.Equal(t, 17, pt)
				require.Equal(t, 3, ct)
			}
		})
	}
}
