package providerprofile

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AlertStore 告警持久化接口
type AlertStore interface {
	// SaveIfNew 保存告警；若同一 (credential, trigger_date, alert_type) 已存在则跳过（幂等）。
	SaveIfNew(ctx context.Context, a *Alert) error

	// HasUnresolved 查询指定 credential/date/type 是否已有未解决告警。
	HasUnresolved(ctx context.Context, credentialID int64, typ AlertType, date time.Time) (bool, error)
}

// PGAlertStore PostgreSQL 实现
type PGAlertStore struct {
	db *pgxpool.Pool
}

// NewPGAlertStore 创建
func NewPGAlertStore(db *pgxpool.Pool) *PGAlertStore {
	return &PGAlertStore{db: db}
}

// SaveIfNew 见接口文档。provider_profile_alerts 无唯一约束，应用层去重。
func (s *PGAlertStore) SaveIfNew(ctx context.Context, a *Alert) error {
	var detailsJSON []byte
	if a.Details != nil {
		b, err := marshalJSON(a.Details)
		if err != nil {
			return fmt.Errorf("marshal alert details: %w", err)
		}
		detailsJSON = b
	}
	// 先查重，再插入。
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM provider_profile_alerts
		              WHERE credential_id=$1 AND trigger_date=$2 AND alert_type=$3)`,
		a.CredentialID, a.TriggerDate, a.Type).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check existing alert: %w", err)
	}
	if exists {
		return nil
	}

	_, err = s.db.Exec(ctx, `
		INSERT INTO provider_profile_alerts (
			credential_id, provider_id, alert_type, alert_level, trigger_date,
			current_score, previous_score, score_change, dimension,
			message, details, action_taken)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		a.CredentialID, a.ProviderID, a.Type, a.Level, a.TriggerDate,
		nullScore(a.CurrentScore), nullScore(a.PreviousScore), nullScore(a.ScoreChange),
		nullableString(a.Dimension), a.Message, jsonBytesOrNULL(detailsJSON), nullableString(a.ActionTaken))
	if err != nil {
		return fmt.Errorf("insert alert: %w", err)
	}
	return nil
}

// HasUnresolved 见接口文档
func (s *PGAlertStore) HasUnresolved(ctx context.Context, credentialID int64, typ AlertType, date time.Time) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM provider_profile_alerts
		              WHERE credential_id=$1 AND alert_type=$2 AND trigger_date=$3
		                AND resolved_at IS NULL)`,
		credentialID, typ, date).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check unresolved alert: %w", err)
	}
	return exists, nil
}

// nullScore returns nil for zero scores so the numeric(5,2) column stays NULL
// (a genuine 0.0 score is meaningless in this domain — scores are always positive).
func nullScore(f float64) interface{} {
	if f == 0 {
		return nil
	}
	return f
}

// nullableString returns nil for empty strings so text columns stay NULL.
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// jsonBytesOrNULL returns nil for an empty/nil slice so the jsonb column stays NULL.
func jsonBytesOrNULL(b []byte) interface{} {
	if len(b) == 0 {
		return nil
	}
	return b
}
