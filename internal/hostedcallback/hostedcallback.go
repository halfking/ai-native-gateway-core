// Package hostedcallback provides the delivery mechanics for hosted-task
// signed callbacks: SSRF-validated URLs (acceptance-time gate mirrors the
// safehttpclient blocked ranges; delivery-time protection is safehttpclient
// itself) and HMAC-signed POST envelopes (outbox.SignPayload headers).
//
// 签名契约复用 internal/outbox（§2.4）：SignPayload(secret, timestamp, nonce,
// body) = "hmac-sha256=<hex>"，头为 X-Gateway-Event-Signature / -Timestamp /
// -Nonce，外加 X-Correlation-ID。
package hostedcallback

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/outbox"
	"github.com/kaixuan/llm-gateway-go/internal/safehttpclient"
)

// 头名与 outbox 对齐（同一套签名语义，接收方按同一契约验签）。
const (
	HeaderSignature     = outbox.HeaderSignature
	HeaderTimestamp     = outbox.HeaderTimestamp
	HeaderNonce         = outbox.HeaderNonce
	HeaderCorrelationID = outbox.HeaderCorrelationID
)

// Deliverer 投递一次签名回调。
type Deliverer struct {
	client *safehttpclient.SafeHTTPClient
	now    func() time.Time
	nonce  func() (string, error)
}

// NewDeliverer 构造投递器。timeout 为单次投递超时；allowlist 显式开启内网
// 目标（§9：callback SSRF 对策 —— safehttpclient+allowlist+redirect 复验）。
func NewDeliverer(timeout time.Duration, allowlist []string) *Deliverer {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Deliverer{
		client: safehttpclient.NewWithAllowlist(timeout, allowlist),
		now:    time.Now,
		nonce:  randomNonce,
	}
}

// SetTestHooks 覆盖时间/随机源（测试用）。
func (d *Deliverer) SetTestHooks(now func() time.Time, nonce func() (string, error)) {
	if now != nil {
		d.now = now
	}
	if nonce != nil {
		d.nonce = nonce
	}
}

// Result 是一次投递的结果。
type Result struct {
	StatusCode int  // 0 = 网络层失败
	Delivered  bool // 2xx
	Retryable  bool // 网络错误/5xx；4xx 不重试（矩阵 E）
}

// Deliver POST payload 到 urlStr，带 HMAC 签名头。correlationID 通常为
// hosted_task_id。
func (d *Deliverer) Deliver(ctx context.Context, urlStr, secret string, payload []byte, correlationID string) (Result, error) {
	nonce, err := d.nonce()
	if err != nil {
		return Result{}, fmt.Errorf("hostedcallback: nonce: %w", err)
	}
	timestamp := d.now().UTC().Format(time.RFC3339Nano)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(payload))
	if err != nil {
		return Result{}, fmt.Errorf("hostedcallback: build: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, outbox.SignPayload(secret, timestamp, nonce, payload))
	if correlationID != "" {
		req.Header.Set(HeaderCorrelationID, correlationID)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		// safehttpclient 的 SSRF 拒绝视为不可重试（目标非法，重试无意义）。
		if strings.Contains(err.Error(), "ssrf:") {
			return Result{Retryable: false}, err
		}
		return Result{Retryable: true}, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return Result{StatusCode: resp.StatusCode, Delivered: true}, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// 4xx：接收方明确拒绝，重试无意义 → DLQ（矩阵 E）。
		return Result{StatusCode: resp.StatusCode, Retryable: false}, fmt.Errorf("hostedcallback: receiver rejected with %d", resp.StatusCode)
	default:
		return Result{StatusCode: resp.StatusCode, Retryable: true}, fmt.Errorf("hostedcallback: receiver returned %d", resp.StatusCode)
	}
}

// ─── 受理时 URL 校验（§4.1：callback SSRF 校验在创建时完成）────────────────

// ValidateURL 在任务受理时校验回调 URL：仅 http/https；字面 IP 与解析目标
// 不得落在私网/回环/metadata 等阻断段，除非命中 allowlist。与
// safehttpclient 的投递时防线一致（acceptance gate + delivery gate）。
func ValidateURL(rawURL string, allowlist []string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("callback url parse: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("callback url scheme %q not allowed (http/https only)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("callback url missing host")
	}
	if strings.Contains(host, "@") {
		return fmt.Errorf("callback url host contains @ (parser bypass)")
	}
	al := newAllowlist(allowlist)
	if al.IsAllowed(host) {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("callback url IP %s is in blocked range (private/loopback/link-local/metadata)", host)
		}
		return nil
	}
	return nil
}

// HostHash 返回 URL 的 sha256 诊断指纹（hosted_tasks.callback_url_hash）。
func HostHash(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return hex.EncodeToString(sum[:])
}

func randomNonce() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// ─── allowlist（与 safehttpclient 语义一致：精确主机 / CIDR / *.后缀）──────

type allowlist struct {
	exact   map[string]bool
	cidrs   []*net.IPNet
	suffixs []string
}

func newAllowlist(rules []string) *allowlist {
	al := &allowlist{exact: map[string]bool{}}
	for _, r := range rules {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if strings.HasPrefix(r, "*.") {
			al.suffixs = append(al.suffixs, strings.ToLower(strings.TrimPrefix(r, "*.")))
			continue
		}
		if _, cidr, err := net.ParseCIDR(r); err == nil {
			al.cidrs = append(al.cidrs, cidr)
			continue
		}
		al.exact[strings.ToLower(r)] = true
	}
	return al
}

func (al *allowlist) IsAllowed(host string) bool {
	if al == nil || host == "" {
		return false
	}
	host = strings.ToLower(host)
	if al.exact[host] {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		for _, c := range al.cidrs {
			if c.Contains(ip) {
				return true
			}
		}
		return false
	}
	for _, s := range al.suffixs {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

// isBlockedIP 与 safehttpclient 的阻断段保持一致（私网/回环/link-local/
// metadata/CGNAT/ULA/组播/文档段）。此处独立维护受理时快检；投递时仍由
// safehttpclient 全量复验（含 DNS rebinding 防御）。
func isBlockedIP(ip net.IP) bool {
	blocked := []string{
		"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "100.64.0.0/10", "198.18.0.0/15", "224.0.0.0/4",
		"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "255.255.255.255/32",
		"0.0.0.0/32",
		"::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "::/128",
	}
	for _, cidr := range blocked {
		_, n, _ := net.ParseCIDR(cidr)
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// BuildEnvelope 渲染回调投递体（§3.1 ⑤：终态通知 + result 引用）。固定
// event_id 供接收方幂等（重投不重复执行）。
func BuildEnvelope(eventID, eventType, taskID, tenantID string, payload map[string]any) ([]byte, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	body := map[string]any{
		"event_id":       eventID,
		"schema_version": "1.0",
		"type":           eventType,
		"hosted_task_id": taskID,
		"tenant_id":      tenantID,
		"occurred_at":    time.Now().UTC().Format(time.RFC3339Nano),
		"payload":        payload,
	}
	return json.Marshal(body)
}
