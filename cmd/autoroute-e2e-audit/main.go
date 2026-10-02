// cmd/autoroute-e2e-audit — auto 匹配能力端到端审计采集器。
//
// 读取 autoroute/testdata/auto_matching_suite.jsonl（与离线回归同一套件），
// 逐条以 model=auto 打网关 /v1/chat/completions，解析 X-Gw-Auto-Decision
// 响应头，对照 expected_task 判定分类正确性，并记录所选 credential/model、
// 候选 top3、时延与实际服务模型（dispatch 失败换模型后可能与 chosen 不一致）。
//
// 用法：
//
//	# 默认：每个用例发一次真实上游调用（消耗 LLM token）
//	go run ./cmd/autoroute-e2e-audit \
//	  -gateway http://127.0.0.1:8782 \
//	  -api-key sk-xxx \
//	  -suite autoroute/testdata/auto_matching_suite.jsonl \
//	  -out /tmp/auto_e2e_results.jsonl
//
//	# 2026-09-29: 不消耗上游 token 的回归模式。仅验证 auto 决策层
//	# （X-Gw-Auto-Decision header + 选中的 model），网关发回合成
//	# OpenAI 响应。可对 252 / 本地高频跑 — Pass / Fail 判定与
//	# live 模式等价。X-Gw-Source-Actor 由本工具内置为
//	# "autoroute-e2e-audit"（受信任白名单成员），无需手动配置。
//	go run ./cmd/autoroute-e2e-audit \
//	  -gateway http://252.llm-gateway:8782 \
//	  -api-key sk-xxx \
//	  -test-mode mock \
//	  -suite autoroute/testdata/auto_matching_suite.jsonl \
//	  -out /tmp/auto_e2e_mock_results.jsonl
//
// 设计约定见 docs/audit/2026-09-14-auto-matching-prompt-e2e-audit.md。
// 不在参数或仓内存储真实 key；超时/上游错误如实记录为 error 结果行。
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// suiteCase 与 autoroute/auto_matching_suite_test.go 保持同构（无法共享包，
// 该结构体定义在 _test.go 里）。
type suiteCase struct {
	Name         string `json:"name"`
	Bucket       string `json:"bucket"`
	ExpectedTask string `json:"expected_task"`
	KnownFailure bool   `json:"known_failure"`
	ExpectNote   string `json:"expect_note"`
	System       string `json:"system"`
	ClientType   string `json:"client_type"`
	Prompt       string `json:"prompt"`
	Tools        int    `json:"tools"`
	ToolResults  bool   `json:"tool_results"`
	Image        bool   `json:"image"`
	PadToTokens  int    `json:"pad_to_tokens"`
}

// autoDecision 是 X-Gw-Auto-Decision 头的 wire 格式（domains/streaming/auto_route.go）。
type autoDecision struct {
	TaskType       string  `json:"task_type"`
	Confidence     float64 `json:"confidence"`
	Profile        string  `json:"profile"`
	Classifier     string  `json:"classifier"`
	Reason         string  `json:"reason"`
	ChosenModel    string  `json:"chosen_model"`
	ChosenRawModel string  `json:"chosen_raw_model"`
	ChosenCredID   int64   `json:"chosen_credential_id"`
	CacheReused    bool    `json:"cache_reused"`
	FallbackUsed   bool    `json:"fallback_used"`
	CandidatesTop3 []struct {
		Model          string  `json:"model"`
		CompositeScore float64 `json:"composite_score"`
		PriceScore     float64 `json:"price_score"`
		ChannelQuality float64 `json:"channel_quality"`
		Reliability    float64 `json:"reliability"`
		RouteTier      string  `json:"route_tier"`
	} `json:"candidates_top3"`
}

