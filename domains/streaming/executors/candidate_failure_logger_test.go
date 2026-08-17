package executors

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// TestSafeErrorMessage_2026_07_20 exercises the safeErrorMessage helper
// added to defend against the same nil-pointer dereference that crashed
// minimax-m3 chat requests on 2026-07-19/20. Even though
// (*upstream.Error).Error() is now nil-receiver safe, this helper
// makes buildRow robust against ANY error type a future caller might
// pass, and against any future regression in upstream.
func TestSafeErrorMessage_2026_07_20(t *testing.T) {
	t.Run("nil interface returns empty", func(t *testing.T) {
		var err error // nil interface
		assert.Equal(t, "", safeErrorMessage(err))
	})

	t.Run("typed-nil *upstream.Error does not panic", func(t *testing.T) {
		var nilUErr *upstreampkg.Error
		var iface error = nilUErr
		// Sanity: typed-nil wrapped in error interface is non-nil at
		// the interface level (Go's classic gotcha). Note that
		// assert.NotNil uses reflect.ValueOf().IsNil() which sees
		// the underlying nil pointer and reports nil — so we must
		// compare with == nil directly.
		if iface == nil {
			t.Fatalf("typed-nil wrapped in interface must be non-nil; test setup is wrong")
		}

		// The PRIMARY contract is "must not panic". The returned
		// string can be either:
		//   - "<nil upstream.Error>" (when (*Error).Error() is nil-receiver safe), or
		//   - "" (when safeErrorMessage's last-resort recover catches
		//     a panic from a non-safe Error type)
		// Both are acceptable outcomes — what matters is that the
		// caller survives.
		var got string
		assert.NotPanics(t, func() {
			got = safeErrorMessage(iface)
		}, "safeErrorMessage must not panic on typed-nil *upstream.Error")
		_ = got // see comment above; result is implementation-defined
	})

	t.Run("populated error renders normally", func(t *testing.T) {
		err := &upstreampkg.Error{
			Kind:       upstreampkg.KindRateLimit,
			Message:    "rate limited",
			Err:        fmt.Errorf("429"),
			StatusCode: 429,
		}
		assert.Equal(t, "[rate_limit] rate limited: 429", safeErrorMessage(err))
	})

	t.Run("arbitrary error renders normally", func(t *testing.T) {
		err := fmt.Errorf("connection reset by peer")
		assert.Equal(t, "connection reset by peer", safeErrorMessage(err))
	})
}

// TestBuildRow_SessionIDAndExplicitKind (2026-08-17, V358): candidate
// failure rows carry the session id directly, and the stream-interruption
// call path passes its pre-classified kind so "other side closed"-style
// failures are not flattened to the message-fallback transient kind.
func TestBuildRow_SessionIDAndExplicitKind(t *testing.T) {
	w := &CandidateFailureWriter{}

	t.Run("session id lands on the row", func(t *testing.T) {
		row := w.buildRow("req-1", "tenant-1", "sess-1", 7, 9, "glm-5.2", 3,
			fmt.Errorf("stream_interrupted: network_error"), errorsx.KindNetwork, nil, nil, nil)
		assert.Equal(t, "sess-1", row.SessionID)
	})

	t.Run("explicit kind wins over the message fallback", func(t *testing.T) {
		// "stream_interrupted: network_error" classifies as transient by
		// message — the executor's pre-classified KindNetwork must win.
		row := w.buildRow("req-1", "tenant-1", "sess-1", 7, 9, "glm-5.2", 3,
			fmt.Errorf("stream_interrupted: network_error"), errorsx.KindNetwork, nil, nil, nil)
		assert.Equal(t, "network", row.ErrorKind)
		require.NotNil(t, row.Retryable)
		assert.True(t, *row.Retryable)
	})

	t.Run("empty explicit kind keeps message classification", func(t *testing.T) {
		row := w.buildRow("req-2", "tenant-1", "", 1, 2, "m", 0,
			fmt.Errorf("connection reset by peer"), "", nil, nil, nil)
		assert.Equal(t, "", row.SessionID)
		assert.Equal(t, "network", row.ErrorKind)
	})

	t.Run("upstream error kind is preserved without explicit kind", func(t *testing.T) {
		ue := &upstreampkg.Error{Kind: upstreampkg.KindRateLimit, Message: "limited", Err: fmt.Errorf("429"), StatusCode: 429}
		row := w.buildRow("req-3", "tenant-1", "sess-3", 1, 2, "m", 0, ue, "", nil, nil, nil)
		assert.Equal(t, "rate_limit", row.ErrorKind)
	})
}
