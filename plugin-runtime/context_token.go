package pluginruntime

import (
	"crypto/hmac"
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
	nonce := fmt.Sprintf("%d", ts) // P0 简单 nonce；P3 改随机
	h.Set("X-Gateway-Plugin-ID", pluginID)
	h.Set("X-Gateway-Tenant-ID", tenantID)
	h.Set("X-Gateway-Context-Timestamp", strconv.FormatInt(ts, 10))
	h.Set("X-Gateway-Context-Nonce", nonce)
	h.Set("X-Gateway-Context-Signature", signContext(secret, pluginID, tenantID, ts, nonce))
}
