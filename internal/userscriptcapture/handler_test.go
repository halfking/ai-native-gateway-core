// userscriptcapture 单测 — HMAC 验证、契约校验、handler 端到端（fake store）。
package userscriptcapture

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── HMAC 验证 ──────────────────────────────────────────────────────────

func sign(secret, ts, nonce string, body []byte) string {
	v := NewVerifier(secret, 0)
	_ = v
	// 直接用包内同款公式手算签名（不经过 VerifyRaw，避免同源错）。
	mac := hmacSHA256(secret, ts+"."+nonce+"."+string(body))
	return SignatureScheme + mac
}

func TestVerifierAcceptsValidAndRejectsTampering(t *testing.T) {
	body := []byte(`{"x":1}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "n-1"
	v := NewVerifier("secret-32-bytes-xxxxxxxxxxxxxxxxx", 0)

	if err := v.VerifyRaw([]byte(ts), []byte(nonce), body, sign("secret-32-bytes-xxxxxxxxxxxxxxxxx", ts, nonce, body)); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	// 篡改 body。
	if err := v.VerifyRaw([]byte(ts), []byte(nonce), []byte(`{"x":2}`), sign("secret-32-bytes-xxxxxxxxxxxxxxxxx", ts, nonce, body)); err == nil {
		t.Fatal("tampered body accepted")
	}
	// 错 secret。
	if err := v.VerifyRaw([]byte(ts), []byte(nonce), body, sign("wrong-secret-32-bytes-xxxxxxxxxxxxxx", ts, nonce, body)); err == nil {
		t.Fatal("wrong secret accepted")
	}
	// 时间窗过期。
	old := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	if err := v.VerifyRaw([]byte(old), []byte("n-2"), body, sign("secret-32-bytes-xxxxxxxxxxxxxxxxx", old, "n-2", body)); err == nil {
		t.Fatal("stale timestamp accepted")
	}
	// nonce 重放。
	sig2 := sign("secret-32-bytes-xxxxxxxxxxxxxxxxx", ts, "n-3", body)
	if err := v.VerifyRaw([]byte(ts), []byte("n-3"), body, sig2); err != nil {
		t.Fatalf("first use rejected: %v", err)
	}
	if err := v.VerifyRaw([]byte(ts), []byte("n-3"), body, sig2); err == nil {
		t.Fatal("nonce replay accepted")
	}
}

// ── 契约校验 ──────────────────────────────────────────────────────────

func validBatch() Batch {
	return Batch{
		SchemaVersion: SchemaVersion, SourceSystem: "chatgpt-web",
		ClientSessionKey: "csk-1",
		Messages: []Message{{
			SourceConversationID: "conv-1", SourceMessageID: "m-1",
			Sequence: 1, ContentRevision: 1, Role: "user",
			OccurredAt: time.Now().UTC(), IdempotencyKey: "idem-1",
			Content: "用户偏好深色主题",
		}},
	}
}

func TestBatchValidation(t *testing.T) {
	vb := validBatch()
	if err := vb.Validate(); err != nil {
		t.Fatalf("valid batch rejected: %v", err)
	}
	bad := validBatch()
	bad.SchemaVersion = "2.0"
	if err := bad.Validate(); err == nil {
		t.Fatal("wrong schema_version accepted")
	}
	bad2 := validBatch()
	bad2.Messages[0].Role = "robot"
	if err := bad2.Validate(); err == nil {
		t.Fatal("bad role accepted")
	}
	bad3 := validBatch()
	bad3.Messages = nil
	if err := bad3.Validate(); err == nil {
		t.Fatal("empty batch accepted")
	}
	bad4 := validBatch()
	bad4.Messages[0].IdempotencyKey = ""
	if err := bad4.Validate(); err == nil {
		t.Fatal("missing idempotency_key accepted")
	}
}

// ── handler 端到端（fake keys + fake store）────────────────────────────

type fakeKeys struct{ tenant string }

func (f fakeKeys) VerifyKeyTenant(raw string) (string, bool) {
	return f.tenant, raw == "sk-valid"
}

// fakeStore 记录 Persist 调用；duplicateAt 模拟某 idempotency 已存在。
type fakeStore struct {
	Calls      []Batch
	Tenants    []string
	duplicated map[string]bool
}

func (f *fakeStore) Persist(ctx context.Context, tenant, endUser string, b Batch) (Ack, error) {
	f.Calls = append(f.Calls, b)
	f.Tenants = append(f.Tenants, tenant)
	ack := Ack{BatchID: "batch-1", Messages: []MessageAck{}}
	for i := range b.Messages {
		m := &b.Messages[i]
		if f.duplicated[m.IdempotencyKey] {
			ack.Duplicates++
			ack.Messages = append(ack.Messages, MessageAck{SourceMessageID: m.SourceMessageID, Duplicate: true})
			continue
		}
		ack.Accepted++
		ack.Messages = append(ack.Messages, MessageAck{SourceMessageID: m.SourceMessageID, BodyRef: BodyRefPrefix + "uuid-" + m.SourceMessageID})
	}
	return ack, nil
}

// storeShim 让 fakeStore 顶替 *Store 字段（handler 只调 Persist——通过
// 小接口替换会改结构；直接构造 Handler 并覆写 store 方法不可行，
// 因此 handler 测试经由 serveCapture 辅助把 Persist 注入）。
type persistFunc func(ctx context.Context, tenant, endUser string, b Batch) (Ack, error)

func serveCapture(t *testing.T, secret string, keys KeyVerifier, persist persistFunc, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(HandlerConfig{Secret: secret, Keys: keys})
	h.store = &Store{}
	h.persistHook = persist
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func postRequest(t *testing.T, body any, ts, nonce, sig string) *http.Request {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/userscript/captures", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer sk-valid")
	req.Header.Set("X-End-User-Id", "user-1")
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, sig)
	return req
}

func TestHandlerHappyPath(t *testing.T) {
	secret := "secret-32-bytes-xxxxxxxxxxxxxxxxx"
	b := validBatch()
	raw, _ := json.Marshal(b)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := postRequest(t, b, ts, "n-h1", sign(secret, ts, "n-h1", raw))

	var got Batch
	var gotTenant string
	w := serveCapture(t, secret, fakeKeys{tenant: "tenant-1"},
		func(_ context.Context, tenant, _ string, bb Batch) (Ack, error) {
			got, gotTenant = bb, tenant
			return Ack{BatchID: "b1", Accepted: 1, Messages: []MessageAck{{SourceMessageID: "m-1", BodyRef: BodyRefPrefix + "x"}}}, nil
		}, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if gotTenant != "tenant-1" || got.Messages[0].Content != "用户偏好深色主题" {
		t.Fatalf("persist got tenant=%s batch=%+v", gotTenant, got)
	}
	var ack Ack
	_ = json.Unmarshal(w.Body.Bytes(), &ack)
	if ack.BatchID != "b1" || ack.Accepted != 1 || !strings.HasPrefix(ack.Messages[0].BodyRef, BodyRefPrefix) {
		t.Fatalf("ack = %+v", ack)
	}
}

func TestHandlerNegativePaths(t *testing.T) {
	secret := "secret-32-bytes-xxxxxxxxxxxxxxxxx"
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	b := validBatch()
	raw, _ := json.Marshal(b)
	ok := sign(secret, ts, "n-n1", raw)
	okPersist := func(context.Context, string, string, Batch) (Ack, error) { return Ack{}, nil }

	// 无 api key。
	req := postRequest(t, b, ts, "n-n2", ok)
	req.Header.Set("Authorization", "")
	if w := serveCapture(t, secret, fakeKeys{tenant: "t"}, okPersist, req); w.Code != http.StatusUnauthorized {
		t.Fatalf("no key status = %d", w.Code)
	}
	// 坏签名。
	bad := "hmac-sha256=" + "00"
	if w := serveCapture(t, secret, fakeKeys{tenant: "t"}, okPersist, postRequest(t, b, ts, "n-n3", bad)); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad sig status = %d", w.Code)
	}
	// schema 错。
	badBatch := validBatch()
	badBatch.SchemaVersion = "9"
	badRaw, _ := json.Marshal(badBatch)
	if w := serveCapture(t, secret, fakeKeys{tenant: "t"}, okPersist,
		postRequest(t, badBatch, ts, "n-n4", sign(secret, ts, "n-n4", badRaw))); w.Code != http.StatusBadRequest {
		t.Fatalf("bad schema status = %d", w.Code)
	}
	// GET 拒绝。
	get := httptest.NewRequest(http.MethodGet, "/v1/userscript/captures", nil)
	if w := serveCapture(t, secret, fakeKeys{tenant: "t"}, okPersist, get); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d", w.Code)
	}
}

// pgx.Tx 引用仅为保持与 store.go 相同的导入集合（编译期一致性）。
var _ = func() pgx.Tx { return nil }

func hmacSHA256(secret, payload string) string {
	mac := newMAC(secret)
	mac.Write([]byte(payload))
	return fmt.Sprintf("%x", mac.Sum(nil))
}
