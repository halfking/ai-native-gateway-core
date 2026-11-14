package streaming

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // same handler authentication surface
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

type sanitizeVerifiedKeyContextKey struct{}

type sanitizeVerifiedKey struct {
	rawKey string
	info   *authentication.KeyInfo
	err    error
}

// prepareSanitizeRequest resolves tenant identity before allocating a
// tenant-scoped placeholder. The verified result is memoized for the handler's
// normal auth branch, so Verify's cache/touch side effects run only once.
func (h *ChatHandler) prepareSanitizeRequest(r *http.Request) (*http.Request, bool) {
	if h == nil || r == nil {
		return r, false
	}
	if h.keyVerifier == nil || !h.keyVerifier.Enabled() {
		// Chat's static-key fallback authenticates inside serveWithExecutor.
		// Reject before the sanitizer can persist attacker-controlled mappings.
		if h.staticDataPlaneKey != "" {
			rawKey := extractBearerToken(r)
			if rawKey == "" || subtle.ConstantTimeCompare([]byte(h.staticDataPlaneKey), []byte(rawKey)) != 1 {
				return r, false
			}
		}
		// Local no-verifier mode has one fixed tenant. Never use a caller header.
		return r.WithContext(sanitize.WithAuthenticatedTenant(r.Context(), "default")), true
	}
	rawKey := extractBearerToken(r)
	if rawKey == "" {
		// The existing handler emits its protocol-specific missing-key error.
		return r, false
	}
	info, err := h.keyVerifier.Verify(r.Context(), rawKey)
	if err == nil && info == nil {
		err = errors.New("key verifier returned nil key info")
	}
	ctx := context.WithValue(r.Context(), sanitizeVerifiedKeyContextKey{}, sanitizeVerifiedKey{
		rawKey: rawKey, info: info, err: err,
	})
	if err != nil {
		// The normal handler consumes the memoized failure and cannot dispatch.
		return r.WithContext(ctx), false
	}
	ctx = sanitize.WithAuthenticatedTenant(ctx, tenant(info))
	return r.WithContext(ctx), true
}

func verifyRequestKey(r *http.Request, verifier requestKeyVerifier, rawKey string) (*authentication.KeyInfo, error) {
	if r != nil {
		if cached, ok := r.Context().Value(sanitizeVerifiedKeyContextKey{}).(sanitizeVerifiedKey); ok && cached.rawKey == rawKey {
			return cached.info, cached.err
		}
	}
	return verifier.Verify(r.Context(), rawKey)
}

// VerifiedKeyInfoFromContext 返回请求上已验证的调用方 KeyInfo（nil = 无 DB
// key 身份：静态 key 旁路 / 内部回环 / 无验证模式）。
//
// 2026-11-15（限流统一按 key 轮）：路由 wrap 层（异常检测观察面、per-key
// 并发闸）从此处读真实身份。此前 wrapAnomalyDetector 读裸字符串 context 键
// "api_key_id"/"session_id"——全仓无任何写入方 ⇒ 恒零值，异常检测的 api_key_id
// 指标自上线以来恒为 0（2026-11-14 211 事故取证定案）。本函数是唯一读取口，
// 不得再出现第二份字符串键读取。
func VerifiedKeyInfoFromContext(ctx context.Context) *authentication.KeyInfo {
	if cached, ok := ctx.Value(sanitizeVerifiedKeyContextKey{}).(sanitizeVerifiedKey); ok {
		return cached.info
	}
	return nil
}

// WithVerifiedKeyInfoForTest 仅供测试构造已验证 key 上下文（生产路径由
// prepareSanitizeRequest 装配）。
func WithVerifiedKeyInfoForTest(ctx context.Context, info *authentication.KeyInfo) context.Context {
	return context.WithValue(ctx, sanitizeVerifiedKeyContextKey{}, sanitizeVerifiedKey{info: info})
}
