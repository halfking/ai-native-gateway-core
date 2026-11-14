package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// Server is the IPC server that handles plugin requests. A stopped server cannot
// be restarted; create a new Server for a new lifecycle.
type Server struct {
	socketPath string
	listener   net.Listener
	handlers   map[string]Handler

	mu        sync.RWMutex
	started   bool
	stopped   bool
	stopOnce  sync.Once
	stopErr   error
	cancel    context.CancelFunc
	stopWatch func() bool
	conns     map[net.Conn]struct{}
	workers   sync.WaitGroup
}

// Handler processes IPC requests and returns a response. Handlers must return
// when ctx is canceled so Stop can wait for their completion.
type Handler func(ctx context.Context, request []byte) ([]byte, error)

// NewServer creates a new IPC server listening on the given socket path.
func NewServer(socketPath string) *Server {
	return &Server{
		socketPath: socketPath,
		handlers:   make(map[string]Handler),
		conns:      make(map[net.Conn]struct{}),
	}
}

// RegisterHandler registers a handler for a specific message type.
func (s *Server) RegisterHandler(messageType string, handler Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[messageType] = handler
}

// Start starts the IPC server and begins accepting connections. It rejects a
// second start and a start after Stop, before touching the socket path.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return errors.New("IPC server is stopped")
	}
	if s.started {
		return errors.New("IPC server is already started")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove old socket: %w", err)
	}
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.socketPath, err)
	}
	// Retain the upstream owner-only socket contract before accepting requests.
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		listener.Close()
		return fmt.Errorf("chmod socket %s: %w", s.socketPath, err)
	}
	s.listener = listener
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	s.stopWatch = context.AfterFunc(ctx, func() { s.Stop() })
	s.workers.Add(1)
	go s.acceptLoop(ctx, listener)
	log.Printf("INFO: IPC server listening on %s", s.socketPath)
	return nil
}

func (s *Server) acceptLoop(ctx context.Context, listener net.Listener) {
	defer s.workers.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				log.Printf("ERROR: Accept failed: %v", err)
			}
			return
		}
		s.mu.Lock()
		if s.stopped || ctx.Err() != nil {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.workers.Add(1)
		s.mu.Unlock()
		go s.handleConnection(ctx, conn)
	}
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	defer func() {
		conn.Close()
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		s.workers.Done()
	}()
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return
	}
	request, err := ReadMessage(conn)
	if err != nil || ctx.Err() != nil {
		return
	}
	response, err := s.processRequest(ctx, request)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		errorResp := map[string]interface{}{
			"type":    "error",
			"success": false,
			"error":   err.Error(),
		}
		response, err = json.Marshal(errorResp)
		if err != nil {
			log.Printf("ERROR: Encode error response: %v", err)
			return
		}
	}
	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return
	}
	if err := WriteMessage(conn, response); err != nil && ctx.Err() == nil {
		log.Printf("ERROR: Write failed: %v", err)
	}
}

func (s *Server) processRequest(ctx context.Context, request []byte) ([]byte, error) {
	var baseReq struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(request, &baseReq); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	s.mu.RLock()
	handler, exists := s.handlers[baseReq.Type]
	s.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("unknown message type: %s", baseReq.Type)
	}
	return handler(ctx, request)
}

// Stop cancels active handlers, closes all connections, and waits for handlers
// and the accept loop to exit. Concurrent calls wait for the same completion.
func (s *Server) Stop() error {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		if s.stopWatch != nil {
			s.stopWatch()
		}
		if s.cancel != nil {
			s.cancel()
		}
		listener := s.listener
		conns := make([]net.Conn, 0, len(s.conns))
		for conn := range s.conns {
			conns = append(conns, conn)
		}
		s.mu.Unlock()

		if listener != nil {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				s.stopErr = err
			}
		}
		for _, conn := range conns {
			conn.Close()
		}
		s.workers.Wait()
		// UnixListener.Close unlinks its own socket. A second removal here could
		// delete a replacement server that started while handlers were draining.
		log.Printf("INFO: IPC server stopped")
	})
	return s.stopErr
}
