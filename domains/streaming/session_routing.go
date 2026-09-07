package streaming

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/session" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/settings"
)

var SessionHeadersPriority = []string{
	"X-Gw-Session-Id",
	"X-Session-Id",
	"X-Conversation-Id",
	"X-Chat-Session-Id",
	"X-Thread-Id",
}

// deriveGatewaySessionID maps a non-gateway client session identity onto the
// canonical gateway namespace instead of discarding it. Clients in the wild
// (e.g. ZCode's per-session bare-UUID x-session-id) send a STABLE id with no
// gw_/gt_/gs_ prefix; the previous behaviour treated such ids as unknown and
// minted a fresh gw_<uuid> per request via the CreateV2 fallback, so one
// client conversation never grouped under a single request_logs.gw_session_id
// and session_turns stayed at turn 1 forever. Deriving "gw_" + <client id>
// keeps the gateway namespace canonical while making the mapping
// deterministic: the derived id registers via the honored gw_ branch
// (EnsureV2WithID) and resolves via Get→Touch from the second request on, so
// turns accumulate per client session. Input must already be sanitized.
func deriveGatewaySessionID(sessionID string) string {
	if sessionID == "" ||
		strings.HasPrefix(sessionID, "gw_") ||
		strings.HasPrefix(sessionID, "gt_") ||
		strings.HasPrefix(sessionID, "gs_") {
		return sessionID
	}
	return "gw_" + sessionID
}

func extractSessionIDFromHeaders(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, header := range SessionHeadersPriority {
		value := r.Header.Get(header)
		if value == "" {
			continue
		}
		if header == "X-Gw-Session-Id" {
			return sanitizeGwSessionHeader(value)
		}
		return sanitizeRequestCorrelationID(value)
	}
	return ""
}

var defaultSessionFieldPriority = []string{
	"gwsessionid",
	"sessionid",
	"session",
	"conversationid",
	"chatsessionid",
	"threadid",
}

var sessionBodyKeyOverrides []string

func SetSessionIDBodyKeys(keys []string) {
	sessionBodyKeyOverrides = append([]string(nil), keys...)
}

func configuredSessionFieldPriority() []string {
	seen := make(map[string]struct{}, len(defaultSessionFieldPriority))
	merged := make([]string, 0, len(defaultSessionFieldPriority)+4)
	for _, key := range defaultSessionFieldPriority {
		seen[key] = struct{}{}
		merged = append(merged, key)
	}
	for _, item := range sessionBodyKeyOverrides {
		appendSessionFieldAlias(&merged, seen, item)
	}
	for _, item := range sessionBodyKeySettings() {
		appendSessionFieldAlias(&merged, seen, item)
	}
	return merged
}

func appendSessionFieldAlias(target *[]string, seen map[string]struct{}, item string) {
	normalized := normalizeSessionFieldName(item)
	if normalized == "" {
		return
	}
	if _, ok := seen[normalized]; ok {
		return
	}
	seen[normalized] = struct{}{}
	*target = append(*target, normalized)
}

func sessionBodyKeySettings() []string {
	if settings.Global == nil {
		return nil
	}
	raw, _, err := settings.Global.EffectiveValue(settings.ScopePlatform, "session.id_body_keys", "")
	if err != nil || len(raw) == 0 {
		return nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return splitSessionFieldList(single)
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	return nil
}

func splitSessionFieldList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func extractSessionIDFromRequest(r *http.Request, body []byte) string { //nolint:unused
	if sessionID := extractSessionIDFromBody(body); sessionID != "" {
		return sessionID
	}
	return extractSessionIDFromHeaders(r)
}

func extractSessionIDFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return findSessionIDValue(payload)
}

func findSessionIDValue(node any) string {
	switch typed := node.(type) {
	case map[string]any:
		for _, key := range configuredSessionFieldPriority() {
			for rawKey, rawValue := range typed {
				if normalizeSessionFieldName(rawKey) != key {
					continue
				}
				if sessionID := sessionValueString(rawValue); sessionID != "" {
					return sessionID
				}
			}
		}
		for _, rawValue := range typed {
			if sessionID := findSessionIDValue(rawValue); sessionID != "" {
				return sessionID
			}
		}
	case []any:
		for _, item := range typed {
			if sessionID := findSessionIDValue(item); sessionID != "" {
				return sessionID
			}
		}
	}
	return ""
}

func sessionValueString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = sanitizeRequestCorrelationID(text)
	if strings.HasPrefix(text, "gw_") {
		return sanitizeGwSessionHeader(text)
	}
	return text
}

func normalizeSessionFieldName(value string) string {
	replacer := strings.NewReplacer("-", "", "_", "", ".", "", " ", "")
	return strings.ToLower(replacer.Replace(strings.TrimSpace(value)))
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
//     命名空间直接落 request_logs.gw_session_id,session_turns 轮次聚合恒 1;
//  2. Redis 未知的受信 gw_ id(新会话首请求)幂等注册 EnsureV2WithID(异步、
//     尽力而为、带 panic 防护)— 否则每次后续请求都重复 ErrSessionNotFound,
//     Touch 与轮次状态永不生效。branch 命名空间(gt_/gs_)不注册,与 chat 一致。
// 返回规范化后的 sessionID 与解析到的 SessionInfo(可为 nil)。
func normalizeAndRegisterClientSession(
	r *http.Request,
	sessionID string,
	getter interface {
		Get(ctx context.Context, id string) (*session.Session, error)
	},
	keyInfo *authentication.KeyInfo,
) (string, *session.Session) {
	sessionID = deriveGatewaySessionID(sessionID)
	if getter == nil || keyInfo == nil || r == nil {
		return sessionID, nil
	}
	si, getErr := getter.Get(r.Context(), sessionID)
	if getErr == nil && si != nil {
		return sessionID, si
	}
	if getErr == session.ErrSessionNotFound &&
		strings.HasPrefix(sessionID, "gw_") && !isBranchSessionID(sessionID) {
		if ensurer, ok := getter.(interface {
			EnsureV2WithID(ctx context.Context, sessionID string, apiKeyID int, tenantID, deviceSeed, taskID string) (*session.Session, bool, error)
		}); ok {
			regDeviceSeed := r.Header.Get("X-Device-Seed")
			if regDeviceSeed == "" {
				regDeviceSeed = r.Header.Get("X-Machine-Id")
			}
			regTaskID := sanitizeRequestCorrelationID(r.Header.Get("X-Gw-Task-Id"))
			go func(sid string, key *authentication.KeyInfo, seed, task string) {
				defer func() {
					if rec := recover(); rec != nil {
						slog.Warn("session register (honored id) panicked",
							"session_id", sid, "panic", rec)
					}
				}()
				regCtx, regCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer regCancel()
				if _, _, err := ensurer.EnsureV2WithID(regCtx, sid, key.ID, key.TenantID, seed, task); err != nil {
					slog.Warn("session register (honored id) failed",
						"session_id", sid, "error", err)
				}
			}(sessionID, keyInfo, regDeviceSeed, regTaskID)
		}
	}
	return sessionID, nil
}
