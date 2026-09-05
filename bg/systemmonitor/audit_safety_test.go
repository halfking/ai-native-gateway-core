package systemmonitor

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/secret"
)

func TestAuditWriteAcceptsNilExtrasWithTokenCount(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	task := &Task{
		ID:           1,
		TaskType:     TaskTypeDirectPing,
		Automaticity: AutomaticityMandatory,
		Source:       SourceNodeProbe,
		CredentialID: 2,
		RawModel:     "model",
		Status:       TaskStatusSuccess,
		Attempt:      1,
		MaxAttempts:  1,
		TokenCount:   9,
	}
	mock.ExpectExec("INSERT INTO system_probe_runs").
		WithArgs(
			int64(1), string(TaskTypeDirectPing), string(AutomaticityMandatory), int64(2), int64(0), "model",
			string(SourceNodeProbe), "", string(TaskStatusSuccess), 1, 1,
			0, 0, 9,
			pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(),
		).WillReturnResult(pgxmock.NewResult("INSERT", 1))

	audit := &Audit{db: mock}
	if err := audit.Write(context.Background(), task, nil, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestClassifyResultNilResultWithoutError(t *testing.T) {
	sm := &SystemMonitor{}
	status, extras := sm.classifyResult(&Task{}, nil, nil)
	if status != TaskStatusFailed {
		t.Fatalf("status = %q, want %q", status, TaskStatusFailed)
	}
	if got := extras["err_code"]; got != "executor_nil_result" {
		t.Fatalf("err_code = %v, want executor_nil_result", got)
	}
	if got := extras["err_detail"]; got != "executor returned nil result" {
		t.Fatalf("err_detail = %v, want stable nil-result detail", got)
	}
}

func TestClassifyResultNilResultPreservesError(t *testing.T) {
	sm := &SystemMonitor{}
	status, extras := sm.classifyResult(&Task{}, nil, errors.New("executor failed"))
	if status != TaskStatusFailed {
		t.Fatalf("status = %q, want %q", status, TaskStatusFailed)
	}
	if got := extras["err_detail"]; got != "executor failed" {
		t.Fatalf("err_detail = %v, want executor error", got)
	}
}

func TestNewSystemMonitorPassesKeyringToExecutor(t *testing.T) {
	var key [32]byte
	key[0] = 1
	keyring, err := secret.NewKeyring(map[string][32]byte{"test": key}, "test")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	sm, err := NewSystemMonitor(Config{Keyring: keyring})
	if err != nil {
		t.Fatalf("NewSystemMonitor: %v", err)
	}
	if sm.executor.keyring != keyring {
		t.Fatal("executor did not receive configured keyring")
	}
	sm.Stop()
}
