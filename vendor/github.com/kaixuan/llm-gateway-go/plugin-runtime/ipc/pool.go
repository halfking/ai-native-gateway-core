package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// ErrClientClosed is returned when a client is closed before a request completes.
var ErrClientClosed = errors.New("IPC client is closed")

// connectionPool manages a pool of Unix socket connections for reuse.
type connectionPool struct {
	socketPath string
	maxSize    int

	mu     sync.Mutex
	conns  chan net.Conn
	closed bool
	active map[net.Conn]struct{}
	ctx    context.Context
	cancel context.CancelFunc
}

// newConnectionPool creates a new connection pool.
func newConnectionPool(socketPath string, maxSize int) *connectionPool {
	if maxSize <= 0 {
		maxSize = 5
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &connectionPool{
		socketPath: socketPath,
		maxSize:    maxSize,
		conns:      make(chan net.Conn, maxSize),
		active:     make(map[net.Conn]struct{}),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Get gets a connection from the pool or creates a new one.
func (p *connectionPool) Get() (net.Conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClientClosed
	}

	// Try to get from pool
	select {
	case conn := <-p.conns:
		p.mu.Unlock()
		return conn, nil
	default:
		p.mu.Unlock()
		// Pool is empty, create new connection
		return p.dial()
	}
}

// Put returns a connection to the pool.
func (p *connectionPool) Put(conn net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		delete(p.active, conn)
		conn.Close()
		return
	}

	// Try to return to pool
	select {
	case p.conns <- conn:
		p.active[conn] = struct{}{}
	default:
		// Pool is full, close connection
		delete(p.active, conn)
		conn.Close()
	}
}

// Remove removes a bad connection from the pool.
func (p *connectionPool) Remove(conn net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.active, conn)
	conn.Close()
}

// dial creates a new connection to the Unix socket.
func (p *connectionPool) dial() (net.Conn, error) {
	return p.dialContext(context.Background())
}

func (p *connectionPool) dialContext(ctx context.Context) (net.Conn, error) {
	if err := p.closedError(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopCancel := context.AfterFunc(p.ctx, cancel)
	defer stopCancel()
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", p.socketPath)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		if conn != nil {
			conn.Close()
		}
		return nil, ErrClientClosed
	}
	if err != nil {
		return nil, fmt.Errorf("dial unix socket: %w", err)
	}
	p.active[conn] = struct{}{}
	return conn, nil
}

func (p *connectionPool) closedError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClientClosed
	}
	return nil
}

// Close closes all connections in the pool.
func (p *connectionPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	p.closed = true
	p.cancel()

	// Active requests and idle connections share ownership, so Close interrupts
	// requests already in flight as well as preventing new dials.
	for conn := range p.active {
		conn.Close()
		delete(p.active, conn)
	}
	for len(p.conns) > 0 {
		<-p.conns
	}

	return nil
}

// Size returns the current number of connections in the pool.
func (p *connectionPool) Size() int {
	return len(p.conns)
}
