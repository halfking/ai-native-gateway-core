// Package bg/systemmonitor — types_test.go
//
// 单元测试：types.go (TaskType/Priority/Automaticity/Validate)
package systemmonitor

import (
	"testing"
	"time"
)

func TestTaskType_Valid(t *testing.T) {
	cases := []struct {
		in   TaskType
		want bool
	}{
		{TaskTypeDirectPing, true},
		{TaskTypeGatewayPing, true},
		{TaskTypeChatMinimal, true},
		{TaskTypeChatTool, true},
		{TaskTypeChatStream, true},
		{TaskTypeHTTPPing, true},
		{"unknown", false},
		{"", false},
	}
	for _, c := range cases {
		if got := c.in.Valid(); got != c.want {
			t.Errorf("TaskType(%q).Valid() = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTaskType_Priority(t *testing.T) {
	cases := []struct {
		in   TaskType
		want int16
	}{
		{TaskTypeHTTPPing, 90},
		{TaskTypeChatMinimal, 60},
		{TaskTypeDirectPing, 50},
		{TaskTypeGatewayPing, 40},
		{TaskTypeChatTool, 30},
		{TaskTypeChatStream, 20},
	}
	for _, c := range cases {
		if got := c.in.Priority(); got != c.want {
			t.Errorf("TaskType(%q).Priority() = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAutomaticity_Valid(t *testing.T) {
	cases := []struct {
		in   Automaticity
		want bool
	}{
		{AutomaticityMandatory, true},
		{AutomaticityAutomatic, true},
		{"periodic", false},
		{"", false},
	}
	for _, c := range cases {
		if got := c.in.Valid(); got != c.want {
			t.Errorf("Automaticity(%q).Valid() = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTaskStatus_IsTerminal(t *testing.T) {
	terminal := []TaskStatus{
		TaskStatusSuccess, TaskStatusFailed, TaskStatusExpired,
		TaskStatusSkipped, TaskStatusTimeout, TaskStatusNetworkError,
	}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("TaskStatus(%q).IsTerminal() = false, want true", s)
		}
	}
	notTerminal := []TaskStatus{TaskStatusReady, TaskStatusRunning}
	for _, s := range notTerminal {
		if s.IsTerminal() {
			t.Errorf("TaskStatus(%q).IsTerminal() = true, want false", s)
		}
	}
}

func TestTask_Validate(t *testing.T) {
	base := func() *Task {
		return &Task{
			TaskType:     TaskTypeDirectPing,
			Automaticity: AutomaticityMandatory,
			CredentialID: 11,
			RawModel:     "glm-5.2",
		}
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("baseline Validate: %v", err)
	}

	// Bad TaskType
	bad := base()
	bad.TaskType = "weird"
	if err := bad.Validate(); err == nil {
		t.Error("expected error for invalid task_type")
	}

	// Bad Automaticity
	bad = base()
	bad.Automaticity = "periodic"
	if err := bad.Validate(); err == nil {
		t.Error("expected error for invalid automaticity")
	}

	// Bad CredentialID
	bad = base()
	bad.CredentialID = 0
	if err := bad.Validate(); err == nil {
		t.Error("expected error for credential_id=0")
	}

	// Empty RawModel
	bad = base()
	bad.RawModel = ""
	if err := bad.Validate(); err == nil {
		t.Error("expected error for empty raw_model")
	}

	// Default MaxAttempts set automatically
	t1 := base()
	if err := t1.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if t1.MaxAttempts != 3 {
		t.Errorf("mandatory default max_attempts = %d, want 3", t1.MaxAttempts)
	}

	// automatic → max_attempts = 1
	t2 := base()
	t2.Automaticity = AutomaticityAutomatic
	if err := t2.Validate(); err != nil {
		t.Fatalf("validate auto: %v", err)
	}
	if t2.MaxAttempts != 1 {
		t.Errorf("automatic default max_attempts = %d, want 1", t2.MaxAttempts)
	}
}

func TestTask_Keys(t *testing.T) {
	task := &Task{
		ID:           42,
		CredentialID: 11,
		RawModel:     "glm-5.2",
	}
	if got, want := task.HashKey(), "llmgw:monitor:tasks:42"; got != want {
		t.Errorf("HashKey() = %q, want %q", got, want)
	}
	if got, want := task.InflightKey(), "llmgw:monitor:inflight:11:glm-5.2"; got != want {
		t.Errorf("InflightKey() = %q, want %q", got, want)
	}
	if got, want := task.RecentSuccessKey(), "llmgw:monitor:node:recent_success:11:glm-5.2"; got != want {
		t.Errorf("RecentSuccessKey() = %q, want %q", got, want)
	}
}

func TestMarshalTaskForLua_TimesAsMs(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	task := &Task{
		ID:           42,
		TaskType:     TaskTypeDirectPing,
		Automaticity: AutomaticityMandatory,
		Source:       SourceNodeProbe,
		CredentialID: 11,
		ProviderID:   587,
		RawModel:     "glm-5.2",
		EnqueuedAt:   now,
		ScheduledAt:  now,
		NextRunAt:    now,
		Attempt:      1,
		MaxAttempts:  3,
	}
	out, err := marshalTaskForLua(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// 必须包含 scheduled_at_ms 这种 number 字段
	for _, key := range []string{
		"\"id\":42",
		"\"task_type\":\"direct_ping\"",
		"\"priority\":50",
	} {
		if !contains(out, key) {
			t.Errorf("expected %q in output, got %s", key, out)
		}
	}
	// ms 时间戳存在性（不校验精确值，依赖测试运行环境时钟）
	for _, key := range []string{
		"\"scheduled_at_ms\":",
		"\"enqueued_at_ms\":",
		"\"next_run_at_ms\":",
	} {
		if !contains(out, key) {
			t.Errorf("expected %q in output, got %s", key, out)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
