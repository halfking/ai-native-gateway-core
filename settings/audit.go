package settings

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditEntry mirrors one row in settings_audit.
type AuditEntry struct {
	SettingKey   string          `json:"setting_key"`
	TenantID     string          `json:"tenant_id,omitempty"`
	Action       string          `json:"action"` // "update" | "rollback" | "delete"
	OldValue     json.RawMessage `json:"old_value,omitempty"`
	NewValue     json.RawMessage `json:"new_value,omitempty"`
	OperatorUser string          `json:"operator_user"`
	OperatorRole string          `json:"operator_role"`
	ClientIP     string          `json:"client_ip"`
	CreatedAt    time.Time       `json:"created_at"`
}

// WriteAudit inserts one row into settings_audit. Best-effort: errors
// are logged but do not fail the upstream call.
func WriteAudit(ctx context.Context, pool *pgxpool.Pool, e AuditEntry) {
	if pool == nil {
		return
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	var tenantArg any
	if e.TenantID != "" {
		tenantArg = e.TenantID
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO settings_audit
			(setting_key, tenant_id, action, old_value, new_value,
			 operator_user, operator_role, client_ip, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, e.SettingKey, tenantArg, e.Action, e.OldValue, e.NewValue,
		e.OperatorUser, e.OperatorRole, e.ClientIP, e.CreatedAt)
	if err != nil {
		slog.Warn("settings: audit write failed",
			"key", e.SettingKey, "err", err)
	}
}

// ListAudit returns recent audit entries for a setting (or all if key="").
func ListAudit(ctx context.Context, pool *pgxpool.Pool, settingKey, tenantID string, limit int) ([]AuditEntry, error) {
	if limit < 1 || limit > 500 {
		limit = 50
	}
	var rows interface {
		Next() bool
		Scan(dest ...any) error
		Close()
		Err() error
	}
	var err error
	if settingKey != "" {
		rows, err = pool.Query(ctx, `
			SELECT setting_key, COALESCE(tenant_id,''), action,
			       COALESCE(old_value::text,''), COALESCE(new_value::text,''),
			       operator_user, operator_role, COALESCE(client_ip,''), created_at
			  FROM settings_audit
			 WHERE setting_key = $1
			   AND ($2 = '' OR tenant_id = $2)
			   AND created_at > now() - INTERVAL '7 days'
			 ORDER BY created_at DESC
			 LIMIT $3
		`, settingKey, tenantID, limit)
	} else {
		rows, err = pool.Query(ctx, `
			SELECT setting_key, COALESCE(tenant_id,''), action,
			       COALESCE(old_value::text,''), COALESCE(new_value::text,''),
			       operator_user, operator_role, COALESCE(client_ip,''), created_at
			  FROM settings_audit
			 WHERE ($1 = '' OR tenant_id = $1)
			   AND created_at > now() - INTERVAL '7 days'
			 ORDER BY created_at DESC
			 LIMIT $2
		`, tenantID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var oldV, newV []byte
		if err := rows.Scan(&e.SettingKey, &e.TenantID, &e.Action,
			&oldV, &newV, &e.OperatorUser, &e.OperatorRole,
			&e.ClientIP, &e.CreatedAt); err != nil {
			continue
		}
		if len(oldV) > 0 {
			e.OldValue = json.RawMessage(oldV)
		}
		if len(newV) > 0 {
			e.NewValue = json.RawMessage(newV)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CleanupOldAudit deletes audit rows older than `retention` (Q6: C = 7 days).
// Returns the number of rows deleted.
func CleanupOldAudit(ctx context.Context, pool *pgxpool.Pool, retention time.Duration) (int64, error) {
	tag, err := pool.Exec(ctx, `
		DELETE FROM settings_audit WHERE created_at < $1
	`, time.Now().Add(-retention))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// FromHTTPRequest extracts operator identity. We don't import admin
// here (cycle); callers pass user/role from their auth context.
func FromHTTPRequest(r *http.Request, user, role string) string {
	if user == "" {
		user = "unknown"
	}
	if role == "" {
		role = "anonymous"
	}
	ip := r.RemoteAddr
	_ = user
	_ = role
	return ip
}

