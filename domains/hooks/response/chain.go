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
			finalResult = result
		} else {
			if result.ShouldBlock {
				finalResult.ShouldBlock = true
			}
			if len(result.ModifiedBody) > 0 {
				finalResult.ModifiedBody = result.ModifiedBody
				currentReq = &InterceptRequest{
					SessionID:     currentReq.SessionID,
					RequestID:     currentReq.RequestID,
					TenantID:      currentReq.TenantID,
					ClientModel:   currentReq.ClientModel,
					ResponseBody:  result.ModifiedBody,
					TokensUsed:    currentReq.TokensUsed,
					ContextWindow: currentReq.ContextWindow,
					MessageCount:  currentReq.MessageCount,
					FinishReason:  currentReq.FinishReason,
					IsStreaming:   currentReq.IsStreaming,
				}
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
			finalResult = result
		} else {
			if result.ShouldBlock {
				finalResult.ShouldBlock = true
			}
			if len(result.ModifiedChunk) > 0 {
				finalResult.ModifiedChunk = result.ModifiedChunk
				currentChunk = result.ModifiedChunk
			}
			if len(result.InjectAfter) > 0 {
				finalResult.InjectAfter = result.InjectAfter
			}
		}

		if result.ShouldBlock {
			break
		}
	}

	return finalResult, nil
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
		}
	}

	return finalResult, nil
}
