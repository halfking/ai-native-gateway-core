package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// Server is the IPC server that handles plugin requests.
type Server struct {
	socketPath string
	listener   net.Listener
	handlers   map[string]Handler

	mu      sync.RWMutex
	stopCh  chan struct{}
	stopped bool
}

// Handler processes IPC requests and returns a response.
type Handler func(ctx context.Context, request []byte) ([]byte, error)

// NewServer creates a new IPC server listening on the given socket path.
func NewServer(socketPath string) *Server {
	return &Server{
		socketPath: socketPath,
		handlers:   make(map[string]Handler),
		stopCh:     make(chan struct{}),
	}
}

// RegisterHandler registers a handler for a specific message type.
func (s *Server) RegisterHandler(messageType string, handler Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[messageType] = handler
}

// Start starts the IPC server and begins accepting connections.
func (s *Server) Start(ctx context.Context) error {
	// Remove existing socket file if present
	os.Remove(s.socketPath)

	// Create Unix socket listener
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.socketPath, err)
	}

	// 2026-11-14 audit D2: net.Listen creates the socket under the process
	// umask (0755 by default) — any local user could connect and invoke the
	// registered handlers (extension_point / health_check). There is no
	// peer-credential check on this transport, so restrict to owner-only as
	// the fail-closed baseline.
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		listener.Close()
		return fmt.Errorf("chmod socket %s: %w", s.socketPath, err)
	}

	s.listener = listener
	log.Printf("INFO: IPC server listening on %s", s.socketPath)

	// Start accepting connections
	go s.acceptLoop(ctx)

	return nil
}

// acceptLoop accepts incoming connections.
func (s *Server) acceptLoop(ctx context.Context) {
	for {
		select {
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}

		// Set accept timeout to allow checking stop signal
		if ul, ok := s.listener.(*net.UnixListener); ok {
			ul.SetDeadline(time.Now().Add(1 * time.Second))
		}

		conn, err := s.listener.Accept()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // Timeout, check stop signal
			}
			log.Printf("ERROR: Accept failed: %v", err)
			return
		}

		// Handle connection in goroutine
		go s.handleConnection(ctx, conn)
	}
}

// handleConnection handles a single client connection.
func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// Set read timeout
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// Read request
	buf := make([]byte, 1024*1024) // 1MB buffer
	n, err := conn.Read(buf)
	if err != nil {
		return
	}

	request := buf[:n]

	// Process request
	response, err := s.processRequest(ctx, request)
	if err != nil {
		// Send error response
		errorResp := map[string]interface{}{
			"type":    "error",
			"success": false,
			"error":   err.Error(),
		}
		response, _ = json.Marshal(errorResp)
	}

	// Write response
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(response); err != nil {
		log.Printf("ERROR: Write failed: %v", err)
	}
}

// processRequest processes an IPC request and returns a response.
func (s *Server) processRequest(ctx context.Context, request []byte) ([]byte, error) {
	// Parse request to determine message type
	var baseReq struct {
		Type string `json:"type"`
	}

	if err := json.Unmarshal(request, &baseReq); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}

	// Get handler for message type
	s.mu.RLock()
	handler, exists := s.handlers[baseReq.Type]
	s.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unknown message type: %s", baseReq.Type)
	}

	// Execute handler
	return handler(ctx, request)
}

// Stop stops the IPC server and releases resources.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil
	}

	s.stopped = true
	close(s.stopCh)

	if s.listener != nil {
		s.listener.Close()
	}

	// Remove socket file
	os.Remove(s.socketPath)

	log.Printf("INFO: IPC server stopped")
	return nil
}
