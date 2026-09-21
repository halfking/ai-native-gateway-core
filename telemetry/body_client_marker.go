package telemetry

import (
	"encoding/json"
	"strings"
)

// ExtractClientTypeFromBody inspects a JSON request body for client-specific
// markers that survive when User-Agent / X-Gw-Client-Type are absent (e.g.
// internal calls, reverse-proxied SDK clients, curl with stripped UA).
//
// 2026-09-21 audit (P1: client detection parity for domestic coding agents):
// added parameter-level identification for the three domestic coding-agent
// clients catalogued in docs/client-formats/{zcode,minimax-code,deepseek-code}.md.
//
// Returns the canonical client-type string (see clienttype.Normalize) or
// "" when no marker matches. The caller is responsible for feeding the
// result through clienttype.Normalize and stitching it into the existing
// detection chain — effective order after R52:
// header/UA + system-prompt (fillAttemptMeta) → body-marker (refreshMeta,
// overrides weak names only).
//
// Recognition rules (first match wins):
//
//	zcode          — body.metadata.zcode_version present
//	                OR any tools[].function.name starts with "zcode_"
//	minimax-code   — body.metadata.client_type == "minimax_code"
//	                OR X-Code-Session-Id header is set on the request
//	deepseek-code  — body.metadata.deepseek_session_id is set
//	                OR model starts with "deepseek-" AND tools carry the
//	                  DeepSeek Code tool set (R52: neither signal alone is
//	                  sufficient — model choice ≠ client identity, and
//	                  {read,write,edit,search}_file/execute_command are
//	                  generic coding-agent tool names)
func ExtractClientTypeFromBody(body []byte, xCodeSessionID string) string {
	if len(body) == 0 {
		// Header-only path is still useful even with empty body.
		if xCodeSessionID != "" {
			return "minimax-code"
		}
		return ""
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		// Body is not JSON (rare — only happens when the upstream rejects
		// before parsing). In that case fall back to header-only check.
		if xCodeSessionID != "" {
			return "minimax-code"
		}
		return ""
	}

	// ── 1. metadata block (OpenAI & Anthropic both carry metadata) ─────
	if metaRaw, ok := root["metadata"]; ok {
		var meta map[string]json.RawMessage
		if err := json.Unmarshal(metaRaw, &meta); err == nil {
			// zcode
			if _, has := meta["zcode_version"]; has {
				return "zcode"
			}
			// minimax-code — both spellings tolerated (snake & space)
			if ct := decodeString(meta["client_type"]); ct != "" {
				switch strings.ToLower(strings.TrimSpace(ct)) {
				case "minimax_code", "minimax-code", "minimaxcode":
					return "minimax-code"
				}
			}
			// deepseek-code
			if _, has := meta["deepseek_session_id"]; has {
				return "deepseek-code"
			}
		}
	}

	// ── 2+3. model 前缀 × tools 组合信号（R52 收紧）────────────────────
	// model 以 deepseek-* 开头单独不再判 deepseek-code：模型选择 ≠ 客户端
	// 身份（conversion-audit.md:86 自警），任何 SDK 客户端调 deepseek-chat /
	// deepseek-reasoner 都会被误归类。同理 read_file/write_file 等是大量
	// 编码 agent 的通用工具名，单独也不足判。两者共现才判 deepseek-code；
	// zcode_* 前缀工具是命名空间级强信号，维持独立判定。
	deepseekModel := false
	if modelRaw, ok := root["model"]; ok {
		deepseekModel = strings.HasPrefix(strings.ToLower(decodeString(modelRaw)), "deepseek-")
	}
	toolClient := ""
	if toolsRaw, ok := root["tools"]; ok {
		toolClient = clientTypeFromTools(toolsRaw)
	}
	if toolClient == "zcode" {
		return "zcode"
	}
	if deepseekModel && toolClient == "deepseek-code" {
		return "deepseek-code"
	}

	// ── 4. header-only fallback (X-Code-Session-Id is MiniMax Code's
	//      session identifier; it's set on every request and is the most
	//      reliable signal that doesn't require parsing the body) ──
	if xCodeSessionID != "" {
		return "minimax-code"
	}

	return ""
}

// clientTypeFromTools inspects the request's tools[] array for client-
// specific naming conventions. Returns the canonical client type or "".
//
//	zcode_*            → "zcode"
//	{read,write,edit,search,execute}_file, execute_command → "deepseek-code"
func clientTypeFromTools(toolsRaw json.RawMessage) string {
	var tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
		Name string `json:"name"` // Anthropic / OpenAI Responses shape
	}
	if err := json.Unmarshal(toolsRaw, &tools); err != nil {
		return ""
	}
	deepseekNames := map[string]bool{
		"read_file":       true,
		"write_file":      true,
		"edit_file":       true,
		"search_files":    true,
		"execute_command": true,
	}
	for _, t := range tools {
		name := strings.ToLower(t.Function.Name)
		if name == "" {
			name = strings.ToLower(t.Name)
		}
		if strings.HasPrefix(name, "zcode_") {
			return "zcode"
		}
		if deepseekNames[name] {
			return "deepseek-code"
		}
	}
	return ""
}

// decodeString extracts a string from a json.RawMessage, returning ""
// on missing or non-string values.
func decodeString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
