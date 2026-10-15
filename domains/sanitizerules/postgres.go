package sanitizerules

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }
func storeError(operation string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New("sanitize rule " + operation + " failed")
}

type rowScanner interface{ Scan(...any) error }

const versionColumns = `id,parent_id,rollback_of,config,actor,comment,created_at,review_links`

func scanVersion(row rowScanner) (Version, error) {
	var v Version
	var data, links []byte
	err := row.Scan(&v.ID, &v.ParentID, &v.RollbackOf, &data, &v.Actor, &v.Comment, &v.CreatedAt, &links)
	if err != nil {
		return Version{}, storeError("version read", err)
	}
	if json.Unmarshal(data, &v.Config) != nil || json.Unmarshal(links, &v.ReviewLinks) != nil {
		return Version{}, errors.New("sanitize rule stored version invalid")
	}
	return v, nil
}
func (s *PostgresStore) Current(ctx context.Context) (Version, error) {
	if s == nil || s.db == nil {
		return Version{}, errors.New("sanitize rule database unavailable")
	}
	v, err := scanVersion(s.db.QueryRowContext(ctx, `SELECT `+versionColumns+` FROM sanitize_rule_versions WHERE id=(SELECT version_id FROM sanitize_rule_head WHERE singleton=TRUE)`))
	if errors.Is(err, ErrNotFound) {
		return Version{}, nil
	}
	return v, err
}
func (s *PostgresStore) GetVersion(ctx context.Context, id int64) (Version, error) {
	return scanVersion(s.db.QueryRowContext(ctx, `SELECT `+versionColumns+` FROM sanitize_rule_versions WHERE id=$1`, id))
}
func (s *PostgresStore) ListVersions(ctx context.Context, limit int) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+versionColumns+` FROM sanitize_rule_versions ORDER BY id DESC LIMIT $1`, boundedLimit(limit))
	if err != nil {
		return nil, storeError("versions read", err)
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err = rows.Err(); err != nil {
		return nil, storeError("versions read", err)
	}
	return out, nil
}
func (s *PostgresStore) Publish(ctx context.Context, base int64, cfg Config, actor, comment string, rollback int64, links ...ReviewLink) (Version, error) {
	if err := validatePublish(cfg, actor, comment, rollback); err != nil {
		return Version{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Version{}, storeError("publication", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO sanitize_rule_head(singleton,version_id) VALUES(TRUE,0) ON CONFLICT DO NOTHING`); err != nil {
		return Version{}, storeError("publication lock", err)
	}
	var current int64
	if err = tx.QueryRowContext(ctx, `SELECT version_id FROM sanitize_rule_head WHERE singleton=TRUE FOR UPDATE`).Scan(&current); err != nil {
		return Version{}, storeError("publication lock", err)
	}
	if current != base {
		return Version{}, ErrConflict
	}
	if rollback != 0 {
		target, e := scanVersion(tx.QueryRowContext(ctx, `SELECT `+versionColumns+` FROM sanitize_rule_versions WHERE id=$1`, rollback))
		if e != nil {
			return Version{}, e
		}
		cfg = target.Config
		if err = Validate(cfg); err != nil {
			return Version{}, err
		}
	}
	// Event rows are locked inside this same transaction, so reviewed state and
	// tenant ownership cannot change between validation and version association.
	for _, link := range links {
		var status string
		err = tx.QueryRowContext(ctx, `SELECT status FROM sanitize_rule_events WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, link.ID, link.TenantID).Scan(&status)
		if err != nil {
			return Version{}, storeError("review link", err)
		}
		if status == "pending" {
			return Version{}, fmt.Errorf("%w: event must be reviewed before publication", ErrInvalid)
		}
	}
	data, _ := json.Marshal(cfg)
	encodedLinks, _ := json.Marshal(links)
	if links == nil {
		encodedLinks = []byte("[]")
	}
	v, err := scanVersion(tx.QueryRowContext(ctx, `INSERT INTO sanitize_rule_versions(parent_id,rollback_of,config,actor,comment,review_links) VALUES($1,$2,$3,$4,$5,$6) RETURNING `+versionColumns, current, rollback, data, actor, comment, encodedLinks))
	if err != nil {
		return Version{}, err
	}
	for _, link := range links {
		if _, err = tx.ExecContext(ctx, `UPDATE sanitize_rule_events SET resolved_version=$1 WHERE id=$2 AND tenant_id=$3`, v.ID, link.ID, link.TenantID); err != nil {
			return Version{}, storeError("review link", err)
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sanitize_rule_head SET version_id=$1 WHERE singleton=TRUE`, v.ID); err != nil {
		return Version{}, storeError("publication head", err)
	}
	if err = tx.Commit(); err != nil {
		return Version{}, storeError("publication commit", err)
	}
	return v, nil
}

const eventColumns = `id,fingerprint,tenant_id,rule_id,version,phase,action,status,sample_masked,count,first_seen,last_seen,reviewer,review_comment,reviewed_at,resolved_version,sensitive_type,field_kind`

func scanEvent(row rowScanner) (Event, error) {
	var e Event
	var reviewed sql.NullTime
	err := row.Scan(&e.ID, &e.Fingerprint, &e.TenantID, &e.RuleID, &e.Version, &e.Phase, &e.Action, &e.Status, &e.SampleMasked, &e.Count, &e.FirstSeen, &e.LastSeen, &e.Reviewer, &e.ReviewComment, &reviewed, &e.ResolvedVersion, &e.Type, &e.Field)
	if err != nil {
		return Event{}, storeError("event read", err)
	}
	if reviewed.Valid {
		e.ReviewedAt = &reviewed.Time
	}
	return e, nil
}
func (s *PostgresStore) RecordEvent(ctx context.Context, event Event, limit int) error {
	e, err := prepareEvent(event)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError("event write", err)
	}
	defer tx.Rollback()
	// Serialize the bounded-retention update across instances. Requests never wait
	// here: Manager owns a finite background queue and a context-bound worker.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(741968280105)`); err != nil {
		return storeError("event retention lock", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sanitize_rule_events(fingerprint,tenant_id,rule_id,version,phase,action,sample_masked,first_seen,last_seen,sensitive_type,field_kind) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8,$9,$10) ON CONFLICT(fingerprint) DO UPDATE SET count=sanitize_rule_events.count+1,last_seen=EXCLUDED.last_seen`, e.Fingerprint, e.TenantID, e.RuleID, e.Version, e.Phase, e.Action, e.SampleMasked, e.LastSeen, e.Type, e.Field)
	if err != nil {
		return storeError("event write", err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sanitize_rule_events WHERE id IN (SELECT id FROM sanitize_rule_events ORDER BY last_seen DESC,id DESC OFFSET $1)`, retentionLimit(limit)); err != nil {
		return storeError("event retention", err)
	}
	if err = tx.Commit(); err != nil {
		return storeError("event commit", err)
	}
	return nil
}
func (s *PostgresStore) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+eventColumns+` FROM sanitize_rule_events WHERE ($1='' OR tenant_id=$1) AND ($2='' OR status=$2) AND ($3='' OR action=$3) ORDER BY last_seen DESC,id DESC LIMIT $4`, f.TenantID, f.Status, f.Action, boundedLimit(f.Limit))
	if err != nil {
		return nil, storeError("events read", err)
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		return nil, storeError("events read", err)
	}
	return out, nil
}
