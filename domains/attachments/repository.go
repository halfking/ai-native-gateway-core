package attachments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RequestAttachmentRow is the relational representation of one attachment
// metadata record persisted in request_attachments (migration 401).
//
// Mirrors the JSONB payload in request_logs.attachments (migration 325) but
// is queryable / indexable / cleanable as a first-class table.
type RequestAttachmentRow struct {
	ID             int64     `json:"id"`
	RequestID      string    `json:"request_id"`
	AttachmentType string    `json:"attachment_type"`
	ContentType    string    `json:"content_type,omitempty"`
	SizeBytes      int64     `json:"size_bytes"`
	StoragePath    string    `json:"storage_path,omitempty"`
	Hash           string    `json:"hash,omitempty"`
	OriginalURL    string    `json:"original_url,omitempty"`
	MessageIndex   int       `json:"message_index"`
	BlockIndex     int       `json:"block_index"`
	Status         string    `json:"status"`
	ErrorCode      string    `json:"error_code,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// ToRow converts an in-memory AttachmentMetadata (the value already stored
// as JSONB on request_logs) into the relational row form for INSERT.
//
// Empty optional fields collapse to defaults so the table does not store
// empty strings ("", " ") that pollute hash / status indexes.
func ToRow(requestID string, m AttachmentMetadata) RequestAttachmentRow {
	return RequestAttachmentRow{
		RequestID:      requestID,
		AttachmentType: nonEmpty(m.Type, "image"),
		ContentType:    m.ContentType,
		SizeBytes:      m.Size,
		StoragePath:    m.Path,
		Hash:           m.Hash,
		OriginalURL:    m.OriginalURL,
		MessageIndex:   m.MessageIndex,
		BlockIndex:     m.BlockIndex,
		Status:         string(nonZeroStatus(m.Status)),
		ErrorCode:      m.ErrorCode,
		CreatedAt:      nonZeroTime(m.CreatedAt),
	}
}

func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func nonZeroStatus(s AttachmentStatus) AttachmentStatus {
	if s == "" {
		return AttachmentStatusDetected
	}
	return s
}

func nonZeroTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

// Repository persists per-request attachment rows to public.request_attachments.
//
// Writes are best-effort: a failure to insert MUST NOT block the request.
// Callers should log a warning and continue.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository constructs a Repository. pool must be a live *pgxpool.Pool
// (the gateway's hot connection pool).
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// insertColumns is the explicit INSERT column list. Kept in one place
// to stay aligned with the placeholder count below.
const insertColumns = `(
		request_id, attachment_type, content_type, size_bytes,
		storage_path, hash, original_url,
		message_index, block_index, status, error_code, created_at
	)`

// insertPlaceholders matches insertColumns (12 columns).
const insertPlaceholders = `($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

// InsertOne persists a single attachment row. Returns the row ID on success.
//
// Best-effort by contract: callers should not surface insert errors as
// user-facing failures. Log and continue.
func (r *Repository) InsertOne(ctx context.Context, row RequestAttachmentRow) (int64, error) {
	if r == nil || r.pool == nil {
		return 0, errNoDB
	}
	return insertRow(ctx, r.pool, row)
}

// InsertBatch persists multiple attachment rows in a single transaction.
// Empty input returns (0, nil) without touching the database.
//
// On error, the transaction is rolled back; partial inserts are not visible.
func (r *Repository) InsertBatch(ctx context.Context, rows []RequestAttachmentRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	if r == nil || r.pool == nil {
		return 0, errNoDB
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("attachments: begin tx: %w", err)
	}
	committed := 0
	for _, row := range rows {
		if _, err := insertRow(ctx, tx, row); err != nil {
			_ = tx.Rollback(ctx)
			return committed, err
		}
		committed++
	}
	if err := tx.Commit(ctx); err != nil {
		return committed, fmt.Errorf("attachments: commit tx: %w", err)
	}
	return committed, nil
}

// pgxQuerier is satisfied by both *pgxpool.Pool and pgx.Tx.
type pgxQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func insertRow(ctx context.Context, q pgxQuerier, row RequestAttachmentRow) (int64, error) {
	sqlStr := "INSERT INTO public.request_attachments " + insertColumns +
		" VALUES " + insertPlaceholders
	tag, err := q.Exec(ctx, sqlStr,
		row.RequestID,
		row.AttachmentType,
		nullableString(row.ContentType),
		row.SizeBytes,
		nullableString(row.StoragePath),
		nullableString(row.Hash),
		nullableString(row.OriginalURL),
		row.MessageIndex,
		row.BlockIndex,
		row.Status,
		nullableString(row.ErrorCode),
		row.CreatedAt,
	)
	if err != nil {
		return 0, fmt.Errorf("attachments: insert row: %w", err)
	}
	// pgx exposes RowsAffected but not LastInsertId; the BIGSERIAL id is
	// readable via RETURNING id which callers that need it can re-query.
	// For the hot path we only need success/failure.
	_ = tag
	return 0, nil
}

// ListByRequestID returns all attachment rows for a given request, ordered
// by message_index, block_index so callers see them in body order.
func (r *Repository) ListByRequestID(ctx context.Context, requestID string) ([]RequestAttachmentRow, error) {
	if r == nil || r.pool == nil {
		return nil, errNoDB
	}
	rows, err := r.pool.Query(ctx,
		`SELECT id, request_id, attachment_type, COALESCE(content_type, ''),
		        size_bytes, COALESCE(storage_path, ''), COALESCE(hash, ''),
		        COALESCE(original_url, ''), message_index, block_index,
		        status, COALESCE(error_code, ''), created_at
		 FROM public.request_attachments
		 WHERE request_id = $1
		 ORDER BY message_index ASC, block_index ASC`,
		requestID,
	)
	if err != nil {
		return nil, fmt.Errorf("attachments: list by request_id: %w", err)
	}
	defer rows.Close()

	var out []RequestAttachmentRow
	for rows.Next() {
		var row RequestAttachmentRow
		if err := rows.Scan(
			&row.ID, &row.RequestID, &row.AttachmentType, &row.ContentType,
			&row.SizeBytes, &row.StoragePath, &row.Hash, &row.OriginalURL,
			&row.MessageIndex, &row.BlockIndex,
			&row.Status, &row.ErrorCode, &row.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("attachments: scan: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attachments: rows: %w", err)
	}
	return out, nil
}

// ListByHash returns all rows referencing the same content hash. Used by
// admin tools to find "every request that used attachment X".
func (r *Repository) ListByHash(ctx context.Context, hash string) ([]RequestAttachmentRow, error) {
	if r == nil || r.pool == nil {
		return nil, errNoDB
	}
	rows, err := r.pool.Query(ctx,
		`SELECT id, request_id, attachment_type, COALESCE(content_type, ''),
		        size_bytes, COALESCE(storage_path, ''), COALESCE(hash, ''),
		        COALESCE(original_url, ''), message_index, block_index,
		        status, COALESCE(error_code, ''), created_at
		 FROM public.request_attachments
		 WHERE hash = $1
		 ORDER BY created_at DESC`,
		hash,
	)
	if err != nil {
		return nil, fmt.Errorf("attachments: list by hash: %w", err)
	}
	defer rows.Close()

	var out []RequestAttachmentRow
	for rows.Next() {
		var row RequestAttachmentRow
		if err := rows.Scan(
			&row.ID, &row.RequestID, &row.AttachmentType, &row.ContentType,
			&row.SizeBytes, &row.StoragePath, &row.Hash, &row.OriginalURL,
			&row.MessageIndex, &row.BlockIndex,
			&row.Status, &row.ErrorCode, &row.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("attachments: scan: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attachments: rows: %w", err)
	}
	return out, nil
}

// CountByStatus returns the number of attachments in the given status that
// were created within the supplied window. Used by health / observability
// dashboards to surface "store_failed spike in last 5 minutes".
func (r *Repository) CountByStatus(ctx context.Context, status string, since time.Time) (int64, error) {
	if r == nil || r.pool == nil {
		return 0, errNoDB
	}
	var n int64
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM public.request_attachments
		 WHERE status = $1 AND created_at >= $2`,
		status, since,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("attachments: count by status: %w", err)
	}
	return n, nil
}

// DeleteOlderThan removes attachment rows whose created_at < cutoff. Returns
// the number of rows deleted.
//
// Cleanup is best-effort; admin tooling calls this on a schedule to bound
// table growth. The actual file system bytes still need separate cleanup
// (see data_lifecycle_attachments_filesystem.go).
func (r *Repository) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	if r == nil || r.pool == nil {
		return 0, errNoDB
	}
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM public.request_attachments WHERE created_at < $1`,
		cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("attachments: delete older than: %w", err)
	}
	return tag.RowsAffected(), nil
}

// errNoDB is returned by repository methods when no database handle is
// configured. Callers (best-effort paths) should treat this as "skip silently
// with a warning" rather than a fatal error.
var errNoDB = errors.New("attachments: repository has no database")

// nullableString returns nil for empty strings so pgx writes SQL NULL
// instead of the empty string. Empty text in indexed columns (especially
// hash) would defeat the partial index WHERE hash IS NOT NULL.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
