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
