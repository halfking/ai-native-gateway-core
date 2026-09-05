package pluginruntime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	m.ManifestPath = abs
	return &m, nil
}

func (m *Manifest) validate() error {
	if m.SchemaVersion != 0 && m.SchemaVersion != 1 && m.SchemaVersion != 2 {
		return fmt.Errorf("unsupported schema_version %d", m.SchemaVersion)
	}
	if m.PluginID == "" {
		return fmt.Errorf("plugin_id required")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`).MatchString(m.PluginID) {
		return fmt.Errorf("invalid plugin_id %q", m.PluginID)
	}
	if m.SchemaVersion >= 2 && !validSemVer(m.PluginVersion) {
		return fmt.Errorf("plugin_version must be strict SemVer")
	}
	contract := m.GatewayCompatibility.APIContract
	if contract != SupportedAPIContract && contract != SupportedAPIContractV2 {
		return fmt.Errorf("unsupported api_contract %q", contract)
	}
	if m.SchemaVersion == 2 && contract != SupportedAPIContractV2 {
		return fmt.Errorf("schema_version 2 requires %s", SupportedAPIContractV2)
	}
	if m.SchemaVersion < 2 && contract == SupportedAPIContractV2 {
		return fmt.Errorf("api_contract %s requires schema_version 2", SupportedAPIContractV2)
	}
	if err := validateStringSet("permissions", m.Permissions, m.Capabilities); err != nil {
		return err
	}
	if err := validateHooks(m.Hooks); err != nil {
		return err
	}
	if err := validateLicense(m.Activation.License); err != nil {
		return err
	}
	if err := validateConfigSchema(m.ConfigSchema, 0); err != nil {
		return err
	}
	if m.Runtime.Protocol != "" && m.Runtime.Protocol != "http-unix-socket" {
		return fmt.Errorf("unsupported runtime protocol %q", m.Runtime.Protocol)
	}
	if m.Runtime.Entrypoint == "" {
		return fmt.Errorf("runtime entrypoint required")
	}
	if filepath.IsAbs(m.Runtime.Entrypoint) || strings.Contains(filepath.Clean(m.Runtime.Entrypoint), ".."+string(filepath.Separator)) || filepath.Clean(m.Runtime.Entrypoint) == ".." {
		return fmt.Errorf("runtime entrypoint must remain inside plugin directory")
	}
	if m.Runtime.HandshakePath == "" || m.Runtime.HealthPath == "" || !strings.HasPrefix(m.Runtime.HandshakePath, "/") || !strings.HasPrefix(m.Runtime.HealthPath, "/") {
		return fmt.Errorf("runtime handshake/health path required and must be absolute")
	}
	if m.Runtime.ShutdownGraceSecs < 0 || m.Runtime.ShutdownGraceSecs > 300 {
		return fmt.Errorf("runtime shutdown_grace_seconds must be between 0 and 300")
	}
	for _, binding := range m.Bindings {
		if err := ValidateBinding(m.PluginID, binding, BindingValidationOptions{}); err != nil {
			return err
		}
	}
	// 2026-09-05 审计闭环8：远程插件菜单的 schema/裁剪校验（路径注入、
	// 自由文本 group、无界 order、tenant/platform 矛盾位）。
	if err := validateNavPages(m.PluginID, m.Pages); err != nil {
		return fmt.Errorf("invalid pages: %w", err)
	}
	for _, version := range []string{m.GatewayCompatibility.MinVersion, m.GatewayCompatibility.MaxVersion} {
		if version != "" && !validSemVer(version) {
			return fmt.Errorf("gateway compatibility version %q must be strict SemVer", version)
		}
	}
	return nil
}

var semVerPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

func validSemVer(v string) bool { return semVerPattern.MatchString(v) }

func validateStringSet(name string, values, allowed []string) error {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(allowed))
	for _, v := range allowed {
		set[strings.TrimSpace(v)] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			return fmt.Errorf("%s must not contain empty values", name)
		}
		if _, ok := set[v]; !ok {
			return fmt.Errorf("%s %q is not declared in capabilities", name, v)
		}
		if _, ok := seen[v]; ok {
			return fmt.Errorf("%s contains duplicate %q", name, v)
		}
		seen[v] = struct{}{}
	}
	return nil
}

func validateHooks(hooks []string) error {
	allowed := map[string]bool{"on_install": true, "on_activate": true, "on_deactivate": true, "on_uninstall": true}
	seen := map[string]bool{}
	for _, h := range hooks {
		if !allowed[h] {
			return fmt.Errorf("unsupported hook %q", h)
		}
		if seen[h] {
			return fmt.Errorf("duplicate hook %q", h)
		}
		seen[h] = true
	}
	return nil
}

func validateLicense(l License) error {
	mode := l.Mode
	if mode == "" {
		mode = "open"
	}
	if mode != "open" && mode != "entitlement" && mode != "offline-cert" {
		return fmt.Errorf("unsupported license mode %q", mode)
	}
	return nil
}

func validateConfigSchema(schema ConfigSchema, depth int) error {
	if schema == nil {
		return nil
	}
	if depth > 3 {
		return fmt.Errorf("config_schema nesting exceeds depth 3")
	}
	if len(schema) > 64 {
		return fmt.Errorf("config_schema has too many keys")
	}
	for key, value := range schema {
		if !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`).MatchString(key) {
			return fmt.Errorf("invalid config_schema key %q", key)
		}
		switch v := value.(type) {
		case map[string]any:
			if err := validateConfigSchema(ConfigSchema(v), depth+1); err != nil {
				return err
			}
		case []any:
			if len(v) > 64 {
				return fmt.Errorf("config_schema array too large")
			}
		default:
			// JSON scalar values are safe metadata.
		}
	}
	return nil
}

// CompareSemVer compares strict SemVer values (-1, 0, +1), including
// prerelease precedence. Build metadata does not affect precedence.
func CompareSemVer(a, b string) int {
	parse := func(v string) (major, minor, patch int, pre []string) {
		parts := strings.SplitN(v, "+", 2)
		core := strings.SplitN(parts[0], "-", 2)
		fmt.Sscanf(core[0], "%d.%d.%d", &major, &minor, &patch)
		if len(core) == 2 {
			pre = strings.Split(core[1], ".")
		}
		return
	}
	am, an, ap, apre := parse(a)
	bm, bn, bp, bpre := parse(b)
	if am != bm {
		if am < bm {
			return -1
		}
		return 1
	}
	if an != bn {
		if an < bn {
			return -1
		}
		return 1
	}
	if ap != bp {
		if ap < bp {
			return -1
		}
		return 1
	}
	if len(apre) == 0 && len(bpre) == 0 {
		return 0
	}
	if len(apre) == 0 {
		return 1
	}
	if len(bpre) == 0 {
		return -1
	}
	for i := 0; i < len(apre) && i < len(bpre); i++ {
		aID, bID := apre[i], bpre[i]
		if aID == bID {
			continue
		}
		ai, ae := strconv.Atoi(aID)
		bi, be := strconv.Atoi(bID)
		if ae == nil && be == nil {
			if ai < bi {
				return -1
			}
			return 1
		}
		if ae == nil {
			return -1
		}
		if be == nil {
			return 1
		}
		if aID < bID {
			return -1
		}
		return 1
	}
	if len(apre) < len(bpre) {
		return -1
	}
	if len(apre) > len(bpre) {
		return 1
	}
	return 0
}
