package response

import (
	"context"
	"log/slog"
)

// InterceptorChain chains multiple ResponseInterceptors together.
type InterceptorChain struct {
	interceptors []ResponseInterceptor
}

// NewInterceptorChain creates a new chain with the given interceptors.
func NewInterceptorChain(interceptors ...ResponseInterceptor) *InterceptorChain {
	return &InterceptorChain{
		interceptors: interceptors,
	}
}

// ListInterceptors returns a copy of the underlying interceptor slice,
// in registration order. Useful for code that needs to extend the
// chain without mutating the existing slice (e.g. SmartSaniGuard
// appending a restore interceptor onto a chain built by initGoalControl).
func (c *InterceptorChain) ListInterceptors() []ResponseInterceptor {
	if c == nil || len(c.interceptors) == 0 {
		return nil
	}
	out := make([]ResponseInterceptor, len(c.interceptors))
	copy(out, c.interceptors)
	return out
}

// InterceptNonStream executes all interceptors in the chain for non-streaming responses.
func (c *InterceptorChain) InterceptNonStream(ctx context.Context, req *InterceptRequest) (*InterceptResult, error) {
	if c == nil || len(c.interceptors) == 0 {
		return nil, nil
	}

	var finalResult *InterceptResult
	currentReq := req

	for i, interceptor := range c.interceptors {
		result, err := interceptor.InterceptNonStream(ctx, currentReq)
		if err != nil {
			slog.Warn("interceptor_chain: interceptor failed",
				"index", i,
				"error", err,
				"session_id", req.SessionID,
			)
			continue
		}

		if result == nil {
			continue
		}

		if finalResult == nil {
			finalResult = &InterceptResult{}
		}
		if result.ShouldBlock {
			finalResult.ShouldBlock = true
		}
		if len(result.ModifiedBody) > 0 {
			finalResult.ModifiedBody = result.ModifiedBody
			// Copy the whole request so newly added capability fields survive
			// between interceptors. The first modifier must reach the next one.
			nextReq := *currentReq
			nextReq.ResponseBody = result.ModifiedBody
			currentReq = &nextReq
		}
		if len(result.InjectFollowUp) > 0 {
			finalResult.InjectFollowUp = result.InjectFollowUp
		}
		if result.Action != "" {
			finalResult.Action = result.Action
		}
		if len(result.Metadata) > 0 {
			if finalResult.Metadata == nil {
				finalResult.Metadata = make(map[string]interface{})
			}
			for k, v := range result.Metadata {
				finalResult.Metadata[k] = v
			}
		}
		if result.ClientSignalKind != "" {
			finalResult.ClientSignalKind = result.ClientSignalKind
			finalResult.ClientSignalPayload = append([]byte(nil), result.ClientSignalPayload...)
			finalResult.ClientSignalAttempts = result.ClientSignalAttempts
		}

		if result.ShouldBlock {
			break
		}
	}

	return finalResult, nil
}

// InterceptStreamChunk executes all interceptors for a stream chunk.
func (c *InterceptorChain) InterceptStreamChunk(ctx context.Context, chunk []byte, meta *StreamMeta) (*ChunkResult, error) {
	if c == nil || len(c.interceptors) == 0 {
		return nil, nil
	}

	var finalResult *ChunkResult
	currentChunk := chunk

	for i, interceptor := range c.interceptors {
		result, err := interceptor.InterceptStreamChunk(ctx, currentChunk, meta)
		if err != nil {
			slog.Warn("interceptor_chain: stream chunk interceptor failed",
				"index", i,
				"error", err,
				"session_id", meta.SessionID,
			)
			continue
		}

		if result == nil {
			continue
		}

		if finalResult == nil {
			finalResult = &ChunkResult{}
		}
		if result.ShouldBlock {
			finalResult.ShouldBlock = true
		}
		if result.SuppressChunk {
			// A later interceptor is withholding this frame. Earlier
			// replacements or injections have not passed that
			// interceptor's release decision and must not escape through
			// the writer.
			finalResult.SuppressChunk = true
			finalResult.ModifiedChunk = nil
			finalResult.InjectAfter = nil
		}
		// R24-C (2026-09-29 round 26): the historical guard here was
		// `!finalResult.SuppressChunk || result.SuppressChunk`, which is
		// always true — a withholding result carrying its own replacement
		// re-populated the fields the suppress branch had just cleared, and
		// the withheld frame's content stayed visible to any consumer of
		// the chain result. Once suppression is set, replacements and
		// injections must stay empty.
		if len(result.ModifiedChunk) > 0 && !finalResult.SuppressChunk {
			finalResult.ModifiedChunk = result.ModifiedChunk
			currentChunk = result.ModifiedChunk
		}
		if len(result.InjectAfter) > 0 && !finalResult.SuppressChunk {
			finalResult.InjectAfter = result.InjectAfter
		}

		if result.ShouldBlock || result.SuppressChunk {
			break
		}
	}

	return finalResult, nil
}

// FlushStreamPending releases checked frames held by optional interceptors.
// It is deliberately separate from InterceptStreamEnd, which runs goal and
// audit hooks once with the assembled response in the HTTP handler.
func (c *InterceptorChain) FlushStreamPending(ctx context.Context, meta *StreamMeta) ([]byte, error) {
	if c == nil {
		return nil, nil
	}
	var out []byte
	for _, interceptor := range c.interceptors {
		flusher, ok := interceptor.(StreamPendingFlusher)
		if !ok {
			continue
		}
		chunk, err := flusher.FlushStreamPending(ctx, meta)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
	}
	return out, nil
}

// StreamPendingFlusher is the optional interface implemented by interceptors
// that buffer outbound text for cross-frame compliance checks. Passthrough
// interceptors (most output governance that doesn't combine frames) do not
// implement it; writers that call chain.FlushStreamPending must fall back
// to "no frame to emit" when none of the registered interceptors return a
// release.
type StreamPendingFlusher interface {
	FlushStreamPending(ctx context.Context, meta *StreamMeta) ([]byte, error)
}

// InterceptStreamEnd executes all interceptors when a stream ends.
func (c *InterceptorChain) InterceptStreamEnd(ctx context.Context, meta *StreamMeta) (*EndResult, error) {
	if c == nil || len(c.interceptors) == 0 {
		return nil, nil
	}

	var finalResult *EndResult

	for i, interceptor := range c.interceptors {
		result, err := interceptor.InterceptStreamEnd(ctx, meta)
		if err != nil {
			slog.Warn("interceptor_chain: stream end interceptor failed",
				"index", i,
				"error", err,
				"session_id", meta.SessionID,
			)
			continue
		}

		if result == nil {
			continue
		}

		if finalResult == nil {
			finalResult = result
		} else {
			if len(result.InjectFollowUp) > 0 {
				finalResult.InjectFollowUp = result.InjectFollowUp
			}
			if result.Action != "" {
				finalResult.Action = result.Action
			}
			if len(result.Metadata) > 0 {
				if finalResult.Metadata == nil {
					finalResult.Metadata = make(map[string]interface{})
				}
				for k, v := range result.Metadata {
					finalResult.Metadata[k] = v
				}
			}
			if result.ClientSignalKind != "" {
				finalResult.ClientSignalKind = result.ClientSignalKind
				finalResult.ClientSignalPayload = append([]byte(nil), result.ClientSignalPayload...)
				finalResult.ClientSignalAttempts = result.ClientSignalAttempts
			}
		}
	}

	return finalResult, nil
}
