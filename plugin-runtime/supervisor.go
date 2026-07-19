package pluginruntime

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
)

// command 是对一个插件进程的抽象，便于测试注入 fake。
type command interface {
	Start(ctx context.Context) error
	Wait() error
	Stop() error
}

// SupervisorConfig 是 supervisor 的初始化配置。
type SupervisorConfig struct {
	SocketDir     string
	ContextSecret []byte
}

// Supervisor 管理所有已启动的插件进程。P0 为内存态骨架。
type Supervisor struct {
	cfg            SupervisorConfig
	mu             sync.Mutex
	procs          map[string]command
	commandFactory func(socketPath string) command
}

// NewSupervisor 构造一个新的 supervisor，默认 commandFactory 走 exec 实现。
func NewSupervisor(cfg SupervisorConfig) *Supervisor {
	s := &Supervisor{cfg: cfg, procs: map[string]command{}}
	s.commandFactory = func(socketPath string) command { return &execCommand{socketPath: socketPath} }
	return s
}

// Start 启动插件进程并返回初始状态。握手（ready 判定）由 handshake.go 完成。
func (s *Supervisor) Start(ctx context.Context, m *Manifest) (*PluginState, error) {
	socketPath := filepath.Join(s.cfg.SocketDir, m.PluginID+".sock")
	cmd := s.commandFactory(socketPath)
	if err := cmd.Start(ctx); err != nil {
		return nil, fmt.Errorf("start plugin %s: %w", m.PluginID, err)
	}
	s.mu.Lock()
	s.procs[m.PluginID] = cmd
	s.mu.Unlock()
	return &PluginState{
		PluginID:      m.PluginID,
		PluginVersion: m.PluginVersion,
		Status:        "starting",
		SocketPath:    socketPath,
	}, nil
}

// Stop 停止并注销一个插件进程。
func (s *Supervisor) Stop(pluginID string) error {
	s.mu.Lock()
	cmd, ok := s.procs[pluginID]
	delete(s.procs, pluginID)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("plugin %s not running", pluginID)
	}
	return cmd.Stop()
}

// execCommand 是真实实现（用 os/exec 启动 entrypoint）。
type execCommand struct {
	socketPath string
}

func (e *execCommand) Start(ctx context.Context) error { return nil } // P0 桩；P4 实装 exec
func (e *execCommand) Wait() error                     { return nil }
func (e *execCommand) Stop() error                     { return nil }
