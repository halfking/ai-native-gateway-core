package telemetry

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRequestLogger_Enabled(t *testing.T) {
	rl := &RequestLogger{
		config: &RequestLoggerConfig{Enabled: true},
		db:    nil,
	}
	assert.True(t, rl.Enabled())

	rl = &RequestLogger{
		config: nil,
		db:    nil,
	}
	assert.False(t, rl.Enabled())

	rl = &RequestLogger{
		config: &RequestLoggerConfig{Enabled: false},
		db:    nil,
	}
	assert.False(t, rl.Enabled())
}

func TestRequestLogger_CreateInitial_NoDB(t *testing.T) {
	rl := &RequestLogger{
		config: &RequestLoggerConfig{Enabled: true},
		db:    nil,
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
		config:    &RequestLoggerConfig{Enabled: true},
		db:        nil,
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
		db:    nil,
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
		config:    &RequestLoggerConfig{Enabled: true, QueueSize: 1},
		db:        nil,
		asyncQueue: make(chan *LogUpdate, 1),
		done:      make(chan struct{}),
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
		config:    &RequestLoggerConfig{Enabled: true},
		db:        nil,
		asyncQueue: queue,
		done:      make(chan struct{}),
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
		db:    nil,
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
		config:    &RequestLoggerConfig{Enabled: true, QueueSize: 10},
		db:        nil,
		asyncQueue: queue,
		done:      make(chan struct{}),
		wg:        sync.WaitGroup{},
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
