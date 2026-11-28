// cmd/gateway/external_hook_init.go — W3 外部钩子的生产装配面。
//
// 2026-11-27 修订（W3 上线前必修项）：
//
// W3.1-W3.5 的代码事实上早已落地（提交 22fcf788c1 + 8deff3ac82），但
// `SetExternalHooks` 在 main 里**零调用**、全仓 `EXTERNAL_HOOK` env 零命中
// ⇒ 生产环境该链路完全不可达。本文件补上装配面：
//
//   - 配置开关（默认关）：LLM_GATEWAY_EXTERNAL_HOOK_ENABLED
//   - 钩子来源：LLM_GATEWAY_EXTERNAL_HOOK_MANIFEST（单个 manifest 路径）
//     或 LLM_GATEWAY_EXTERNAL_HOOKS_DIR（目录扫描，*.json）
//   - 装载：复用 plugin-runtime 的 Loader（Start→Handshake→Health，任一步失败即回滚）
//   - 接线：chatHandler.SetExternalHooks(integrator)
//
// 设计约束（安全默认值）：
//
//   - **默认关**：未显式开启时完全不装配，不产生任何外部调用。
//   - **单钩子优先**：多个 manifest 同时存在时只装第一个成功的，其余告警跳过
//     （W3 当前只支持单一 hook 宿主；多钩子编排属后续演进）。
//   - **装载失败不阻断启动**：钩子不可用时网关照常提供服务（W3.2 fail-open 精神），
//     但显式告警，避免"以为装上了其实没有"。
//   - **熔断器默认开启**：连续 5 次失败打开熔断 30s。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// externalHookConfig 是 W3 外部钩子的装配配置。
type externalHookConfig struct {
	// Enabled 总开关；默认 false（不装配）。
	Enabled bool
	// ManifestPaths 候选 manifest 路径（显式配置在前，目录扫描在后）。
	ManifestPaths []string
	// Timeout 单次钩子调用超时；<=0 用合同默认 1s。
	Timeout time.Duration
	// CircuitThreshold 熔断阈值；<=0 用默认 5 次。
	CircuitThreshold int
	// CircuitOpenWindow 熔断打开时长；<=0 用默认 30s。
	CircuitOpenWindow time.Duration
	// AllowRemoteBodyCapture 是否允许 remote 来源捕获正文；默认 false（W3.4 隐私原则）。
	AllowRemoteBodyCapture bool
	// TrustedProxyHops 受信反向代理层数（XFF 信任链，2026-11-28）；0 = 不信任 XFF。
	TrustedProxyHops int
}

// loadExternalHookConfig 从环境读取装配配置。
//
// 环境变量：
//
//	LLM_GATEWAY_EXTERNAL_HOOK_ENABLED         "1"/"true" 开启（默认关）
//	LLM_GATEWAY_EXTERNAL_HOOK_MANIFEST        单个 hook manifest 路径
//	LLM_GATEWAY_EXTERNAL_HOOKS_DIR            目录扫描（*.json）
//	LLM_GATEWAY_EXTERNAL_HOOK_TIMEOUT_MS      调用超时（默认 1000）
//	LLM_GATEWAY_EXTERNAL_HOOK_CIRCUIT_THRESHOLD    熔断阈值（默认 5）
//	LLM_GATEWAY_EXTERNAL_HOOK_CIRCUIT_WINDOW_MS     熔断窗口（默认 30000）
//	LLM_GATEWAY_EXTERNAL_HOOK_ALLOW_REMOTE_BODY    "1" 允许 remote 捕获正文（默认禁）
//	LLM_GATEWAY_EXTERNAL_HOOK_TRUSTED_PROXY_HOPS   受信反向代理层数（默认 0=不信任 XFF）
func loadExternalHookConfig() externalHookConfig {
	cfg := externalHookConfig{
		Enabled:                envBool("LLM_GATEWAY_EXTERNAL_HOOK_ENABLED", false),
		Timeout:                time.Duration(envInt("LLM_GATEWAY_EXTERNAL_HOOK_TIMEOUT_MS", 1000)) * time.Millisecond,
		CircuitThreshold:       envInt("LLM_GATEWAY_EXTERNAL_HOOK_CIRCUIT_THRESHOLD", externalhost.DefaultCircuitThreshold),
		CircuitOpenWindow:      time.Duration(envInt("LLM_GATEWAY_EXTERNAL_HOOK_CIRCUIT_WINDOW_MS", int(externalhost.DefaultCircuitOpenWindow.Milliseconds()))) * time.Millisecond,
		AllowRemoteBodyCapture: envBool("LLM_GATEWAY_EXTERNAL_HOOK_ALLOW_REMOTE_BODY", false),
		TrustedProxyHops:       envInt("LLM_GATEWAY_EXTERNAL_HOOK_TRUSTED_PROXY_HOPS", 0),
	}

	if p := strings.TrimSpace(os.Getenv("LLM_GATEWAY_EXTERNAL_HOOK_MANIFEST")); p != "" {
		cfg.ManifestPaths = append(cfg.ManifestPaths, p)
	}
	if dir := strings.TrimSpace(os.Getenv("LLM_GATEWAY_EXTERNAL_HOOKS_DIR")); dir != "" {
		cfg.ManifestPaths = append(cfg.ManifestPaths, scanHookManifests(dir)...)
	}
	return cfg
}

