// Package userscriptcapture — 网关侧 userscript capture 摄取
// （P0-3 M1，ADR docs/adr/2026-11-02-userscript-capture-gateway-endpoint.md）。
//
// M1 范围：D2 API + D3 认证 + D5 存储 + D4 body_ref 签发。
// 不在 M1：outbox 事件（M2）、响应携带 canonical session（M3）。
//
// 认证两层（ADR D3）：
//   - 传输层：X-Userscript-Signature/Timestamp/Nonce 三头 HMAC-SHA256，
//     签名内容 timestamp.nonce.raw_body（与 ASM ingress 同款语义）；
//     时间窗 ±5m，nonce 防重放（内存 LRU，进程级；多副本下窗内重放
//     风险由 HMAC secret 独立性 + 时间窗收敛，跨副本 nonce 表记 M2 债）。
//   - 身份层：Authorization Bearer <api-key>（数据面验证 → tenant）+
//     X-End-User-Id（用户身份，缺省 "anonymous"）。
package userscriptcapture

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"sync"
	"time"
)

const (
	// HeaderNames 与 ASM 契约（contracts/userscript-capture.md）一致。
	HeaderSignature = "X-Userscript-Signature"
	HeaderTimestamp = "X-Userscript-Timestamp"
	HeaderNonce     = "X-Userscript-Nonce"

	SignatureScheme = "hmac-sha256="

	// DefaultTimestampWindow 对齐 ASM ingress 的 5 分钟窗。
	DefaultTimestampWindow = 5 * time.Minute
	// DefaultNonceTTL 对齐 ASM：10 分钟（> 时间窗，重放窗内必拒）。
	DefaultNonceTTL = 10 * time.Minute
)

var (
	errSignature   = errors.New("userscript capture: signature invalid")
	errTimestamp   = errors.New("userscript capture: timestamp outside window")
	errNonceReplay = errors.New("userscript capture: nonce replayed")
)

// newMAC 构造 HMAC-SHA256（测试与 VerifyRaw 共用同款原语）。
func newMAC(secret string) hash.Hash {
	mac := hmac.New(func() hash.Hash { return sha256.New() }, []byte(secret))
	return mac
}

// Verifier 校验 HMAC 三头。
type Verifier struct {
	secret []byte
	window time.Duration
	nonce  *nonceCache
	now    func() time.Time
}

// NewVerifier 构造；secret 为空即未配置（handler 侧不挂路由）。
func NewVerifier(secret string, window time.Duration) *Verifier {
	if window <= 0 {
		window = DefaultTimestampWindow
	}
	return &Verifier{
		secret: []byte(secret),
		window: window,
		nonce:  newNonceCache(DefaultNonceTTL),
		now:    time.Now,
	}
}

// VerifyRaw 校验原始请求要素，返回失败原因（nil=通过）。
// signed = timestamp + "." + nonce + "." + rawBody。
func (v *Verifier) VerifyRaw(timestamp, nonce, rawBody []byte, signature string) error {
	if v == nil || len(v.secret) == 0 {
		return errSignature
	}
	mac := hmac.New(func() hash.Hash { return sha256.New() }, v.secret)
	mac.Write(timestamp)
	mac.Write([]byte{'.'})
	mac.Write(nonce)
	mac.Write([]byte{'.'})
	mac.Write(rawBody)
	want := SignatureScheme + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(signature)) {
		return errSignature
	}
	// 时间窗（timestamp 为 Unix 秒）。
	var ts int64
	for _, c := range timestamp {
		if c < '0' || c > '9' {
			return errTimestamp
		}
		ts = ts*10 + int64(c-'0')
	}
	now := v.now()
	sample := time.Unix(ts, 0)
	if diff := now.Sub(sample); diff > v.window || diff < -v.window {
		return errTimestamp
	}
	if !v.nonce.claim(string(nonce)) {
		return errNonceReplay
	}
	return nil
}

// nonceCache 进程内防重放（近似 LRU：超窗即过期）。
type nonceCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
	now  func() time.Time
}

func newNonceCache(ttl time.Duration) *nonceCache {
	return &nonceCache{ttl: ttl, seen: map[string]time.Time{}, now: time.Now}
}

func (c *nonceCache) claim(nonce string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	// 惰性清理：规模小（窗内请求数），全扫可接受。
	for n, at := range c.seen {
		if now.Sub(at) > c.ttl {
			delete(c.seen, n)
		}
	}
	if _, dup := c.seen[nonce]; dup {
		return false
	}
	c.seen[nonce] = now
	return true
}
