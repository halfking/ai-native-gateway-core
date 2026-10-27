package streaming

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/session" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/internal/sessionid"
)

// SessionHeadersPriority mirrors sessionid.DefaultHeadersPriority for
// back-compat readers. Prefer sessionid.Headers() / sessionid.Resolve.
var SessionHeadersPriority = sessionid.DefaultHeadersPriority

// deriveGatewaySessionID delegates to sessionid.DeriveGatewayID (single source).
func deriveGatewaySessionID(sessionID string) string {
	return sessionid.DeriveGatewayID(sessionID)
}

func extractSessionIDFromHeaders(r *http.Request) string {
	if r == nil {
		return ""
	}
	id, _ := sessionid.FromHeaders(r.Header)
	return id
}

// SetSessionIDBodyKeys forwards body-field aliases to sessionid (hot path + cfg).
func SetSessionIDBodyKeys(keys []string) {
	sessionid.SetBodyKeyOverrides(keys)
}

// SetSessionIDHeaders forwards header aliases to sessionid (hot path + cfg).
func SetSessionIDHeaders(keys []string) {
	sessionid.SetHeaderOverrides(keys)
}

// 2026-09-08: 过期的 //nolint:unused 已删除 — 该函数被 messages.go 与
// responses.go 的会话解析调用,并非死代码。
// 2026-10-28: 真值已迁 internal/sessionid；本函数保留无 Derive 语义以兼容
// normalizeAndRegisterClientSession（其内部再 Derive）。
func extractSessionIDFromRequest(r *http.Request, body []byte) string {
	if sessionID := extractSessionIDFromBody(body); sessionID != "" {
		return sessionID
	}
	return extractSessionIDFromHeaders(r)
}

func extractSessionIDFromBody(body []byte) string {
	return sessionid.FromBody(body)
}

func detectAndHandleModelSwitch(
	ctx context.Context,
	sessionPref *session.SessionPreference,
	sessionID string,
	clientModel string,
) (modelChanged bool, previousModel string) {
	if sessionPref == nil || sessionID == "" || clientModel == "" {
		return false, ""
	}
	val, found := sessionPref.Get(ctx, sessionID)
	if !found || val == nil || val.Model == "" {
		return false, ""
	}
	if val.Model == clientModel {
		return false, ""
	}
	prevModel := val.Model
	_ = sessionPref.ClearOnModelSwitch(ctx, sessionID, prevModel, clientModel)
	return true, prevModel
}

func generateSystemSessionID() string {
	return "gw_" + uuid.New().String()
}

// normalizeAndRegisterClientSession 2026-09-08: 将 /v1/messages 与
// /v1/responses 的"客户端已提供 id"分支与 chat 路径(handler.go)对齐 —
// b02a5c385 的确定性映射 + 幂等注册此前只接了 chat:
//  1. 非网关稳定身份(裸 UUID x-session-id)确定性映射 gw_<id>;否则异构
//     命名空间直接落 _request_logs.gw_session_id,session_turns 轮次聚合恒 1;
//  2. Redis 未知的受信 gw_ id(新会话首请求)幂等注册 EnsureV2WithID(异步、
//     尽力而为、带 panic 防护)— 否则每次后续请求都重复 ErrSessionNotFound,
//     Touch 与轮次状态永不生效。branch 命名空间(gt_/gs_)不注册,与 chat 一致。
//
// 返回规范化后的 sessionID 与解析到的 SessionInfo(可为 nil)。
// errClientSessionForbidden is returned by normalizeAndRegisterClientSession
// when a client-supplied session id exists but is not owned by the
// authenticated key/tenant. The handler maps this to a 403 without
// creating a turn (V3-A02 client-bring-session contract).
var errClientSessionForbidden = errors.New("client session forbidden")

// errClientSessionUnavailable is returned when the session store itself
// failed (network / availability), which the handler maps to a 503. This
// is deliberately distinct from errClientSessionForbidden: an ownership
// rejection is the client's fault, a store fault is ours — mixing them
// would 403 legitimate traffic during a Redis outage.
var errClientSessionUnavailable = errors.New("client session store unavailable")

