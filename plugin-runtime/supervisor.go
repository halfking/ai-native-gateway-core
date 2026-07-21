package pluginruntime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
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
	// SigningPubkey 是 ed25519 公钥 hex；为空时跳过 manifest 签名校验（开发模式）。
	SigningPubkey string
}

// Supervisor 管理所有已启动的插件进程。P0 为内存态骨架。
type Supervisor struct {
	cfg            SupervisorConfig
	mu             sync.Mutex
	procs          map[string]command
	states         map[string]*PluginState
	manifests      map[string]*Manifest // P7: for Restart
	commandFactory func(socketPath, entrypoint string, env []string) command
}

// NewSupervisor 构造一个新的 supervisor，默认 commandFactory 走 exec 实现。
// 若 cfg.SocketDir 指定的目录不存在则会创建（修复 P5 中"插件因 .sockets/ 缺失而退出"的问题）。
func NewSupervisor(cfg SupervisorConfig) *Supervisor {
	if cfg.SocketDir != "" {
		_ = os.MkdirAll(cfg.SocketDir, 0o755) // ignore "already exists"；真正的失败会在插件尝试 listen 时暴露
	}
	s := &Supervisor{cfg: cfg, procs: map[string]command{}, states: map[string]*PluginState{}, manifests: map[string]*Manifest{}}
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return newExecCommand(socketPath, entrypoint, env, s.cfg.ContextSecret)
	}
	return s
}

// Start 启动插件进程并返回初始状态。握手（ready 判定）由 handshake.go 完成。
// entrypoint 来自 manifest.Runtime.Entrypoint；socket/context secret/contract 通过 env 注入。
func (s *Supervisor) Start(ctx context.Context, m *Manifest) (*PluginState, error) {
	if err := VerifyManifestSignature(m.ManifestPath, s.cfg.SigningPubkey); err != nil {
		return nil, fmt.Errorf("plugin %s manifest signature: %w", m.PluginID, err)
	}
	socketPath := filepath.Join(s.cfg.SocketDir, m.PluginID+".sock")
	manifestPath := m.ManifestPath
	env := []string{
		"AI_SESSION_MANAGER_PLUGIN_SOCKET=" + socketPath,
		"GATEWAY_PLUGIN_CONTRACT=" + m.GatewayCompatibility.APIContract,
		"AI_SESSION_MANAGER_MANIFEST=" + manifestPath,
	}
	if len(s.cfg.ContextSecret) > 0 {
		env = append(env, "AI_SESSION_MANAGER_GATEWAY_CONTEXT_SECRET="+string(s.cfg.ContextSecret))
	}
	cmd := s.commandFactory(socketPath, m.Runtime.Entrypoint, env)
	if err := cmd.Start(ctx); err != nil {
		return nil, fmt.Errorf("start plugin %s: %w", m.PluginID, err)
	}
	st := &PluginState{
		PluginID:      m.PluginID,
		PluginVersion: m.PluginVersion,
		Status:        "starting",
		SocketPath:    socketPath,
		Pid:           cmd.Pid(),
	}
	s.mu.Lock()
	s.procs[m.PluginID] = cmd
	s.states[m.PluginID] = st
	s.manifests[m.PluginID] = m
	s.mu.Unlock()
	return st, nil
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

// SocketPathOf returns the unix socket path for a started plugin ("" if not started).
func (s *Supervisor) SocketPathOf(pluginID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.states[pluginID]; ok {
		return st.SocketPath
	}
	return ""
}

// Restart stops the old process (if running) and re-Starts with the original
// manifest. Used for crash recovery (same version, not an upgrade).
func (s *Supervisor) Restart(pluginID string) error {
	s.mu.Lock()
	m, ok := s.manifests[pluginID]
	oldCmd, oldRunning := s.procs[pluginID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("plugin %s manifest not found (never started)", pluginID)
	}
	if oldRunning && oldCmd != nil {
		_ = oldCmd.Stop()
		s.mu.Lock()
		delete(s.procs, pluginID)
		s.mu.Unlock()
	}
	if _, err := s.Start(context.Background(), m); err != nil {
		return fmt.Errorf("restart plugin %s: %w", pluginID, err)
	}
	return nil
}

// Upgrade stops the currently running plugin (if any) and starts the new
// manifest. If the new Start fails, the supervisor restores the previous
// manifest in its index so the caller and health-loop see the canonical
// version. The old process is already stopped by this point; callers that
// need the old version running again must call Restart after a failed Upgrade.
// Returns nil on success.
func (s *Supervisor) Upgrade(ctx context.Context, newManifest *Manifest) error {
	s.mu.Lock()
	oldManifest := s.manifests[newManifest.PluginID]
	oldCmd, oldRunning := s.procs[newManifest.PluginID]
	s.mu.Unlock()

	// 1. stop old process first (it's about to be replaced)
	if oldRunning && oldCmd != nil {
		_ = oldCmd.Stop()
		s.mu.Lock()
		delete(s.procs, newManifest.PluginID)
		s.mu.Unlock()
	}

	// 2. start new
	if _, err := s.Start(ctx, newManifest); err != nil {
		// rollback: restore old manifest in index so health-loop/caller see
		// the canonical version. We do NOT auto-restart old here — if old
		// must keep running, caller calls Restart after failed Upgrade.
		s.mu.Lock()
		if oldManifest != nil {
			s.manifests[newManifest.PluginID] = oldManifest
		} else {
			delete(s.manifests, newManifest.PluginID)
			delete(s.states, newManifest.PluginID)
		}
		s.mu.Unlock()
		return fmt.Errorf("upgrade plugin %s: %w", newManifest.PluginID, err)
	}
	return nil
}

// ManifestOf returns the stored manifest for a plugin (nil if never started).
func (s *Supervisor) ManifestOf(pluginID string) *Manifest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manifests[pluginID]
}

