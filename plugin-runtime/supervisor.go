package pluginruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/eventbus"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/limiter"
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
	SocketDir string
	// ContextSecret remains gateway-local and is used only to redact child output.
	// Plugin-to-gateway calls must use a dedicated, revocable transport identity;
	// the gateway secret is never placed in a child environment.
	ContextSecret []byte
	// SigningPubkey 是 ed25519 公钥 hex；为空时跳过 manifest 签名校验（开发模式）。
	SigningPubkey string
	// PluginEnv supplies per-plugin runtime transport identity, never gateway credentials.
	PluginEnv func(pluginID string) []string
	// EventBus 用于发布插件生命周期事件（可选）
	EventBus eventbus.EventBus
	// DefaultLimits 是全局默认资源限制（可选）。Manifest 未声明
	// resource_limits 时应用；nil 时无任何默认限制（历史行为）。
	DefaultLimits *ResourceLimits
}

// Supervisor 管理所有已启动的插件进程。P0 为内存态骨架。
type Supervisor struct {
	cfg            SupervisorConfig
	mu             sync.Mutex
	startMu        sync.Mutex // 串行化 Start：进程已运行检查与 proc 落位必须原子
	procs          map[string]command
	states         map[string]*PluginState
	manifests      map[string]*Manifest // P7: for Restart
	limiters       map[string]*limiter.ResourceLimiter
	commandFactory func(socketPath, entrypoint string, env []string) command
}

// NewSupervisor 构造一个新的 supervisor，默认 commandFactory 走 exec 实现。
// 若 cfg.SocketDir 指定的目录不存在则会创建（修复 P5 中"插件因 .sockets/ 缺失而退出"的问题）。
func NewSupervisor(cfg SupervisorConfig) *Supervisor {
	if cfg.SocketDir != "" {
		_ = os.MkdirAll(cfg.SocketDir, 0o755) // ignore "already exists"；真正的失败会在插件尝试 listen 时暴露
	}
	s := &Supervisor{cfg: cfg, procs: map[string]command{}, states: map[string]*PluginState{}, manifests: map[string]*Manifest{}, limiters: map[string]*limiter.ResourceLimiter{}}
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return newExecCommand(socketPath, entrypoint, env, s.cfg.ContextSecret)
	}
	return s
}