// sessionAbsentErr reports whether a session Get error means "no such
// session (anymore)" as opposed to "the store is degraded".
//
// r1029（2026-10-14）：ErrSessionExpired 此前被当成 store fault 走 503
// 分支——但过期是**确定性终态**（Redis TTL 到期），同 body 重试必再败；
// 实测 auto_summary 内部回环对该 503 反复重试（日志 2040 次 transient
// retry / 4051 次 failed），且 503 响应体曾误用 SESSION_FORBIDDEN 文案
// 把归因引向所有权。语义上过期 ≈ NotFound：id 保持原样重新登记/认领，
// 会话在同一个 id 下重新开始。
func sessionAbsentErr(err error) bool {
	return errors.Is(err, session.ErrSessionNotFound) ||
		errors.Is(err, session.ErrSessionExpired)
}

func normalizeAndRegisterClientSession(
	r *http.Request,
	sessionID string,
	getter interface {
		Get(ctx context.Context, id string) (*session.Session, error)
	},
	keyInfo *authentication.KeyInfo,
) (string, *session.Session, error) {
	sessionID = deriveGatewaySessionID(sessionID)
	if getter == nil || keyInfo == nil || r == nil {
		return sessionID, nil, nil
	}
	si, getErr := getter.Get(r.Context(), sessionID)
	if getErr != nil && !sessionAbsentErr(getErr) {
		// R25-W (V3-A02, 2026-09-30 round 30): a store fault is not a
		// verdict on ownership. Returning (id, nil, nil) here would let an
		// unverified client-supplied session id flow into dispatch while
		// the store is degraded; returning forbidden would blame the
		// client for our outage. Surface a distinct error so handlers can
		// stop dispatch (503) instead.
		return sessionID, nil, errClientSessionUnavailable
	}
	if getErr == nil && si != nil {
		// R24-B (V3-A02, 2026-09-29 round 26): a client-supplied session id
		// must belong to the authenticated key. The chat path has enforced
		// this since handler.go's own lookup (orphan sessions bind to the
		// first key, mismatched owners get 403); without the same check here
		// a sessionInfo from another tenant/key flowed straight into
		// applyResolvedGatewaySession and logCtx. Cross-key usage of a known
		// session is rejected; an orphan (APIKeyID == 0, e.g. legacy rows) is
		// bound to the requesting key before it is adopted.
		if si.APIKeyID != keyInfo.ID {
			if si.APIKeyID == 0 {
				// Orphan adoption is tenant-guarded: a legacy row already
				// stamped with a foreign tenant must not be hijacked by a
				// different tenant's key merely because its APIKeyID is 0.
				if si.TenantID != "" && si.TenantID != keyInfo.TenantID {
					return sessionID, nil, errClientSessionForbidden
				}
				if binder, ok := getter.(interface {
					BindAPIKey(ctx context.Context, sessionID string, apiKeyID int, tenantID string) error
				}); ok {
					if bindErr := binder.BindAPIKey(r.Context(), sessionID, keyInfo.ID, keyInfo.TenantID); bindErr == nil {
						si.APIKeyID = keyInfo.ID
						si.TenantID = keyInfo.TenantID
						return sessionID, si, nil
					} else {
						slog.Warn("client session bind failed",
							"session_id", sessionID, "error", bindErr)
					}
				}
			}
			return sessionID, nil, errClientSessionForbidden
		}
		// Same key but a different tenant row is still a cross-tenant
		// steal attempt (key ids are only unique within their issuer).
		if si.TenantID != "" && si.TenantID != keyInfo.TenantID {
			return sessionID, nil, errClientSessionForbidden
		}
		return sessionID, si, nil
	}
	if sessionAbsentErr(getErr) &&
		strings.HasPrefix(sessionID, "gw_") && !isBranchSessionID(sessionID) {
		regDeviceSeed := r.Header.Get("X-Device-Seed")
		if regDeviceSeed == "" {
			regDeviceSeed = r.Header.Get("X-Machine-Id")
		}
		regTaskID := sanitizeRequestCorrelationID(r.Header.Get("X-Gw-Task-Id"))
		// R25-W (V3-A02, 2026-09-30 round 30): claim the honored id
		// SYNCHRONOUSLY and atomically (see ensureClaimedClientSession).
		claimed, claimErr := ensureClaimedClientSession(r.Context(), getter, sessionID, keyInfo, regDeviceSeed, regTaskID)
		if claimErr != nil {
			return sessionID, nil, claimErr
		}
		if claimed == nil {
			// Getter has no atomic ensurer — the client-selected id cannot
			// be claimed safely, so fall back to a fresh system id rather
			// than trusting an unowned identity.
			return generateSystemSessionID(), nil, nil
		}
		return sessionID, claimed, nil
	}
	return sessionID, nil, nil
}

