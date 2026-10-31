// clientkey.go —— 客户端凭据的唯一提取口（2026-10-26，Gemini 原生认证修复轮）。
//
// 背景：本仓此前有**两份**互不知情的凭据提取实现——
//
//	middleware/auth_mw.go        → 只认 Authorization: Bearer + x-api-key（Anthropic）
//	domains/streaming/handler.go → 只认 Authorization: Bearer + x-api-key
//
// 于是**厂方标准文档里写明的 Gemini 认证形态一张都没实现**：
//
//	docs/vendor-formats/gemini.md §HTTP 形态
//	    POST /v1beta/models/{MODEL}:generateContent?key={API_KEY}
//	    「API key 通过 query string 或 Authorization: Bearer 头传递。」
//	docs/archive/process/format-conversion/厂家数据格式/gemini/generate-content.md §认证
//	    ?key={API_KEY} 或 Authorization: Bearer {ACCESS_TOKEN}
//	docs/供应商协议优化.md §8.3
//	    「Header：x-goog-api-key: <key> 或 ?key=<key>」
//	docs/QUICK_REFERENCE.md §Gemini协议
//	    POST /v1/models/gemini-pro:generateContent  +  x-goog-api-key: xxx
//
// ⇒ 原生 Gemini 客户端（google-genai SDK / Gemini CLI 等）一律 401 missing_key，
//
//	而且**死在 mux 之前**（全局静态 key 门）⇒ 连 Gemini handler 都进不去。
//
// 本文件把这几种形态收成**一份**实现（唯一事实源）：
//
//  1. Authorization: Bearer <token>   —— 通用形态
//  2. x-api-key: <key>                —— Anthropic
//  3. x-goog-api-key: <key>           —— Google（Gemini）
//  4. ?key=<key>                      —— Gemini 的 query string 形态
//
// ★ 形态 4 **只在 Gemini 原生路径上生效**：key 是个极通用的 query 参数名，
//
//	全局认它当凭据源 ⇒ 未来任何一个把 `?key=` 当业务参数用的端点都会被
//	静默劫持。收口到 IsGeminiNativePath 后，形态 4 与厂方契约一一对应
//	（厂方也只在这条路由上认 ?key=）。
//
// ★ 本包**不依赖**任何协议实现包（internal/ir 等）：凭据提取必须能被
//
//	middleware（链最外侧）引用，而 middleware 不该为了读一个 header
//	把整个 IR 层拖进来。代价是 Gemini 路径判定在本包里有一份**拷贝**，
//	与 domains/streaming/handler_gemini.go 的 geminiModelPathRe 并列
//	⇒ 两侧由契约测试钉死（domains/streaming/handler_gemini_auth_test.go
//	的 TestGeminiNativePathMatchesHandlerParser）⇒ 漂移即红。
package auth

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// CredentialSource names the presentation form a client credential arrived
// in. It exists so operators can tell "no credential" from "credential of
// an unexpected shape" in logs without ever logging the credential itself.
type CredentialSource string

const (
	// SourceNone means no credential was presented at all.
	SourceNone CredentialSource = ""
	// SourceBearer means `Authorization: Bearer <token>`.
	SourceBearer CredentialSource = "bearer"
	// SourceAPIKey means the Anthropic-style `x-api-key` header.
	SourceAPIKey CredentialSource = "x-api-key"
	// SourceGoogAPIKey means the Google-style `x-goog-api-key` header.
	SourceGoogAPIKey CredentialSource = "x-goog-api-key"
	// SourceQueryKey means the Gemini-style `?key=` query parameter.
	SourceQueryKey CredentialSource = "query:key"
)

// Header names for the vendor-specific credential headers.
const (
	// HeaderAPIKey is the Anthropic Messages API key header.
	HeaderAPIKey = "x-api-key"
	// HeaderGoogAPIKey is the Google (Gemini) API key header.
	HeaderGoogAPIKey = "x-goog-api-key"
)

// QueryKeyParam is the Gemini query-string credential parameter name.
//
// ★ It is only honoured on Gemini-native paths (IsGeminiNativePath) — see
// the package comment for why it is NOT a global credential source.
const QueryKeyParam = "key"

// RedactedValue replaces a credential value in log output.
const RedactedValue = "REDACTED"

