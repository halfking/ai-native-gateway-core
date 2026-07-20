package pluginruntime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// command 是对一个插件进程的抽象，便于测试注入 fake。
type command interface {
	Start(ctx context.Context) error
	Wait() error
	Stop() error
	Pid() int
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
	commandFactory func(socketPath, entrypoint string, env []string) command
}

// NewSupervisor 构造一个新的 supervisor，默认 commandFactory 走 exec 实现。
func NewSupervisor(cfg SupervisorConfig) *Supervisor {
	s := &Supervisor{cfg: cfg, procs: map[string]command{}}
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return newExecCommand(socketPath, entrypoint, env)
	}
	return s
}

// Start 启动插件进程并返回初始状态。握手（ready 判定）由 handshake.go 完成。
// entrypoint 来自 manifest.Runtime.Entrypoint；socket/context secret/contract 通过 env 注入。
func (s *Supervisor) Start(ctx context.Context, m *Manifest) (*PluginState, error) {
	socketPath := filepath.Join(s.cfg.SocketDir, m.PluginID+".sock")
	env := []string{
		"AI_SESSION_MANAGER_PLUGIN_SOCKET=" + socketPath,
		"GATEWAY_PLUGIN_CONTRACT=" + m.GatewayCompatibility.APIContract,
	}
	if len(s.cfg.ContextSecret) > 0 {
		env = append(env, "AI_SESSION_MANAGER_GATEWAY_CONTEXT_SECRET="+string(s.cfg.ContextSecret))
	}
	cmd := s.commandFactory(socketPath, m.Runtime.Entrypoint, env)
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
		Pid:           cmd.Pid(),
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

// execCommand 用 os/exec 启动插件 entrypoint。
type execCommand struct {
	mu         sync.Mutex
	socketPath string
	entrypoint string
	env        []string
	cmd        *exec.Cmd
}

func newExecCommand(socketPath, entrypoint string, env []string) *execCommand {
	return &execCommand{socketPath: socketPath, entrypoint: entrypoint, env: env}
}

// Start 启动 entrypoint 进程。不阻塞等待退出（Wait 在独立 goroutine 中调用，
// 仅用于回收僵尸进程；退出码/错误的精细处理在 P5 结构化日志中补齐）。
func (e *execCommand) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := exec.CommandContext(ctx, e.entrypoint)
	c.Env = append(os.Environ(), e.env...)
	// 插件 stdout/stderr 暂时直接复用 gateway 日志流；P5 改为结构化捕获。
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	if err := c.Start(); err != nil {
		return fmt.Errorf("exec %s: %w", e.entrypoint, err)
	}
	e.cmd = c
	go c.Wait() // 回收僵尸进程；Stop 时通过 kill 终止
	return nil
}

// Wait 阻塞直到进程退出。若进程尚未 Start，则返回 nil。
func (e *execCommand) Wait() error {
	e.mu.Lock()
	c := e.cmd
	e.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.Wait()
}

// Stop 终止进程。若未 Start 或已退出，返回 nil。
func (e *execCommand) Stop() error {
	e.mu.Lock()
	c := e.cmd
	e.mu.Unlock()
	if c == nil || c.Process == nil {
		return nil
	}
	return c.Process.Kill()
}

// Pid 返回进程 PID；未启动时返回 0。
func (e *execCommand) Pid() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd == nil || e.cmd.Process == nil {
		return 0
	}
	return e.cmd.Process.Pid
}