type resultRow struct {
	Name         string        `json:"name"`
	Bucket       string        `json:"bucket"`
	ExpectedTask string        `json:"expected_task"`
	KnownFailure bool          `json:"known_failure"`
	HTTPStatus   int           `json:"http_status"`
	Error        string        `json:"error,omitempty"`
	LatencyMS    int64         `json:"latency_ms"`
	ServedModel  string        `json:"served_model,omitempty"`
	GotTask      string        `json:"got_task,omitempty"`
	Pass         *bool         `json:"pass"`
	Decision     *autoDecision `json:"decision,omitempty"`
	// 2026-09-29: TestMode echoes the X-Gw-Test-Mode header sent on the wire
	// so a downstream reader can split live rows from mock rows. Empty means
	// "live / no override" — the audit ran the real upstream path. This is
	// how scripts can answer "how many cases did we save by mocking?" by
	// simply counting non-empty TestMode rows.
	TestMode string `json:"test_mode,omitempty"`
}

const paddingParagraph = "This section of the transcript discusses the quarterly platform review, including reliability metrics, capacity planning notes, incident timelines, and follow-up actions agreed by the team. "

// estimateTokens mirrors the gateway (ascii/4 + cjk*2) so padding lands just
// over the long-context threshold like the offline harness.
func estimateTokens(b []byte) int {
	ascii, cjk := 0, 0
	for _, r := range string(b) {
		if r >= 0x4E00 && r <= 0x9FFF {
			cjk++
		} else {
			ascii++
		}
	}
	return ascii/4 + cjk*2
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func isConnRefused(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "EOF") && strings.Contains(err.Error(), "Post"))
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "autoroute-e2e-audit: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	gateway := flag.String("gateway", "http://127.0.0.1:8782", "gateway base URL")
	apiKey := flag.String("api-key", os.Getenv("AUTO_AUDIT_API_KEY"), "gateway API key (or AUTO_AUDIT_API_KEY); required")
	suite := flag.String("suite", "autoroute/testdata/auto_matching_suite.jsonl", "suite JSONL path")
	out := flag.String("out", "", "results JSONL output path (default stdout only)")
	maxTokens := flag.Int("max-tokens", 24, "completion budget per case")
	timeout := flag.Duration("timeout", 180*time.Second, "per-request timeout")
	// 2026-09-29: X-Gw-Test-Mode integration. Default "" sends a real upstream
	// call (legacy behaviour); "mock" sets X-Gw-Test-Mode: mock so the
	// gateway returns the synthetic OpenAI body without burning an LLM
	// token. The mock path is the cost-regression option for running the
	// audit suite against a live gateway (252, 245, local) on a tight
	// cadence — it only verifies the auto matching decision, not the
	// upstream response. The decision header X-Gw-Auto-Decision still
	// arrives in the response so Pass / Fail verdicts are identical.
	//
	// The gateway authorises non-live modes with a shared secret
	// (LLM_GW_TEST_MODE_TOKEN on the server, AUTO_AUDIT_TEST_MODE_TOKEN
	// here), NOT with X-Gw-Source-Actor: that header is stripped by the
	// gateway's loopback middleware for every non-loopback caller, so an
	// actor-based gate silently downgrades mock to live — and the run then
	// burns real upstream tokens while appearing to be a free mock run.
	// runCase therefore verifies the X-Gw-Mock-Marker response header and
	// reports a loud error when a requested mock did not take effect.
	testMode := flag.String("test-mode", "", "X-Gw-Test-Mode value sent on every request; '' = live, 'mock' = no upstream call (auto decision only)")
	testModeToken := flag.String("test-mode-token", os.Getenv("AUTO_AUDIT_TEST_MODE_TOKEN"), "shared secret matching the gateway's LLM_GW_TEST_MODE_TOKEN; required when -test-mode is set")
	flag.Parse()

	if strings.TrimSpace(*apiKey) == "" {
		fatalf("-api-key or AUTO_AUDIT_API_KEY is required; do not store real keys in the repository")
	}
	if strings.TrimSpace(*testMode) != "" && strings.TrimSpace(*testModeToken) == "" {
		// Fail before the first request: without the secret the gateway
		// downgrades every request to live, and the run would spend real
		// upstream money while reporting mock rows.
		fatalf("-test-mode %q requires -test-mode-token (or AUTO_AUDIT_TEST_MODE_TOKEN) matching the gateway's LLM_GW_TEST_MODE_TOKEN; without it the gateway silently runs LIVE and this run costs real tokens",
			*testMode)
	}

	cases := loadSuite(*suite)
	var writer io.Writer = os.Stdout
	var outFile *os.File
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fatalf("create out: %v", err)
		}
		defer f.Close()
		outFile = f
		writer = f
	}
	enc := json.NewEncoder(writer)

	client := &http.Client{Timeout: *timeout}
	passCount, failCount, errCount, knownXpass := 0, 0, 0, 0
	for i, tc := range cases {
		row := runCase(client, *gateway, *apiKey, *testMode, *testModeToken, tc, *maxTokens)
		_ = enc.Encode(row)
		switch {
		case row.Error != "":
			errCount++
			fmt.Fprintf(os.Stderr, "[%02d] %-34s ERROR %s\n", i+1, tc.Name, row.Error)
		case *row.Pass:
			passCount++
			if tc.KnownFailure {
				knownXpass++
			}
			fmt.Fprintf(os.Stderr, "[%02d] %-34s PASS  got=%s model=%s cred=%d %dms\n",
				i+1, tc.Name, row.GotTask, row.ServedModel, row.Decision.ChosenCredID, row.LatencyMS)
		default:
			failCount++
			fmt.Fprintf(os.Stderr, "[%02d] %-34s FAIL  got=%s want=%s model=%s\n",
				i+1, tc.Name, row.GotTask, tc.ExpectedTask, row.ServedModel)
		}
	}
	fmt.Fprintf(os.Stderr, "\nsummary: total=%d pass=%d fail=%d error=%d known_xpass=%d\n",
		len(cases), passCount, failCount, errCount, knownXpass)
	_ = outFile
}

