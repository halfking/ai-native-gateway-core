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
