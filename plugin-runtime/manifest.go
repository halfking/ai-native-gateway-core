package pluginruntime

import (
	"bytes"
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
	// ★ 2026-11-25 12h 修订审计轮 P1-2：schema_version >= 3 的 manifest 走
	// **严格解码**（未知字段直接报错），而不是像 v1/v2 那样静默丢弃。
	//
	//	★ 为什么要分版本：encoding/json 默认**忽略**结构体里没有的字段 ⇒
	//	  一份声明了运行时不认识的字段的 manifest，会被「加载成功」而该字段
	//	  **无声丢失**（插件以为自己配了的东西根本没生效）⇒ 这正是
	//	  「缺席被当成数据」那一类假绿在**配置面**上的形态。
	//	★ 为什么只对 v3 收紧：v1/v2 的历史 manifest 可能带运行时不关心的
	//	  附加字段（仓内既有 manifest 就是这样），一次性收紧会让存量环境
	//	  全部加载失败 ⇒ 收紧必须**随版本号递进**，这正是版本号存在的意义。
	//	★ v3 因此有了「与 v2 不同的行为」：v3 承诺「我声明的每个字段都被
	//	  运行时认识」，不认识的当场报错而不是静默吞掉。
	if m.SchemaVersion >= strictManifestSchemaVersion {
		if err := strictUnmarshalManifest(data, &m); err != nil {
			return nil, fmt.Errorf("parse manifest (schema_version %d, strict): %w", m.SchemaVersion, err)
		}
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

// strictManifestSchemaVersion 是「严格解码」的起始 schema_version。
//
// ★ 取 3 的理由：schema_version 3 是本仓首个**显式放开**的版本号
//
//	（validate() 接受 0/1/2/3），而它的扩展字段（API / Requires / Provides /
//	Essential / Defaults / SettingsSchema / ClientAssets）在此之前就已存在
//	⇒ v3 与 v2 在结构上同构 ⇒ 若不给 v3 一个**可观察的差异**，版本号就只是
//	一个没有语义的标签（声明 v3 与声明 v2 完全同构）。
const strictManifestSchemaVersion = 3

// strictUnmarshalManifest 用 DisallowUnknownFields 解码 manifest。
//
// ★ 用 Decoder 而不是 json.Unmarshal：DisallowUnknownFields 只在 Decoder 上
//
//	可用（json.Unmarshal 没有这个开关）。
//
// ★ 报错信息里带**未知字段名**（encoding/json 的原话就是
//
//	`json: unknown field "xxx"`）⇒ 运维能直接看出是哪一项没被运行时认识。
func strictUnmarshalManifest(data []byte, m *Manifest) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(m); err != nil {
		return err
	}
	return nil
}

func (m *Manifest) validate() error {
	if m.SchemaVersion != 0 && m.SchemaVersion != 1 && m.SchemaVersion != 2 && m.SchemaVersion != 3 {
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
	if m.SchemaVersion >= 2 && contract != SupportedAPIContractV2 {
		return fmt.Errorf("schema_version %d requires %s", m.SchemaVersion, SupportedAPIContractV2)
	}
	if m.SchemaVersion < 2 && contract == SupportedAPIContractV2 {
		return fmt.Errorf("api_contract %s requires schema_version >= 2", SupportedAPIContractV2)
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
	if err := validateServiceNames("requires", m.Requires); err != nil {
		return err
	}
	if err := validateServiceNames("provides", m.Provides); err != nil {
		return err
	}
	if m.API != "" {
		trimmed := strings.TrimSpace(m.API)
		if !strings.HasPrefix(trimmed, "^") {
			return fmt.Errorf("api %q must be a caret range like ^1", m.API)
		}
		// Validate format: check that it's parseable
		apiVersion := strings.TrimPrefix(trimmed, "^")
		if _, _, ok := parseAPIVersion(apiVersion); !ok {
			return fmt.Errorf("api %q has invalid version format", m.API)
		}
		// Note: compatibility with HostAPI is checked at runtime, not during validation
	}
	if m.SettingsSchema != nil {
		if m.SettingsSchema.Type != "" && m.SettingsSchema.Type != "object" {
			return fmt.Errorf("settings_schema.type must be object")
		}
		if len(m.SettingsSchema.Properties) > 64 {
			return fmt.Errorf("settings_schema has too many properties")
		}
	}
	for _, a := range m.ClientAssets {
		if a == "" || filepath.IsAbs(a) || strings.Contains(filepath.Clean(a), ".."+string(filepath.Separator)) {
			return fmt.Errorf("client_assets entry %q must be a relative path inside the plugin", a)
		}
	}
	return nil
}

func validateServiceNames(label string, names []string) error {
	seen := map[string]struct{}{}
	re := regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			return fmt.Errorf("%s must not contain empty values", label)
		}
		if !re.MatchString(n) {
			return fmt.Errorf("invalid %s service name %q", label, n)
		}
		if _, ok := seen[n]; ok {
			return fmt.Errorf("%s contains duplicate %q", label, n)
		}
		seen[n] = struct{}{}
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
