package goal

// history_store.go — reconstructs a conversation transcript from request_logs
// for the audit hook.
//
// The gateway is stateless w.r.t. conversation history: each client request
// carries the full messages array, and the gateway does not persist it as a
// first-class object. But request_logs DOES store, per request:
//   - request_body  (JSONB) — the client's chat-completions request (full messages)
//   - response_body (JSONB) — the upstream response (assistant reply)
// both correlated by gw_session_id (indexed by idx_request_logs_session_outbound).
//
// So to give the audit hook "the full conversation", we query request_logs by
// session and turn each row into a request→response pair. The audit prompt then
// references the complete back-and-forth instead of only the last reply.
//
// Cost control:
//   - LIMIT cap (default 20 rows) bounds the query.
//   - Per-message text is truncated so a pathological 1M-token history can't
//     blow up the audit prompt.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// HistoryMessage is a single chat message in the reconstructed transcript.
type HistoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// HistoryStore reconstructs conversation transcripts per session.
type HistoryStore interface {
	// FetchBySession returns the recent request→response transcript for a
	// session, oldest-first, capped at limit rows. An empty slice (not nil
	// error) means no history was found.
	FetchBySession(ctx context.Context, sessionID, tenantID string, limit int) ([]HistoryMessage, error)
}

// PGHistoryStore implements HistoryStore against the request_logs table.
type PGHistoryStore struct {
	db *sql.DB
}

// NewPGHistoryStore builds a HistoryStore backed by the given pool.
func NewPGHistoryStore(db *sql.DB) *PGHistoryStore {
	return &PGHistoryStore{db: db}
}

// defaultHistoryLimit caps how many request_logs rows are pulled per audit.
// Each row is a full turn; 20 turns is plenty for judging task execution while
// keeping the audit prompt and DB load bounded.
const defaultHistoryLimit = 20

// maxHistoryMessageChars caps an individual message's contribution to the
// audit prompt. ~12k chars ≈ 3k tokens per message; with 20 rows × a few
// messages each this stays well under typical model context windows.
const maxHistoryMessageChars = 12000

// FetchBySession reconstructs the transcript by reading the most recent
// request_logs rows for the session and merging their request messages with the
// assistant reply that followed.
//
// Rows are read newest-first (the index is (gw_session_id, ts DESC)) then
// reversed so the transcript is chronological.
func (s *PGHistoryStore) FetchBySession(ctx context.Context, sessionID, tenantID string, limit int) ([]HistoryMessage, error) {
	if s == nil || s.db == nil || sessionID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultHistoryLimit
	}

	const query = `
		SELECT request_body, response_body
		FROM request_logs
		WHERE gw_session_id = $1
		  AND COALESCE(tenant_id, '') = COALESCE(NULLIF($2, ''), tenant_id)
		  AND request_body IS NOT NULL
		ORDER BY ts DESC
		LIMIT $3`

	rows, err := s.db.QueryContext(ctx, query, sessionID, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("goal: query session history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type pair struct {
		req  []HistoryMessage
		resp *HistoryMessage
	}
	var pairs []pair

	for rows.Next() {
		var reqBody, respBody sql.NullString
		if err := rows.Scan(&reqBody, &respBody); err != nil {
			return nil, fmt.Errorf("goal: scan session history: %w", err)
		}

		reqMsgs := parseRequestMessages(reqBody.String)
		var assistant *HistoryMessage
		if respBody.Valid && respBody.String != "" {
			if am := parseAssistantReply(respBody.String); am != nil {
				assistant = am
			}
		}
		pairs = append(pairs, pair{req: reqMsgs, resp: assistant})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("goal: iterate session history: %w", err)
	}

	// pairs is newest-first; reverse to chronological order.
	out := make([]HistoryMessage, 0, len(pairs)*2)
	for i := len(pairs) - 1; i >= 0; i-- {
		p := pairs[i]
		// Only carry forward the last user message of each request to avoid
		// duplicating the growing history the client re-sends every turn.
		if len(p.req) > 0 {
			last := p.req[len(p.req)-1]
			out = append(out, HistoryMessage{
				Role:    last.Role,
				Content: truncate(last.Content, maxHistoryMessageChars),
			})
		}
		if p.resp != nil {
			out = append(out, HistoryMessage{
				Role:    p.resp.Role,
				Content: truncate(p.resp.Content, maxHistoryMessageChars),
			})
		}
	}
	return out, nil
}

// parseRequestMessages extracts the messages array from a chat-completions
// request body. Returns nil if the body isn't the expected shape.
func parseRequestMessages(raw string) []HistoryMessage {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return nil
	}
	out := make([]HistoryMessage, 0, len(body.Messages))
	for _, m := range body.Messages {
		if m.Role == "" {
			continue
		}
		out = append(out, HistoryMessage{Role: m.Role, Content: m.Content})
	}
	return out
}

// parseAssistantReply extracts the assistant's text from a chat-completions
// response body (streamed responses are already reassembled before logging).
// Returns nil if the body has no usable content.
func parseAssistantReply(raw string) *HistoryMessage {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var body struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return nil
	}
	if len(body.Choices) == 0 || strings.TrimSpace(body.Choices[0].Message.Content) == "" {
		return nil
	}
	m := body.Choices[0].Message
	return &HistoryMessage{Role: firstNonEmpty(m.Role, "assistant"), Content: m.Content}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Cut on a rune boundary to avoid splitting multi-byte sequences.
	r := []rune(s)
	if len(r) <= max {
		// byte-len exceeds but rune-len fits — keep whole string.
		return s
	}
	return string(r[:max]) + "\n…[truncated]"
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// noopHistoryStore returns no history. Used when no DB is available so the
// audit hook still works (degrading to last-reply-only behaviour).
type noopHistoryStore struct{}

func (noopHistoryStore) FetchBySession(_ context.Context, _, _ string, _ int) ([]HistoryMessage, error) {
	return nil, nil
}

// NoopHistoryStore returns a HistoryStore that always reports no history.
func NoopHistoryStore() HistoryStore { return noopHistoryStore{} }

// FormatHistoryForPrompt renders a transcript into a prompt-friendly block.
// Each message becomes "role: content" lines, which the audit prompt embeds.
func FormatHistoryForPrompt(msgs []HistoryMessage) string {
	if len(msgs) == 0 {
		return "(no prior history available)"
	}
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "%s: %s\n\n", m.Role, m.Content)
	}
	return b.String()
}
