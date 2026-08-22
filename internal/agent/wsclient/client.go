// Package wsclient — local instance <-> center WebSocket client.
//
// Decision baseline 2=A: WS is the preferred channel for register/heartbeat/cmd.
// On 3 consecutive failures the client yields to HTTPHeartbeatFallback, which
// POSTs to /maintain-api/public/instances/heartbeat every 30s.
//
// Why stdlib only: the protocol is a few JSON frames; pulling gorilla/nhooyr
// for a 200-line client is not worth the dependency surface.
package wsclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	wsGUID          = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	defaultPing     = 10 * time.Second
	defaultRetryMax = 3
)

// Frame is the JSON envelope exchanged on the channel.
type Frame struct {
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Timestamp int64           `json:"ts,omitempty"`
}

// Handler decides what to do with inbound frames. Returning an error closes
// the connection.
type Handler func(Frame) error

// Config configures the client.
type Config struct {
	URL          string        // ws:// or wss:// endpoint (e.g. wss://llm.kxpms.cn/maintain-api/ws/instance)
	InstanceID   string        // instance_id passed via X-Instance-Id header
	LicenseKey   string        // optional bootstrap proof (X-License-Key)
	HardwareHash string        // optional bootstrap proof (X-Hardware-Hash)
	DialTimeout  time.Duration // defaults to 5s
	PingInterval time.Duration // defaults to 10s
	MaxFailures  int           // before yielding to fallback; defaults to 3
}

// Status reports the current channel for the ConnectivityBadge UI.
type Status string

const (
	StatusWS           Status = "ws"
	StatusHTTP         Status = "http"
	StatusDisconnected Status = "disconnected"
)

// Client owns the WS connection state and exposes Run/Fallback.
type Client struct {
	cfg     Config
	status  Status
	statusM sync.RWMutex
}

// New returns a Client. Call Run to actually connect.
func New(cfg Config) *Client {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.PingInterval == 0 {
		cfg.PingInterval = defaultPing
	}
	if cfg.MaxFailures == 0 {
		cfg.MaxFailures = defaultRetryMax
	}
	return &Client{cfg: cfg, status: StatusDisconnected}
}

// CurrentStatus returns the latest channel. Read-only; safe for UI polling.
func (c *Client) CurrentStatus() Status {
	c.statusM.RLock()
	defer c.statusM.RUnlock()
	return c.status
}

func (c *Client) setStatus(s Status) {
	c.statusM.Lock()
	c.status = s
	c.statusM.Unlock()
}

// Run blocks until ctx is cancelled or MaxFailures consecutive dial failures
// occur. After that it returns ErrYieldedToFallback; callers should then
// invoke Fallback in a separate goroutine for HTTP polling.
var ErrYieldedToFallback = errors.New("wsclient: yielded to HTTP fallback")

// Fallback polls the public HTTP heartbeat endpoint every 30s until ctx
// cancels. The ConnectivityBadge will report StatusHTTP while this is
// active.
func (c *Client) Fallback(ctx context.Context, httpPOST func() error) error {
	c.setStatus(StatusHTTP)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := httpPOST(); err != nil {
			slog.Warn("wsclient: heartbeat fallback failed", "err", err)
		}
		select {
		case <-ctx.Done():
			c.setStatus(StatusDisconnected)
			return nil
		case <-ticker.C:
		}
	}
}

// Run implements the WS dial + read/write loop with exponential backoff.
func (c *Client) Run(ctx context.Context, handle Handler) error {
	backoff := time.Second
	failures := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := c.dialOnce(ctx, handle)
		if err == nil {
			// clean disconnect via ctx cancellation
			return nil
		}
		if errors.Is(err, context.Canceled) {
			return err
		}
		failures++
		slog.Warn("wsclient: dial failed", "err", err, "attempt", failures)
		if failures >= c.cfg.MaxFailures {
			c.setStatus(StatusDisconnected)
			return ErrYieldedToFallback
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (c *Client) dialOnce(ctx context.Context, handle Handler) error {
	u, err := url.Parse(c.cfg.URL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	d := net.Dialer{Timeout: c.cfg.DialTimeout}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return err
	}
	if u.Scheme == "wss" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: u.Hostname()})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return err
		}
		conn = tlsConn
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		conn.Close()
		return err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	accept := wsAccept(key)

	req := &http.Request{
		Method: http.MethodGet,
		URL:    u,
		Header: http.Header{
			"Upgrade":               []string{"websocket"},
			"Connection":            []string{"Upgrade"},
			"Sec-WebSocket-Key":     []string{key},
			"Sec-WebSocket-Version": []string{"13"},
			"X-Instance-Id":         []string{c.cfg.InstanceID},
		},
		Host: host,
	}
	if c.cfg.LicenseKey != "" {
		req.Header.Set("X-License-Key", c.cfg.LicenseKey)
	}
	if c.cfg.HardwareHash != "" {
		req.Header.Set("X-Hardware-Hash", c.cfg.HardwareHash)
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != accept {
		conn.Close()
		return errors.New("wsclient: bad Sec-WebSocket-Accept")
	}

	c.setStatus(StatusWS)

	// Hand the buffered reader to the read loop; raw conn to write loop.
	errCh := make(chan error, 2)
	go func() { errCh <- c.writeLoop(ctx, conn) }()
	go func() { errCh <- c.readLoop(ctx, br, handle) }()

	select {
	case <-ctx.Done():
		conn.Close()
		return ctx.Err()
	case err := <-errCh:
		conn.Close()
		return err
	}
}

func (c *Client) readLoop(ctx context.Context, br *bufio.Reader, handle Handler) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Each frame is JSON + newline (server side mirrors this).
		line, err := br.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var f Frame
		if err := json.Unmarshal(line, &f); err != nil {
			slog.Warn("wsclient: bad frame", "err", err)
			continue
		}
		if err := handle(f); err != nil {
			return err
		}
	}
}

func (c *Client) writeLoop(ctx context.Context, conn net.Conn) error {
	ticker := time.NewTicker(c.cfg.PingInterval)
	defer ticker.Stop()
	ping := Frame{Type: "ping"}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			data, _ := json.Marshal(ping)
			if _, err := conn.Write(append(data, '\n')); err != nil {
				return err
			}
		}
	}
}

func wsAccept(clientKey string) string {
	h := sha1.Sum([]byte(clientKey + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}
