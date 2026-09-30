package credential

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// collectedObserver 收集迁移事件供断言。观察者契约要求非阻塞——这里只做
// append，与生产接线（原子计数 + buffered channel send）同级。
func collectedObserver(out *[]StateChange) func(StateChange) {
	return func(sc StateChange) { *out = append(*out, sc) }
}

// TestBreakerNotifiesTransitionsToObserver 钉住 §三#4 的核心契约：真实迁移
// （closed→open / open→half_open→closed）逐一通知观察者，带正确的
// from/to/Kind 与 OPEN 态的 CoolingUntil；阈值以下的失败不迁移也不通知。
func TestBreakerNotifiesTransitionsToObserver(t *testing.T) {
	var events []StateChange
	m := NewManager()
	m.SetObserver(collectedObserver(&events))

	// 阈值 2：两次 KindNetwork 确认失败 → closed→open，带冷却截止。
	m.RecordFailure(1, 9, KindNetwork)
	if len(events) != 0 {
		t.Fatalf("below-threshold failure must not transition, got %v", events)
	}
	m.RecordFailure(1, 9, KindNetwork)
	if len(events) != 1 {
		t.Fatalf("expected 1 open event, got %d: %v", len(events), events)
	}
	openEv := events[0]
	if openEv.From != StateClosed || openEv.To != StateOpen {
		t.Fatalf("open event from/to = %v→%v, want closed→open", openEv.From, openEv.To)
	}
	if openEv.Kind != KindNetwork || openEv.CredentialID != 9 || openEv.ProviderID != 1 {
		t.Fatalf("open event identity/kind = %+v", openEv)
	}
	if openEv.CoolingUntil.IsZero() || !openEv.CoolingUntil.After(time.Now()) {
		t.Fatalf("open event CoolingUntil must be in the future, got %v", openEv.CoolingUntil)
	}

	// 探测恢复：冷却截止置为过去 → Allow() 完成 open→half_open 迁移。
	b := m.Get(1, 9)
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-time.Second)
	b.mu.Unlock()
	if !b.Allow() {
		t.Fatal("expected probe admission after cooling expiry")
	}
	if len(events) != 2 {
		t.Fatalf("expected half-open event, got %d: %v", len(events), events)
	}
	if events[1].From != StateOpen || events[1].To != StateHalfOpen {
		t.Fatalf("half-open event = %v→%v, want open→half_open", events[1].From, events[1].To)
	}
	if !events[1].CoolingUntil.IsZero() {
		t.Fatalf("half-open CoolingUntil must be zero, got %v", events[1].CoolingUntil)
	}

	// 探针成功 → closed。
	b.RecordSuccess()
	if len(events) != 3 || events[2].From != StateHalfOpen || events[2].To != StateClosed {
		t.Fatalf("expected half_open→closed event, got %v", events)
	}
}

// TestBreakerNotifiesQuarantineAndReset 覆盖 QUARANTINED 迁移与 admin Reset
// 的观察者上报（Reset 是 force_enable/clear_circuit 的路径）。
func TestBreakerNotifiesQuarantineAndReset(t *testing.T) {
	var events []StateChange
	m := NewManager()
	m.SetObserver(collectedObserver(&events))

	m.RecordFailureWithBillingMode(2, 8, KindQuota, "per_token")
	m.RecordFailureWithBillingMode(2, 8, KindQuota, "per_token")
	if len(events) != 1 || events[0].To != StateQuarantined {
		t.Fatalf("expected closed→quarantined, got %v", events)
	}
	if events[0].From != StateClosed {
		t.Fatalf("quarantine from = %v, want closed", events[0].From)
	}

	m.Reset(2, 8)
	if len(events) != 2 || events[1].From != StateQuarantined || events[1].To != StateClosed {
		t.Fatalf("expected quarantined→closed on reset, got %v", events)
	}
}

// TestBreakerOpenReentryNotifiesCoolingExtension 钉住 OPEN 原地重入：已开路
// 再吃满阈值失败会顺延冷却窗口，DB 侧 cooling_until 必须收到刷新事件，
// 否则列里会停留第一次开路的过期时间。
func TestBreakerOpenReentryNotifiesCoolingExtension(t *testing.T) {
	var events []StateChange
	m := NewManager()
	m.SetObserver(collectedObserver(&events))

	m.RecordFailure(3, 7, KindNetwork)
	m.RecordFailure(3, 7, KindNetwork) // → open #1
	m.RecordFailure(3, 7, KindNetwork) // 已开路：重入顺延
	m.RecordFailure(3, 7, KindNetwork) // 阈值再次满足
	var openEvents []StateChange
	for _, ev := range events {
		if ev.To == StateOpen {
			openEvents = append(openEvents, ev)
		}
	}
	if len(openEvents) < 2 {
		t.Fatalf("open re-entry must notify again, got %d open events of %v", len(openEvents), events)
	}
	first, last := openEvents[0], openEvents[len(openEvents)-1]
	if !last.CoolingUntil.After(first.CoolingUntil) {
		t.Fatalf("re-entry must extend cooling: first=%v last=%v", first.CoolingUntil, last.CoolingUntil)
	}
}

