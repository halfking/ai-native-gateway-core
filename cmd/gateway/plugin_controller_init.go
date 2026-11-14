package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
	runtimeconfig "github.com/kaixuan/llm-gateway-go/plugin-runtime/config"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/controller"
	"gopkg.in/yaml.v3"
)

// Explicit configuration selects one lifecycle owner. Invalid configuration
// must never fall back to the legacy start-all behavior.
func pluginControllerDirectory(path, legacyDir, listen string) (string, error) {
	if path == "" {
		return legacyDir, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var cfg runtimeconfig.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", err
	}
	if err := runtimeconfig.NewValidator().Validate(&cfg); err != nil {
		return "", err
	}
	if cfg.Security.TLSEnabled {
		return "", fmt.Errorf("plugin runtime configuration cannot enable gateway TLS")
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	if strconv.Itoa(cfg.Server.Port) != port || normalizePluginListenHost(cfg.Server.Host) != normalizePluginListenHost(host) {
		return "", fmt.Errorf("runtime server must match gateway listen address %s", listen)
	}
	dir, err := filepath.Abs(cfg.Plugins.Directory)
	if err != nil {
		return "", err
	}
	if legacyDir != "" {
		legacy, err := filepath.Abs(legacyDir)
		if err != nil {
			return "", err
		}
		if legacy != dir {
			return "", fmt.Errorf("runtime plugins directory differs from LLM_GATEWAY_PLUGINS_DIR")
		}
	}
	return dir, nil
}

func normalizePluginListenHost(host string) string {
	if host == "0.0.0.0" || host == "::" {
		return ""
	}
	return host
}

func startPluginController(ctx context.Context, path string, registry *pluginruntime.Registry, capabilities *pluginruntime.CapabilityRegistry) (*controller.Controller, error) {
	return controller.Start(ctx, controller.Options{ConfigPath: path, Registry: registry, Capabilities: capabilities, SigningPubkey: os.Getenv("LLM_GATEWAY_PLUGIN_SIGNING_PUBKEY")})
}

// Configuration-managed plugins must not also be modified by imperative APIs.
// The legacy mode retains its existing activation/install endpoints.
type configManagedPluginControl struct{}

func (configManagedPluginControl) Activate(context.Context, string) error {
	return errPluginConfigManaged
}
func (configManagedPluginControl) Deactivate(context.Context, string) error {
	return errPluginConfigManaged
}
func (configManagedPluginControl) Uninstall(context.Context, string) error {
	return errPluginConfigManaged
}
func (configManagedPluginControl) Install(context.Context, string, string) error {
	return errPluginConfigManaged
}

var errPluginConfigManaged = &pluginLifecycleError{code: "plugin.managed_by_runtime_config", msg: "plugin lifecycle is managed by gateway.yaml; update the configured enabled list or installed bundle"}
