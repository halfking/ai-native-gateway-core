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

func TestReplayFallback_InitialUsesProvisionalMergeAndTerminalGuard(t *testing.T) {
	mockDB, err := pgxmock.NewConn()
	require.NoError(t, err)
	defer mockDB.Close(context.Background())

	mockDB.ExpectExec(`INSERT INTO request_wal_hot`).
		WithArgs("req-replay", "default", "gw_provisional", StatusPending, StageReceived, "", true).
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
	})
	require.NoError(t, err)
	err = rl.ReplayFallback(context.Background(), dbdegradation.BackupRecord{
		RecordKey: "req-replay:initial",
		Payload:   payload,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
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
