// Package admin — data_lifecycle_jobs.go
//
// 2026-07-13: data lifecycle async job registry.

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type JobType string

const (
	JobTypePromoteHot    JobType = "promote_hot"
	JobTypeDropPartition JobType = "drop_partition"
	JobTypeVacuum        JobType = "vacuum"
	JobTypeVacuumFull    JobType = "vacuum_full"
	JobTypeReindex       JobType = "reindex"
)

type JobStatus string

const (
	JobStatusQueued    JobStatus = "queued"
	JobStatusRunning   JobStatus = "running"
	JobStatusSucceeded JobStatus = "succeeded"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

type JobRun struct {
	RunID       string             `json:"run_id"`
	Op          JobType            `json:"op"`
	Status      JobStatus          `json:"status"`
	Params      map[string]any     `json:"params,omitempty"`
	Progress    *JobProgress       `json:"progress,omitempty"`
	Result      map[string]any     `json:"result,omitempty"`
	Error       string             `json:"error,omitempty"`
	Message     string             `json:"message,omitempty"`
	StartedAt   *time.Time         `json:"started_at,omitempty"`
	HeartbeatAt *time.Time         `json:"heartbeat_at,omitempty"`
	FinishedAt  *time.Time         `json:"finished_at,omitempty"`
	DurationMS  int64              `json:"duration_ms"`
	Operator    string             `json:"operator,omitempty"`
	cancelFn    context.CancelFunc `json:"-"`
}

type JobProgress struct {
	Done    int64   `json:"done"`
	Total   int64   `json:"total"`
	Percent float64 `json:"percent"`
	Batches int     `json:"batches"`
	Message string  `json:"message,omitempty"`
}

type jobRegistry struct {
	mu      sync.Mutex
	running map[string]*JobRun
	history []*JobRun
	nextID  atomic.Int64
	maxKeep int
}

const defaultJobHistoryKeep = 50

func (h *Handler) getJobRegistry() *jobRegistry {
	h.jobRegistryMu.Lock()
	defer h.jobRegistryMu.Unlock()
	if h.jobRegistry == nil {
		h.jobRegistry = &jobRegistry{running: map[string]*JobRun{}, history: []*JobRun{}, maxKeep: 50}
	}
	return h.jobRegistry
}

func (h *Handler) StartJob(op JobType, params map[string]any, operator string, fn func(ctx context.Context, run *JobRun)) *JobRun {
	now := time.Now()
	registry := h.getJobRegistry()
	seq := registry.nextID.Add(1)
	run := &JobRun{RunID: fmt.Sprintf("job-%s-%d-%d", string(op), now.Unix(), seq), Op: op, Status: JobStatusQueued, Params: params, Message: "queued", StartedAt: &now, Operator: operator}
	registry.mu.Lock()
	registry.running[run.RunID] = run
	registry.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	run.cancelFn = cancel
	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				h.failJob(run, fmt.Sprintf("panic: %v", r))
			}
			h.finalizeJob(run)
		}()
		run.Status = JobStatusRunning
		hb := time.Now()
		run.HeartbeatAt = &hb
		run.Message = "running"
		fn(ctx, run)
	}()
	return run
}

func (h *Handler) UpdateProgress(run *JobRun, done, total int64, batches int, msg string) {
	if run == nil {
		return
	}
	registry := h.getJobRegistry()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, ok := registry.running[run.RunID]; !ok {
		return
	}
	pct := 0.0
	if total > 0 {
		pct = float64(done) / float64(total) * 100.0
	}
	run.Progress = &JobProgress{Done: done, Total: total, Percent: pct, Batches: batches, Message: msg}
	hb := time.Now()
	run.HeartbeatAt = &hb
	if msg != "" {
		run.Message = msg
	}
}

func (h *Handler) TouchHeartbeat(run *JobRun, msg string) {
	if run == nil {
		return
	}
	registry := h.getJobRegistry()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, ok := registry.running[run.RunID]; !ok {
		return
	}
	hb := time.Now()
	run.HeartbeatAt = &hb
	if msg != "" {
		run.Message = msg
	}
}

func (h *Handler) SetResult(run *JobRun, result map[string]any, msg string) {
	if run == nil {
		return
	}
	registry := h.getJobRegistry()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	run.Result = result
	if msg != "" {
		run.Message = msg
	}
}

