// handler.go — POST /v1/userscript/captures 用户态摄取端点（P0-3 M1）。
package userscriptcapture

import (
	"context"
	"log/slog"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// KeyVerifier 是身份层的最小验证面（domains/authentication.KeyVerifier
// 的窄化）：raw api key → tenant id。
type KeyVerifier interface {
	VerifyKeyTenant(rawKey string) (tenantID string, ok bool)
}

// HandlerConfig 构造参数。Secret 空 = 未装配（main 不挂路由）。
type HandlerConfig struct {
	Secret       string
	Keys         KeyVerifier
	Store        *Store
	OutboxWriter OutboxWriter
}

// Handler 服务摄取端点。
type Handler struct {
	verifier *Verifier
	keys     KeyVerifier
	store    *Store
	// outboxWriter 由装配层注入（M2：同事务写 userscript.captured.v1；
	// nil = 事件未装配——M1 形态，仅摄取落库）。
	outboxWriter OutboxWriter
	// persistHook 是测试注入点（非空时替代 store.Persist）。
	persistHook func(ctx context.Context, tenant, endUser string, b Batch) (Ack, error)
	// sessionExists 可注入（测试）；nil 时用 store.SessionExists。
	sessionExists func(ctx context.Context, tenantID, sessionID string) bool
}

// NewHandler 构造。
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		verifier:    NewVerifier(cfg.Secret, 0),
		keys:        cfg.Keys,
		store:       cfg.Store,
		outboxWriter: cfg.OutboxWriter,
	}
}

// ServeHTTP 实现 POST /v1/userscript/captures。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	// 身份层：Bearer api key → tenant。
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	tenantID, ok := h.keys.VerifyKeyTenant(raw)
	if !ok || tenantID == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid or missing api key")
		return
	}
	endUser := strings.TrimSpace(r.Header.Get("X-End-User-Id"))
	if endUser == "" {
		endUser = "anonymous"
	}
	// 传输层：HMAC 三头。
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxEnvelopeBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	if len(body) > MaxEnvelopeBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", "batch exceeds envelope cap")
		return
	}
	sig := r.Header.Get(HeaderSignature)
	ts := r.Header.Get(HeaderTimestamp)
	nonce := r.Header.Get(HeaderNonce)
	if sig == "" || ts == "" || nonce == "" {
		writeErr(w, http.StatusUnauthorized, "signature_missing", "HMAC headers required")
		return
	}
	if err := h.verifier.VerifyRaw([]byte(ts), []byte(nonce), body, sig); err != nil {
		writeErr(w, http.StatusUnauthorized, "signature_invalid", err.Error())
		return
	}

	var batch Batch
	if jsonErr := json.Unmarshal(body, &batch); jsonErr != nil {
		writeErr(w, http.StatusBadRequest, "schema_invalid", "invalid JSON body")
		return
	}
	if err := batch.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "schema_invalid", err.Error())
		return
	}

	var ack Ack
	var persistErr error
	if h.persistHook != nil {
		ack, persistErr = h.persistHook(r.Context(), tenantID, endUser, batch)
	} else {
		ack, persistErr = h.store.Persist(r.Context(), tenantID, endUser, batch, h.outboxWriter)
	}
	if persistErr != nil {
		slog.Warn("userscript capture persist failed", "tenant", tenantID, "error", persistErr)
		writeErr(w, http.StatusServiceUnavailable, "capture_unavailable", "persistence failed")
		return
	}
	// M3：请求标注的 canonical 会话（X-Gw-Session-Id）经存在性校验后
	// 随 ack 返回——纯浏览器批次的关联缺口由这一跳闭合；不存在的
	// 会话静默不标注（userscript 的头可能来自任意页面，不报错）。
	if sid := strings.TrimSpace(r.Header.Get("X-Gw-Session-Id")); sid != "" {
		if h.sessionExists == nil {
			h.sessionExists = h.store.SessionExists
		}
		if h.sessionExists(r.Context(), tenantID, sid) {
			ack.CanonicalSession = sid
		}
	}
	writeJSON(w, http.StatusOK, ack)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "message": msg})
}