// ensureClaimedClientSession atomically claims an unknown gw_ session id for
// the authenticated key (R25-W, V3-A02 round 30). The previous fire-and-forget
// registration let two concurrent first requests for the same client-selected
// id both pass with no owner; the winner was decided by whichever goroutine
// landed first — an unowned id could be claimed by whichever tenant raced
// faster. EnsureV2WithID is the atomic insert-if-absent: the first caller
// wins; later claimants receive the winner's session and are adjudicated
// here (same key → adopt; orphan → bind; foreign key/tenant → forbidden).
//
// Returns (claimed, nil) on success, (nil, errClientSessionForbidden |
// errClientSessionUnavailable) on rejection/fault, and (nil, nil) when the
// getter implements no atomic ensurer — callers must then fall back to a
// freshly generated system id instead of trusting the client-supplied one.
func ensureClaimedClientSession(
	ctx context.Context,
	getter interface {
		Get(ctx context.Context, id string) (*session.Session, error)
	},
	sessionID string,
	keyInfo *authentication.KeyInfo,
	deviceSeed, taskID string,
) (*session.Session, error) {
	ensurer, ok := getter.(interface {
		EnsureV2WithID(ctx context.Context, sessionID string, apiKeyID int, tenantID, deviceSeed, taskID string) (*session.Session, bool, error)
	})
	if !ok || ensurer == nil {
		return nil, nil
	}
	claimed, _, ensureErr := ensurer.EnsureV2WithID(ctx, sessionID, keyInfo.ID, keyInfo.TenantID, deviceSeed, taskID)
	if ensureErr != nil {
		slog.Warn("session register (honored id) failed",
			"session_id", sessionID, "error", ensureErr)
		return nil, errClientSessionUnavailable
	}
	if claimed == nil {
		// No session and no error is a store contract violation — treat it
		// as a store fault rather than trusting the unverified id.
		return nil, errClientSessionUnavailable
	}
	if claimed.APIKeyID == keyInfo.ID {
		if claimed.TenantID != "" && claimed.TenantID != keyInfo.TenantID {
			return nil, errClientSessionForbidden
		}
		return claimed, nil
	}
	if claimed.APIKeyID == 0 {
		// Orphan (legacy row / pre-bind race state). Adoption is
		// tenant-guarded: a foreign-tenant orphan is a steal attempt.
		if claimed.TenantID != "" && claimed.TenantID != keyInfo.TenantID {
			return nil, errClientSessionForbidden
		}
		if binder, ok := getter.(interface {
			BindAPIKey(ctx context.Context, sessionID string, apiKeyID int, tenantID string) error
		}); ok {
			if bindErr := binder.BindAPIKey(ctx, sessionID, keyInfo.ID, keyInfo.TenantID); bindErr == nil {
				claimed.APIKeyID = keyInfo.ID
				claimed.TenantID = keyInfo.TenantID
				return claimed, nil
			} else {
				slog.Warn("client session bind failed",
					"session_id", sessionID, "error", bindErr)
			}
		}
		return nil, errClientSessionForbidden
	}
	// Claimed by a foreign key — the race already has a winner.
	return nil, errClientSessionForbidden
}