// Start starts a plugin process in a restricted environment. Readiness is not
// granted here: callers must complete handshake, health, and binding checks.
func (s *Supervisor) Start(ctx context.Context, m *Manifest) (*PluginState, error) {
	if m == nil || m.PluginID == "" {
		return nil, fmt.Errorf("plugin manifest with id required")
	}
	// 2026-11-14 审计轮（D-S2）：Start 无「已在运行」互斥时，并发 activate
	// 产生两个进程抢同一 <id>.sock，先者被覆盖成 supervisor 不可停止的孤儿
	// 进程。startMu 使检查与落位串行；Restart 先 delete 再 Start 不受影响。
	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.mu.Lock()
	if _, running := s.procs[m.PluginID]; running {
		s.mu.Unlock()
		return nil, fmt.Errorf("plugin %s already running", m.PluginID)
	}
	s.mu.Unlock()
	if m.Runtime.Entrypoint == "" && m.ManifestPath != "" {
		return nil, fmt.Errorf("plugin manifest entrypoint required")
	}
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
	if s.cfg.PluginEnv != nil {
		env = append(env, s.cfg.PluginEnv(m.PluginID)...)
	}

	// ResourceLimiter 接线：Manifest 声明的 resource_limits 优先，否则回落到
	// SupervisorConfig.DefaultLimits（全局默认）；两者都未配置则不创建 limiter
	// （向后兼容：未配置 limits 的插件行为与历史版本完全一致）。
	limits := m.ResourceLimits
	if limits == nil {
		limits = s.cfg.DefaultLimits
	}
	var rl *limiter.ResourceLimiter
	if limits != nil {
		var err error
		rl, err = limiter.NewResourceLimiter(m.PluginID, *limits)
		if err != nil {
			return nil, fmt.Errorf("create resource limiter for %s: %w", m.PluginID, err)
		}
	}

	cmd := s.commandFactory(socketPath, m.Runtime.Entrypoint, env)

	// 应用资源限制：execCommand 通过 prepare 钩子在 Start 内部（exec.Cmd 创建
	// 之后、进程启动之前）注入 cgroup/SysProcAttr 配置。Apply 失败说明限制配置
	// 本身非法（如内存格式错误）——fail-fast，不启动不受控的进程。
	if rl != nil {
		if execCmd, ok := cmd.(*execCommand); ok {
			execCmd.prepare = rl.Apply
		}
		// fakeCommand（测试注入）没有底层 exec.Cmd，跳过——cgroup 限制只对
		// 真实进程有意义；limiter 本身照常登记，便于测试断言。
	}

	if err := cmd.Start(ctx); err != nil {
		if rl != nil {
			_ = rl.Stop()
		}
		return nil, fmt.Errorf("start plugin %s: %w", m.PluginID, err)
	}

	// 分配进程到 cgroup（启动后；monitor 模式下开启监控）。失败仅警告不阻止
	// 启动——资源限制是保护措施，不应因降级失败而拒绝插件服务。
	if rl != nil {
		if err := rl.AssignProcess(cmd.Pid()); err != nil {
			fmt.Printf("WARN: failed to assign plugin %s process to resource limits: %v\n", m.PluginID, err)
		}
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
	if rl != nil {
		s.limiters[m.PluginID] = rl
	}
	s.mu.Unlock()

	// 发布 plugin.started 事件
	s.publishEvent(ctx, "plugin.started", m.PluginID, map[string]interface{}{
		"plugin_id":      m.PluginID,
		"plugin_version": m.PluginVersion,
		"pid":            cmd.Pid(),
		"status":         "starting",
	})

	return st, nil
}

// Stop 停止并注销一个插件进程。
func (s *Supervisor) Stop(pluginID string) error {
	s.mu.Lock()
	cmd, ok := s.procs[pluginID]
	rl := s.limiters[pluginID]
	delete(s.procs, pluginID)
	delete(s.limiters, pluginID)
	if st := s.states[pluginID]; st != nil {
		st.Status = "stopped"
		st.SocketPath = ""
		st.Pid = 0
	}
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("plugin %s not running", pluginID)
	}

	// 停止 ResourceLimiter（清理 cgroup / 停止监控）。失败仅警告：插件进程
	// 已停止，limiter 清理失败不应让调用方误判停止失败。
	if rl != nil {
		if err := rl.Stop(); err != nil {
			fmt.Printf("WARN: failed to stop resource limiter for plugin %s: %v\n", pluginID, err)
		}
	}

	// 发布 plugin.stopped 事件
	s.publishEvent(context.Background(), "plugin.stopped", pluginID, map[string]interface{}{
		"plugin_id": pluginID,
		"status":    "stopped",
	})

	return cmd.Stop()
}

// LimiterOf 返回插件当前生效的 ResourceLimiter（未配置限制或未启动时为 nil）。
// 供运维/观测面读取监控统计。
func (s *Supervisor) LimiterOf(pluginID string) *limiter.ResourceLimiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limiters[pluginID]
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
	oldRL := s.limiters[pluginID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("plugin %s manifest not found (never started)", pluginID)
	}
	if oldRunning && oldCmd != nil {
		_ = oldCmd.Stop()
		if oldRL != nil {
			_ = oldRL.Stop()
		}
		s.mu.Lock()
		delete(s.procs, pluginID)
		delete(s.limiters, pluginID)
		s.mu.Unlock()
	}
	if _, err := s.Start(context.Background(), m); err != nil {
		return fmt.Errorf("restart plugin %s: %w", pluginID, err)
	}
	return nil
}

