package reconciliation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/mattn/go-sqlite3"
)

var errAttemptMissing = errors.New("provider attempt intent is missing")

type attemptEvent struct {
	Kind              string
	ID                string
	Intent            *Attempt       `json:",omitempty"`
	Usage             *UsageEvidence `json:",omitempty"`
	At                time.Time
	Status            int    `json:",omitempty"`
	ProviderRequestID string `json:",omitempty"`
	Outcome           string `json:",omitempty"`
}

// AttemptOutbox journals supplier evidence on the configured persistent disk.
// Its projector only writes PostgreSQL facts. It cannot replay HTTP requests.
type AttemptOutbox struct {
	journal, projectorLock *sql.DB
	apply                  func(context.Context, attemptEvent) error
	wake, done             chan struct{}
	cancel                 context.CancelFunc
}

func OpenAttemptOutbox(ctx context.Context, backupDir string, pool *pgxpool.Pool) (*AttemptOutbox, error) {
	if pool == nil {
		return nil, errors.New("provider attempt projection database is unavailable")
	}
	store := NewAttemptRecorder(pool)
	// A shared backup volume may serve multiple databases. A queue belongs to
	// one configured host, port, and database without persisting credentials.
	config := pool.Config().ConnConfig
	target := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", config.Host, config.Port, config.Database)))
	path := filepath.Join(backupDir, "provider-attempts", fmt.Sprintf("%x.db", target[:16]))
	outbox, err := openAttemptOutbox(ctx, path, store.applyEvent)
	if err != nil {
		return nil, err
	}
	worker, cancel := context.WithCancel(context.Background())
	outbox.cancel = cancel
	go outbox.run(worker)
	return outbox, nil
}