func loadSuite(path string) []suiteCase {
	f, err := os.Open(path)
	if err != nil {
		fatalf("open suite: %v", err)
	}
	defer f.Close()
	var cases []suiteCase
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var c suiteCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			fatalf("suite line %d: %v", lineNo, err)
		}
		cases = append(cases, c)
	}
	if len(cases) == 0 {
		fatalf("suite %s has no cases", path)
	}
	return cases
}

// buildMessages assembles the chat body for one case, mirroring the signal
// semantics the gateway extracts (tools array, role=tool message, image part,
// long-context padding).
func buildMessages(tc suiteCase, padParagraph func(string) string) (messages []map[string]any) {
	prompt := tc.Prompt
	if tc.PadToTokens > 0 {
		prompt = padParagraph(prompt)
	}
	if tc.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": tc.System})
	}
	if tc.Image {
		messages = append(messages, map[string]any{"role": "user", "content": []map[string]any{
			{"type": "text", "text": prompt},
			{"type": "image_url", "image_url": map[string]string{
				"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
			}},
		}})
		return messages
	}
	messages = append(messages, map[string]any{"role": "user", "content": prompt})
	if tc.ToolResults && tc.Tools > 0 {
		messages = append(messages, map[string]any{
			"role":    "assistant",
			"content": nil,
			"tool_calls": []map[string]any{{
				"id":   "call_1",
				"type": "function",
				"function": map[string]any{
					"name":      "tool_1",
					"arguments": `{"q":"x"}`,
				},
			}},
		})
		messages = append(messages, map[string]any{
			"role":         "tool",
			"tool_call_id": "call_1",
			"content":      "tool result sample text",
		})
	}
	return messages
}

func buildTools(n int) []map[string]any {
	tools := make([]map[string]any, 0, n)
	for i := 1; i <= n; i++ {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        fmt.Sprintf("tool_%d", i),
				"description": fmt.Sprintf("generic audit tool %d", i),
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"q": map[string]string{"type": "string"}},
				},
			},
		})
	}
	return tools
}

