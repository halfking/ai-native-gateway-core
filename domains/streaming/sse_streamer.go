package streaming

import (
	"errors"
	"sync"
	"time"
)

// SSEStreamer SSE 格式流式器。
//
// 工作原理：从 ctx.ResponseChan 读取上游字节，每条数据包装为
// StreamChunk{Type: "content"} 写入 ch；上游 ch 关闭时发送
// StreamChunk{Type: "done"} 并 close(ch)。
//
// 客户端断开检测：每次 send 等待 5s；超时认为客户端已断开，立即返回
// （不再发送 done 块）。
//
// 错误处理：Stream() 本身不返回错误给 Pipeline；流中错误通过
// StreamChunk{Type: "error"} 上报。
type SSEStreamer struct {
	// ChunkTimeout 单个 chunk send 等待超时。
	// <=0 使用默认 5s。
	ChunkTimeout time.Duration
	// ErrSendTimeout 客户端断开时的错误（用于测试断言）。
	ErrSendTimeout error
}

// NewSSEStreamer 构造一个 SSE 流式器。
func NewSSEStreamer() *SSEStreamer {
	return &SSEStreamer{
		ChunkTimeout:  5 * time.Second,
		ErrSendTimeout: errors.New("streaming: client send timeout"),
	}
}

// Name 返回流式器名称。
func (s *SSEStreamer) Name() string { return "sse" }

// Stream 启动流式处理。
//
// 行为契约：
//   - 关闭上游 ch（ctx.ResponseChan）后，发送 done 块并 close(ch)。
//   - 单次 send 超时返回 ErrSendTimeout（认为客户端断开）。
//   - 函数返回前必定 close(ch)（使用 sync.Once 避免重复 close）。
func (s *SSEStreamer) Stream(ctx StreamContext, ch chan<- *StreamChunk) error {
	if ch == nil {
		return errors.New("streaming: nil output channel")
	}
	timeout := s.ChunkTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	// sync.Once 保证 ch 只 close 一次
	var once sync.Once
	closeOnce := func() {
		once.Do(func() { close(ch) })
	}
	defer closeOnce()

	idx := 0
	for data := range ctx.ResponseChan {
		idx++
		chunk := &StreamChunk{
			Data:   data,
			Type:   ChunkTypeContent,
			Index:  idx,
			EmitAt: time.Now(),
		}
		select {
		case ch <- chunk:
		case <-time.After(timeout):
			return s.ErrSendTimeout
		}
	}

	// 正常结束：发送 done 块（带超时保护）
	select {
	case ch <- &StreamChunk{Type: ChunkTypeDone, Index: idx + 1, EmitAt: time.Now()}:
	case <-time.After(timeout):
		// done 发送超时也返回；close 由 defer 保证
	}
	return nil
}
