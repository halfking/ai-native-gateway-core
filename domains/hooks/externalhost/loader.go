// Package externalhost 的进程装载面：把外部钩子进程挂到 plugin-runtime supervisor 上。
//
// 本文件是 W3 宿主骨架的第二件（第一件是 host.go 的调用面）。
// 它复用 plugin-runtime 的既有能力，不重复造进程管理：
//
//   - supervisor.Start/Stop/Restart/SocketPathOf：进程生命周期与 socket 分配。
//   - manifest 校验（Runtime.Protocol=http-unix-socket / Entrypoint / HandshakePath / HealthPath）。
//   - handshake：装载前的就绪握手（版本、plugin_id、capabilities 比对）。
//   - PreciseHealthCheck：socket 存活探活。
//
// 装载流程（fail-closed，任何一步失败都不留残余进程）：
//
//	Start → Handshake → HealthCheck → 就绪
//
// 卸载流程：先摘流量（停止调用），再 Stop进程（释放 socket 与子进程）。
package externalhost

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// Loader 负责把外部钩子进程装载起来。
type Loader struct {
	sup *pluginruntime.Supervisor
	// handshakeTimeout 握手超时。
	handshakeTimeout time.Duration
	// healthTimeout 健康探活超时。
	healthTimeout time.Duration
	// httpClient 握手/探活用的 HTTP 客户端。
	httpClient *http.Client
}

// LoaderConfig 是 Loader 配置。
type LoaderConfig struct {
	// Supervisor 进程管理器；nil 表示不托管进程（只做握手与探活校验）。
	Supervisor *pluginruntime.Supervisor
	// HandshakeTimeout 握手超时，默认 5s。
	HandshakeTimeout time.Duration
	// HealthTimeout 健康探活超时，默认 3s。
	HealthTimeout time.Duration
	// HTTPClient 握手/探活用客户端；nil 时按 unix socket 自行构造。
	HTTPClient *http.Client
}

// NewLoader 构造 Loader。
func NewLoader(cfg LoaderConfig) *Loader {
	l := &Loader{
		sup:              cfg.Supervisor,
		handshakeTimeout: cfg.HandshakeTimeout,
		healthTimeout:    cfg.HealthTimeout,
		httpClient:       cfg.HTTPClient,
	}
	if l.handshakeTimeout <= 0 {
		l.handshakeTimeout = 5 * time.Second
	}
	if l.healthTimeout <= 0 {
		l.healthTimeout = 3 * time.Second
	}
	return l
}

// HookManifest描述一个外部钩子进程。
//
// 它复用 plugin-runtime 的 Manifest 结构（Runtime.Protocol 必须为
// "http-unix-socket"），因此外部钩子与普通插件共享同一套装载与生命周期语义——
// 这是"Go RPC 宿主复用 plugin-runtime"的核心收益。
type HookManifest = pluginruntime.Manifest

// LoadedHook 是一个已装载就绪的外部钩子。
type LoadedHook struct {
	// PluginID 钩子标识。
	PluginID string
	// SocketPath 钩子监听的 unix socket 路径。
	SocketPath string
	// Pid 钩子进程 PID（0 表示未由 supervisor 托管）。
	Pid int
	// Handshake 握手响应。
	Handshake *pluginruntime.HandshakeResponse
}

// Load 装载一个外部钩子进程。
//
// 语义：任一步失败即回滚（停止已启动的进程），不留下残余状态。
// 已就绪的钩子返回 LoadedHook；调用方据此构造 Host。
func (l *Loader) Load(ctx context.Context, m *HookManifest) (*LoadedHook, error) {
	if m == nil || m.PluginID == "" {
		return nil, fmt.Errorf("external hook manifest with plugin id required")
	}
	if m.Runtime.Protocol != "http-unix-socket" {
		return nil, fmt.Errorf("external hook %s: unsupported runtime protocol %q (want http-unix-socket)", m.PluginID, m.Runtime.Protocol)
	}

	var socketPath string
	if l.sup != nil {
		state, err := l.sup.Start(ctx, m)
		if err != nil {
			return nil, fmt.Errorf("external hook %s: start: %w", m.PluginID, err)
		}
		socketPath = state.SocketPath
	} else {
		// 无 supervisor 时，socket 路径由外部注入（测试或常驻进程场景）。
		socketPath = socketPathFromManifest(m)
		if socketPath == "" {
			return nil, fmt.Errorf("external hook %s: socket path required when supervisor is absent", m.PluginID)
		}
	}

	rollback := func(cause error) (*LoadedHook, error) {
		if l.sup != nil {
			_ = l.sup.Stop(m.PluginID)
		}
		return nil, cause
	}

	base := "http://unix"
	client := l.httpClient
	if client == nil {
		// unix 传输必须禁用环境代理（见 host.go unixHTTPClient 注释）。
		client = unixHTTPClient(socketPath, l.handshakeTimeout)
	}

	hsCtx, cancel := context.WithTimeout(ctx, l.handshakeTimeout)
	defer cancel()
	hs, err := pluginruntime.HandshakeContext(hsCtx, client, base, m.Runtime.HandshakePath, m)
	if err != nil {
		return rollback(fmt.Errorf("external hook %s: handshake: %w", m.PluginID, err))
	}

	health := pluginruntime.PreciseHealthCheck(socketPath, m.Runtime.HealthPath, l.healthTimeout)
	if err := health(); err != nil {
		return rollback(fmt.Errorf("external hook %s: health check: %w", m.PluginID, err))
	}

	loaded := &LoadedHook{PluginID: m.PluginID, SocketPath: socketPath, Handshake: hs}
	if l.sup != nil {
		if state := l.sup.ManifestOf(m.PluginID); state != nil {
			_ = state
		}
	}
	return loaded, nil
}

// Unload 卸载外部钩子进程。
func (l *Loader) Unload(pluginID string) error {
	if l.sup == nil {
		return nil
	}
	return l.sup.Stop(pluginID)
}

// socketPathFromManifest 从 manifest约定里推导 socket 路径。
//
// 约定：入口参数里的 {socket} 占位由 supervisor 替换；此处只处理
// supervisor 缺席的直连场景——从 Runtime.Entrypoint 的 --socket 参数解析。
func socketPathFromManifest(m *HookManifest) string {
	entry := m.Runtime.Entrypoint
	const flag = "--socket="
	for i := 0; i+len(flag) <= len(entry); i++ {
		if entry[i:i+len(flag)] == flag {
			return entry[i+len(flag):]
		}
	}
	return ""
}
