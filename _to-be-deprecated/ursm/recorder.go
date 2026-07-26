package ursm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// RecordRequest is the single input type for all state writes.
// Every caller (executor, probes, manual) uses the same struct.
type RecordRequest struct {
	CredentialID int
	StdModel     string // 标准化模型名 → key: ursm:node:{credID}:{stdModel}
	RawModel     string // 供应商实际模型名 → 写入 HASH + 模型索引
	ProviderID   int    // 供应商 ID → 路由评分用
	FpSlotLimit  int    // 指纹槽上限 → 路由评分用
	ConcLimit    int    // 并发上限 → 路由评分用
	Success      bool
	LatencyMs    int
	ErrorKind    string
	Source       string // "request" | "probe_v2" | "model_probe" | "manual"
}

// NodeReadState is the full runtime state for a (cred, model) pair.
// Returned by GetNodeState for router scoring + degraded-mode routing.
type NodeReadState struct {
	Available        bool
	ConsecutiveFail  int
	LastError        string
	RawModel         string // 供应商实际模型名 (降级时用于 API 调用)
	ProviderID       int    // 供应商 ID (降级时路由用)
	FpSlotLimit      int    // 指纹槽上限 (路由评分用)
	ConcurrencyLimit int    // 并发上限 (路由评分用)
	LastSuccessAt    int64
	LastFailureAt    int64
	DisabledUntil    int64
	LatencyAvgMs     int
}

// StateRecorder is the unified state read/write entry point.
// Writes go through a Lua script (atomic HGETALL -> judge -> HMSET + EXPIRE).
// Reads are direct HGETALL — no caching layer, no DB fallback, fail-open on miss.
type StateRecorder struct {
	redis redis.UniversalClient
	db    *pgxpool.Pool
}

// NewStateRecorder creates a StateRecorder.
// db is optional — if nil, state_change_log will not be written.
func NewStateRecorder(redis redis.UniversalClient, db *pgxpool.Pool) *StateRecorder {
	return &StateRecorder{redis: redis, db: db}
}

// Record is the only state write entry point.
func (r *StateRecorder) Record(ctx context.Context, req RecordRequest) error {
	key := nodeKey(req.CredentialID, req.StdModel)
	idxKey := modelIndexKey(req.StdModel)
	now := time.Now().Unix()

	kind := "failure"
	if req.Success {
		kind = "success"
	}

	result, err := recordStateScript.Run(ctx, r.redis,
		[]string{key, idxKey},
		kind,
		req.ErrorKind,
		strconv.FormatInt(now, 10),
		strconv.Itoa(req.LatencyMs),
		"2",   // fail_threshold
		"300", // cooldown_seconds
		req.RawModel,
		strconv.Itoa(req.ProviderID),
		strconv.Itoa(req.FpSlotLimit),
		strconv.Itoa(req.ConcLimit),
	).Result()
	if err != nil {
		return fmt.Errorf("record state: %w", err)
	}

	// Lua returns [stateChanged, transitionReason, consecutiveFail, fromState, toState]
	vals, ok := result.([]interface{})
	if !ok || len(vals) < 5 {
		return nil
	}

	changed := vals[0].(string) == "true"
	if !changed || r.db == nil {
		return nil
	}

	reason, _ := vals[1].(string)
	consecutiveFail, _ := strconv.Atoi(vals[2].(string))
	fromState := vals[3].(string)
	toState := vals[4].(string)

	eventType := "unavailable_available"
	if fromState == "1" && toState == "0" {
		eventType = "available_unavailable"
	}

	r.logStateChange(ctx, req, eventType, reason, consecutiveFail)
	return nil
}

// IsAvailable checks whether a (cred, model) node is routable.
// fail-open: missing key or Redis error defaults to available=true.
func (r *StateRecorder) IsAvailable(ctx context.Context, credID int, model string) (bool, string) {
	key := nodeKey(credID, model)
	data, err := r.redis.HGetAll(ctx, key).Result()
	if err != nil || len(data) == 0 {
		return true, ""
	}

	now := time.Now().Unix()
	disabledUntil, _ := strconv.ParseInt(data["disabled_until"], 10, 64)
	consecutiveFail, _ := strconv.Atoi(data["consecutive_fail"])
	lastError := data["last_error"]

	if disabledUntil > 0 && now >= disabledUntil {
		return true, ""
	}

	if data["available"] == "0" {
		return false, lastError
	}

	if consecutiveFail >= 2 {
		return false, lastError
	}

	return true, ""
}

// GetNodeState returns the full node state for router scoring (concurrency, latency).
// Returns zero-value state if key missing.
func (r *StateRecorder) GetNodeState(ctx context.Context, credID int, model string) (*NodeReadState, error) {
	key := nodeKey(credID, model)
	data, err := r.redis.HGetAll(ctx, key).Result()
	if err != nil || len(data) == 0 {
		return &NodeReadState{Available: true}, nil
	}

	now := time.Now().Unix()
	disabledUntil, _ := strconv.ParseInt(data["disabled_until"], 10, 64)

	state := &NodeReadState{
		Available:        data["available"] != "0",
		ConsecutiveFail:  mustInt(data["consecutive_fail"]),
		LastError:        data["last_error"],
		RawModel:         data["raw_model"],
		ProviderID:       mustInt(data["provider_id"]),
		FpSlotLimit:      mustInt(data["fp_slot_limit"]),
		ConcurrencyLimit: mustInt(data["concurrency_limit"]),
		LastSuccessAt:    mustInt64(data["last_success_at"]),
		LastFailureAt:    mustInt64(data["last_failure_at"]),
		DisabledUntil:    disabledUntil,
		LatencyAvgMs:     mustInt(data["latency_avg_ms"]),
	}

	if disabledUntil > 0 && now >= disabledUntil {
		state.Available = true
		state.ConsecutiveFail = 0
		state.DisabledUntil = 0
	}

	if state.ConsecutiveFail >= 2 {
		state.Available = false
	}

	return state, nil
}

func (r *StateRecorder) logStateChange(ctx context.Context, req RecordRequest, eventType, reason string, consecutiveFail int) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	snapshot := map[string]interface{}{
		"error_kind": req.ErrorKind,
		"source":     req.Source,
		"latency_ms": req.LatencyMs,
	}
	snapshotJSON, _ := json.Marshal(snapshot)

	_, err := r.db.Exec(ctx, `
		INSERT INTO state_change_log
			(credential_id, raw_model_name, event_type, reason, error_kind, consecutive_fail, snapshot, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
	`, req.CredentialID, req.StdModel, eventType, reason,
		stringPtrIfNotEmpty(req.ErrorKind), consecutiveFail, snapshotJSON)
	if err != nil {
		slog.Warn("state_change_log insert failed",
			"credential_id", req.CredentialID,
			"std_model", req.StdModel,
			"error", err)
	}
}

func stringPtrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func mustInt(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}

func mustInt64(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}
