package telemetry

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// timeArg 是 pgxmock 的自定义参数匹配器：按**时刻**比较 time.Time。
//
// ★ 为什么不用 reflect.DeepEqual（pgxmock 的默认比较）：它会连 location 指针、
//
//	单调时钟读数一起比 ⇒ 同一个时刻经 JSON 往返（UTC 化）之后就被判为不等
//	⇒ 假红。判据要问的是「是不是那个时刻」，不是「是不是同一个 struct 位模式」。
type timeArg struct{ want time.Time }

func (a timeArg) Match(v interface{}) bool {
	t, ok := v.(time.Time)
	return ok && t.Equal(a.want)
}

// TestReplayFallback_InitialUsesProvisionalMergeAndTerminalGuard 覆盖三件事：
// ① 回放一条 initial 记录 ⇒ 走 upsertInitial（不是 persistUpdate）；
// ② 载荷里的 Provisional=true ⇒ 合并时不覆盖库里的 gw_session_id（$7）；
// ③ 终态守卫用的是「status <> 'pending'」而不是老的
//
//	「status IN ('success','failure')」—— 后者会让 rate_limited /
//	cancelled / unknown 被后续更新**回退**成 pending。
//
// ★★ 2026-11-25：本用例曾长期与生产契约脱节 ⇒ 一直红（本轮修）。脱节有两处：
//
//	【1】cdd155fff 把 upsertInitial 的 INSERT 从 7 个参数扩到 10 个
//	     （新增 $7 provisional / $8 created_at / $9 error / $10 completed_at），
//	     而本用例仍按旧的 7 参断言 ⇒ pgxmock 直接报
//	     "expected 7, but got 10 arguments"。
//	【2】cdd155fff 同时新增了「收单时刻必须有值」的守卫
//	     （"request WAL backup has no receipt timestamp"），
//	     而本用例的载荷与备份记录**都没有时刻** ⇒ 守卫先于 SQL 触发。
//	⇒ 结论：红的不是生产代码，是**用例停留在旧契约**；本轮按当前契约重写。
func TestReplayFallback_InitialUsesProvisionalMergeAndTerminalGuard(t *testing.T) {
	mockDB, err := pgxmock.NewConn()
	require.NoError(t, err)
	defer mockDB.Close(context.Background())

	receivedAt := time.Date(2026, 11, 25, 4, 0, 0, 0, time.UTC)

	// ★ 判据锚在 SQL 的**终态守卫片段**上（(?s) 让 . 能跨行）：
	//   只锚 "INSERT INTO request_wal_hot" 的话，这条用例名里的
	//   "TerminalGuard" 就是一句没人验证的承诺。
	mockDB.ExpectExec(`(?s)INSERT INTO request_wal_hot.*WHEN request_wal_hot\.status <> 'pending'`).
		WithArgs(
			"req-replay",              // $1 request_id
			"default",                 // $2 tenant_id
			"gw_provisional",          // $3 gw_session_id
			StatusPending,             // $4 status
			StageReceived,             // $5 stage
			"",                        // $6 client_model
			true,                      // $7 provisional ⇒ 合并时保留库里的会话
			timeArg{want: receivedAt}, // $8 created_at ＝ 收单时刻（不是 NOW()）
			(*string)(nil),            // $9 error
			(*time.Time)(nil),         // $10 completed_at
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	rl := &RequestLogger{
		config: &RequestLoggerConfig{Enabled: true},
		db:     mockDB,
	}
	payload, err := json.Marshal(&InitialRequest{
		RequestID:   "req-replay",
		TenantID:    "default",
		SessionID:   "gw_provisional",
		Provisional: true,
		ReceivedAt:  receivedAt,
	})
	require.NoError(t, err)
	err = rl.ReplayFallback(context.Background(), dbdegradation.BackupRecord{
		RecordKey: "req-replay:initial",
		Payload:   payload,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

// TestReplayFallback_InitialFallsBackToRecordTimestamp 钉住「老备份没有
// ReceivedAt 时的回落口径」：用**记录时间**（BackupRecord.Timestamp）兜底。
//
// ★ 这条回落是生产代码里明写的（"Backups written before ReceivedAt used the
//
//	file record time"），但此前**没有任何用例覆盖** ⇒ 回落到零值时会把一条
//	本该写进库的记录变成「没有结论」，而没人看得见。
func TestReplayFallback_InitialFallsBackToRecordTimestamp(t *testing.T) {
	mockDB, err := pgxmock.NewConn()
	require.NoError(t, err)
	defer mockDB.Close(context.Background())

	recordTime := time.Date(2026, 11, 24, 22, 15, 0, 0, time.UTC)

	mockDB.ExpectExec(`INSERT INTO request_wal_hot`).
		WithArgs(
			"req-replay",              // $1 request_id
			"default",                 // $2 tenant_id
			"gw-session",              // $3 gw_session_id
			StatusPending,             // $4 status
			StageReceived,             // $5 stage
			"",                        // $6 client_model
			false,                     // $7 provisional
			timeArg{want: recordTime}, // $8 created_at ＝ 记录时间（回落口径）
			(*string)(nil),            // $9 error
			(*time.Time)(nil),         // $10 completed_at
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	rl := &RequestLogger{
		config: &RequestLoggerConfig{Enabled: true},
		db:     mockDB,
	}
	// 载荷里**没有** ReceivedAt（老版本备份的形态）
	payload, err := json.Marshal(&InitialRequest{
		RequestID: "req-replay",
		TenantID:  "default",
		SessionID: "gw-session",
	})
	require.NoError(t, err)
	err = rl.ReplayFallback(context.Background(), dbdegradation.BackupRecord{
		RecordKey: "req-replay:initial",
		Timestamp: recordTime,
		Payload:   payload,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

// TestReplayFallback_InitialWithoutReceiptTimestampIsRejected 钉住新守卫：
// 载荷与记录**都没有**时刻 ⇒ created_at 无从写起 ⇒ 整条不写、报明确错误。
//
// ★ 「不写」比「写个占位时刻」更安全：request_wal_hot.created_at 是收单时刻，
//
//	写 NOW() 会把「这条记录何时被网关收到」篡改成「何时被回放」。
//
// ★ 断言「没有落库」用 mock 的零期望实现：任何一次 Exec 都会让
//
//	pgxmock 返回未预期调用的错误 ⇒ 错误文案会与断言不符 ⇒ 用例转红。
func TestReplayFallback_InitialWithoutReceiptTimestampIsRejected(t *testing.T) {
	mockDB, err := pgxmock.NewConn()
	require.NoError(t, err)
	defer mockDB.Close(context.Background())

	rl := &RequestLogger{
		config: &RequestLoggerConfig{Enabled: true},
		db:     mockDB,
	}
	payload, err := json.Marshal(&InitialRequest{
		RequestID: "req-replay",
		TenantID:  "default",
		SessionID: "gw-session",
	})
	require.NoError(t, err)

	err = rl.ReplayFallback(context.Background(), dbdegradation.BackupRecord{
		RecordKey: "req-replay:initial",
		Payload:   payload,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no receipt timestamp")
	// 零期望 ⇒ 任何一次落库都会在这里暴露
	require.NoError(t, mockDB.ExpectationsWereMet())

	stats := rl.OverflowCounts()
	assert.Equal(t, uint64(1), stats.ReplayAttempt)
	assert.Equal(t, uint64(1), stats.ReplayFailure)
	assert.Equal(t, uint64(0), stats.ReplaySuccess)
}

func TestRequestLogger_QueueOverflowWritesMarkerAndStats(t *testing.T) {
	fallback := newStubBackupWriter()
	rl := &RequestLogger{
		config:     &RequestLoggerConfig{Enabled: true},
		asyncQueue: make(chan *LogUpdate, 1),
		fallback:   fallback,
		done:       make(chan struct{}),
	}
	rl.Update(&LogUpdate{RequestID: "req-1", Stage: StageCompressed})
	rl.Update(&LogUpdate{RequestID: "req-2", Stage: StageTransformed})

	stats := rl.OverflowCounts()
	assert.Equal(t, uint64(1), stats.QueueOverflow)
	assert.Equal(t, []string{"req-2:update"}, fallback.seen())
}

type recordedFallback struct {
	key     string
	payload []byte
}

type failingBackupWriter struct {
	calls []recordedFallback
}

func (w *failingBackupWriter) WriteRequestLog(context.Context, string, any) error { return nil }

func (w *failingBackupWriter) WriteRequestWAL(_ context.Context, key string, payload any) error {
	encoded, _ := json.Marshal(payload)
	w.calls = append(w.calls, recordedFallback{key: key, payload: encoded})
	if len(w.calls) == 1 {
		return assert.AnError
	}
	return nil
}

func (w *failingBackupWriter) records() []recordedFallback { return w.calls }

func TestRequestLogger_QueueOverflowWritesOriginalUpdateBeforeMarker(t *testing.T) {
	fallback := newPayloadBackupWriter()
	rl := &RequestLogger{
		config:     &RequestLoggerConfig{Enabled: true},
		asyncQueue: make(chan *LogUpdate, 1),
		fallback:   fallback,
		done:       make(chan struct{}),
	}
	first := &LogUpdate{RequestID: "req-1", Stage: StageCompressed, Status: StatusPending}
	second := &LogUpdate{RequestID: "req-2", Stage: StageTransformed, Status: StatusPending, OutboundBody: []byte("must-not-be-in-marker")}
	rl.Update(first)
	rl.Update(second)

	stats := rl.OverflowCounts()
	assert.Equal(t, uint64(1), stats.QueueOverflow)
	assert.Equal(t, []string{"req-2:update"}, fallback.keys())
	assert.Equal(t, second, fallback.payload("req-2:update"))
	assert.Empty(t, fallback.keysWithPrefix("request_logger:overflow:"))
}

func TestRequestLogger_QueueOverflowFallbackFailureWritesSafeMarker(t *testing.T) {
	fallback := &failingBackupWriter{}
	rl := &RequestLogger{
		config:     &RequestLoggerConfig{Enabled: true},
		asyncQueue: make(chan *LogUpdate, 1),
		fallback:   fallback,
		done:       make(chan struct{}),
	}
	rl.Update(&LogUpdate{RequestID: "req-occupied", Stage: StageCompressed, Status: StatusPending})
	rl.Update(&LogUpdate{RequestID: "req-safe", Stage: StageTransformed, Status: StatusPending, OutboundBody: []byte("secret-body")})
	records := fallback.records()
	require.Len(t, records, 2)
	assert.Equal(t, "req-safe:update", records[0].key)
	assert.Equal(t, "request_logger:overflow:req-safe", records[1].key)
	assert.NotContains(t, string(records[1].payload), "secret-body")
	assert.Contains(t, string(records[1].payload), "request_id")
	assert.Contains(t, string(records[1].payload), "stage")
	assert.Contains(t, string(records[1].payload), "status")
	assert.Contains(t, string(records[1].payload), "reason")
}

func TestRequestLogger_ReplayFallback_OverflowMarkerIsNoOp(t *testing.T) {
	rl := &RequestLogger{config: &RequestLoggerConfig{Enabled: true}}
	payload, err := json.Marshal(map[string]any{
		"kind":       "request_logger_overflow",
		"request_id": "req-marker",
		"stage":      StageTransformed,
		"status":     StatusFailure,
		"reason":     "queue_full",
	})
	require.NoError(t, err)

	err = rl.ReplayFallback(context.Background(), dbdegradation.BackupRecord{
		RecordKey: "request_logger:overflow:req-marker",
		Payload:   payload,
	})
	require.NoError(t, err)
	stats := rl.OverflowCounts()
	assert.Equal(t, uint64(1), stats.ReplayAttempt)
	assert.Equal(t, uint64(1), stats.ReplaySuccess)
	assert.Equal(t, uint64(1), stats.ReplayMarker)
	assert.Equal(t, uint64(0), stats.ReplayFailure)
}

func TestRequestLogger_StopIsIdempotentAndDrainsQueue(t *testing.T) {
	mockDB, err := pgxmock.NewConn()
	require.NoError(t, err)
	defer mockDB.Close(context.Background())
	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE request_wal_hot SET`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectCommit()
	rl := &RequestLogger{
		config:     &RequestLoggerConfig{Enabled: true, BatchSize: 10, FlushTimeout: time.Hour},
		asyncQueue: make(chan *LogUpdate, 2),
		done:       make(chan struct{}),
		db:         mockDB,
	}
	rl.asyncQueue <- &LogUpdate{RequestID: "req-drain", Stage: StageCompressed}
	rl.wg.Add(1)
	go rl.worker()
	rl.Stop()
	rl.Stop()

	require.NoError(t, mockDB.ExpectationsWereMet())
}

func TestRequestLogger_ReplayFallbackCountsFailure(t *testing.T) {
	rl := &RequestLogger{config: &RequestLoggerConfig{Enabled: true}}
	err := rl.ReplayFallback(context.Background(), dbdegradation.BackupRecord{
		RecordKey: "req-replay:update",
		Payload:   []byte(`{"RequestID":"req-replay"}`),
	})
	require.Error(t, err)
	stats := rl.OverflowCounts()
	assert.Equal(t, uint64(1), stats.ReplayAttempt)
	assert.Equal(t, uint64(1), stats.ReplayFailure)
}

func TestRequestLogger_Enabled(t *testing.T) {
	rl := &RequestLogger{
		config: &RequestLoggerConfig{Enabled: true},
		db:     nil,
	}
	assert.True(t, rl.Enabled())

	rl = &RequestLogger{
		config: nil,
		db:     nil,
	}
	assert.False(t, rl.Enabled())

	rl = &RequestLogger{
		config: &RequestLoggerConfig{Enabled: false},
		db:     nil,
	}
	assert.False(t, rl.Enabled())
}

func TestRequestLogger_CreateInitial_NoDB(t *testing.T) {
	rl := &RequestLogger{
		config: &RequestLoggerConfig{Enabled: true},
		db:     nil,
	}
	err := rl.CreateInitial(context.Background(), &InitialRequest{
		RequestID:   "test-123",
		TenantID:    "test-tenant",
		SessionID:   "gw_session_abc",
		ClientModel: "gpt-4",
	})
	assert.NoError(t, err)
}

func TestRequestLogger_Update_NoDB(t *testing.T) {
	rl := &RequestLogger{
		config:     &RequestLoggerConfig{Enabled: true},
		db:         nil,
		asyncQueue: make(chan *LogUpdate, 100),
	}
	rl.Update(&LogUpdate{
		RequestID: "test-123",
		Stage:     StageCompressed,
		Status:    StatusPending,
	})
}

func TestRequestLogger_UpdateSync_NoDB(t *testing.T) {
	rl := &RequestLogger{
		config: &RequestLoggerConfig{Enabled: true},
		db:     nil,
	}
	err := rl.UpdateSync(context.Background(), &LogUpdate{
		RequestID: "test-123",
		Stage:     StageCompressed,
		Status:    StatusPending,
	})
	assert.NoError(t, err)
}

func TestRequestLogger_Update_NonBlocking(t *testing.T) {
	rl := &RequestLogger{
		config:     &RequestLoggerConfig{Enabled: true, QueueSize: 1},
		db:         nil,
		asyncQueue: make(chan *LogUpdate, 1),
		done:       make(chan struct{}),
	}
	rl.Update(&LogUpdate{RequestID: "test-1", Stage: StageCompressed})
	rl.Update(&LogUpdate{RequestID: "test-2", Stage: StageCompressed})
}

func TestRequestLogger_UpdateBuilder(t *testing.T) {
	now := time.Now()
	update := NewRequestLogger(nil, nil).NewUpdateBuilder().
		RequestID("req-123").
		Stage(StageCompressed).
		Status(StatusPending).
		Error("").
		CompressionStrategy("delta_append").
		CompressionMeta(map[string]interface{}{"msg_count": 10}).
		CompletionTokens(100).
		PromptTokens(50).
		CompletedAt(now).
		Build()

	assert.Equal(t, "req-123", update.RequestID)
	assert.Equal(t, StageCompressed, update.Stage)
	assert.Equal(t, StatusPending, update.Status)
	assert.Equal(t, "delta_append", update.CompressionStrategy)
	assert.Equal(t, 100, update.CompletionTokens)
	assert.Equal(t, 50, update.PromptTokens)
	assert.Equal(t, now, update.CompletedAt)
}

func TestRequestLogger_UpdateBuilder_LogAsync(t *testing.T) {
	queue := make(chan *LogUpdate, 10)
	rl := &RequestLogger{
		config:     &RequestLoggerConfig{Enabled: true},
		db:         nil,
		asyncQueue: queue,
		done:       make(chan struct{}),
	}
	t.Logf("rl.config.Enabled = %v, queue cap = %d, queue len = %d", rl.config.Enabled, cap(queue), len(queue))
	t.Logf("rl.Enabled() = %v", rl.Enabled())

	rl.NewUpdateBuilder().
		RequestID("req-async").
		Stage(StageCompressed).
		LogAsync(rl)

	t.Logf("After LogAsync: queue len = %d", len(queue))

	select {
	case u := <-queue:
		assert.Equal(t, "req-async", u.RequestID)
		assert.Equal(t, StageCompressed, u.Stage)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for update")
	}
}

func TestRequestLogger_UpdateBuilder_LogSync(t *testing.T) {
	rl := &RequestLogger{
		config: &RequestLoggerConfig{Enabled: true},
		db:     nil,
	}

	err := NewRequestLogger(nil, nil).NewUpdateBuilder().
		RequestID("req-sync").
		Stage(StageCompressed).
		LogSync(context.Background(), rl)

	assert.NoError(t, err)
}

func TestStageConstants(t *testing.T) {
	assert.Equal(t, 0, StageReceived)
	assert.Equal(t, 1, StageCompressed)
	assert.Equal(t, 2, StageTransformed)
	assert.Equal(t, 3, StageExecuting)
	assert.Equal(t, 4, StageCompleted)
	assert.Equal(t, 10, StageCompressFail)
	assert.Equal(t, 11, StageTransformFail)
	assert.Equal(t, 12, StageExecuteFail)
	assert.Equal(t, 13, StageResponseFail)
}

func TestStatusConstants(t *testing.T) {
	assert.Equal(t, "pending", StatusPending)
	assert.Equal(t, "success", StatusSuccess)
	assert.Equal(t, "failure", StatusFailure)
}

func TestRequestLogger_NewRequestLogger_Defaults(t *testing.T) {
	rl := NewRequestLogger(nil, nil)
	assert.NotNil(t, rl)
	assert.NotNil(t, rl.config)
	assert.Equal(t, 10000, rl.config.QueueSize)
	assert.Equal(t, 50, rl.config.BatchSize)
	assert.Equal(t, 100*time.Millisecond, rl.config.FlushTimeout)
	assert.True(t, rl.config.Enabled)
}

func TestRequestLogger_NewRequestLogger_CustomConfig(t *testing.T) {
	cfg := &RequestLoggerConfig{
		QueueSize:    5000,
		BatchSize:    25,
		FlushTimeout: 200 * time.Millisecond,
		Enabled:      false,
	}
	rl := NewRequestLogger(nil, cfg)
	assert.NotNil(t, rl)
	assert.Equal(t, 5000, rl.config.QueueSize)
	assert.Equal(t, 25, rl.config.BatchSize)
	assert.Equal(t, 200*time.Millisecond, rl.config.FlushTimeout)
	assert.False(t, rl.config.Enabled)
}

func TestRequestLogger_Stop(t *testing.T) {
	queue := make(chan *LogUpdate, 10)
	rl := &RequestLogger{
		config:     &RequestLoggerConfig{Enabled: true, QueueSize: 10},
		db:         nil,
		asyncQueue: queue,
		done:       make(chan struct{}),
		wg:         sync.WaitGroup{},
	}
	rl.wg.Add(1)
	go rl.worker()
	rl.Stop()
}

func TestUpdateBuilder_AllFields(t *testing.T) {
	pid := int64(123)
	cid := int64(456)
	now := time.Now()

	update := NewRequestLogger(nil, nil).NewUpdateBuilder().
		RequestID("req-full").
		Stage(StageExecuting).
		Status(StatusSuccess).
		Error("").
		OutboundBody([]byte("test body")).
		CompressionStrategy("sliding_window").
		CompressionMeta(map[string]interface{}{"key": "value"}).
		CompletionTokens(200).
		PromptTokens(100).
		CompletedAt(now).
		UpstreamRequestAt(now).
		UpstreamResponseAt(now).
		UpstreamProviderID(&pid).
		UpstreamCredentialID(&cid).
		Build()

	assert.Equal(t, "req-full", update.RequestID)
	assert.Equal(t, StageExecuting, update.Stage)
	assert.Equal(t, StatusSuccess, update.Status)
	assert.Equal(t, []byte("test body"), update.OutboundBody)
	assert.Equal(t, "sliding_window", update.CompressionStrategy)
	assert.Equal(t, map[string]interface{}{"key": "value"}, update.CompressionMeta)
	assert.Equal(t, 200, update.CompletionTokens)
	assert.Equal(t, 100, update.PromptTokens)
	assert.Equal(t, now, update.CompletedAt)
	assert.Equal(t, now, update.UpstreamRequestAt)
	assert.Equal(t, now, update.UpstreamResponseAt)
	assert.Equal(t, &pid, update.UpstreamProviderID)
	assert.Equal(t, &cid, update.UpstreamCredentialID)
}
