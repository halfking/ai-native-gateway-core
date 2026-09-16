package hostedcallback

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/outbox"
)

// 矩阵 E（SSRF）：loopback/RFC1918/metadata/IPv6 回环/CGNAT 全拒；
// allowlist 显式放行覆盖。
func TestValidateURLBlockedRanges(t *testing.T) {
	blocked := []string{
		"http://127.0.0.1:8080/cb",
		"http://10.0.0.5/cb",
		"http://172.16.1.1/cb",
		"http://192.168.1.1/cb",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.100.100.200/cb", // Aliyun metadata（CGNAT 段）
		"http://[::1]/cb",
		"http://[fd00::1]/cb",
		"http://0.0.0.0/cb",
		"file:///etc/passwd",
		"gopher://x",
		"http://user@10.0.0.1/cb", // parser bypass 形态（host 含 @ 的变体在 url.Parse 后已归 host）
	}
	for _, raw := range blocked {
		if err := ValidateURL(raw, nil); err == nil {
			t.Errorf("ValidateURL(%q) must reject", raw)
		}
	}
	// allowlist 精确/CIDR/后缀放行。
	if err := ValidateURL("http://127.0.0.1:9000/cb", []string{"127.0.0.1"}); err != nil {
		t.Errorf("allowlisted loopback rejected: %v", err)
	}
	if err := ValidateURL("http://10.1.2.3/cb", []string{"10.0.0.0/8"}); err != nil {
		t.Errorf("allowlisted RFC1918 rejected: %v", err)
	}
	if err := ValidateURL("https://cb.internal.corp/x", []string{"*.internal.corp"}); err != nil {
		t.Errorf("allowlisted suffix rejected: %v", err)
	}
	// 后缀匹配必须命中真实子域（*.internal.corp 不等于任意以 internal.corp
	// 开头的字符串）；非匹配域名按公网域名规则放行（投递时 safehttpclient
	// 仍有 DNS rebinding 防线）。
	if err := ValidateURL("https://evil.internal.corp.attacker.com/x", []string{"*.internal.corp"}); err != nil {
		t.Errorf("unrelated public host must follow public-domain rule: %v", err)
	}
	// 公网域名正常放行（DNS rebinding 防御在投递时由 safehttpclient 复验）。
	if err := ValidateURL("https://example.com/hook", nil); err != nil {
		t.Errorf("public URL rejected: %v", err)
	}
}

// 矩阵 E（签名/语义）：HMAC 头契约 = outbox.SignPayload；2xx delivered；
// 4xx 不重试；5xx 可重试。
func TestDelivererSignsAndClassifies(t *testing.T) {
	var gotSig, gotTS, gotNonce, gotCorr string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get(HeaderSignature)
		gotTS = r.Header.Get(HeaderTimestamp)
		gotNonce = r.Header.Get(HeaderNonce)
		gotCorr = r.Header.Get(HeaderCorrelationID)
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDeliverer(time.Second, []string{"127.0.0.1"})
	d.SetTestHooks(
		func() time.Time { return time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC) },
		func() (string, error) { return "nonce-1", nil },
	)
	res, err := d.Deliver(context.Background(), srv.URL, "sekrit", []byte(`{"event_id":"e1"}`), "ht_1")
	if err != nil || !res.Delivered {
		t.Fatalf("deliver: res=%+v err=%v", res, err)
	}
	want := outbox.SignPayload("sekrit", "2026-09-15T08:00:00Z", "nonce-1", gotBody)
	if gotSig != want {
		t.Errorf("signature = %q, want %q (outbox.SignPayload contract)", gotSig, want)
	}
	if gotCorr != "ht_1" || gotNonce != "nonce-1" || gotTS == "" {
		t.Errorf("headers wrong: corr=%q nonce=%q ts=%q", gotCorr, gotNonce, gotTS)
	}
}

func TestDelivererClassifiesStatusCodes(t *testing.T) {
	cases := []struct {
		code          int
		wantDelivered bool
		wantRetryable bool
	}{
		{200, true, false},
		{204, true, false},
		{400, false, false}, // 4xx 不重试（DLQ 直达）
		{404, false, false},
		{500, false, true}, // 5xx 退避重试
		{503, false, true},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.code)
		}))
		// allowlist 放行 loopback：本用例验证状态分类，SSRF 拦截另测。
		d := NewDeliverer(time.Second, []string{"127.0.0.1"})
		res, _ := d.Deliver(context.Background(), srv.URL, "s", []byte(`{}`), "")
		srv.Close()
		if res.Delivered != tc.wantDelivered || res.Retryable != tc.wantRetryable {
			t.Errorf("code %d: got (%v,%v), want (%v,%v)", tc.code,
				res.Delivered, res.Retryable, tc.wantDelivered, tc.wantRetryable)
		}
	}
}

func TestBuildEnvelopeStableEventID(t *testing.T) {
	body, err := BuildEnvelope("hosted_ht1_ev3", "hosted_task.completed", "ht1", "tenant-A",
		map[string]any{"status": "completed"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{`"event_id":"hosted_ht1_ev3"`, `"type":"hosted_task.completed"`, `"tenant_id":"tenant-A"`, `"schema_version":"1.0"`} {
		if !strings.Contains(s, want) {
			t.Errorf("envelope missing %s: %s", want, s)
		}
	}
}
