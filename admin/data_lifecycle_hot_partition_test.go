package admin

import (
	"testing"
	"time"
)

func TestHotJobManager_AddGet(t *testing.T) {
	m := newHotJobManager()
	job := &HotPromoteJob{
		ID:          "job-1",
		TableName:   "_request_logs_hot",
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
	if got.TableName != "_request_logs_hot" {
		t.Errorf("expected _request_logs_hot, got %s", got.TableName)
	}
	if got.Status != HotJobStatusPending {
		t.Errorf("expected pending, got %s", got.Status)
	}
}

func TestHotJobManager_ListAll_OrderByStartedAt(t *testing.T) {
	m := newHotJobManager()
	now := time.Now()
	for i, name := range []string{"_request_logs_hot", "usage_ledger_hot", "routing_decision_log_hot"} {
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
	m.add(&HotPromoteJob{ID: "j1", TableName: "_request_logs_hot", StartedAt: time.Now()})
	m.add(&HotPromoteJob{ID: "j2", TableName: "usage_ledger_hot", StartedAt: time.Now()})
	m.add(&HotPromoteJob{ID: "j3", TableName: "_request_logs_hot", StartedAt: time.Now()})

	rl := m.listByTable("_request_logs_hot")
	if len(rl) != 2 {
		t.Fatalf("expected 2 _request_logs_hot jobs, got %d", len(rl))
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
			TableName: "_request_logs_hot",
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
		TableName:  "_request_logs_hot",
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
		TableName: "_request_logs_hot",
		Status:    HotJobStatusRunning,
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	h.hotJobMgr.add(running)

	done := &HotPromoteJob{
		ID:         "done_1",
		TableName:  "_request_logs_hot",
		Status:     HotJobStatusSuccess,
		StartedAt:  time.Now().Add(-time.Hour),
		FinishedAt: timePtr(time.Now()),
		UpdatedAt:  time.Now(),
	}
	h.hotJobMgr.add(done)

	got := h.findRunningJobForTable("_request_logs_hot")
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
		TableName:  "_request_logs_hot",
		Status:     HotJobStatusFailed,
		StartedAt:  time.Now().Add(-time.Minute),
		FinishedAt: timePtr(time.Now()),
	}
	h.hotJobMgr.add(done2)
	got3 := h.findRunningJobForTable("_request_logs_hot")
	if got3 == nil || got3.ID != "running_1" {
		t.Errorf("expected still running_1, got %v", got3)
	}
}

func TestDefaultHotRetentionHours(t *testing.T) {
	if defaultHotRetentionHours != 8 {
		t.Errorf("expected default retention 8h, got %d", defaultHotRetentionHours)
	}
}

func TestHotPromoteTableMap(t *testing.T) {
	// 2026-07-14: model_probe_runs_hot 切换为纯 hot 表策略，
	// 不再 promote，移除了对应的 map 项。
	// 2026-09-01: 补上 session_bodies_hot（promote 函数由迁移 615/626/638
	// 定义），此前手动 promote 无法触达该表。
	// 2026-09-05: 补上 supplier_errors_hot（promote 函数由迁移 V371 定义，
	// 供应商错误唯一事实源的 8h 保留/批量迁移入口，审计闭环1/5）。
	//
	// ★ 2026-10-20（迁移 890，v1 族真删）：两条 v1 族**仍在**清单里 ——
	// 不是为了还能 promote（表与函数都被 DROP，早就不行了），而是为了保住
	// **手动 promote 端点的退役语义**（对退役族回 409 + 退役理由，而不是
	// 笼统的"不支持该表"）。自动 cron 侧由**能力探针**兜住（函数不存在 ⇒
	// INFO 的 SKIP，不刷 42883 噪声）。把条目摘掉会改掉手动端点的拒绝形状
	// ⇒ 既有退役契约测试直接变红（实测）。
	expected := []string{
		"_request_logs_hot",
		"usage_ledger_hot",
		"request_wal_hot",
		"routing_decision_log_hot",
		"credential_model_index_hot",
		"_request_logs_bodies_hot",
		"credit_ledger_hot",
		"tool_usage_stats_hot",
		"candidate_failure_logs_hot",
		"supplier_errors_hot",
		"session_turns_hot",
		"session_bodies_hot",
		"handoff_logs_hot",
		"session_module_executions_hot",
		"dashboard_access_events_hot",
		"auto_route_selections_hot",
		// 706（存储优化方案 v2 S1a）
		"session_memora_hot",
		"session_censors_hot",
		"session_tools_hot",
		// 733（会话存储解耦 v3）：turn 特征层
		"session_turn_details_hot",
	}
	for _, name := range expected {
		if _, ok := hotPromoteTableMap[name]; !ok {
			t.Errorf("expected %s in hotPromoteTableMap", name)
		}
	}
	if hotPromoteTableMap["session_bodies_hot"] != "promote_session_bodies_hot_to_partition" {
		t.Errorf("session_bodies_hot must map to promote_session_bodies_hot_to_partition, got %s", hotPromoteTableMap["session_bodies_hot"])
	}
	if len(hotPromoteTableMap) != len(expected) {
		t.Errorf("expected %d tables, got %d", len(expected), len(hotPromoteTableMap))
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}
