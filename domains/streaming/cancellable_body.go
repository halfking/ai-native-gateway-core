package streaming

// cancellable_body.go — 2026-09-05 审计闭环6。
//
// 审计缺口：SSE reader（bufio.Reader 包装 resp.Body）阻塞在 Read 上时，
// 取消请求 ctx 不会唤醒它——上游保持连接不断流（慢速滴流 / 半开连接），
// 读循环会一直挂到上游断开，goroutine 与请求资源随之泄漏。
//
// 修复契约：ctxCancellableBody 监听 ctx.Done()，取消时强制 Close 底层
// body。io.Closer 的文档语义保证 Close 会使阻塞中的 Read 返回错误，
// 读循环既有的 ctx.Err() 分支（anthropic_bridge.go client_cancel 等）
// 随即给出正确的中断原因。这是「底层 Close 不响应时仍可取消」的
// transport 级兜底：即使 Close 不能中断 Read 的传输实现，watcher 也
// 保证了 Close 至少被调用一次（幂等）。
//
// watcher 生命周期：ctx.Done 或显式 Close 任一发生即退出，不泄漏。

import (
	"context"
	"io"
	"sync"
)

// ctxCancellableBody 包装 io.ReadCloser：ctx 取消时强制关闭底层 body。
type ctxCancellableBody struct {
	io.ReadCloser

	closeBody sync.Once
	closeErr  error
	stopOnce  sync.Once
	stop      chan struct{}
}

// newCtxCancellableBody 把 body 与 ctx 绑定。ctx 为 nil 时原样返回包装
// （无 watcher，行为等同原始 body）。
func newCtxCancellableBody(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	c := &ctxCancellableBody{ReadCloser: body, stop: make(chan struct{})}
	if ctx == nil {
		return c
	}
	go func() {
		select {
		case <-ctx.Done():
			c.forceClose()
		case <-c.stop:
		}
	}()
	return c
}

func (c *ctxCancellableBody) forceClose() {
	c.closeBody.Do(func() { c.closeErr = c.ReadCloser.Close() })
	c.stopOnce.Do(func() { close(c.stop) })
}

// Close 幂等关闭：先停 watcher，再关底层 body（只关一次）。
func (c *ctxCancellableBody) Close() error {
	c.stopOnce.Do(func() { close(c.stop) })
	c.closeBody.Do(func() { c.closeErr = c.ReadCloser.Close() })
	return c.closeErr
}
