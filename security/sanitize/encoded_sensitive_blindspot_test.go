// Guard for audit round 242: the request-side sanitizer does not see through
// base64 encoding, while the injection detector's problem (待裁决 93, round 227)
// is the same class of blindness in a different package.
//
// Measured on 2026-10-04, same code path, same sensitive value, one variable:
//
//	arm A (control)  "call 13800138000"      -> "call {SENSITIVE:phone:1}"
//	arm B            "call MTM4MDAxMzgwMDA=" -> forwarded VERBATIM,
//	                                             placeholders=0, refs=0,
//	                                             SanitizeInfo absent entirely
//	arm C            "mail YUBiLmNvbQ=="     -> forwarded VERBATIM
//
// The values above are the standard base64 of the plaintext ones, so a single
// decode recovers them; the supplier the gateway forwards to does not have to
// be clever to read them.
//
// Why the arm A control is not optional: without it, "arm B was not
// sanitized" is indistinguishable from "this harness never sanitizes anything"
// — the same absent-vs-fact confusion that has bitten this repo repeatedly.
//
// ⚠️ THIS IS A TRIPWIRE ON AN OPEN FINDING, AND IT IS DELIBERATELY GREEN TODAY.
//
// It prints the measurement on every run rather than failing, because the fix
// is a product decision: whether the sanitizer should decode every base64-looking
// blob, re-scan it, and what to do when the decoded text is sensitive. Round 215
// hit the same shape of question for `action.results[].url` and recorded it as
// a product口径 call rather than fixing it. Encoding that decision here would
// change behaviour on the hot request path, so it is registered, not guessed.
//
// If this test goes RED, the blind spot is gone. That is GOOD: close the
// pending item, and convert this test into a plain positive assertion that
// encoded forms are sanitized. Do NOT delete it.
package sanitize

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/stretchr/testify/require"
)

type b64ProbeResult struct {
	forwarded    string
	placeholders int
	refs         int
	hasInfo      bool
}

// runB64Probe drives one request body through the real request-side sanitizer
// middleware and reports what came out. Same construction the existing
// input_protocols_test.go harness uses, so the two are comparable.
func runB64Probe(t *testing.T, body string) b64ProbeResult {
	t.Helper()
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	require.NoError(t, err)

	var res b64ProbeResult
	var info compression.SanitizeInfo
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		res.forwarded = string(b)
		info, res.hasInfo = compression.SanitizeInfoFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Gw-Session-Id", "session-b64probe")
	req = req.WithContext(WithAuthenticatedTenant(req.Context(), "tenant-b64probe"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	res.placeholders = info.Stats.PlaceholderCount
	res.refs = len(info.MessageRefs)
	return res
}

// TestSanitizerBlindSpotForEncodedSensitiveValueIsMeasured is the tripwire.
//
// It FAILS when arm B/C start being sanitized, because that means the finding
// is fixed and this file must be rewritten rather than trusted.
func TestSanitizerBlindSpotForEncodedSensitiveValueIsMeasured(t *testing.T) {
	const phone = "13800138000"
	const email = "a@b.com"
	encPhone := base64.StdEncoding.EncodeToString([]byte(phone))
	encEmail := base64.StdEncoding.EncodeToString([]byte(email))

	// --- arm A: the control. If this does not sanitize, the harness is
	// vacuous and every conclusion below is meaningless (§190).
	ctrl := runB64Probe(t, `{"model":"m","messages":[{"role":"user","content":"call `+phone+`"}]}`)
	if !strings.Contains(ctrl.forwarded, "{SENSITIVE:") || strings.Contains(ctrl.forwarded, phone) {
		t.Fatalf("判据失效（对照组没被脱敏）：forwarded=%s placeholders=%d —— "+
			"下面关于「编码形态没被脱敏」的结论全部不成立，请先修夹具。",
			ctrl.forwarded, ctrl.placeholders)
	}

	// --- arm B/C: the same values, base64-encoded.
	arms := []struct{ name, encoded, secret string }{
		{"phone", encPhone, phone},
		{"email", encEmail, email},
	}
	var stillBlind []string
	for _, a := range arms {
		res := runB64Probe(t, `{"model":"m","messages":[{"role":"user","content":"`+a.encoded+`"}]}`)
		t.Logf("arm %s: encoded=%q placeholders=%d refs=%d hasInfo=%v forwarded=%s",
			a.name, a.encoded, res.placeholders, res.refs, res.hasInfo, res.forwarded)
		if !strings.Contains(res.forwarded, "{SENSITIVE:") {
			stillBlind = append(stillBlind, a.name)
			// Sanitizing the encoded form implies the decoded secret must not
			// survive either; both hold or neither does.
			if !strings.Contains(res.forwarded, a.encoded) {
				t.Errorf("arm %s 的出向体既无占位符也无原编码串，形态未知，请人工看", a.name)
			}
		}
	}

	if len(stillBlind) == 0 {
		t.Errorf("✅ 盲区已消失：base64 编码形态现在会被脱敏（arm %v）。\n\n"+
			"请接着处置编码类盲区的裁决项（与待裁决 93 同族，227 号只审了注入检测那一侧），\n"+
			"并把本测试改成正向断言。**不要直接删掉本文件。**", stillBlind)
		return
	}
	t.Logf("⚠️ 盲区仍在：%v 的编码形态原样出向（对照组 arm A 正常脱敏，"+
		"证明不是夹具坏了）。一次 base64 解码即可还原，供应商无需额外技巧。", stillBlind)
}