// execCommand 用 os/exec 启动插件 entrypoint。
type execCommand struct {
	mu         sync.Mutex
	socketPath string
	entrypoint string
	env        []string
	secret     []byte
	cmd        *exec.Cmd
	done       chan struct{} // 在 Wait 完成后关闭；Stop 用它判断进程是否已退出
}

func newExecCommand(socketPath, entrypoint string, env []string, secret []byte) *execCommand {
	return &execCommand{socketPath: socketPath, entrypoint: entrypoint, env: env, secret: secret, done: make(chan struct{})}
}

// Start 启动 entrypoint 进程。不阻塞等待退出（Wait 在独立 goroutine 中调用，
// 仅用于回收僵尸进程；退出码/错误的精细处理在 P5 结构化日志中补齐）。
func (e *execCommand) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := exec.CommandContext(ctx, e.entrypoint)
	c.Env = append(os.Environ(), e.env...)
	// 插件 stdout/stderr 经过 redacting writer 过滤掉 context secret 后再写入 gateway 日志流；
	// 防止插件把 env 打到日志里造成 HMAC secret 泄露。P5 计划改为结构化捕获。
	c.Stdout = NewRedactingWriter(e.secret, os.Stderr)
	c.Stderr = NewRedactingWriter(e.secret, os.Stderr)
	if err := c.Start(); err != nil {
		return fmt.Errorf("exec %s: %w", e.entrypoint, err)
	}
	e.cmd = c
	go func() { _ = c.Wait(); close(e.done) }() // 回收僵尸进程；Stop 通过 done 判断是否已退出
	return nil
}

// Wait 阻塞直到进程退出。若进程尚未 Start，则返回 nil。
// 通过 done 通道等待（而非再次调用 c.Wait），避免与 Start 中的后台 Wait 并发。
func (e *execCommand) Wait() error {
	e.mu.Lock()
	c := e.cmd
	e.mu.Unlock()
	if c == nil {
		return nil
	}
	<-e.done
	return nil
}

// Stop 优雅终止进程：先发 SIGTERM，给进程 5 秒优雅退出的窗口；
// 超时仍存活则 Kill。未 Start 或已退出时返回 nil。
func (e *execCommand) Stop() error {
	e.mu.Lock()
	c := e.cmd
	e.mu.Unlock()
	if c == nil || c.Process == nil {
		return nil
	}
	_ = c.Process.Signal(syscall.SIGTERM)
	select {
	case <-e.done:
		return nil
	case <-time.After(5 * time.Second):
		return c.Process.Kill()
	}
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
