package sessionsummary

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// v2SessionBodiesSource is the V2 MessageSource: it reads from
// public.session_bodies (+ session_turns for the model name) instead of the V1
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
// straight off session_bodies. LIMIT 20 and ascending ts order match V1.
type v2SessionBodiesSource struct {
	pool *pgxpool.Pool
}

// NewV2SessionBodiesSource constructs a MessageSource that reads from the V2
// public.session_bodies (+ session_turns for the model) tables. Intended for
// Summarizer.SetMessageSource when the sessions_v2_compression_read flag is on
// (docs/omni-ref3 A1). A nil pool yields per-call errors rather than a panic,
// matching the V1 source's nil-safety contract.
func NewV2SessionBodiesSource(pool *pgxpool.Pool) MessageSource {
	return &v2SessionBodiesSource{pool: pool}
}

// v2TurnRow is the per-turn projection decoded from the joined
// session_turns/session_bodies query.
type v2TurnRow struct {
	RequestID    string
	Model        string
	Ts           time.Time
	RequestDelta []byte // raw JSONB; may be nil/empty
}

// sessionMessageV2 is the JSON shape of a single message inside request_delta.
// It mirrors the v2.Message struct but is kept local to avoid an import cycle
// (domains/session/v2 does not expose Message for reuse here without coupling).
type sessionMessageV2 struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

const v2SessionBodiesBaseQuery = `
	SELECT
		b.request_id,
		COALESCE(t.model, '') AS model,
		b.ts,
		b.request_delta
	FROM public.session_bodies b
		LEFT JOIN public.session_turns t
		  ON t.tenant_id = b.tenant_id
		 AND t.request_id = b.request_id
	WHERE b.session_id = $1
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
		Content:   last.Content,
		Model:     r.Model,
		Timestamp: r.Ts,
	}, true
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
