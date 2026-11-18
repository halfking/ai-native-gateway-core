package credential

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// TestDBStateSyncRollup 验证 DB 上卷逻辑
// W1 缺口① (2026-11-19): 上卷态应为该凭据所有模型的最差状态
func TestDBStateSyncRollup(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mgr := NewManager()
	sync := NewDBStateSync(mockDB, 8, mgr)

	// 创建两个模型的 breaker
	b1 := mgr.GetOrCreatePerModel(1, 100, "gpt-4")
	b2 := mgr.GetOrCreatePerModel(1, 100, "gpt-3.5-turbo")

	// 场景 1：只有一个模型 Open，上卷态应为 Open
	b1.RecordFailure(KindUpstreamDown)
	b1.RecordFailure(KindUpstreamDown)

	// 预期 DB 写入 "open"
	mockDB.ExpectExec(`UPDATE credentials`).
		WithArgs("open", pgxmock.AnyArg(), pgxmock.AnyArg(), 100).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	sc := StateChange{
		ProviderID:   1,
		CredentialID: 100,
		From:         StateClosed,
		To:           StateOpen,
		Kind:         KindUpstreamDown,
	}
	sync.Observe(sc)

	// 启动消费
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	go sync.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}

	// 场景 2：第二个模型也 Open，上卷态仍为 Open
	mockDB.ExpectExec(`UPDATE credentials`).
		WithArgs("open", pgxmock.AnyArg(), pgxmock.AnyArg(), 100).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	b2.RecordFailure(KindUpstreamDown)
	b2.RecordFailure(KindUpstreamDown)

	sc2 := StateChange{
		ProviderID:   1,
		CredentialID: 100,
		From:         StateClosed,
		To:           StateOpen,
		Kind:         KindUpstreamDown,
	}
	sync.Observe(sc2)
	time.Sleep(100 * time.Millisecond)

	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}

	// 场景 3：一个模型 Quarantined，上卷态应为 Quarantined（存储为 "open"）
	mockDB.ExpectExec(`UPDATE credentials`).
		WithArgs("open", pgxmock.AnyArg(), nil, 100).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	b2.RecordFailure(KindQuota)
	b2.RecordFailure(KindQuota)

	sc3 := StateChange{
		ProviderID:   1,
		CredentialID: 100,
		From:         StateOpen,
		To:           StateQuarantined,
		Kind:         KindQuota,
	}
	sync.Observe(sc3)
	time.Sleep(100 * time.Millisecond)

	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}
}

// TestDBStateSyncWithoutManager 验证向后兼容性
// W1 缺口① (2026-11-19): manager 为 nil 时使用单个 breaker 的状态
func TestDBStateSyncWithoutManager(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	// manager 为 nil（向后兼容模式）
	sync := NewDBStateSync(mockDB, 8, nil)

	// 预期 DB 写入单个 breaker 的状态
	mockDB.ExpectExec(`UPDATE credentials`).
		WithArgs("open", pgxmock.AnyArg(), pgxmock.AnyArg(), 100).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	sc := StateChange{
		ProviderID:   1,
		CredentialID: 100,
		From:         StateClosed,
		To:           StateOpen,
		Kind:         KindUpstreamDown,
	}
	sync.Observe(sc)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	go sync.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}
}
