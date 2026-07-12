package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type JobType string

const (
	JobTypeDropPartition JobType = "drop_partition"
	JobTypeVacuum        JobType = "vacuum"
	JobTypeVacuumFull    JobType = "vacuum_full"
	JobTypeReindex       JobType = "reindex"
)

type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusSuccess   JobStatus = "success"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

type JobRun struct {
	ID         string             `json:"id"`
	Type       JobType            `json:"type"`
	Status     JobStatus          `json:"status"`
	Target     string             `json:"target"`
	Message    string             `json:"message,omitempty"`
	Error      string             `json:"error,omitempty"`
	StartedAt  time.Time          `json:"started_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
	FinishedAt *time.Time         `json:"finished_at,omitempty"`
	DurationMS int64              `json:"duration_ms"`
	Result     map[string]any     `json:"result,omitempty"`
	cancelFn   context.CancelFunc `json:"-"`
}

type jobRegistry struct {
	mu   sync.RWMutex
	jobs map[string]*JobRun
}

func newJobRegistry() *jobRegistry {
	return &jobRegistry{
		jobs: make(map[string]*JobRun),
	}
}

func (r *jobRegistry) add(job *JobRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[job.ID] = job
}

func (r *jobRegistry) get(id string) (*JobRun, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.jobs[id]
	return j, ok
}

func (r *jobRegistry) list() []*JobRun {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*JobRun, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool {
		return out[i].StartedAt.After(out[k].StartedAt)
	})
	return out
}

func (r *jobRegistry) cancel(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return false
	}
	if j.Status != JobStatusPending && j.Status != JobStatusRunning {
		return false
	}
	if j.cancelFn != nil {
		j.cancelFn()
		j.Status = JobStatusCancelled
		now := time.Now().UTC()
		j.FinishedAt = &now
		j.UpdatedAt = now
	}
	return true
}

func newJobRun(jobType JobType, target string) *JobRun {
	return &JobRun{
		ID:        uuid.NewString(),
		Type:      jobType,
		Status:    JobStatusPending,
		Target:    target,
		StartedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Result:    make(map[string]any),
	}
}

// handleLifecycleJobs GET /api/admin/data-lifecycle/jobs
//
// 列出所有通用异步任务（按 StartedAt DESC），以及当前 running 任务。
func (h *Handler) handleLifecycleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	all := h.lifecycleJobs.list()
	running := make([]*JobRun, 0)
	history := make([]*JobRun, 0)
	for _, j := range all {
		if j.Status == JobStatusPending || j.Status == JobStatusRunning {
			running = append(running, j)
		} else {
			history = append(history, j)
		}
	}
	// 清理 cancelFn（不序列化）
	sanitize := func(jobs []*JobRun) []*JobRun {
		out := make([]*JobRun, len(jobs))
		for i, j := range jobs {
			c := *j
			c.cancelFn = nil
			out[i] = &c
		}
		return out
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"running": sanitize(running),
		"history": sanitize(history),
	})
}

// handleLifecycleJobByID GET/POST /api/admin/data-lifecycle/jobs/{id}
//
// GET: 查询单个任务状态
// POST: 取消任务（需要 /cancel 路径后缀）
func (h *Handler) handleLifecycleJobByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/admin/data-lifecycle/jobs/")
	id = strings.TrimSpace(id)
	if id == "" {
		writeError(w, http.StatusBadRequest, "job id is required")
		return
	}

	// POST 到 /jobs/{id}/cancel → 取消任务
	if r.Method == http.MethodPost && strings.HasSuffix(id, "/cancel") {
		id = strings.TrimSuffix(id, "/cancel")
		ok := h.lifecycleJobs.cancel(id)
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

	job, ok := h.lifecycleJobs.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	c := *job
	c.cancelFn = nil
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(c)
}

// logRunningJobs 是生命周期任务的定时状态记录（用于指标/调试），每 30 秒记录一次仍然 running 的任务
func (h *Handler) logRunningJobs() {
	all := h.lifecycleJobs.list()
	for _, j := range all {
		if j.Status == JobStatusRunning || j.Status == JobStatusPending {
			slog.Info("lifecycle-job-running", "job_id", j.ID, "type", j.Type, "target", j.Target, "status", j.Status)
		}
	}
}
