// Package routing — read side (CQRS query) for routing overrides.
//
// Task B1 (PoC): this package hosts the ListRoutes query alongside the
// control/routing command package. Together they form the PoC for
// splitting admin/routing_overrides.go's read and write surfaces into
// independent packages.
//
// Why a separate package from control/routing:
//   - Go's import graph stays one-way: admin -> query/routing, with no
//     cycle back through control/routing.
//   - Future read replicas, caching, or projection tables can be
//     added here without touching the write path.
//   - The query layer never mutates state, so it can be safely retried
//     and parallelized — properties the write side cannot offer.
package routing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RouteDTO is the read model returned by List. It mirrors the
// routing_overrides table exactly so the admin facade can JSON-encode
// it without an extra mapping layer.
//
// All pointer fields are nullable in the DB and absent in the JSON
// when nil — keep that contract or downstream admin UIs will mis-render
// "expires_at: null" as "expires_at: "".
type RouteDTO struct {
	ID          int64      `json:"id"`
	TaskType    string     `json:"task_type"`
	Profile     string     `json:"profile"`
	Mode        string     `json:"mode"`
	ModelChosen *string    `json:"model_chosen,omitempty"`
	Reason      string     `json:"reason"`
	CreatedBy   *string    `json:"created_by,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// ListRoutesQuery is the input to Handler.List.
//
// ActiveOnly = true filters out rows whose expires_at is in the past
// (matching the OverrideStore active-set semantics). TaskType and
// Profile are optional equality filters; empty string means "any".
type ListRoutesQuery struct {
	ActiveOnly bool
	TaskType   string
	Profile    string
}

// Handler executes ListRoutesQuery against the routing_overrides
// table. Read-only and safe for concurrent use.
type Handler struct {
	db *pgxpool.Pool
}

// NewHandler wires the query handler to a pgxpool. Passing nil panics
// (mirrors control/routing.NewHandler).
func NewHandler(db *pgxpool.Pool) *Handler {
	if db == nil {
		panic("query/routing: NewHandler requires a non-nil *pgxpool.Pool")
	}
	return &Handler{db: db}
}

// List returns all routing overrides matching the query, ordered by
// (task_type, profile, mode, model_chosen) to give callers a stable
// iteration order — the admin UI relies on this for diff rendering.
//
// Errors:
//   - any non-nil error is a database failure; the caller should map
//     it to 500.
func (h *Handler) List(ctx context.Context, q ListRoutesQuery) ([]RouteDTO, error) {
	sql, args := buildListSQL(q)

	rows, err := h.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query/routing: list overrides: %w", err)
	}
	//nolint:errcheck // rows.Close() returns the most recent Query error
	// which we surface via rows.Err() below.
	defer rows.Close()

	out := make([]RouteDTO, 0)
	for rows.Next() {
		var r RouteDTO
		if err := rows.Scan(
			&r.ID, &r.TaskType, &r.Profile, &r.Mode,
			&r.ModelChosen, &r.Reason, &r.CreatedBy,
			&r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("query/routing: scan override: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query/routing: iterate overrides: %w", err)
	}
	return out, nil
}

// buildListSQL assembles the SELECT statement and its bind args.
// Exported as ListSQL for callers that want to embed it in a larger
// CTE or query (e.g. the dashboard endpoint joins it with usage stats).
//
// Args are 1-indexed to match pgx's $N placeholders. The function is
// pure (no DB calls) so it's trivially unit-testable.
func buildListSQL(q ListRoutesQuery) (sql string, args []any) {
	var b strings.Builder
	b.WriteString(`SELECT id, task_type, profile, mode, model_chosen, reason, created_by, expires_at, created_at, updated_at
	      FROM routing_overrides WHERE 1=1`)
	if q.ActiveOnly {
		b.WriteString(` AND (expires_at IS NULL OR expires_at > NOW())`)
	}
	if q.TaskType != "" {
		args = append(args, q.TaskType)
		fmt.Fprintf(&b, " AND task_type = $%d", len(args))
	}
	if q.Profile != "" {
		args = append(args, q.Profile)
		fmt.Fprintf(&b, " AND profile = $%d", len(args))
	}
	b.WriteString(" ORDER BY task_type, profile, mode, model_chosen")
	return b.String(), args
}
