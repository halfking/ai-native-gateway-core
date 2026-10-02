package sessionsummary

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// v2SessionBodiesSource is the V2 MessageSource: it reads from the
// public.session_bodies_unified view (hot ∪ partition; see migration 625/637)
// (+ session_turns for the model name) instead of the V1
// request_logs / request_logs_bodies tables.
//
// It exists so the summarizer can keep working once V1 request bodies are
// retired (docs/omni-ref3 A1 / P3). It is NOT installed by default; A1 wires it
// in behind the sessions_v2_compression_read flag via Summarizer.SetMessageSource.
//
// Row-shape parity with pgRequestLogsSource:
// V1 emits one row per turn whose content is request_body.messages[-1] (the
// user prompt of that turn, role defaulting to "user"). To keep summary LLM
// input comparable across sources, this V2 source collapses each turn's
// request_delta to its LAST message too (request_delta holds only the messages
// new to that turn — see BodiesRecord). If a turn has no request_delta, it is
// skipped (mirroring V1, which simply has no row for a turn with no body).
//
// The model comes from session_turns.model via a join; ts and request_id come
// straight off session_bodies_unified. LIMIT 20 and ascending ts order match
// V1.
type v2SessionBodiesSource struct {
	pool *pgxpool.Pool
}

// NewV2SessionBodiesSource constructs a MessageSource that reads from the V2
// public.session_bodies_unified view (+ session_turns for the model).
// Intended for Summarizer.SetMessageSource when the
// sessions_v2_compression_read flag is on (docs/omni-ref3 A1). A nil pool
// yields per-call errors rather than a panic, matching the V1 source's
// nil-safety contract.
func NewV2SessionBodiesSource(pool *pgxpool.Pool) MessageSource {
	return &v2SessionBodiesSource{pool: pool}
}

// v2TurnRow is the per-turn projection decoded from the joined
// session_turns/session_bodies_unified query.
type v2TurnRow struct {
	RequestID    string
	Model        string
	Ts           time.Time
	RequestDelta []byte // raw JSONB; may be nil/empty
}

// sessionMessageV2 is the JSON shape of a single message inside request_delta.
// It mirrors the v2.Message struct but is kept local to avoid an import cycle
// (domains/session/v2 does not expose Message for reuse here without coupling).
// Content is a RawMessage: text-only messages carry a JSON string, but
// multimodal messages (MessageFromIR's $ir envelope path) carry a block ARRAY —
// decoding into a plain string fails the whole unmarshal and silently drops
// the turn from summary input (IR-audit P1-2, 2026-09-16).
type sessionMessageV2 struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// v2SessionBodiesBaseQuery reads the unified hot∪partition view, NOT the
// public.session_bodies parent table: fresh bodies_writer rows land in
// session_bodies_hot (8-hour window) and rows promoted on their write day
// live in today's partition, so the parent table alone misses both ends of
// the recency window. The view's comment mandates exactly this for admin
// readers (migrations 625/637).
const v2SessionBodiesBaseQuery = `
	SELECT
		b.request_id,
		COALESCE(t.model, '') AS model,
		b.ts,
		b.request_delta
	FROM public.session_bodies_unified b
		LEFT JOIN public.session_turns_with_current_month t
		  ON t.tenant_id = b.tenant_id
		 AND t.request_id = b.request_id
	WHERE b.session_id = $1
	  -- Wave 1 A5 (2026-09-22): 网关影子轮（origin_actor='goal-%'，见
	  -- response_interceptor_helpers.go followUpSourceActor）不进会话拼装链
	  -- （设计 §5.12"影子指令不进入会话上下文"）。影子轮镜像行保留在
	  -- session_turns 供对账（方案 18 §3），仅装配型读者在此排除。
	  AND COALESCE(t.origin_actor, '') NOT LIKE 'goal-%'
`

