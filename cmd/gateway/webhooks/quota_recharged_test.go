// File: cmd/gateway/webhooks/quota_recharged_test.go
//
// 2026-08-26 hzx-2 / 充值回调 webhook (落点 B) 测试。
//
// 覆盖：
//   - HMAC 验签：合法签名 200 / 错误签名 401 / 缺签名 401
//   - body 校验：missing credential_id 400 / unsupported event_type 400 / stale ts 401
//   - 成功路径：调用 ServeHTTP 后 OnQuotaRecharged 被异步触发，校验参数 (credID, source)
//   - secret 未配置 → fail-secure 401
//   - method guard：GET 返回 405
//   - metric 计数器 accepted/rejected 累加

package webhooks

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// testSecret 是测试用 HMAC secret；保证不为空且稳定。
var testSecret = []byte("test-webhook-secret-2026-08-26")

// signForTest 复用 handler 内部算法，便于构造合法 / 非法签名。
func signForTest(body []byte) string {
	mac := hmac.New(sha256.New, testSecret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// makeBody 构造测试用的 QuotaRechargedBody JSON，ts 默认取 now。
func makeBody(t *testing.T, override func(b *QuotaRechargedBody)) []byte {
	t.Helper()
	b := QuotaRechargedBody{
		CredentialID: 42,
		Vendor:       "test-vendor",
		EventType:    "recharge.completed",
		Timestamp:    time.Now().Unix(),
	}
	if override != nil {
		override(&b)
	}
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return out
}

// newTestHandler 构造一个通用 handler，metrics 走独立 registry 避免污染全局。
func newTestHandler(onQuotaRecharged func(int, string)) *QuotaRechargedHandler {
	reg := prometheus.NewRegistry()
	return &QuotaRechargedHandler{
		Secret:           testSecret,
		OnQuotaRecharged: onQuotaRecharged,
		Metrics:          NewQuotaWebhookMetricsForTest(reg),
	}
}

// doRequest 一次性 POST helper。
func doRequest(h http.Handler, body []byte, signature string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/webhooks/quota/recharged", bytes.NewReader(body))
	if signature != "" {
		r.Header.Set(QuotaRechargedSignatureHeader, signature)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}

// ─────────────────────────────────────────────────────────────────────
// 验签
// ─────────────────────────────────────────────────────────────────────

func TestQuotaRechargedHandler_ValidSignature_Accepted(t *testing.T) {
	var called sync.WaitGroup
	called.Add(1)
	var gotID int
	var gotSource string
	h := newTestHandler(func(credID int, source string) {
		gotID = credID
		gotSource = source
		called.Done()
	})

	body := makeBody(t, nil)
	rr := doRequest(h, body, signForTest(body))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", got)
	}

	// 异步 OnQuotaRecharged：等最多 2s。
	doneCh := make(chan struct{})
	go func() {
		called.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("OnQuotaRecharged was not called within 2s")
	}
	if gotID != 42 {
		t.Fatalf("expected credential_id=42, got %d", gotID)
	}
	if gotSource != "recharge.completed" {
		t.Fatalf("expected source=recharge.completed, got %q", gotSource)
	}
}

func TestSecretSource_HexSecret(t *testing.T) {
	t.Setenv(QuotaRechargedSecretEnv, hex.EncodeToString(testSecret))
	if got := SecretSource(); !bytes.Equal(got, testSecret) {
		t.Fatalf("hex secret decoded as %q, want %q", got, testSecret)
	}
}

func TestQuotaRechargedHandler_MissingVendor(t *testing.T) {
	h := newTestHandler(func(int, string) {
		t.Fatal("OnQuotaRecharged must NOT be called when vendor is missing")
	})
	body := makeBody(t, func(b *QuotaRechargedBody) { b.Vendor = "" })
	rr := doRequest(h, body, signForTest(body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestQuotaRechargedHandler_InvalidSignature_Unauthorized(t *testing.T) {
	h := newTestHandler(func(int, string) {
		t.Fatal("OnQuotaRecharged must NOT be called on signature mismatch")
	})

	body := makeBody(t, nil)
	// 错误的 signature（用不同的 secret 算）
	mac := hmac.New(sha256.New, []byte("wrong-secret"))
	mac.Write(body)
	wrongSig := hex.EncodeToString(mac.Sum(nil))

	rr := doRequest(h, body, wrongSig)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestQuotaRechargedHandler_MissingSignature_Unauthorized(t *testing.T) {
	h := newTestHandler(func(int, string) {
		t.Fatal("OnQuotaRecharged must NOT be called when signature header is missing")
	})
	body := makeBody(t, nil)
	rr := doRequest(h, body, "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestQuotaRechargedHandler_SecretMissing_FailSecure(t *testing.T) {
	h := &QuotaRechargedHandler{
		Secret:  nil, // 故意为空
		Metrics: NewQuotaWebhookMetricsForTest(prometheus.NewRegistry()),
		OnQuotaRecharged: func(int, string) {
			t.Fatal("must NOT be called when secret is missing")
		},
	}
	body := makeBody(t, nil)
	rr := doRequest(h, body, signForTest(body))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%q", rr.Code, rr.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────
// body 校验
// ─────────────────────────────────────────────────────────────────────

func TestQuotaRechargedHandler_MissingCredentialID_BadRequest(t *testing.T) {
	h := newTestHandler(func(int, string) {
		t.Fatal("OnQuotaRecharged must NOT be called on invalid body")
	})
	body := makeBody(t, func(b *QuotaRechargedBody) { b.CredentialID = 0 })
	rr := doRequest(h, body, signForTest(body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestQuotaRechargedHandler_UnsupportedEventType_BadRequest(t *testing.T) {
	h := newTestHandler(func(int, string) {
		t.Fatal("OnQuotaRecharged must NOT be called on unsupported event_type")
	})
	body := makeBody(t, func(b *QuotaRechargedBody) { b.EventType = "random.noise" })
	rr := doRequest(h, body, signForTest(body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rr.Code, rr.Body.String())
	}
}

func TestQuotaRechargedHandler_StaleTS_Unauthorized(t *testing.T) {
	h := newTestHandler(func(int, string) {
		t.Fatal("OnQuotaRecharged must NOT be called on stale ts")
	})
	// 把 ts 推到 1 小时之前（远超 300s 窗口）
	body := makeBody(t, func(b *QuotaRechargedBody) { b.Timestamp = time.Now().Add(-1 * time.Hour).Unix() })
	rr := doRequest(h, body, signForTest(body))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%q", rr.Code, rr.Body.String())
	}
}

func TestQuotaRechargedHandler_FutureTS_Unauthorized(t *testing.T) {
	h := newTestHandler(func(int, string) {
		t.Fatal("OnQuotaRecharged must NOT be called on future ts")
	})
	// 推到 1 小时之后
	body := makeBody(t, func(b *QuotaRechargedBody) { b.Timestamp = time.Now().Add(1 * time.Hour).Unix() })
	rr := doRequest(h, body, signForTest(body))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%q", rr.Code, rr.Body.String())
	}
}

func TestQuotaRechargedHandler_ZeroTS_BadRequest(t *testing.T) {
	h := newTestHandler(func(int, string) {
		t.Fatal("OnQuotaRecharged must NOT be called on zero ts")
	})
	body := makeBody(t, func(b *QuotaRechargedBody) { b.Timestamp = 0 })
	rr := doRequest(h, body, signForTest(body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestQuotaRechargedHandler_InvalidJSON_BadRequest(t *testing.T) {
	h := newTestHandler(func(int, string) {
		t.Fatal("OnQuotaRecharged must NOT be called on invalid JSON")
	})
	body := []byte("not-json-at-all")
	rr := doRequest(h, body, signForTest(body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────
// HTTP method guard
// ─────────────────────────────────────────────────────────────────────

func TestQuotaRechargedHandler_GET_MethodNotAllowed(t *testing.T) {
	h := newTestHandler(nil)
	r := httptest.NewRequest(http.MethodGet, "/api/webhooks/quota/recharged", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────
// body size 限制：超限 body 必须明确拒绝，不能先截断再验签。
// ─────────────────────────────────────────────────────────────────────

func TestQuotaRechargedHandler_BodySizeLimit(t *testing.T) {
	h := newTestHandler(func(int, string) {
		t.Fatal("OnQuotaRecharged must NOT be called for oversized body")
	})
	huge := bytes.Repeat([]byte("x"), 2<<20)
	rr := doRequest(h, huge, signForTest(huge))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized body, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────
// window.reset 也应通过
// ─────────────────────────────────────────────────────────────────────

func TestQuotaRechargedHandler_WindowReset_Accepted(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	h := newTestHandler(func(credID int, source string) {
		if source != "window.reset" {
			t.Errorf("expected source=window.reset, got %q", source)
		}
		wg.Done()
	})
	body := makeBody(t, func(b *QuotaRechargedBody) { b.EventType = "window.reset" })
	rr := doRequest(h, body, signForTest(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rr.Code, rr.Body.String())
	}
	wg.Wait()
}

// ─────────────────────────────────────────────────────────────────────
// metrics 累加校验
// ─────────────────────────────────────────────────────────────────────

// counterValue 取 CounterVec 当前累加值。
func counterValue(t *testing.T, c *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	m, err := c.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("get counter: %v", err)
	}
	var mfo dto.Metric
	if err := m.(prometheus.Metric).Write(&mfo); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return mfo.Counter.GetValue()
}

func TestQuotaRechargedHandler_AcceptedCounterIncrements(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewQuotaWebhookMetricsForTest(reg)
	h := &QuotaRechargedHandler{
		Secret:           testSecret,
		Metrics:          metrics,
		OnQuotaRecharged: func(int, string) {}, // 吞掉 async
	}

	body := makeBody(t, nil)
	rr := doRequest(h, body, signForTest(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := counterValue(t, metrics.QuotaWebhookAcceptedTotal, "recharge.completed"); got != 1 {
		t.Fatalf("expected accepted counter=1, got %v", got)
	}
}

func TestQuotaRechargedHandler_RejectedCounterIncrements(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewQuotaWebhookMetricsForTest(reg)
	h := &QuotaRechargedHandler{
		Secret:           testSecret,
		Metrics:          metrics,
		OnQuotaRecharged: func(int, string) {},
	}

	body := makeBody(t, nil)
	// 错签名
	rr := doRequest(h, body, "deadbeef")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	if got := counterValue(t, metrics.QuotaWebhookRejectedTotal, "signature_mismatch"); got != 1 {
		t.Fatalf("expected rejected counter=1, got %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────
// 工具方法
// ─────────────────────────────────────────────────────────────────────

// NewQuotaWebhookMetricsForTest 是 NewQuotaWebhookMetrics 的"独立 registry"变体，
// 避免单元测试往 prometheus.DefaultRegisterer 里塞 vec（会导致跨测试污染）。
func NewQuotaWebhookMetricsForTest(reg prometheus.Registerer) *QuotaWebhookMetrics {
	return newQuotaWebhookMetricsWith(reg)
}

// newQuotaWebhookMetricsWith 是 NewQuotaWebhookMetrics 的内部版本，
// 接受自定义 registry（生产路径用默认 registry；测试用 fresh registry）。
func newQuotaWebhookMetricsWith(reg prometheus.Registerer) *QuotaWebhookMetrics {
	accepted := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_quota_webhook_accepted_total",
		Help: "Total accepted quota webhook requests by event_type.",
	}, []string{"event_type"})
	rejected := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_quota_webhook_rejected_total",
		Help: "Total rejected quota webhook requests by reason.",
	}, []string{"reason"})
	reg.MustRegister(accepted, rejected)
	return &QuotaWebhookMetrics{
		QuotaWebhookAcceptedTotal: accepted,
		QuotaWebhookRejectedTotal: rejected,
	}
}

// ioLimitReaderSanity 确认 io.LimitReader 行为，避免静默篡改实现。
func TestIOLimitReaderSanity(t *testing.T) {
	src := bytes.NewReader(bytes.Repeat([]byte("a"), 100))
	out, err := io.ReadAll(io.LimitReader(src, 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 10 {
		t.Fatalf("expected 10 bytes, got %d", len(out))
	}
}
