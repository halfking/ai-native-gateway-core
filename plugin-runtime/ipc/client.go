package ipc

import (
	"context"
	"fmt"
	"time"
)

// Client is the IPC client used by plugins to communicate with the core.
type Client struct {
	socketPath string
	pool       *connectionPool
}

// NewClient creates a new IPC client that connects to the given socket path.
func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		pool:       newConnectionPool(socketPath, 5), // Default pool size: 5
	}
}

// Send sends a request and waits for the response.
func (c *Client) Send(ctx context.Context, request []byte) ([]byte, error) {
	// Get connection from pool (creates new connection each time in this simple version)
	conn, err := c.pool.dial()
	if err != nil {
		return nil, fmt.Errorf("get connection: %w", err)
	}
	defer conn.Close()

	// Determine deadline from context
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(10 * time.Second)
	}

	// Set write deadline
	conn.SetWriteDeadline(deadline)

	// Write request
	if _, err := conn.Write(request); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Set read deadline
	conn.SetReadDeadline(deadline)

	// Read response
	buf := make([]byte, 1024*1024) // 1MB buffer
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return buf[:n], nil
}

// SendWithTimeout sends a request with a specific timeout.
func (c *Client) SendWithTimeout(request []byte, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return c.Send(ctx, request)
}

// Close closes the client and releases all resources.
func (c *Client) Close() error {
	return c.pool.Close()
}
