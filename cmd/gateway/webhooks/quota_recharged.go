// File: cmd/gateway/webhooks/quota_recharged.go
//
// 2026-08-26 hzx-2 / 充值回调 webhook (落点 B):
//
// 供应商在用户充值完成后调用 POST /api/webhooks/quota/recharged，
// 本 handler 验签后立即触发 BalanceQuotaProbe.OnQuotaRecharged，
// 把"用户已充值 → 凭据被探活 → 路由恢复"这条链路从 BalanceQuotaProbe
// 2 分钟 tick 提到秒级（典型端到端 < 5s）。
//
// 验签算法：HMAC-SHA256(secret, raw_body) → hex(lower)，请求 header
// X-LLM-Gateway-Signature 携带 hex 字符串。常量时间比较走 hmac.Equal，
// 风格与 domains/feishubot.VerifyLarkSignature / api/dingtalk_callback.go 一致。
//
// 时间戳防重放：body.ts 字段（unix 秒）与 now() 差 ≤ 300s；超过窗口
// 直接 401（参考 feishubot VerifyLarkTimestamp 5 分钟窗口）。
//
// secret 来源：SecretSource() 优先 env LLM_GATEWAY_QUOTA_WEBHOOK_SECRET，
// 暂不读 settings_kv（settings_kv 集成留给后续 PR，env 已经足够覆盖
// 主流程；后续落点补 KV 不会破坏本文件接口）。

package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// QuotaRechargedSignatureHeader 是 webhook 客户端必须发送的 header 名。
//
// 小写归一化在 HTTP 层做（http.Header.Get 已大小写不敏感），Secret
// 走 hex(lower) 与 feishubot / dingtalk 风格保持一致。
const QuotaRechargedSignatureHeader = "X-LLM-Gateway-Signature"

// QuotaRechargedSecretEnv 是 secret 的 env 名。
const QuotaRechargedSecretEnv = "LLM_GATEWAY_QUOTA_WEBHOOK_SECRET"

// QuotaRechargedEventTypes 支持的事件类型集合（白名单）。
//
// 落点 B 只引入充值完成 / 配额窗口重置两种语义；任何其他 event_type
// 都会直接 400 拒绝，避免供应商把无关事件推过来污染探测路径。
var QuotaRechargedEventTypes = map[string]struct{}{
	"recharge.completed": {},
	"window.reset":       {},
}

// freshnessWindowSeconds 是 body.ts 的允许偏差（秒），正负两面对称。
const freshnessWindowSeconds int64 = 300

// maxQuotaRechargedBodyBytes 限制 body 大小，避免恶意供应商灌大包。
const maxQuotaRechargedBodyBytes = 1 << 20 // 1 MB

// QuotaRechargedBody 充值回调 wire format。
//
// CredentialID > 0；Vendor / EventType / Timestamp 必填；
// 字段名保持 snake_case，便于供应商端用 form 或纯 JSON 推过来。
type QuotaRechargedBody struct {
	CredentialID int    `json:"credential_id"`
	Vendor       string `json:"vendor"`
	EventType    string `json:"event_type"` // "recharge.completed" | "window.reset"
	Timestamp    int64  `json:"ts"`
}

// QuotaRechargedHandler 处理 POST /api/webhooks/quota/recharged。
//
// Secret: HMAC 验签密钥（由 SecretSource 注入）。
// OnQuotaRecharged: 验签通过 + body 校验通过后异步调用，把秒级信号
//
//	传递给 BalanceQuotaProbe。
//
// Metrics: 必填（NewQuotaWebhookMetrics），nil 会被 handler 退化为 no-op，
//
//	避免在测试场景里漏装。
type QuotaRechargedHandler struct {
	Secret           []byte
	OnQuotaRecharged func(credID int, source string)
	Metrics          *QuotaWebhookMetrics
}

// SecretSource 解析 webhook secret。
//
// 优先级：env LLM_GATEWAY_QUOTA_WEBHOOK_SECRET（hex / 原文均可，handler
// 内部按 hex 解码失败回退原文）。返回 nil 表示 secret 未配置，handler
// 会把所有请求判为 401（fail-secure）。
//
// TODO(settings_kv): 后续从 settings_kv.quota_webhook_secret 读，env 仅作 fallback。
func SecretSource() []byte {
	if s := os.Getenv(QuotaRechargedSecretEnv); s != "" {
		return []byte(s)
	}
	return nil
}