// scanHookManifests 扫描目录下的 *.json manifest（按路径排序保证确定性）。
func scanHookManifests(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Warn("external hook manifest dir unreadable", "dir", dir, "error", err)
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out
}

// initExternalHooks 完成 W3 外部钩子的装载与接线。
//
// 返回值：
//   - integrator 非 nil ⇒ 已装配成功，调用方应 SetExternalHooks(integrator)
//   - integrator 为 nil ⇒ 未装配（开关关闭 / 无 manifest / 装载失败），网关照常服务
//
// 失败语义：钩子装载失败**不阻断网关启动**（fail-open 精神），但显式告警。
func initExternalHooks(ctx context.Context, cfg externalHookConfig, sup *pluginruntime.Supervisor) *externalhost.PipelineIntegrator {
	if !cfg.Enabled {
		return nil
	}
	if len(cfg.ManifestPaths) == 0 {
		slog.Warn("external hook enabled but no manifest configured",
			"env_manifest", "LLM_GATEWAY_EXTERNAL_HOOK_MANIFEST",
			"env_dir", "LLM_GATEWAY_EXTERNAL_HOOKS_DIR")
		return nil
	}

	loader := externalhost.NewLoader(externalhost.LoaderConfig{Supervisor: sup})

	for _, path := range cfg.ManifestPaths {
		manifest, err := loadHookManifest(path)
		if err != nil {
			slog.Warn("external hook manifest load failed", "path", path, "error", err)
			continue
		}

		loaded, err := loader.Load(ctx, manifest)
		if err != nil {
			slog.Warn("external hook load failed", "plugin_id", manifest.PluginID, "path", path, "error", err)
			continue
		}

		host := externalhost.New(externalhost.Config{
			SocketPath:             loaded.SocketPath,
			Timeout:                cfg.Timeout,
			AllowRemoteBodyCapture: cfg.AllowRemoteBodyCapture,
		})
		integrator := externalhost.NewPipelineIntegratorWithCircuit(host, cfg.CircuitThreshold, cfg.CircuitOpenWindow)

		slog.Info("external hook wired",
			"plugin_id", manifest.PluginID,
			"socket", loaded.SocketPath,
			"timeout_ms", cfg.Timeout.Milliseconds(),
			"circuit_threshold", cfg.CircuitThreshold,
			"circuit_window_ms", cfg.CircuitOpenWindow.Milliseconds(),
			"allow_remote_body", cfg.AllowRemoteBodyCapture)
		return integrator
	}

	slog.Warn("external hook enabled but no hook could be loaded", "candidates", len(cfg.ManifestPaths))
	return nil
}

// loadHookManifest 读取并解析外部钩子 manifest。
func loadHookManifest(path string) (*pluginruntime.Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m pluginruntime.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if strings.TrimSpace(m.PluginID) == "" {
		return nil, fmt.Errorf("manifest missing plugin_id")
	}
	m.ManifestPath = path
	return &m, nil
}
