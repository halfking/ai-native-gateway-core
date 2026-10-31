package streaming

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // test-only wiring, matches the package's existing import
	"github.com/kaixuan/llm-gateway-go/domains/credential"   //nolint:depguard // test-only wiring
)

// 2026-10-29 identification round — regression gate for the non-chat
// identification wiring.
//
// The defect these tests pin: /v1/responses and /v1/messages built a
// RequestLogContext but never called SetKey (only the rate-limit branch did)
// and never called EnsureCaptured, so the shared identification chain
// (fillAttemptMeta → UA tokens → body marker → system-prompt
// self-identification) never ran. Every side-table row on those paths landed
// with agent_name / client_ip / client_protocol / fingerprint_raw empty —
// including clients that name themselves in the system prompt.
//
// agent_name's only persistence surface is the side table
// (request_context_attrs), whose single constructor is
// BuildContextAttrsEntry — so that is what the assertions read: asserting it
// asserts what the database receives.
//
// Two layers:
//   ① behaviour — the identification chain turns a system-prompt
//      self-identification into a named agent;
//   ② wiring — the two handlers actually call SetKey + EnsureCaptured. The
//      wiring layer is a source-level assertion because the handler's
//      logCtx is not observable from outside the package; it is the only
//      way to pin the exact defect (a missing call in the handler), and it
//      fails loudly if someone removes the call again.

// TestIdentificationChainDerivesAgentFromSystemPrompt pins layer ①: a client
// that identifies itself ONLY in the system prompt (weak generic UA) is
// recognised.
//
// The UA used here ("Go-http-client/1.1") is deliberately a generic client
// library name — exactly the production shape of the ZCode CLI and MiniMax
// Code traffic measured on 211. Before the fix these rows persisted an
// empty agent_name.
func TestIdentificationChainDerivesAgentFromSystemPrompt(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		body     string
		wantName string
	}{
		{
			name:     "responses_instructions_zcode",
			path:     "/v1/responses",
			body:     `{"model":"glm-5.3-flash","instructions":"You are ZCode, an interactive coding agent","input":[]}`,
			wantName: "zcode",
		},
		{
			name:     "messages_system_mavis",
			path:     "/v1/messages",
			body:     `{"model":"glm-5.3-flash","system":"You are Mavis. You run inside MiniMax Code.","messages":[]}`,
			wantName: "minimax-code",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("User-Agent", "Go-http-client/1.1")

			ch := NewChatHandler(credential.NewManager(), credential.NewLimiter(), nil, nil, nil, nil)
			c := ch.NewRequestLogContext(r, "ident-"+tc.name, time.Now())
			c.SetKey(&authentication.KeyInfo{ID: 4242, TenantID: "tenant-ident"})
			// Same order the handlers use: body first, then EnsureCaptured.
			c.Body = []byte(tc.body)
			c.EnsureCaptured()

			attrs := BuildContextAttrsEntry(c, c.KeyInfo, &c.meta, r.Context())
			if attrs == nil {
				t.Fatal("side-table entry not built")
			}
			if attrs.AgentName == nil {
				t.Fatalf("agent_name empty; want %q (the name must come from the system prompt)", tc.wantName)
			}
			if *attrs.AgentName != tc.wantName {
				t.Fatalf("agent_name = %q, want %q", *attrs.AgentName, tc.wantName)
			}
		})
	}
}

