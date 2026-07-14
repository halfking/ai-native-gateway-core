package admin

import (
	"testing"
	"time"
)

func TestHotJobManager_AddGet(t *testing.T) {
	m := newHotJobManager()
	job := &HotPromoteJob{
		ID:          "job-1",
		TableName:   "request_logs_hot",
		Status:      HotJobStatusPending,
		StartedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		TriggeredBy: "manual",
	}
	m.add(job)

	got, ok := m.get("job-1")
	if !ok {
		t.Fatal("expected to find job-1")
	}
	if got.TableName != "request_logs_hot" {
		t.Errorf("expected request_logs_hot, got %s", got.TableName)
	}
	if got.Status != HotJobStatusPending {
		t.Errorf("expected pending, got %s", got.Status)
	}
}

func TestHotJobManager_ListAll_OrderByStartedAt(t *testing.T) {
	m := newHotJobManager()
	now := time.Now()
	for i, name := range []string{"request_logs_hot", "usage_ledger_hot", "routing_decision_log_hot"} {
		m.add(&HotPromoteJob{
			ID:        "job-" + name,
			TableName: name,
			StartedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now,
		})
	}
	all := m.listAll()
	if len(all) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(all))
	}
	// listAll sorts by StartedAt DESC
	if all[0].ID != "job-routing_decision_log_hot" {
		t.Errorf("expected last inserted first, got %s", all[0].ID)
	}
}

func TestHotJobManager_ListByTable(t *testing.T) {
	m := newHotJobManager()
	m.add(&HotPromoteJob{ID: "j1", TableName: "request_logs_hot", StartedAt: time.Now()})
	m.add(&HotPromoteJob{ID: "j2", TableName: "usage_ledger_hot", StartedAt: time.Now()})
	m.add(&HotPromoteJob{ID: "j3", TableName: "request_logs_hot", StartedAt: time.Now()})

	rl := m.listByTable("request_logs_hot")
	if len(rl) != 2 {
		t.Fatalf("expected 2 request_logs_hot jobs, got %d", len(rl))
	}
	ul := m.listByTable("usage_ledger_hot")
	if len(ul) != 1 {
		t.Fatalf("expected 1 usage_ledger_hot job, got %d", len(ul))
	}
	empty := m.listByTable("nonexistent")
	if len(empty) != 0 {
		t.Errorf("expected 0 for nonexistent, got %d", len(empty))
	}
}

func TestHotJobManager_PerTableEviction(t *testing.T) {
	m := newHotJobManager()
	// Insert more than hotJobMaxPerTable for one table
	for i := 0; i < hotJobMaxPerTable+5; i++ {
		m.add(&HotPromoteJob{
			ID:        string(rune('a'+i)) + "_id",
			TableName: "request_logs_hot",
			StartedAt: time.Now(),
		})
	}
	all := m.listAll()
	if len(all) != hotJobMaxPerTable {
		t.Errorf("expected %d after eviction, got %d", hotJobMaxPerTable, len(all))
	}
}

func TestHotJobManager_GlobalEviction(t *testing.T) {
	m := newHotJobManager()
	// Insert more than hotJobMaxGlobal across many tables
	for i := 0; i < hotJobMaxGlobal+5; i++ {
		tableName := "table_" + string(rune('a'+i%26))
		m.add(&HotPromoteJob{
			ID:        string(rune('a'+i%26)) + "_" + string(rune('0'+i%10)),
			TableName: tableName,
			StartedAt: time.Now(),
		})
	}
	all := m.listAll()
	if len(all) > hotJobMaxGlobal {
		t.Errorf("expected <= %d after eviction, got %d", hotJobMaxGlobal, len(all))
	}
}

func TestHotJobManager_ExpiredEviction(t *testing.T) {
	m := newHotJobManager()
	old := time.Now().Add(-2 * hotJobRetention) // older than retention
	done := time.Now().Add(-2 * hotJobRetention)
	m.add(&HotPromoteJob{
		ID:         "old_job",
		TableName:  "request_logs_hot",
		StartedAt:  old,
		UpdatedAt:  old,
		FinishedAt: &done,
		Status:     HotJobStatusSuccess,
	})
	all := m.listAll()
	if len(all) != 0 {
		t.Errorf("expected expired job to be evicted, got %d", len(all))
	}
}

func TestFindRunningJobForTable(t *testing.T) {
	h := &Handler{hotJobMgr: newHotJobManager()}
	running := &HotPromoteJob{
		ID:        "running_1",
		TableName: "request_logs_hot",
		Status:    HotJobStatusRunning,
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	h.hotJobMgr.add(running)

	done := &HotPromoteJob{
		ID:         "done_1",
		TableName:  "request_logs_hot",
		Status:     HotJobStatusSuccess,
		StartedAt:  time.Now().Add(-time.Hour),
		FinishedAt: timePtr(time.Now()),
		UpdatedAt:  time.Now(),
	}
	h.hotJobMgr.add(done)

	got := h.findRunningJobForTable("request_logs_hot")
	if got == nil || got.ID != "running_1" {
		t.Errorf("expected running_1, got %v", got)
	}

	got2 := h.findRunningJobForTable("nonexistent")
	if got2 != nil {
		t.Errorf("expected nil for nonexistent, got %v", got2)
	}

	// 添加一个更新的已结束任务后，findRunningJobForTable 应仍返回 running_1
	done2 := &HotPromoteJob{
		ID:         "done_2",
		TableName:  "request_logs_hot",
		Status:     HotJobStatusFailed,
		StartedAt:  time.Now().Add(-time.Minute),
		FinishedAt: timePtr(time.Now()),
	}
	h.hotJobMgr.add(done2)
	got3 := h.findRunningJobForTable("request_logs_hot")
	if got3 == nil || got3.ID != "running_1" {
		t.Errorf("expected still running_1, got %v", got3)
	}
}

func TestDefaultHotRetentionHours(t *testing.T) {
	if defaultHotRetentionHours != 24 {
		t.Errorf("expected default retention 24h (1 day), got %d", defaultHotRetentionHours)
	}
}

func TestHotPromoteTableMap(t *testing.T) {
	// 2026-07-14: model_probe_runs_hot 切换为纯 hot 表策略，
	// 不再 promote，移除了对应的 map 项。
	expected := []string{
		"request_logs_hot",
		"usage_ledger_hot",
		"request_wal_hot",
		"routing_decision_log_hot",
		"credential_model_index_hot",
		"request_logs_bodies_hot",
		"credit_ledger_hot",
		"tool_usage_stats_hot",
	}
	for _, name := range expected {
		if _, ok := hotPromoteTableMap[name]; !ok {
			t.Errorf("expected %s in hotPromoteTableMap", name)
		}
	}
	if len(hotPromoteTableMap) != len(expected) {
		t.Errorf("expected %d tables, got %d", len(expected), len(hotPromoteTableMap))
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}
