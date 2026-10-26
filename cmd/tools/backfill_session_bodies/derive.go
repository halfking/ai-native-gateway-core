// Package main — backfill public.session_bodies (per-turn deltas) for a session
// from the accumulated _request_logs bodies.
//
// Rationale (full-switch to Sessions V2): session_bodies stores only THIS turn's
// messages (request_delta + response_delta), not the full matryoshka. Historical
// sessions that predate the V2 dual-write have no session_bodies, so the detail
// page and the dual_read validator cannot read them from V2. This tool derives
// per-turn deltas from _request_logs (full body at each turn minus the previous
// turn's full body) and writes them via the existing SessionBodiesWriter.
//
// The delta-derivation core (DeriveTurnDeltas / subtractMessages / parsing) is
// pure and unit-tested; the CLI only adds DB I/O around it.
//
// ★★ FIXME-14371（2026-10-22 §143.71，只登记、零行为变更）：本文件的两条纯函数
//
//	    是「从别处接回累积正文」这条恢复路径的算法内核，但它们【不能直接写回 v2】：
//
//		① ★ 保真缺口：Msg 只有 {Role, Content}，Content 是 plainContent 投影
//		   （字符串逐字 / 数组与多模态取紧凑 JSON）⇒ 丢掉了 tool_calls、多模态结构
//		   与 provider 原生形态 ⇒ ⇒ v2 的 session_bodies 存的是 v2.Message
//		   （{Role, Content, ContentRaw json.RawMessage, ToolCalls, RawContent}，
//		   且 MarshalJSON 的 Source precedence 以 ContentRaw 为准）⇒ ⇒
//		   ⇒ 写回 v2 需要一层【保真适配】（把原始 message JSON 直接映射成
//		   v2.Message，content 进 ContentRaw、tool_calls 进 ToolCalls）⇒
//		   ⇒ ★ 该适配层在仓内【不存在】。
//
//		② ★ 语义前提（实测结论）：DeriveTurnDeltas 是【前缀差分】
//		   （delta_i = full_i − full_{i−1}）⇒ 它要求【同一会话相邻轮次的累积体都在场】。
//		   实测（§143.71）：真实累积体会被改写、也会被裁剪 ⇒ 245 上 content 级
//		   「前一条是后一条真前缀」只成立 51.6% ⇒ ⇒ 「捞回第 N 轮 ⇒ 前 N 轮都能重建」
//		   在【差分】语义下不成立 ⇒ ⇒ 恢复需要走【切轮法】（按 user 边界切累积体），
//		   而切轮法的可靠性由「前缀链」给出（见 runbook §143.71 与
//		   cmd/tools/dbdegradation-salvage 的 -analyze-prefix）。
package main

import (
	"encoding/json"
)

// Msg is a minimal chat message projection used for delta derivation. Content
// is the plain-text projection: a string content verbatim, or compact JSON for
// array/multimodal content.
type Msg struct {
	Role    string
	Content string
}

// DeriveTurnDeltas splits accumulated per-turn full message lists into per-turn
// request deltas (only NEW user messages in this turn) and pairs each with
// that turn's response messages.
//
//	fullMsgs[i] — full accumulated request message list at turn i (matryoshka).
//	respMsgs[i] — assistant reply messages for turn i (may be empty/nil).
//
// The _request_logs full body for turn i contains all previous turns' messages
// (user + assistant) plus the current user message. The delta for turn i is the
// set of user messages that appear in fullMsgs[i] but not in fullMsgs[i-1].
// (System messages are assumed constant and not part of the delta.)
//
// Returns requestDeltas[i] and responseDeltas[i] aligned to the same index.
func DeriveTurnDeltas(fullMsgs, respMsgs [][]Msg) ([][]Msg, [][]Msg) {
	n := len(fullMsgs)
	reqDeltas := make([][]Msg, n)
	respDeltas := make([][]Msg, n)
	prev := []Msg{}
	for i, full := range fullMsgs {
		// Subtract previous full body, then keep only user messages as the request delta.
		// (Assistant messages in the diff belong to the previous turn's response_delta.)
		diff := subtractMessages(full, prev)
		reqDeltas[i] = filterUserOnly(diff)
		if i < len(respMsgs) {
			respDeltas[i] = respMsgs[i]
		}
		prev = full
	}
	return reqDeltas, respDeltas
}

// filterUserOnly keeps only messages with role == "user". System/assistant
// messages are excluded: system is assumed unchanged across turns, assistant
// replies are stored separately in ResponseDelta of the previous turn.
func filterUserOnly(msgs []Msg) []Msg {
	out := make([]Msg, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "user" {
			out = append(out, m)
		}
	}
	return out
}

// subtractMessages returns the messages in full that are not already present in
// prev, preserving full's order. Identity is (role, content); duplicates are
// consumed one-for-one so repeated messages don't collapse.
func subtractMessages(full, prev []Msg) []Msg {
	seen := map[string]int{}
	for _, m := range prev {
		seen[msgKey(m)]++
	}
	var out []Msg
	for _, m := range full {
		k := msgKey(m)
		if seen[k] > 0 {
			seen[k]--
			continue
		}
		out = append(out, m)
	}
	return out
}

func msgKey(m Msg) string { return m.Role + "\x1f" + m.Content }

// rawMsg is a permissive chat message shape (content may be string or array).
type rawMsg struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
}

// ParseRequestMessages extracts the message list from a request body, which may
// be a bare JSON array of messages or an object with a "messages" field.
func ParseRequestMessages(body json.RawMessage) ([]Msg, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var env struct {
		Messages []rawMsg `json:"messages"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Messages != nil {
		return toMsgs(env.Messages), nil
	}
	var arr []rawMsg
	if err := json.Unmarshal(body, &arr); err == nil {
		return toMsgs(arr), nil
	}
	return nil, nil
}

// ParseResponseMessages extracts assistant messages from a response body
// ({choices:[{message}]}) or a single message object.
func ParseResponseMessages(body json.RawMessage) ([]Msg, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var env struct {
		Choices []struct {
			Message rawMsg `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &env); err == nil && len(env.Choices) > 0 {
		out := make([]Msg, 0, len(env.Choices))
		for _, c := range env.Choices {
			out = append(out, toMsg(c.Message))
		}
		return out, nil
	}
	var m rawMsg
	if err := json.Unmarshal(body, &m); err == nil && m.Role != "" {
		return []Msg{toMsg(m)}, nil
	}
	return nil, nil
}

func toMsgs(in []rawMsg) []Msg {
	out := make([]Msg, 0, len(in))
	for _, r := range in {
		out = append(out, toMsg(r))
	}
	return out
}

func toMsg(r rawMsg) Msg {
	return Msg{Role: r.Role, Content: plainContent(r.Content)}
}

// plainContent renders string content verbatim, and array/multimodal content as
// compact JSON. The live writer keys on the string projection, so this keeps
// backfilled deltas comparable to V1 bodies for the common text case.
func plainContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []interface{}
	if err := json.Unmarshal(raw, &arr); err == nil {
		if b, err := json.Marshal(arr); err == nil {
			return string(b)
		}
	}
	return string(raw)
}
