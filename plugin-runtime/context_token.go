package pluginruntime

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// SignContextRequest 把签名内部上下文头写进一个新 request（测试用）。
func SignContextRequest(secret []byte, pluginID, tenantID string, ts int64, nonce string) *http.Request {
	r := &http.Request{Header: http.Header{}}
	r.Header.Set("X-Gateway-Plugin-ID", pluginID)
	r.Header.Set("X-Gateway-Tenant-ID", tenantID)
	r.Header.Set("X-Gateway-Context-Timestamp", strconv.FormatInt(ts, 10))
	r.Header.Set("X-Gateway-Context-Nonce", nonce)
	r.Header.Set("X-Gateway-Context-Signature", signContext(secret, pluginID, tenantID, ts, nonce))
	return r
}

// signContext 必须与 plugin 端 ai-session-manager/internal/plugin/context.go
// 保持字节一致：HMAC-SHA256(secret, fmt.Sprintf("%s|%s|%d|%s", pluginID, tenantID, ts, nonce))，hex 编码。
func signContext(secret []byte, pluginID, tenantID string, ts int64, nonce string) string {
	msg := fmt.Sprintf("%s|%s|%d|%s", pluginID, tenantID, ts, nonce)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignHeaders 把签名头加到已有 Header（供 proxy 注入到转发请求）。
func SignHeaders(h http.Header, secret []byte, pluginID, tenantID string) {
	ts := time.Now().Unix()
	nonce := newNonce(ts)
	h.Set("X-Gateway-Plugin-ID", pluginID)
	h.Set("X-Gateway-Tenant-ID", tenantID)
	h.Set("X-Gateway-Context-Timestamp", strconv.FormatInt(ts, 10))
	h.Set("X-Gateway-Context-Nonce", nonce)
	h.Set("X-Gateway-Context-Signature", signContext(secret, pluginID, tenantID, ts, nonce))
}

// newNonce 返回 "<ts>-<随机 hex>"，保证同秒多次调用 nonce 不同（避免 NonceCache 误判重放）。
func newNonce(ts int64) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read 几乎不会失败；退化用 ts 仍可工作（仅丧失同秒唯一性）。
		return strconv.FormatInt(ts, 10)
	}
	return strconv.FormatInt(ts, 10) + "-" + hex.EncodeToString(b[:])
}