// TestIdentificationChainPrefersStrongUserAgent pins that the system-prompt
// fallback does NOT override a strong UA-derived name — the fallback is a
// fallback, not a replacement.
func TestIdentificationChainPrefersStrongUserAgent(t *testing.T) {
	body := `{"model":"m","instructions":"You are ZCode, an interactive coding agent"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	r.Header.Set("User-Agent", "pi/1.4.0 (linux; node/v22.23.4; x64)")

	ch := NewChatHandler(credential.NewManager(), credential.NewLimiter(), nil, nil, nil, nil)
	c := ch.NewRequestLogContext(r, "ident-strong-ua", time.Now())
	c.SetKey(&authentication.KeyInfo{ID: 4242, TenantID: "tenant-ident"})
	c.Body = []byte(body)
	c.EnsureCaptured()

	attrs := BuildContextAttrsEntry(c, c.KeyInfo, &c.meta, r.Context())
	if attrs == nil || attrs.AgentName == nil {
		t.Fatal("agent_name empty for a strong UA")
	}
	if *attrs.AgentName != "pi" {
		t.Fatalf("agent_name = %q, want %q (a strong UA must win over the system-prompt fallback)", *attrs.AgentName, "pi")
	}
}

// TestNonChatHandlersCallIdentificationChain pins layer ②: the two handlers
// must call SetKey and EnsureCaptured.
//
// This is an AST-level assertion on purpose. The defect was a MISSING CALL
// in the handler — a behavioural test that re-derives the context inside the
// test (as an earlier draft did) would still pass after the handler lost the
// call again, i.e. it would not pin the defect at all. Parsing the source
// keeps the assertion on the actual wiring; comments are invisible to the
// AST, so commenting a call out also fails the gate (a plain text search
// would not).
func TestNonChatHandlersCallIdentificationChain(t *testing.T) {
	cases := []struct {
		file string
		// want lists the call shapes that must appear. The shape includes the
		// first argument, because the file also contains a rate-limit branch
		// that calls SetKey(keyInfo) — asserting the bare method name would
		// pass even if the MAIN flow lost its SetKey call, which is exactly
		// the defect being pinned.
		want []string
	}{
		{
			file: "responses.go",
			want: []string{"SetKey(ki)", "EnsureCaptured()"},
		},
		{
			file: "messages.go",
			want: []string{"SetKey(ki)", "EnsureCaptured()"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			calls := goFileCallShapes(t, filepath.Join(".", tc.file))
			for _, want := range tc.want {
				if !calls[want] {
					t.Errorf("%s never calls %s — the identification chain is not wired on this path "+
						"(its rows persist an empty agent_name / client_ip)", tc.file, want)
				}
			}
		})
	}
}

// goFileCallShapes parses a Go file and returns the set of call shapes
// "Method(arg)" invoked anywhere in it. Comments are not part of the AST, so
// a commented-out call does not register.
func goFileCallShapes(t *testing.T, path string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	calls := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		case *ast.Ident:
			name = fn.Name
		default:
			return true
		}
		shape := name + "()"
		if len(call.Args) > 0 {
			switch arg := call.Args[0].(type) {
			case *ast.Ident:
				shape = name + "(" + arg.Name + ")"
			case *ast.CallExpr:
				// Nested call: record the inner callee name so the shape stays
				// readable (e.g. SetClientModel(extractModelFromBody)).
				inner := "…"
				switch fn := arg.Fun.(type) {
				case *ast.SelectorExpr:
					inner = fn.Sel.Name
				case *ast.Ident:
					inner = fn.Name
				}
				shape = name + "(" + inner + ")"
			default:
				shape = name + "(…)"
			}
		}
		calls[shape] = true
		return true
	})
	return calls
}

// TestNonChatHandlersFillClientModel pins that the main log row's
// client_model is populated on the non-chat paths.
//
// EnsureCaptured's ensureRequestBodyBuffered returns early when the body is
// already buffered, so it does NOT extract the model — the handlers must fill
// client_model themselves, the way the chat path does. Before the fix the
// main log row showed a blank client_model for /v1/responses and
// /v1/messages traffic.
func TestNonChatHandlersFillClientModel(t *testing.T) {
	cases := []struct {
		file string
		want []string
	}{
		{file: "responses.go", want: []string{"logCtx.SetClientModel(extractModelFromBody(bodyBytes))"}},
		{file: "messages.go", want: []string{"logCtx.SetClientModel(extractModelFromBody(bodyBytes))"}},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			calls := goFileCallShapes(t, filepath.Join(".", tc.file))
			if !calls["SetClientModel(extractModelFromBody)"] {
				t.Errorf("%s does not fill client_model from the body — the main log row keeps a blank client_model",
					tc.file)
			}
		})
	}
}

// TestFailureRowsRunSystemPromptFallback pins the failure-path repair.
//
// A failure row is built by recordFailedRequestWithKey, which creates a FRESH
// RequestLogContext. refreshMeta only re-runs the header chain against
// meta.SystemPrompt — it never PRODUCES that field; only EnsureCaptured
// extracts it from the buffered body. So before the fix, failure rows had an
// empty SystemPrompt ⇒ the system-prompt identification fallback never ran
// ⇒ clients that only self-identify in the prompt landed as the weak generic
// name on exactly the rows an operator inspects when a request breaks.
//
// Measured on 211 before the fix: the same request produced agent_name=zcode
// on the success row and agent_name=go-client on the failure row.
//
// Layer ① (behaviour): EnsureCaptured produces the SystemPrompt that the
// fallback consumes.
func TestFailureRowsRunSystemPromptFallback(t *testing.T) {
	body := `{"model":"glm-5.3-flash","instructions":"You are ZCode, an interactive coding agent"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	r.Header.Set("User-Agent", "Go-http-client/1.1")

	ch := NewChatHandler(credential.NewManager(), credential.NewLimiter(), nil, nil, nil, nil)
	c := ch.NewRequestLogContext(r, "failure-ident", time.Now())
	c.SetKey(&authentication.KeyInfo{ID: 4242, TenantID: "tenant-ident"})
	c.Body = []byte(body)
	c.EnsureCaptured()

	if c.meta.SystemPrompt == "" {
		t.Fatal("EnsureCaptured did not produce meta.SystemPrompt — the system-prompt fallback has no input")
	}
	attrs := BuildContextAttrsEntry(c, c.KeyInfo, &c.meta, r.Context())
	if attrs == nil || attrs.AgentName == nil {
		t.Fatal("agent_name empty on the failure path")
	}
	if *attrs.AgentName != "zcode" {
		t.Fatalf("agent_name = %q, want %q", *attrs.AgentName, "zcode")
	}
}

// TestFailedRequestBuilderCapturesSystemPrompt pins layer ② (wiring): the
// failure-row builder must call EnsureCaptured.
//
// It must not call refreshMeta alone — refreshMeta consumes meta.SystemPrompt
// but never produces it, so a builder that only calls refreshMeta silently
// disables the whole system-prompt fallback on failure rows.
func TestFailedRequestBuilderCapturesSystemPrompt(t *testing.T) {
	calls := goFileCallShapes(t, filepath.Join(".", "handler.go"))
	if !calls["EnsureCaptured()"] {
		t.Error("handler.go never calls EnsureCaptured() — the system-prompt fallback has no input on failure rows")
	}
}

// TestReadRequestBodyRestoreKeepsBodyReReadable pins the body-restoration
// repair.
//
// readRequestBody consumes the body without restoring it, so the failure
// paths' captureAttemptBody(r, …) got an empty buffer — which silently
// disabled the system-prompt identification fallback exactly on the failure
// rows an operator inspects when a request breaks.
func TestReadRequestBodyRestoreKeepsBodyReReadable(t *testing.T) {
	const payload = `{"model":"m","instructions":"You are ZCode, an interactive coding agent"}`

	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
	buf, err := readRequestBodyRestore(r, maxBodySize)
	if err != nil {
		t.Fatalf("readRequestBodyRestore: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("first read = %q, want %q", string(buf), payload)
	}

	// The body must still be readable — this is what the failure paths rely on.
	again, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if string(again) != payload {
		t.Fatalf("re-read = %q, want %q", string(again), payload)
	}
}

// TestReadRequestBodyRestoreDoesNotReplayPartialBody pins that an incomplete
// read is NOT replayed as if it were the whole body — replaying a truncated
// body would make downstream parsers see a valid-looking but wrong request.
func TestReadRequestBodyRestoreDoesNotReplayPartialBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("x"))
	// A limit of 0 means the read returns 1 byte (limit+1) with no error;
	// len(buf) > limit ⇒ the restore must be skipped.
	buf, err := readRequestBodyRestore(r, 0)
	if err != nil {
		t.Fatalf("readRequestBodyRestore: %v", err)
	}
	if len(buf) <= 0 {
		t.Fatalf("expected a non-empty over-limit buffer, got %d bytes", len(buf))
	}
	rest, _ := io.ReadAll(r.Body)
	if len(rest) != 0 {
		t.Fatalf("over-limit body must not be replayed, got %d bytes", len(rest))
	}
}