func (h *Handler) failJob(run *JobRun, errMsg string) {
	if run == nil {
		return
	}
	registry := h.getJobRegistry()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	run.Status = JobStatusFailed
	run.Error = errMsg
	run.Message = "failed: " + errMsg
}

func (h *Handler) succeedJob(run *JobRun, msg string) {
	if run == nil {
		return
	}
	registry := h.getJobRegistry()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	run.Status = JobStatusSucceeded
	if msg != "" {
		run.Message = msg
	}
}

func (h *Handler) finalizeJob(run *JobRun) {
	registry := h.getJobRegistry()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, ok := registry.running[run.RunID]; !ok {
		return
	}
	fin := time.Now()
	run.FinishedAt = &fin
	if run.StartedAt != nil {
		run.DurationMS = fin.Sub(*run.StartedAt).Milliseconds()
	}
	delete(registry.running, run.RunID)
	histCopy := *run
	registry.history = append(registry.history, &histCopy)
	if len(registry.history) > registry.maxKeep {
		sort.Slice(registry.history, func(i, j int) bool { return registry.history[i].FinishedAt.Before(*registry.history[j].FinishedAt) })
		drop := len(registry.history) - registry.maxKeep
		registry.history = registry.history[drop:]
	}
}

func (h *Handler) getJob(runID string) *JobRun {
	registry := h.getJobRegistry()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if r, ok := registry.running[runID]; ok {
		return cloneJobRun(r)
	}
	for _, r := range registry.history {
		if r.RunID == runID {
			return cloneJobRun(r)
		}
	}
	return nil
}

func (h *Handler) listJobs(limit int) (running []*JobRun, history []*JobRun) {
	registry := h.getJobRegistry()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for _, r := range registry.running {
		running = append(running, cloneJobRun(r))
	}
	if limit <= 0 || limit > registry.maxKeep {
		limit = registry.maxKeep
	}
	hs := make([]*JobRun, 0, len(registry.history))
	for _, r := range registry.history {
		hs = append(hs, cloneJobRun(r))
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].FinishedAt.After(*hs[j].FinishedAt) })
	if len(hs) > limit {
		hs = hs[:limit]
	}
	return running, hs
}

func cloneJobRun(r *JobRun) *JobRun {
	copy := *r
	if r.Progress != nil {
		pcopy := *r.Progress
		copy.Progress = &pcopy
	}
	copy.Params = cloneAnyMap(r.Params)
	copy.Result = cloneAnyMap(r.Result)
	if r.StartedAt != nil {
		t := *r.StartedAt
		copy.StartedAt = &t
	}
	if r.HeartbeatAt != nil {
		t := *r.HeartbeatAt
		copy.HeartbeatAt = &t
	}
	if r.FinishedAt != nil {
		t := *r.FinishedAt
		copy.FinishedAt = &t
	}
	return &copy
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (r *jobRegistry) cancel(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.running[id]
	if !ok {
		return false
	}
	if j.Status != JobStatusQueued && j.Status != JobStatusRunning {
		return false
	}
	if j.cancelFn != nil {
		j.cancelFn()
	}
	j.Status = JobStatusCancelled
	now := time.Now().UTC()
	j.FinishedAt = &now
	if j.StartedAt != nil {
		j.DurationMS = now.Sub(*j.StartedAt).Milliseconds()
	}
	return true
}

// handleLifecycleJobs GET /api/admin/data-lifecycle/jobs
//
// 列出所有通用异步任务（running + history）。
func (h *Handler) handleLifecycleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	running, history := h.listJobs(50)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"running": running,
		"history": history,
	})
}

// handleLifecycleJobByID GET /api/admin/data-lifecycle/jobs/{id}
//
// 查询单个任务状态。支持 /jobs/{id}/cancel → 取消任务。
func (h *Handler) handleLifecycleJobByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/admin/data-lifecycle/jobs/")
	id = strings.TrimSpace(id)
	if id == "" {
		writeError(w, http.StatusBadRequest, "job id is required")
		return
	}

	if r.Method == http.MethodPost && strings.HasSuffix(id, "/cancel") {
		id = strings.TrimSuffix(id, "/cancel")
		registry := h.getJobRegistry()
		ok := registry.cancel(id)
		if !ok {
			writeError(w, http.StatusNotFound, "job not found or already in terminal state")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"job_id": id,
			"status": "cancellation_requested",
		})
		return
	}

	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	job := h.getJob(id)
	if job == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}