// Upgrade stops the currently running plugin (if any) and starts the new
// manifest. If the new Start fails, the supervisor restores the previous
// manifest in its index AND best-effort re-runs the previous version, so the
// plugin returns to a ready state without caller intervention. Caller-level
// recovery (e.g. upgrade admin API) should still treat the error as a failed
// upgrade and surface it; the rollback is a safety net so that callers like
// the lifecycle admin API don't leave a plugin half-stopped.
//
// If there was no previous version, the new failure simply clears state.
// Returns nil on success.
func (s *Supervisor) Upgrade(ctx context.Context, newManifest *Manifest) error {
	s.mu.Lock()
	oldManifest := s.manifests[newManifest.PluginID]
	oldCmd, oldRunning := s.procs[newManifest.PluginID]
	oldRL := s.limiters[newManifest.PluginID]
	s.mu.Unlock()

	// 1. stop old process first (it's about to be replaced)
	if oldRunning && oldCmd != nil {
		_ = oldCmd.Stop()
		if oldRL != nil {
			_ = oldRL.Stop()
		}
		s.mu.Lock()
		delete(s.procs, newManifest.PluginID)
		delete(s.limiters, newManifest.PluginID)
		s.mu.Unlock()
	}

	// 2. start new
	if _, err := s.Start(ctx, newManifest); err != nil {
		// rollback: restore old manifest, then auto-restart old so plugin is
		// ready (spec §3.4: upgrade failure must restore ready state).
		s.mu.Lock()
		if oldManifest != nil {
			s.manifests[newManifest.PluginID] = oldManifest
		} else {
			delete(s.manifests, newManifest.PluginID)
			delete(s.states, newManifest.PluginID)
		}
		s.mu.Unlock()
		if oldManifest != nil {
			if _, restartErr := s.Start(ctx, oldManifest); restartErr != nil {
				// even rollback Start failed — old process cannot be revived.
				// Best we can do is report both errors; caller may need to
				// deactivate / retry.
				return fmt.Errorf("upgrade plugin %s: %w (rollback start failed: %v)", newManifest.PluginID, err, restartErr)
			}
		}
		return fmt.Errorf("upgrade plugin %s: %w", newManifest.PluginID, err)
	}
	return nil
}

// Uninstall stops the plugin process if running and removes all per-plugin
// state from the supervisor in-memory index (procs/states/manifests). It does
// NOT touch on-disk bundles; the caller (lifecycle admin API or installer)
// is responsible for deleting files and catalog rows after a successful
// Uninstall.
//
// pluginID must match the supervisor's plugin_id regex (validated upstream);
// Uninstall never resolves paths so the directory traversal check in the
// lifecycle layer stays the single source of truth.
func (s *Supervisor) Uninstall(pluginID string) error {
	// Stop first to release the unix socket and the process; ignore
	// "not running" so a Stop-then-Uninstall from the admin API is idempotent.
	_ = s.Stop(pluginID)
	s.mu.Lock()
	delete(s.manifests, pluginID)
	s.mu.Unlock()
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
	// prepare 在 Start 内部创建 exec.Cmd 之后、c.Start() 之前被调用
	// （ResourceLimiter.Apply 借此在启动前注入资源限制）。cmd 不是构造期
	// 创建的——需要 CommandContext 的 ctx 取消语义——所以用钩子而非 Cmd()。
	prepare func(*exec.Cmd) error
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
	c.Env = append([]string(nil), e.env...)
	// 插件 stdout/stderr 经过 redacting writer 过滤掉 context secret 后再写入 gateway 日志流；
	// 防止插件把 env 打到日志里造成 HMAC secret 泄露。P5 计划改为结构化捕获。
	c.Stdout = NewRedactingWriter(e.secret, os.Stderr)
	c.Stderr = NewRedactingWriter(e.secret, os.Stderr)
	if e.prepare != nil {
		if err := e.prepare(c); err != nil {
			return fmt.Errorf("prepare exec %s: %w", e.entrypoint, err)
		}
	}
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

// Cmd 暴露底层 exec.Cmd，供观测/调试面查看启动后的进程句柄。未启动时返回
// nil。资源限制注入不走这里——exec.Cmd 是 Start 内部用 CommandContext 创建
// 的，启动前注入走 prepare 钩子（见 execCommand）。
func (e *execCommand) Cmd() *exec.Cmd {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cmd
}

// publishEvent 发布插件事件到 eventbus（如果已配置）
func (s *Supervisor) publishEvent(ctx context.Context, eventType, pluginID string, payload map[string]interface{}) {
	if s.cfg.EventBus == nil {
		return
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		// 不阻塞主流程，仅记录警告
		return
	}

	event := &eventbus.Event{
		ID:        fmt.Sprintf("%s-%d", pluginID, time.Now().UnixNano()),
		Type:      eventType,
		Source:    "supervisor",
		Timestamp: time.Now(),
		Payload:   payloadBytes,
	}

	// 非阻塞发布
	go func() {
		publishCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.cfg.EventBus.Publish(publishCtx, event)
	}()
}
