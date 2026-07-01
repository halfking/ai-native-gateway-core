package taskmanagement

import (
	"testing"
	"time"
)

func TestTaskGroup_Validation(t *testing.T) {
	group := &TaskGroup{
		ID:          "group_001",
		Name:        "测试任务组",
		Description: "这是一个测试任务组",
		TenantID:    "tenant_001",
		Type:        GroupTypeProject,
		Managers:    []string{"user_1", "user_2"},
		Members:     []string{"user_3", "user_4", "user_5"},
		Rules: GroupingRules{
			TenantFilter:    []string{"tenant_001"},
			RiskLevelFilter: []string{"high", "critical"},
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if group.ID == "" {
		t.Error("group ID should not be empty")
	}

	if len(group.Managers) != 2 {
		t.Errorf("expected 2 managers, got %d", len(group.Managers))
	}

	if len(group.Members) != 3 {
		t.Errorf("expected 3 members, got %d", len(group.Members))
	}
}

func TestRoundRobinBalancer(t *testing.T) {
	balancer := NewRoundRobinBalancer()

	members := []MemberInfo{
		{ID: "user_1", Name: "用户1", Available: true, CurrentLoad: 2, MaxLoad: 10},
		{ID: "user_2", Name: "用户2", Available: true, CurrentLoad: 3, MaxLoad: 10},
		{ID: "user_3", Name: "用户3", Available: true, CurrentLoad: 1, MaxLoad: 10},
	}

	task := &Task{
		ID:       "task_001",
		TenantID: "tenant_001",
	}

	// 第一次选择
	selected1, err := balancer.Select(members, task)
	if err != nil {
		t.Fatalf("failed to select: %v", err)
	}

	// 第二次选择
	selected2, err := balancer.Select(members, task)
	if err != nil {
		t.Fatalf("failed to select: %v", err)
	}

	// 第三次选择
	selected3, err := balancer.Select(members, task)
	if err != nil {
		t.Fatalf("failed to select: %v", err)
	}

	// 验证轮询
	if selected1.ID == selected2.ID || selected2.ID == selected3.ID || selected1.ID == selected3.ID {
		t.Error("round robin should select different members")
	}
}

func TestLeastTasksBalancer(t *testing.T) {
	balancer := NewLeastTasksBalancer()

	members := []MemberInfo{
		{ID: "user_1", Name: "用户1", Available: true, CurrentLoad: 5, MaxLoad: 10},
		{ID: "user_2", Name: "用户2", Available: true, CurrentLoad: 2, MaxLoad: 10},
		{ID: "user_3", Name: "用户3", Available: true, CurrentLoad: 8, MaxLoad: 10},
	}

	task := &Task{
		ID:       "task_001",
		TenantID: "tenant_001",
	}

	selected, err := balancer.Select(members, task)
	if err != nil {
		t.Fatalf("failed to select: %v", err)
	}

	// 应该选择负载最少的 user_2
	if selected.ID != "user_2" {
		t.Errorf("expected user_2, got %s", selected.ID)
	}
}

func TestWeightedBalancer(t *testing.T) {
	balancer := NewWeightedBalancer()

	members := []MemberInfo{
		{ID: "user_1", Name: "用户1", Available: true, Weight: 1, CurrentLoad: 0, MaxLoad: 10},
		{ID: "user_2", Name: "用户2", Available: true, Weight: 5, CurrentLoad: 0, MaxLoad: 10},
		{ID: "user_3", Name: "用户3", Available: true, Weight: 2, CurrentLoad: 0, MaxLoad: 10},
	}

	task := &Task{
		ID:       "task_001",
		TenantID: "tenant_001",
	}

	// 多次选择，统计分布
	counts := make(map[string]int)
	for i := 0; i < 1000; i++ {
		selected, err := balancer.Select(members, task)
		if err != nil {
			t.Fatalf("failed to select: %v", err)
		}
		counts[selected.ID]++
	}

	// user_2 的权重最高，应该被选中的次数最多
	if counts["user_2"] < counts["user_1"] || counts["user_2"] < counts["user_3"] {
		t.Errorf("user_2 should be selected most often, got counts: %v", counts)
	}
}

func TestRandomBalancer(t *testing.T) {
	balancer := NewRandomBalancer()

	members := []MemberInfo{
		{ID: "user_1", Name: "用户1", Available: true, CurrentLoad: 0, MaxLoad: 10},
		{ID: "user_2", Name: "用户2", Available: true, CurrentLoad: 0, MaxLoad: 10},
		{ID: "user_3", Name: "用户3", Available: true, CurrentLoad: 0, MaxLoad: 10},
	}

	task := &Task{
		ID:       "task_001",
		TenantID: "tenant_001",
	}

	// 多次选择，验证随机性
	selected := make(map[string]bool)
	for i := 0; i < 10; i++ {
		member, err := balancer.Select(members, task)
		if err != nil {
			t.Fatalf("failed to select: %v", err)
		}
		selected[member.ID] = true
	}

	// 至少应该选中2个不同的成员
	if len(selected) < 2 {
		t.Error("random balancer should select different members")
	}
}

func TestBalancer_NoAvailableMembers(t *testing.T) {
	balancer := NewLeastTasksBalancer()

	// 所有成员都不可用
	members := []MemberInfo{
		{ID: "user_1", Name: "用户1", Available: false, CurrentLoad: 0, MaxLoad: 10},
		{ID: "user_2", Name: "用户2", Available: false, CurrentLoad: 0, MaxLoad: 10},
	}

	task := &Task{
		ID:       "task_001",
		TenantID: "tenant_001",
	}

	_, err := balancer.Select(members, task)
	if err == nil {
		t.Error("expected error when no members available")
	}
}

func TestBalancer_MaxLoadReached(t *testing.T) {
	balancer := NewLeastTasksBalancer()

	// 所有成员都达到最大负载
	members := []MemberInfo{
		{ID: "user_1", Name: "用户1", Available: true, CurrentLoad: 10, MaxLoad: 10},
		{ID: "user_2", Name: "用户2", Available: true, CurrentLoad: 10, MaxLoad: 10},
	}

	task := &Task{
		ID:       "task_001",
		TenantID: "tenant_001",
	}

	_, err := balancer.Select(members, task)
	if err == nil {
		t.Error("expected error when all members at max load")
	}
}

func TestTask_Creation(t *testing.T) {
	task := &Task{
		ID:             "task_001",
		Type:           TaskTypeApproval,
		Status:         TaskStatusPending,
		TenantID:       "tenant_001",
		SessionID:      "sess_123",
		Priority:       10,
		AssignedGroups: []string{"group_001"},
		Assignees:      []string{"user_1"},
		Metadata: map[string]any{
			"risk_level": "high",
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if task.Type != TaskTypeApproval {
		t.Errorf("expected task type approval, got %s", task.Type)
	}

	if task.Status != TaskStatusPending {
		t.Errorf("expected status pending, got %s", task.Status)
	}

	if len(task.AssignedGroups) != 1 {
		t.Errorf("expected 1 assigned group, got %d", len(task.AssignedGroups))
	}
}

func TestTaskAssignment_Lifecycle(t *testing.T) {
	assignment := &TaskAssignment{
		ID:           "assign_001",
		TaskID:       "task_001",
		TaskType:     TaskTypeApproval,
		GroupID:      "group_001",
		AssigneeID:   "user_1",
		AssigneeName: "张三",
		Status:       TaskStatusPending,
		Priority:     10,
		AssignedAt:   time.Now(),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	// 开始处理
	startTime := time.Now()
	assignment.StartedAt = &startTime
	assignment.Status = TaskStatusInProgress

	if assignment.Status != TaskStatusInProgress {
		t.Error("status should be in_progress")
	}

	if assignment.StartedAt == nil {
		t.Error("started_at should not be nil")
	}

	// 完成任务
	completeTime := time.Now()
	assignment.CompletedAt = &completeTime
	assignment.Status = TaskStatusCompleted

	if assignment.Status != TaskStatusCompleted {
		t.Error("status should be completed")
	}

	if assignment.CompletedAt == nil {
		t.Error("completed_at should not be nil")
	}
}

func TestGroupingRules_Matching(t *testing.T) {
	rules := GroupingRules{
		TenantFilter:    []string{"tenant_001", "tenant_002"},
		RiskLevelFilter: []string{"high", "critical"},
		KeywordMatch:    []string{"审批", "review"},
	}

	if len(rules.TenantFilter) != 2 {
		t.Errorf("expected 2 tenants, got %d", len(rules.TenantFilter))
	}

	if len(rules.RiskLevelFilter) != 2 {
		t.Errorf("expected 2 risk levels, got %d", len(rules.RiskLevelFilter))
	}
}

func TestMemberInfo_Availability(t *testing.T) {
	member := MemberInfo{
		ID:          "user_1",
		Name:        "张三",
		Weight:      5,
		CurrentLoad: 3,
		MaxLoad:     10,
		Available:   true,
	}

	// 检查是否有容量
	hasCapacity := member.Available && (member.MaxLoad == 0 || member.CurrentLoad < member.MaxLoad)
	if !hasCapacity {
		t.Error("member should have capacity")
	}

	// 达到最大负载
	member.CurrentLoad = 10
	hasCapacity = member.Available && (member.MaxLoad == 0 || member.CurrentLoad < member.MaxLoad)
	if hasCapacity {
		t.Error("member should not have capacity")
	}
}

func TestTaskFilter_Validation(t *testing.T) {
	filter := &TaskGroupFilter{
		TenantID: "tenant_001",
		Type:     GroupTypeProject,
		Limit:    50,
		Offset:   0,
	}

	if filter.TenantID == "" {
		t.Error("tenant_id should not be empty")
	}

	if filter.Limit <= 0 {
		t.Error("limit should be positive")
	}

	if filter.Offset < 0 {
		t.Error("offset should not be negative")
	}
}
