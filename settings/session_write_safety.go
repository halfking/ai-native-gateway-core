package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ErrNoRequestWriteSource rejects configurations that lose all canonical turns.
var ErrNoRequestWriteSource = errors.New("request storage requires v1 writes or enabled V2 shadow writes")

func ValidateSessionWriteState(v1, enabled, shadow bool) error {
	if !v1 && !(enabled && shadow) {
		return ErrNoRequestWriteSource
	}
	return nil
}

var sessionWriteKeys = []string{KeyRequestLogsWriteEnabled, "sessions_v2.enabled", "sessions_v2.shadow_write"}

func isSessionWriteKey(key string) bool {
	for _, candidate := range sessionWriteKeys {
		if key == candidate {
			return true
		}
	}
	return false
}

// mutateSessionWriteSetting serializes related platform updates across gateway
// instances. Validation and the Set/Rollback write share one database snapshot;
// a second instance cannot concurrently close the other remaining write source.
func (s *StoreDB) mutateSessionWriteSetting(key string, value any, rollback bool) (jsonRawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), settingsQueryTimeout)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), settingsQueryTimeout)
		defer cleanupCancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('llmgw:session-write-settings', 0))`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT key, value::text, prev_value::text FROM settings_kv WHERE key = ANY($1) FOR UPDATE`, sessionWriteKeys)
	if err != nil {
		return nil, err
	}
	current := make(map[string][]byte, len(sessionWriteKeys))
	var previous []byte
	for rows.Next() {
		var name string
		var raw, prev []byte
		if err := rows.Scan(&name, &raw, &prev); err != nil {
			rows.Close()
			return nil, err
		}
		current[name] = raw
		if name == key {
			previous = prev
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return nil, rowErr
	}
	newValue := previous
	if rollback {
		if len(previous) == 0 {
			return nil, errors.New("no previous value to rollback to")
		}
	} else {
		newValue, err = json.Marshal(value)
		if err != nil {
			return nil, err
		}
	}
	current[key] = newValue
	var state [3]bool
	for index, name := range sessionWriteKeys {
		raw := current[name]
		if len(raw) == 0 {
			spec := Global.Spec(name)
			if spec == nil {
				return nil, fmt.Errorf("session write setting not registered: %s", name)
			}
			if env := os.Getenv(spec.EnvName); env != "" {
				raw = []byte(env)
			} else {
				raw, err = json.Marshal(spec.Default)
				if err != nil {
					return nil, err
				}
			}
		}
		if err := json.Unmarshal(raw, &state[index]); err != nil {
			return nil, fmt.Errorf("session write setting must be boolean: %s", name)
		}
	}
	if err := ValidateSessionWriteState(state[0], state[1], state[2]); err != nil {
		return nil, err
	}
	var result []byte
	if rollback {
		err = tx.QueryRow(ctx, `UPDATE settings_kv SET value=prev_value, prev_value=value,
            updated_at=now(), prev_updated_at=updated_at WHERE key=$1 RETURNING value::text`, key).Scan(&result)
	} else {
		spec := Global.Spec(key)
		if spec == nil {
			return nil, fmt.Errorf("unknown setting: %s", key)
		}
		err = tx.QueryRow(ctx, `INSERT INTO settings_kv(key,value,value_type,scope,category,updated_at)
            VALUES($1,$2::jsonb,$3,'platform',$4,now()) ON CONFLICT(key) DO UPDATE SET
            value=EXCLUDED.value, value_type=EXCLUDED.value_type, category=EXCLUDED.category,
            prev_value=settings_kv.value, prev_updated_at=settings_kv.updated_at, updated_at=now()
            RETURNING prev_value::text`, key, string(newValue), string(spec.Type), string(spec.Category)).Scan(&result)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	InvalidatePlatformValue(key)
	return result, nil
}
