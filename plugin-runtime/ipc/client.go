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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	conn, err := c.pool.dialContext(ctx)
	if err != nil {
		return nil, c.requestError(ctx, "get connection", err)
	}
	defer c.pool.Remove(conn)
	// Closing interrupts both blocked writes and reads, including cancellation
	// of contexts that had no deadline when Send was called.
	stopCancel := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopCancel()

	// Write request
	if err := WriteMessage(conn, request); err != nil {
		return nil, c.requestError(ctx, "write request", err)
	}

	// Read response
	response, err := ReadMessage(conn)
	if err != nil {
		return nil, c.requestError(ctx, "read response", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.pool.closedError(); err != nil {
		return nil, err
	}
	return response, nil
}

func (c *Client) requestError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		err = contextErr
	} else if closedErr := c.pool.closedError(); closedErr != nil {
		err = closedErr
	}
	return fmt.Errorf("%s: %w", operation, err)
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