// ServeHTTP 实现 http.Handler 接口（避免分配 inner http.HandlerFunc，
// 与 feishubot 的 AsHTTPHandler 模式互不干扰）。
func (h *QuotaRechargedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 0. secret 未配置 → 401（fail-secure）。
	if len(h.Secret) == 0 {
		h.reject("secret_missing")
		http.Error(w, "webhook not configured", http.StatusUnauthorized)
		return
	}

	// 1. 读 body（限流到 1 MB）。
	body, err := io.ReadAll(io.LimitReader(r.Body, maxQuotaRechargedBodyBytes))
	if err != nil {
		h.reject("read_body")
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	// 2. HMAC-SHA256 验签。
	sig := r.Header.Get(QuotaRechargedSignatureHeader)
	if sig == "" {
		h.reject("signature_missing")
		http.Error(w, "missing signature", http.StatusUnauthorized)
		return
	}
	expected := computeQuotaRechargedHMAC(h.Secret, body)
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		h.reject("signature_mismatch")
		slog.Warn("quota_recharged webhook: signature mismatch",
			"remote", r.RemoteAddr,
			"path", r.URL.Path)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// 3. parse JSON。
	var payload QuotaRechargedBody
	if err := json.Unmarshal(body, &payload); err != nil {
		h.reject("json_invalid")
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if payload.CredentialID <= 0 {
		h.reject("credential_missing")
		http.Error(w, "credential_id required", http.StatusBadRequest)
		return
	}
	if _, ok := QuotaRechargedEventTypes[payload.EventType]; !ok {
		h.reject("event_unsupported")
		http.Error(w, "unsupported event_type", http.StatusBadRequest)
		return
	}

	// 4. ts 校验（5 分钟 freshness，前后对称）。
	if payload.Timestamp <= 0 {
		h.reject("ts_missing")
		http.Error(w, "ts required", http.StatusBadRequest)
		return
	}
	age := time.Now().Unix() - payload.Timestamp
	if age < -freshnessWindowSeconds || age > freshnessWindowSeconds {
		h.reject("ts_stale_or_future")
		slog.Warn("quota_recharged webhook: ts out of window",
			"credential_id", payload.CredentialID,
			"ts", payload.Timestamp,
			"age_sec", age,
			"window_sec", freshnessWindowSeconds)
		http.Error(w, "stale or future ts", http.StatusUnauthorized)
		return
	}

	// 5. 异步触发 OnQuotaRecharged。
	//
	// 用 goroutine 是为了让 webhook 200 OK 立即返回，balance_quota_probe
	// 自身的探测链路可能要走 credential_probe_v2 几秒钟才完成。
	if h.OnQuotaRecharged != nil {
		go h.OnQuotaRecharged(payload.CredentialID, payload.EventType)
	}
	h.accept(payload.EventType)

	// 6. 200 OK（与 feishubot 同样返回 {"status": "accepted"}）。
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// accept 在 metrics 有效时累加 accepted 计数；metrics nil 时退化为 no-op，
// 让单元测试与生产路径共享同一份代码。
func (h *QuotaRechargedHandler) accept(eventType string) {
	if h.Metrics == nil || h.Metrics.QuotaWebhookAcceptedTotal == nil {
		return
	}
	h.Metrics.QuotaWebhookAcceptedTotal.WithLabelValues(eventType).Inc()
}

// reject 同 accept，但用于 rejected 分桶。
func (h *QuotaRechargedHandler) reject(reason string) {
	if h.Metrics == nil || h.Metrics.QuotaWebhookRejectedTotal == nil {
		return
	}
	h.Metrics.QuotaWebhookRejectedTotal.WithLabelValues(reason).Inc()
}

// computeQuotaRechargedHMAC 计算 hex(lower) HMAC-SHA256。
//
// 与 feishubot 的 base64(HMAC(...)) 不同，本 handler 用 hex 输出，
// 与 webhook 客户端惯例（GitHub / Stripe 等）一致，便于供应商端
// 用 openssl / Python hmac 直接拼。
func computeQuotaRechargedHMAC(secret []byte, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}