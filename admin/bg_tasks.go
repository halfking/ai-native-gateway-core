// Package admin: bg_tasks.go — background_tasks table helpers.
//
// ID-consistency contract (2026-06-26):
//
//	background_tasks row has two pieces of ID info:
//	  1. top-level columns: provider_id, credential_id (nullable)
//	  2. request_json (jsonb): { "provider_id": <int>, "credential_id": <int>, ... }
//
//	Both MUST refer to the same logical (provider, credential) pair, because
//	all INSERT paths (admin/provider_cred_lifecycle.go:111 health_check,
//	admin/provider_credential.go:89 auto_on_create,
//	admin/providers.go:905 auto_on_provider_update,
//	admin/provider_diagnose.go:306 diagnose) pass the same value to both.
//
//	If you ever observe an id_inconsistency field in GET /api/tasks/{id}
//	responses, or the slog.Warn "background_task ID mismatch" line, do this:
//
//	  1. Run the diagnostic query in admin/bg_tasks_test.go commentary / or:
//	       SELECT id, task_type, provider_id, credential_id,
//	              (request_json->>'provider_id')::bigint AS req_pid,
//	              (request_json->>'credential_id')::bigint AS req_cid
//	       FROM background_tasks
//	       WHERE task_type='health_check'
//	         AND ( (request_json->>'provider_id')::bigint IS DISTINCT FROM provider_id
//	            OR (request_json->>'credential_id')::bigint IS DISTINCT FROM credential_id )
//	       ORDER BY id DESC LIMIT 20;
//	  2. Identify the offending INSERT path from the request_json contents
//	     (e.g. "source":"auto_on_create" → admin/provider_credential.go:89).
//	  3. Fix the path so both top-level columns and request_json receive the
//	     same IDs. Until then the frontend will reject such rows via the
//	     assertTaskMatches / assertProviderMatches guards in
//	     web/src/api/providers.ts and DiagTab.vue.
package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func insertBackgroundTask(ctx context.Context, db *pgxpool.Pool, taskType string, providerID *int, credentialID *int, reqJSON any) (int64, error) {
	var id int64
	reqBytes, _ := json.Marshal(reqJSON)
	err := db.QueryRow(ctx, `
		INSERT INTO background_tasks (task_type, provider_id, credential_id, request_json)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, taskType, providerID, credentialID, string(reqBytes)).Scan(&id)
	return id, err
}

func completeBackgroundTask(ctx context.Context, db *pgxpool.Pool, taskID int64, result any) {
	resultBytes, _ := json.Marshal(result)
	_, err := db.Exec(ctx, `
		UPDATE background_tasks SET status = 'succeeded', result_json = $1, finished_at = NOW()
		WHERE id = $2
	`, string(resultBytes), taskID)
	if err != nil {
		slog.Error("completeBackgroundTask failed", "task_id", taskID, "error", err)
	}
}

func failBackgroundTask(ctx context.Context, db *pgxpool.Pool, taskID int64, errMsg string) {
	_, err := db.Exec(ctx, `
		UPDATE background_tasks SET status = 'failed', error = $1, finished_at = NOW()
		WHERE id = $2
	`, errMsg, taskID)
	if err != nil {
		slog.Error("failBackgroundTask failed", "task_id", taskID, "error", err)
	}
}

func getBackgroundTask(ctx context.Context, db *pgxpool.Pool, taskID int64) (map[string]any, error) {
	var id int64
	var taskType, status string
	var providerID, credentialID *int64
	var requestJSON, resultJSON *string
	var errText *string
	var startedAt time.Time
	var finishedAt *time.Time

	err := db.QueryRow(ctx, `
		SELECT id, task_type, provider_id, credential_id, status,
		       request_json::text, result_json::text, error, started_at, finished_at
		FROM background_tasks WHERE id = $1
	`, taskID).Scan(&id, &taskType, &providerID, &credentialID, &status,
		&requestJSON, &resultJSON, &errText, &startedAt, &finishedAt)
	if err != nil {
		return nil, err
	}

	result := map[string]any{
		"id":         id,
		"task_type":  taskType,
		"status":     status,
		"started_at": startedAt,
	}
	if providerID != nil {
		result["provider_id"] = *providerID
	}
	if credentialID != nil {
		result["credential_id"] = *credentialID
	}
	if finishedAt != nil {
		result["finished_at"] = *finishedAt
	}
	if errText != nil {
		result["error"] = *errText
	}
if requestJSON != nil {
		var req any
		//nolint:errcheck // test parse, non-critical
		json.Unmarshal([]byte(*requestJSON), &req)
		result["request"] = req

		// ID-consistency check: the top-level provider_id/credential_id columns
		// must match what is stored inside request_json. If they diverge we have
		// either a bug in some INSERT path or a manually-edited row; flag it so
		// ops can spot the case without grepping the DB.
		if reqMap, ok := req.(map[string]any); ok {
			if mismatch, ok := detectBackgroundTaskIDMismatch(taskID, taskType, providerID, credentialID, reqMap); ok {
				slog.Warn("background_task ID mismatch: top-level columns do not match request_json",
					"task_id", id, "task_type", taskType,
					"top_provider_id", mismatch["top_provider_id"],
					"top_credential_id", mismatch["top_credential_id"],
					"request_provider_id", mismatch["request_provider_id"],
					"request_credential_id", mismatch["request_credential_id"])
				result["id_inconsistency"] = mismatch
			}
		}
	}
	if resultJSON != nil {
		var res any
		//nolint:errcheck // test parse, non-critical
		json.Unmarshal([]byte(*resultJSON), &res)
		result["result"] = res
	}
	return result, nil
}

func getLatestDiagnoseResult(ctx context.Context, db *pgxpool.Pool, providerID int) (map[string]any, error) {
	var id int64
	var resultJSON *string
	var finishedAt *time.Time

	err := db.QueryRow(ctx, `
		SELECT id, result_json::text, finished_at
		FROM background_tasks
		WHERE provider_id = $1 AND task_type = 'diagnose' AND status = 'succeeded'
		ORDER BY finished_at DESC LIMIT 1
	`, providerID).Scan(&id, &resultJSON, &finishedAt)
	if err != nil {
		return nil, err
	}

	result := map[string]any{"task_id": id, "finished_at": finishedAt}
	if resultJSON != nil {
		var res any
		//nolint:errcheck // test parse, non-critical
		json.Unmarshal([]byte(*resultJSON), &res)
		result["result"] = res
	}
	return result, nil
}

// detectBackgroundTaskIDMismatch extracts provider_id/credential_id from a
// parsed request_json map and compares them against the top-level column
// values stored in background_tasks. Returns a non-nil mismatch map and true
// when any present pair disagrees. nil/false otherwise. Extracted from
// getBackgroundTask so it can be unit-tested without a DB.
func detectBackgroundTaskIDMismatch(taskID int64, taskType string, topPID, topCID *int64, req map[string]any) (map[string]any, bool) {
	getInt64 := func(key string) *int64 {
		v, ok := req[key]
		if !ok {
			return nil
		}
		switch x := v.(type) {
		case float64:
			n := int64(x)
			return &n
		case int64:
			return &x
		case int:
			n := int64(x)
			return &n
		case json.Number:
			n, err := x.Int64()
			if err != nil {
				return nil
			}
			return &n
		}
		return nil
	}
	reqPID := getInt64("provider_id")
	reqCID := getInt64("credential_id")

	pidMismatch := reqPID != nil && topPID != nil && *reqPID != *topPID
	cidMismatch := reqCID != nil && topCID != nil && *reqCID != *topCID
	if !pidMismatch && !cidMismatch {
		return nil, false
	}
	return map[string]any{
		"task_id":              taskID,
		"task_type":            taskType,
		"top_provider_id":      topPID,
		"top_credential_id":    topCID,
		"request_provider_id":  reqPID,
		"request_credential_id": reqCID,
	}, true
}