// TestSetObserverRetrofitsExistingBreakers：观察者在 breaker 已创建之后装配
// 也要生效（启动期接线可能晚于首个 GetOrCreate）。
func TestSetObserverRetrofitsExistingBreakers(t *testing.T) {
	var events []StateChange
	m := NewManager()
	m.RecordFailure(4, 6, KindQuota) // 触发 GetOrCreate，此时无观察者
	m.RecordFailure(4, 6, KindQuota) // → quarantined（事件丢弃，无观察者）
	if len(events) != 0 {
		t.Fatalf("no observer yet, got %v", events)
	}
	m.SetObserver(collectedObserver(&events))
	m.Reset(4, 6)
	if len(events) != 1 || events[0].From != StateQuarantined || events[0].To != StateClosed {
		t.Fatalf("retrofitted observer must see reset event, got %v", events)
	}
}

// TestDBStateSyncMirrorsTransitions 用 pgxmock 钉住 DB 面的四种 SQL 形态：
// open（写 opened_at+cooling_until）/ half_open（保 opened_at 清 cooling）/
// closed（全清）/ quarantined（映射 'open'）。
func TestDBStateSyncMirrorsTransitions(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		event   StateChange
		wantSQL string
		wantID  int
	}{
		{
			name: "open writes opened_at and cooling_until",
			event: StateChange{
				ProviderID: 1, CredentialID: 11,
				From: StateClosed, To: StateOpen,
				Kind: KindNetwork, CoolingUntil: now.Add(time.Minute),
			},
			wantSQL: `circuit_state\s*=\s*\$1,\s*circuit_opened_at\s*=\s*\$2,\s*cooling_until\s*=\s*\$3\s*WHERE id\s*=\s*\$4`,
			wantID:  11,
		},
		{
			name:    "half_open keeps opened_at and clears cooling",
			event:   StateChange{ProviderID: 1, CredentialID: 12, From: StateOpen, To: StateHalfOpen, Kind: KindNetwork},
			wantSQL: `circuit_state\s*=\s*'half_open',\s*cooling_until\s*=\s*NULL\s*WHERE id\s*=\s*\$1`,
			wantID:  12,
		},
		{
			name:    "closed clears opened_at and cooling_until",
			event:   StateChange{ProviderID: 1, CredentialID: 13, From: StateOpen, To: StateClosed, Kind: KindNetwork},
			wantSQL: `circuit_state\s*=\s*\$1,\s*circuit_opened_at\s*=\s*\$2,\s*cooling_until\s*=\s*\$3\s*WHERE id\s*=\s*\$4`,
			wantID:  13,
		},
		{
			name:    "quarantined maps to open",
			event:   StateChange{ProviderID: 1, CredentialID: 14, From: StateOpen, To: StateQuarantined, Kind: KindQuota},
			wantSQL: `circuit_state\s*=\s*\$1,\s*circuit_opened_at\s*=\s*\$2,\s*cooling_until\s*=\s*\$3\s*WHERE id\s*=\s*\$4`,
			wantID:  14,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockDB, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mockDB.Close()
			exec := mockDB.ExpectExec(tc.wantSQL)
			if tc.event.To == StateHalfOpen {
				exec = exec.WithArgs(tc.wantID)
			} else {
				exec = exec.WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), tc.wantID)
			}
			exec.WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			s := NewDBStateSync(mockDB, 8)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go s.Run(ctx)

			s.Observe(tc.event)
			deadline := time.After(2 * time.Second)
			for mockDB.ExpectationsWereMet() != nil {
				select {
				case <-deadline:
					t.Fatalf("sync SQL not observed for %+v", tc.event)
				case <-time.After(5 * time.Millisecond):
				}
			}
		})
	}
}

// TestDBStateSyncDropsOnFullQueue：队列满时 Observe 必须丢弃并计数，不得
// 阻塞热路径上的观察者调用。
func TestDBStateSyncDropsOnFullQueue(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	s := NewDBStateSync(mockDB, 2)
	ev := StateChange{ProviderID: 1, CredentialID: 1, From: StateClosed, To: StateOpen, Kind: KindNetwork}
	for i := 0; i < 10; i++ {
		s.Observe(ev) // 无消费者：前 2 条入队，后 8 条丢弃
	}
	if got := s.Dropped(); got != 8 {
		t.Fatalf("Dropped = %d, want 8", got)
	}
}
