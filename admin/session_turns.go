// Package admin — session_turns.go
//
// 2026-09-01 死代码清理（round2）：原 v1 SessionTurnsHandler（HTTP 路由 +
// listTurns/getTurn/snapshot stub + AdminClaims）从未在生产路由挂载，
// 已整体删除；生产路径由 v2 接管（见 session_state_handlers.go 的
// serveSessionTurnSubroute → session_turns_v2.go）。相关审计记录：
// docs/audit/2026-09-01-deadcode-cleanup-round2.md
//
// 本文件仅保留被 session_turns_v2.go / turns_list.go / turns_sessions.go
// 共用的 cursor 编解码与 TurnListItem 符号（从原文件原样迁出，零行为变更）。
package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

type TurnListItem struct {
	TurnNo           int                    `json:"turn_no"`
	Ts               time.Time              `json:"ts"`
	RequestID        string                 `json:"request_id"`
	Title            string                 `json:"title,omitempty"`
	Summary          string                 `json:"summary,omitempty"`
	RequestTokens    int                    `json:"request_tokens"`
	ResponseTokens   int                    `json:"response_tokens"`
	CostUSD          float64                `json:"cost_usd"`
	Model            string                 `json:"model"`
	Provider         string                 `json:"provider"`
	StatusCode       int                    `json:"status_code"`
	LatencyMs        *int                   `json:"latency_ms,omitempty"`
	SubmitMode       string                 `json:"submit_mode"`
	InjectionVerdict string                 `json:"injection_verdict"`
	OutputVerdict    string                 `json:"output_verdict"`
	AttachmentCount  int                    `json:"attachment_count"`
	ChildRequests    []*SessionChildRequest `json:"child_requests"`
	Digest           *TurnDigest            `json:"digest,omitempty"`
}

const (
	defaultTurnsLimit = 50
	maxTurnsLimit     = 200
)

type cursorPayload struct {
	TenantID  string    `json:"t"`
	SessionID string    `json:"s"`
	TurnNo    int       `json:"n"`
	TS        time.Time `json:"ts"`
}

func encodeCursor(p cursorPayload, key []byte) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(append(body, mac.Sum(nil)...)), nil
}

func decodeCursor(value string, key []byte) (cursorPayload, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursorPayload{}, err
	}
	if len(raw) < sha256.Size {
		return cursorPayload{}, errors.New("cursor too short")
	}
	body, sig := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return cursorPayload{}, errors.New("bad signature")
	}
	var payload cursorPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return cursorPayload{}, err
	}
	return payload, nil
}

// errCursorMismatch 表示 cursor 解码成功但其归属租户/会话与当前请求不符。
// 用于区分「无效 cursor」（签名错误/格式非法）与「跨租户复用 cursor」，
// 前者返回 invalid cursor，后者返回 cursor mismatch（2026-08-09 审计修复）。
var errCursorMismatch = errors.New("cursor mismatch")

// validateCursor 解码并校验 cursor 归属。sessionID 为空表示不校验会话维度
// （跨会话轮次列表场景）。返回解码后的 payload 供调用方使用。
func validateCursor(encoded string, key []byte, tenantID, sessionID string) (cursorPayload, error) {
	p, err := decodeCursor(encoded, key)
	if err != nil {
		return cursorPayload{}, err
	}
	if p.TenantID != tenantID || (sessionID != "" && p.SessionID != sessionID) {
		return cursorPayload{}, errCursorMismatch
	}
	return p, nil
}