func runCase(client *http.Client, gateway, apiKey, testMode, testModeToken string, tc suiteCase, maxTokens int) resultRow {
	row := resultRow{Name: tc.Name, Bucket: tc.Bucket, ExpectedTask: tc.ExpectedTask, KnownFailure: tc.KnownFailure}
	// Surface the mode in the JSONL row so a downstream reader can tell
	// mock-mode rows from live-mode rows without re-running the audit.
	// Empty string means "live / no override"; non-empty is the literal
	// X-Gw-Test-Mode value sent on the wire.
	if testMode != "" {
		row.TestMode = testMode
	}

	pad := func(p string) string {
		body := p
		for estimateTokens([]byte(body)) < tc.PadToTokens {
			body += "\n" + paddingParagraph
		}
		return body
	}

	body := map[string]any{
		"model":      "auto",
		"messages":   buildMessages(tc, pad),
		"max_tokens": maxTokens,
	}
	if tc.Tools > 0 {
		body["tools"] = buildTools(tc.Tools)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		row.Error = fmt.Sprintf("marshal: %v", err)
		return row
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(gateway, "/")+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		row.Error = fmt.Sprintf("new request: %v", err)
		return row
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	// 2026-09-29: X-Gw-Test-Mode integration. The gateway authorises
	// non-live modes with a shared secret, NOT with X-Gw-Source-Actor:
	// that header is in loopback.CorrelationHeaders and is stripped from
	// every request that lacks this process's per-boot loopback token, so
	// an actor-based gate always evaluated false for this external tool
	// and silently downgraded the run back to a real upstream call.
	if testMode != "" {
		req.Header.Set("X-Gw-Test-Mode", testMode)
		if testModeToken != "" {
			req.Header.Set("X-Gw-Test-Mode-Token", testModeToken)
		}
	}
	if tc.ClientType != "" {
		req.Header.Set("X-Gw-Client-Type", tc.ClientType)
	}

	start := time.Now()
	var resp *http.Response
	var doErr error
	// 共享工作区里可能有并行会话触发 deploy-local 重启网关；对连接层错误
	// 做短退避重试，跨过部署窗口（不影响业务语义，只影响采集成功率）。
	for attempt := 0; attempt < 4; attempt++ {
		req.Body = io.NopCloser(bytes.NewReader(raw))
		req.ContentLength = int64(len(raw))
		resp, doErr = client.Do(req)
		if doErr == nil || !isConnRefused(doErr) {
			break
		}
		time.Sleep(20 * time.Second)
	}
	row.LatencyMS = time.Since(start).Milliseconds()
	if doErr != nil {
		row.Error = fmt.Sprintf("do: %v", doErr)
		return row
	}
	defer resp.Body.Close()
	row.HTTPStatus = resp.StatusCode

	// 2026-09-29: verify the requested test mode actually took effect
	// before trusting any verdict. A gateway that rejects the token
	// silently downgrades to live and issues a REAL upstream call; the
	// decision header still arrives, so Pass/Fail would look identical
	// while the run quietly spent money. Asserting the marker here is
	// what turns a silent downgrade into a loud, attributable error.
	if strings.TrimSpace(testMode) != "" && testMode != "full" && testMode != "live" {
		if marker := resp.Header.Get("X-Gw-Mock-Marker"); marker == "" {
			row.Error = fmt.Sprintf("test_mode %q requested but gateway returned no X-Gw-Mock-Marker — the mode was rejected and this request ran LIVE against the upstream provider", testMode)
			return row
		}
	}

	if dh := resp.Header.Get("X-Gw-Auto-Decision"); dh != "" {
		var dec autoDecision
		if err := json.Unmarshal([]byte(dh), &dec); err == nil {
			row.Decision = &dec
			row.GotTask = dec.TaskType
		}
	}

	var respBody struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Model string `json:"model"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	respRaw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(respRaw, &respBody); err == nil {
		row.ServedModel = respBody.Model
		if respBody.Error != nil {
			row.Error = fmt.Sprintf("upstream %s: %s", respBody.Error.Code, respBody.Error.Message)
		}
	}

	if row.Error != "" {
		return row
	}
	if resp.StatusCode != http.StatusOK {
		row.Error = fmt.Sprintf("http %d: %s", resp.StatusCode, ansiRe.ReplaceAllString(string(respRaw), ""))
		return row
	}
	pass := row.GotTask == tc.ExpectedTask
	row.Pass = &pass
	return row
}