// fetchTurns runs the parameterised join (tenant + optional since-ts filter,
// ascending ts, LIMIT 20) and decodes rows. Shared by both MessageSource
// methods; they differ only in the since-filter.
func (m *v2SessionBodiesSource) fetchTurns(ctx context.Context, tenantID, sessionKey string, since time.Time) ([]v2TurnRow, error) {
	if m.pool == nil {
		return nil, fmt.Errorf("sessionsummary: v2 message source pool is nil")
	}
	query := v2SessionBodiesBaseQuery
	args := []any{sessionKey}
	argN := 2
	if tenantID != "" {
		query += " AND b.tenant_id = $" + strconv.Itoa(argN)
		args = append(args, tenantID)
		argN++
	}
	if !since.IsZero() {
		query += " AND b.ts > $" + strconv.Itoa(argN)
		args = append(args, since)
		argN++
	}
	query += " ORDER BY b.ts ASC LIMIT 20"

	rows, err := m.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []v2TurnRow
	for rows.Next() {
		var r v2TurnRow
		if err := rows.Scan(&r.RequestID, &r.Model, &r.Ts, &r.RequestDelta); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// collapseToLastRequestMessage turns a turn's request_delta JSONB into a single
// SessionMessage representing that turn's prompt — the V1-equivalent shape. It
// returns ok=false (and no message) for turns with no usable request_delta, so
// callers can simply skip them.
func collapseToLastRequestMessage(r v2TurnRow) (SessionMessage, bool) {
	if len(r.RequestDelta) == 0 || string(r.RequestDelta) == "null" {
		return SessionMessage{}, false
	}
	var msgs []sessionMessageV2
	if err := json.Unmarshal(r.RequestDelta, &msgs); err != nil {
		// A request_delta that isn't a clean message array would already have
		// failed to persist; treat as no usable message rather than poisoning
		// the summary.
		return SessionMessage{}, false
	}
	if len(msgs) == 0 {
		return SessionMessage{}, false
	}
	last := msgs[len(msgs)-1]
	role := last.Role
	if role == "" {
		role = "user" // match V1's COALESCE(..., 'user') default
	}
	return SessionMessage{
		RequestID: r.RequestID,
		Role:      role,
		Content:   v2ContentText(last.Content),
		Model:     r.Model,
		Timestamp: r.Ts,
	}, true
}

// v2ContentText flattens a request_delta message content — a JSON string, a
// block array, or a single block object — into human-readable summary-input
// text. Mirrors domains/sessiondigest contentText: text parts join with
// newlines, media/attachment parts become visible placeholders, so a
// multimodal turn still contributes its prompt (and a marker) instead of
// vanishing from the summary.
func v2ContentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return v2ContentTextValue(value)
}

func v2ContentTextValue(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text := v2ContentTextValue(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		typ := strings.ToLower(v2StringValue(value["type"]))
		switch typ {
		case "text", "input_text", "output_text":
			if text := v2StringValue(value["text"]); text != "" {
				return text
			}
			return v2StringValue(value["content"])
		case "image", "image_url", "input_image", "output_image":
			return "[附图×1]"
		case "audio", "input_audio":
			return "[音频×1]"
		case "file", "document", "attachment", "input_file", "output_file":
			return "[附件×1]"
		}
	}
	return ""
}

func v2StringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func (m *v2SessionBodiesSource) GetSessionMessages(ctx context.Context, tenantID, sessionKey string) ([]SessionMessage, error) {
	turns, err := m.fetchTurns(ctx, tenantID, sessionKey, time.Time{})
	if err != nil {
		return nil, err
	}
	return collapseTurns(turns), nil
}

func (m *v2SessionBodiesSource) GetMessagesSince(ctx context.Context, tenantID, sessionKey string, since time.Time) ([]SessionMessage, error) {
	turns, err := m.fetchTurns(ctx, tenantID, sessionKey, since)
	if err != nil {
		return nil, err
	}
	return collapseTurns(turns), nil
}

// collapseTurns maps each turn row to at most one SessionMessage (the turn's
// last request message), preserving ascending ts order.
func collapseTurns(turns []v2TurnRow) []SessionMessage {
	messages := make([]SessionMessage, 0, len(turns))
	for _, r := range turns {
		sm, ok := collapseToLastRequestMessage(r)
		if !ok {
			continue
		}
		messages = append(messages, sm)
	}
	return messages
}
