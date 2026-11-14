package ipc

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// connectionPool manages a pool of Unix socket connections for reuse.
type connectionPool struct {
	socketPath string
	maxSize    int

	mu     sync.Mutex
	conns  chan net.Conn
	closed bool
}

// newConnectionPool creates a new connection pool.
func newConnectionPool(socketPath string, maxSize int) *connectionPool {
	if maxSize <= 0 {
		maxSize = 5
	}

	return &connectionPool{
		socketPath: socketPath,
		maxSize:    maxSize,
		conns:      make(chan net.Conn, maxSize),
	}
}

// Get gets a connection from the pool or creates a new one.
func (p *connectionPool) Get() (net.Conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("connection pool is closed")
	}
	p.mu.Unlock()

	// Try to get from pool
	select {
	case conn := <-p.conns:
		return conn, nil
	default:
		// Pool is empty, create new connection
		return p.dial()
	}
}

// Put returns a connection to the pool.
func (p *connectionPool) Put(conn net.Conn) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		conn.Close()
		return
	}
	p.mu.Unlock()

	// Try to return to pool
	select {
	case p.conns <- conn:
		// Connection returned to pool
	default:
		// Pool is full, close connection
		conn.Close()
	}
}

// Remove removes a bad connection from the pool.
func (p *connectionPool) Remove(conn net.Conn) {
	conn.Close()
}

// dial creates a new connection to the Unix socket.
func (p *connectionPool) dial() (net.Conn, error) {
	conn, err := net.DialTimeout("unix", p.socketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial unix socket: %w", err)
	}
	return conn, nil
}

// Close closes all connections in the pool.
//
// 2026-11-14 12h 审计轮补（D-Pool1）：原实现 close(p.conns) 后
// `for conn := range p.conns` 排空——与 Put 的「先验 closed 再（锁外）
// select send」存在竞态窗：Put 在 Unlock 后 Close 才置位 ⇒ send on closed
// channel panic。修法 = 不 close channel，改「closed 标志 + 持锁非阻塞
// 排空」：Get/Put 的 closed 先验仍挡住 Close 之后的取还，Close 自身
// drain 掉池内余量。channel 永不 close ⇒ 任何时序的 send 都安全。
func (p *connectionPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	p.closed = true

	// Drain pooled connections (non-blocking: channel is buffered and
	// never closed, so a plain range would block on an empty pool).
	for {
		select {
		case conn := <-p.conns:
			conn.Close()
		default:
			return nil
		}
	}
}

// Size returns the current number of connections in the pool.
func (p *connectionPool) Size() int {
	return len(p.conns)
}
