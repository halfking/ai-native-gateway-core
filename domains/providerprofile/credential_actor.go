package providerprofile

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrManualDisabled 自动恢复被拒绝：该凭证被管理员手动禁用。
var ErrManualDisabled = errors.New("credential is manually disabled; cannot auto-enable")

// CredentialLifecycle 凭证当前生命周期快照
type CredentialLifecycle struct {
	ID             int64
	Status         string // credentials.status
	Lifecycle      string // credentials.lifecycle_status
	Availability   string // credentials.availability_state
	ManualDisabled bool
}

// CredentialActor 抽象对 credentials 表的禁用/启用副作用，便于测试替换。
type CredentialActor interface {
	// Disable 将凭证 lifecycle_status 置为 disabled 并记录 auto_disabled_*。
	// 幂等：已是 disabled 时直接返回 nil。
	Disable(ctx context.Context, credentialID int64, reason string) error

	// Enable 将凭证恢复为 active。若 manual_disabled=true 则返回 ErrManualDisabled。
	// 幂等：已是 active 时直接返回 nil。
	Enable(ctx context.Context, credentialID int64, reason string) error

	// IsWhitelisted 判断 provider 是否在自动禁用白名单中。
	IsWhitelisted(ctx context.Context, providerID int64) (bool, error)

	// CurrentLifecycle 读取当前生命周期快照。
	CurrentLifecycle(ctx context.Context, credentialID int64) (*CredentialLifecycle, error)

	// RecordEvent 向 provider_events 插入一条事件。
	RecordEvent(ctx context.Context, credentialID int64, kind string, payload map[string]interface{}) error
}

// PGCredentialActor PostgreSQL 实现
type PGCredentialActor struct {
	db *pgxpool.Pool
}

// NewPGCredentialActor 创建
func NewPGCredentialActor(db *pgxpool.Pool) *PGCredentialActor {
	return &PGCredentialActor{db: db}
}

// Disable 见接口文档
func (a *PGCredentialActor) Disable(ctx context.Context, credentialID int64, reason string) error {
	tag, err := a.db.Exec(ctx, `
		UPDATE credentials
		SET lifecycle_status     = 'disabled',
		    availability_state   = 'suspended',
		    auto_disabled_at     = now(),
		    auto_disabled_reason = $1,
		    state_updated_at     = now()
		WHERE id = $2
		  AND lifecycle_status = 'active'`,
		reason, credentialID)
	if err != nil {
		return fmt.Errorf("disable credential %d: %w", credentialID, err)
	}
	// tag.RowsAffected()==0 means already disabled or not found — treat as no-op success
	_ = tag
	return nil
}

// Enable 见接口文档
func (a *PGCredentialActor) Enable(ctx context.Context, credentialID int64, reason string) error {
	tag, err := a.db.Exec(ctx, `
		UPDATE credentials
		SET lifecycle_status     = 'active',
		    availability_state   = 'ready',
		    auto_enabled_at      = now(),
		    auto_enabled_reason  = $1,
		    auto_disabled_at     = NULL,
		    auto_disabled_reason = NULL,
		    state_updated_at     = now()
		WHERE id = $2
		  AND manual_disabled = false
		  AND lifecycle_status = 'disabled'`,
		reason, credentialID)
	if err != nil {
		return fmt.Errorf("enable credential %d: %w", credentialID, err)
	}
	if tag.RowsAffected() == 0 {
		// 区分：是不存在/已active，还是 manual_disabled 阻挡
		lc, lerr := a.CurrentLifecycle(ctx, credentialID)
		if lerr != nil {
			return lerr
		}
		if lc.ManualDisabled {
			return ErrManualDisabled
		}
		// 否则已经是 active，幂等成功
	}
	return nil
}

// IsWhitelisted 见接口文档
func (a *PGCredentialActor) IsWhitelisted(ctx context.Context, providerID int64) (bool, error) {
	var exists bool
	err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provider_profile_whitelist WHERE provider_id=$1)`, providerID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check whitelist for provider %d: %w", providerID, err)
	}
	return exists, nil
}

// CurrentLifecycle 见接口文档
func (a *PGCredentialActor) CurrentLifecycle(ctx context.Context, credentialID int64) (*CredentialLifecycle, error) {
	var lc CredentialLifecycle
	err := a.db.QueryRow(ctx, `
		SELECT id, COALESCE(status,'active'), COALESCE(lifecycle_status,'active'),
		       COALESCE(availability_state,'unknown'), COALESCE(manual_disabled,false)
		FROM credentials WHERE id=$1`, credentialID).
		Scan(&lc.ID, &lc.Status, &lc.Lifecycle, &lc.Availability, &lc.ManualDisabled)
	if err != nil {
		return nil, fmt.Errorf("read lifecycle for credential %d: %w", credentialID, err)
	}
	return &lc, nil
}

// RecordEvent 见接口文档。provider_events.id 无默认值，必须显式取序列。
func (a *PGCredentialActor) RecordEvent(ctx context.Context, credentialID int64, kind string, payload map[string]interface{}) error {
	var payloadJSON []byte
	if payload != nil {
		b, err := marshalJSON(payload)
		if err != nil {
			return fmt.Errorf("marshal event payload: %w", err)
		}
		payloadJSON = b
	}
	_, err := a.db.Exec(ctx, `
		INSERT INTO provider_events (id, credential_id, event_kind, payload_json, ts)
		VALUES (nextval('provider_events_id_seq'), $1, $2, $3, now())`,
		credentialID, kind, payloadJSON)
	if err != nil {
		return fmt.Errorf("insert provider_event: %w", err)
	}
	return nil
}