func openAttemptOutbox(ctx context.Context, path string, apply func(context.Context, attemptEvent) error) (*AttemptOutbox, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	journal, err := openAttemptSQLite(ctx, path)
	if err != nil {
		return nil, err
	}
	if _, err := journal.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS attempt_events (
		sequence INTEGER PRIMARY KEY AUTOINCREMENT, payload BLOB NOT NULL, rejected_reason TEXT
	); CREATE INDEX IF NOT EXISTS attempt_events_pending ON attempt_events(sequence) WHERE rejected_reason IS NULL`); err != nil {
		_ = journal.Close()
		return nil, err
	}
	// A separate SQLite file serializes projectors on the same volume without
	// holding the journal write lock during a PostgreSQL operation.
	projectorLock, err := openAttemptSQLite(ctx, path+".projector")
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	return &AttemptOutbox{journal: journal, projectorLock: projectorLock, apply: apply,
		wake: make(chan struct{}, 1), done: make(chan struct{})}, nil
}

func openAttemptSQLite(ctx context.Context, path string) (*sql.DB, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "_busy_timeout=75&_synchronous=FULL&_txlock=immediate"}
	db, err := sql.Open("sqlite3", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (o *AttemptOutbox) Close() error {
	if o.cancel != nil {
		o.cancel()
		<-o.done
	}
	return errors.Join(o.journal.Close(), o.projectorLock.Close())
}

func (o *AttemptOutbox) append(ctx context.Context, event attemptEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := o.journal.ExecContext(ctx, `INSERT INTO attempt_events(payload) VALUES (?)`, payload); err != nil {
		return err
	}
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return nil
}

func (o *AttemptOutbox) Prepare(ctx context.Context, a Attempt) error {
	a = NewAttempt(a)
	return o.append(ctx, attemptEvent{Kind: "prepare", ID: a.ID, Intent: &a})
}

func (o *AttemptOutbox) MarkDispatched(ctx context.Context, id string) error {
	return o.append(ctx, attemptEvent{Kind: "dispatch", ID: id})
}

func (o *AttemptOutbox) MarkNotSent(ctx context.Context, id string) error {
	return o.append(ctx, attemptEvent{Kind: "not_sent", ID: id})
}

func (o *AttemptOutbox) MarkSent(ctx context.Context, id string, at time.Time) error {
	// ★ 2026-11-24（L5）：事件里带零值时间 ⇒ 重放时投影成 0001-01-01
	//   （真 PG 实测）⇒ 收敛在**入队**这一层，重放路径就只会看到真实时间。
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return o.append(ctx, attemptEvent{Kind: "send", ID: id, At: at})
}

func (o *AttemptOutbox) RecordHTTP(ctx context.Context, id string, status int, providerRequestID string, sentAt time.Time) error {
	if sentAt.IsZero() {
		sentAt = time.Now().UTC()
	}
	return o.append(ctx, attemptEvent{Kind: "http", ID: id, Status: status, ProviderRequestID: providerRequestID, At: sentAt})
}

func (o *AttemptOutbox) RecordUsage(ctx context.Context, id string, usage UsageEvidence) error {
	return o.append(ctx, attemptEvent{Kind: "usage", ID: id, Usage: &usage})
}

func (o *AttemptOutbox) Finish(ctx context.Context, id, outcome string) error {
	return o.append(ctx, attemptEvent{Kind: "finish", ID: id, Outcome: outcome, At: time.Now().UTC()})
}

// JournalTransportSend records the write signal before the transport returns.
// The transport still collects the timestamp in memory for a later retry of
// the evidence write if local persistence fails.
func JournalTransportSend(store AttemptStore, id string, at time.Time) {
	outbox, ok := store.(*AttemptOutbox)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := outbox.MarkSent(ctx, id, at); err != nil {
		slog.Warn("provider transport send evidence could not be journaled", "attempt_id", id, "error", err)
	}
}

func (o *AttemptOutbox) run(ctx context.Context) {
	defer close(o.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastError time.Time
	for ctx.Err() == nil {
		count, err := o.drain(ctx)
		if err == nil && count == 64 {
			continue
		}
		if err != nil && ctx.Err() == nil && time.Since(lastError) >= time.Minute {
			slog.Warn("provider attempt evidence awaiting projection", "error", err)
			lastError = time.Now()
		}
		if err != nil {
			// Writes can still accumulate during an outage. Pace retries independently.
			select {
			case <-ctx.Done():
			case <-ticker.C:
			}
			continue
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		case <-o.wake:
		}
	}
}

type recordedAttemptEvent struct {
	seq     int64
	payload []byte
}

func (o *AttemptOutbox) drain(ctx context.Context) (int, error) {
	// Keep the lock until any in-flight projection has returned, including when
	// a shutdown cancels the projection context.
	lock, err := o.projectorLock.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = lock.Rollback() }()
	rows, err := o.journal.QueryContext(ctx, `SELECT sequence,payload FROM attempt_events WHERE rejected_reason IS NULL ORDER BY sequence LIMIT 64`)
	if err != nil {
		return 0, err
	}
	var batch []recordedAttemptEvent
	for rows.Next() {
		var row recordedAttemptEvent
		if err := rows.Scan(&row.seq, &row.payload); err != nil {
			_ = rows.Close()
			return 0, err
		}
		batch = append(batch, row)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, row := range batch {
		if err := o.project(ctx, row); err != nil {
			return 0, err
		}
	}
	return len(batch), nil
}

func (o *AttemptOutbox) project(ctx context.Context, row recordedAttemptEvent) error {
	var event attemptEvent
	reason := ""
	if err := json.Unmarshal(row.payload, &event); err != nil || !event.valid() {
		reason = "invalid_evidence"
	} else {
		applyCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := o.apply(applyCtx, event)
		cancel()
		if err != nil {
			var pgErr *pgconn.PgError
			switch {
			case errors.Is(err, errAttemptMissing):
				reason = "missing_attempt_intent"
			case errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23")):
				reason = "invalid_evidence_" + pgErr.Code
			default:
				return err
			}
		}
	}
	if reason != "" {
		slog.Warn("provider attempt evidence retained for review", "attempt_id", event.ID, "sequence", row.seq, "reason", reason)
		_, err := o.journal.ExecContext(ctx, `UPDATE attempt_events SET rejected_reason=? WHERE sequence=?`, reason, row.seq)
		return err
	}
	_, err := o.journal.ExecContext(ctx, `DELETE FROM attempt_events WHERE sequence=?`, row.seq)
	return err
}

func (e attemptEvent) valid() bool {
	if e.ID == "" {
		return false
	}
	switch e.Kind {
	case "prepare":
		return e.Intent != nil && e.Intent.ID == e.ID && !e.Intent.PreparedAt.IsZero()
	case "usage":
		return e.Usage != nil
	case "send", "http", "finish":
		return !e.At.IsZero()
	case "dispatch", "not_sent":
		return true
	default:
		return false
	}
}

func (r *AttemptRecorder) applyEvent(ctx context.Context, event attemptEvent) error {
	switch event.Kind {
	case "prepare":
		return r.Prepare(ctx, *event.Intent)
	case "dispatch":
		return r.MarkDispatched(ctx, event.ID)
	case "not_sent":
		return r.MarkNotSent(ctx, event.ID)
	case "send":
		return r.MarkSent(ctx, event.ID, event.At)
	case "http":
		return r.RecordHTTP(ctx, event.ID, event.Status, event.ProviderRequestID, event.At)
	case "usage":
		return r.RecordUsage(ctx, event.ID, *event.Usage)
	case "finish":
		return r.finishAt(ctx, event.ID, event.Outcome, event.At)
	default:
		return fmt.Errorf("unrecognized provider attempt evidence kind %q", event.Kind)
	}
}