// geminiNativePathRe matches the Gemini-native ingress paths the gateway
// serves:
//
//	POST /v1beta/models/{model}:generateContent
//	POST /v1beta/models/{model}:streamGenerateContent
//	POST /v1beta/models/{model}:countTokens
//	POST /v1/models/{model}:generateContent
//	POST /v1/models/{model}:streamGenerateContent
//	POST /v1/models/{model}:countTokens
//	GET  /v1beta/models                      ← the Gemini-NATIVE model list
//
// ★ Anchored at the START, deliberately NOT at the end.
//
//   - Start-anchored: the mux subtrees that carry the Gemini surfaces are
//     "/v1beta/models/" and "/v1/models/" (cmd/gateway/main.go). A path
//     outside them is never served as a Gemini route, so the credential
//     scope must not cover it.
//
//   - NOT end-anchored: domains/streaming/handler_gemini.go's own parser
//     (geminiModelPathRe) is a substring match and therefore tolerates a
//     trailing suffix ("...:generateContent/extra"). The credential scope
//     must cover **every route the handler serves** — a narrower scope
//     would leave a served Gemini route unable to authenticate (exactly
//     the defect this round fixes), while a wider one is harmless (the mux
//     404s what it does not serve).
//
// ★ The action set must stay in lockstep with the handler's own parser
// (domains/streaming/handler_gemini.go: geminiModelPathRe). Both list the
// SAME three actions (generateContent / streamGenerateContent /
// countTokens): a scope that misses one leaves that action unable to
// authenticate, while a parser that misses one 404s a route the scope
// happily serves. ⇒ They are two copies of one fact ⇒ they are pinned
// against each other by TestGeminiNativePathMatchesHandlerParser
// (domains/streaming).
//
// ★ The model-list alternative is EXACTLY anchored ("/v1beta/models$"): it
// is a distinct route, not a prefix, so "/v1beta/modelsXYZ" must NOT be
// in scope. And "/v1/models" (no trailing slash) is deliberately NOT
// included: that path is the OpenAI-shaped model list (the OpenAI SDK's
// contract), not a Gemini-native surface.
//
// ★ The scope must cover the model list because the gateway NOW serves it
// (domains/streaming/models.go: ServeGeminiModels). A native Gemini SDK
// commonly presents its key as "?key=" ⇒ without the scope the list
// would 401 while generateContent (same client, header form) works —
// the same "half-fixed" shape as the §433 defect.
//
// Kept in sync with domains/streaming/handler_gemini.go by the contract
// test TestGeminiNativePathMatchesHandlerParser (domains/streaming).
var geminiNativePathRe = regexp.MustCompile(`^(?:/(?:v1beta|v1)/models/[^/:]+:(?:generateContent|streamGenerateContent|countTokens)|/v1beta/models$)`)

// IsGeminiNativePath reports whether p is a Gemini-native generateContent
// path (the routes served by domains/streaming/handler_gemini.go).
func IsGeminiNativePath(p string) bool {
	return geminiNativePathRe.MatchString(p)
}

// ExtractClientKey returns the client credential presented by r together with
// the presentation form it arrived in.
//
// Precedence (highest first) — Bearer → x-api-key → x-goog-api-key → ?key=.
// An explicit `Authorization: Bearer` always wins, so a client that sends
// several forms is never silently re-pointed at a different credential.
//
// The returned credential is only a *presentation* of the key: every caller
// still has to verify it (KeyVerifier / static-key exact match). This
// function never grants access by itself.
func ExtractClientKey(r *http.Request) (string, CredentialSource) {
	if r == nil {
		return "", SourceNone
	}
	if tok := bearerToken(r); tok != "" {
		return tok, SourceBearer
	}
	if key := strings.TrimSpace(r.Header.Get(HeaderAPIKey)); key != "" {
		return key, SourceAPIKey
	}
	if key := strings.TrimSpace(r.Header.Get(HeaderGoogAPIKey)); key != "" {
		return key, SourceGoogAPIKey
	}
	if r.URL != nil && IsGeminiNativePath(r.URL.Path) {
		if key := strings.TrimSpace(r.URL.Query().Get(QueryKeyParam)); key != "" {
			return key, SourceQueryKey
		}
	}
	return "", SourceNone
}

// ★ The Bearer form is parsed by the package's existing bearerToken helper
// (mockprobe_bypass.go) rather than by a third parser — the repo's LESSONS
// 「同一句话不许两份」 applies to parsers too. That helper matches the scheme
// case-insensitively per RFC 7235 §2.1 ("Bearer" and "bearer" are the same
// scheme); the two historical credential extractors in this repo disagreed
// on that point, and the unified form accepts what the data plane already
// accepted.

// credentialQueryParams are the query parameters that carry a credential the
// gateway itself accepts:
//
//   - "key"  — Gemini query-string credential form (this round).
//   - "token" — admin EventSource token (admin/auth.go promotes it to the
//     Authorization header because EventSource cannot set headers).
//
// ★ The match is on the EXACT parameter name: `api_key_id` (admin/auto_route.go,
//
//	admin/turns_sessions.go) is an identifier, not a credential, and must
//	stay readable in the access log.
var credentialQueryParams = map[string]bool{
	QueryKeyParam: true,
	"token":       true,
}

// RedactQueryCredentials returns raw with the value of every
// credential-bearing query parameter replaced by RedactedValue.
//
// ★ Why this exists: the Gemini credential form `?key=<API_KEY>` puts the
// key in the URL. The access log records the raw query string
// (middleware/logging_mw.go) ⇒ without redaction the key would land in the
// rotated log files (and in the panic log) in cleartext.
//
// ★ Redaction runs BEFORE truncation (the caller truncates), so a truncated
// query can never expose a partial secret.
//
// ★ The original parameter ORDER and the encoding of every non-credential
// parameter are preserved — the redaction must not change what operators
// read in the log for ordinary queries.
func RedactQueryCredentials(raw string) string {
	if raw == "" || !strings.Contains(raw, "=") {
		return raw
	}
	parts := strings.Split(raw, "&")
	changed := false
	for i, part := range parts {
		name, value, ok := strings.Cut(part, "=")
		if !ok || value == "" {
			// An empty value carries no secret — leave it visible so the log
			// stays honest about "the parameter was present but empty".
			continue
		}
		decoded := name
		if unescaped, err := url.QueryUnescape(name); err == nil {
			decoded = unescaped
		}
		if !credentialQueryParams[decoded] {
			continue
		}
		parts[i] = name + "=" + RedactedValue
		changed = true
	}
	if !changed {
		return raw
	}
	return strings.Join(parts, "&")
}
