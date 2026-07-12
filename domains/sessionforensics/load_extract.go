package sessionforensics

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// ExtractPySession 对应 extract.py 输出的 schema：
//
//	{
//	  "session_meta": {
//	    "id", "label", "instance", ...,
//	    "tenant_id", "turn_count", ...
//	  },
//	  "turns": [{
//	    "turn", "request_id", "ts", "client_model",
//	    "body_size_bytes", "msg_count",
//	    "request_body": {...json object...},
//	    "outbound_body": {...},
//	    "response_body": {...}
//	  }, ...]
//	}
type ExtractPySession struct {
	SessionMeta ExtractPyMeta   `json:"session_meta"`
	Turns       []ExtractPyTurn `json:"turns"`
}

type ExtractPyMeta struct {
	ID                string `json:"id"`
	Label             string `json:"label"`
	Instance          string `json:"instance"`
	TenantID          string `json:"tenant_id"`
	TurnCount         int    `json:"turn_count"`
	TotalRequestBytes int    `json:"total_request_bytes"`
	Note              string `json:"note,omitempty"`
}

type ExtractPyTurn struct {
	Turn          int             `json:"turn"`
	RequestID     string          `json:"request_id"`
	Ts            string          `json:"ts"`
	ClientModel   string          `json:"client_model"`
	BodySizeBytes int             `json:"body_size_bytes"`
	RespSizeBytes int             `json:"resp_size_bytes"`
	MsgCount      int             `json:"msg_count"`
	Success       bool            `json:"success"`
	ErrorKind     string          `json:"error_kind,omitempty"`
	LatencyMs     int             `json:"latency_ms,omitempty"`
	RequestBody   json.RawMessage `json:"request_body"`
	OutboundBody  json.RawMessage `json:"outbound_body,omitempty"`
	ResponseBody  json.RawMessage `json:"response_body,omitempty"`
}

// LoadExtractPyFile 读 extract.py 写出的 JSON 并转成 SessionPack。
//
//	path: tests/session_replay/sessions/session_<id>.json
//
// 转出来的 SessionPack.Messages[i].Content 是 request_body 的字符串化
// JSON（与 Replayer / Replay 链路兼容）。
func LoadExtractPyFile(path string) (*SessionPack, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw ExtractPySession
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("sessionforensics: parse %s: %w", path, err)
	}
	return raw.ToSessionPack(), nil
}

// ToSessionPack 把 ExtractPySession 转成 sessionforensics.SessionPack。
func (e *ExtractPySession) ToSessionPack() *SessionPack {
	pack := &SessionPack{
		SessionMeta: SessionMeta{
			ID:         e.SessionMeta.ID,
			Title:      e.SessionMeta.Label,
			Instance:   e.SessionMeta.Instance,
			TenantID:   e.SessionMeta.TenantID,
			Source:     "extract.py",
			ExportedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Messages:    make([]ExportMessage, 0, len(e.Turns)),
		Attachments: []ExportAttachment{},
	}
	for _, t := range e.Turns {
		bodyText := string(t.RequestBody)
		if len(bodyText) == 0 {
			bodyText = "{}"
		}
		pack.Messages = append(pack.Messages, ExportMessage{
			Turn:            t.Turn,
			Role:            "user", // extract.py 不区分 per-role，统一标记 user
			Content:         bodyText,
			RequestID:       t.RequestID,
			Model:           t.ClientModel,
			ResponseContent: string(t.ResponseBody),
			Success:         t.Success,
			ErrorKind:       t.ErrorKind,
			LatencyMs:       t.LatencyMs,
			CreatedAt:       t.Ts,
		})
	}
	return pack
}
