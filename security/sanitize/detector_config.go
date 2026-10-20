package sanitize

import (
	"fmt"
	"os"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

// patternConfig mirrors the rule families in configs/sensitive_patterns.yaml.
//
// r1039（2026-10-20）：新增 Medical 一族。
//
// ★ 缺陷本体：`medical:` 整族此前**从未被加载**——`parsePatternConfig` 只遍历
// PII / Secret / Financial 三族，于是 `medical_record` / `diagnosis` /
// `medication` 三条规则**静默失效**：病历号、诊断、用药信息以明文出网关。
// 运维读 YAML 会合理地以为这些规则是活的（它们写着 `enabled: true`）。
//
// ⇒ 现在按「YAML 里写了 enabled+patterns 的族就是规则源」统一加载。
//
// ★ 非规则段（global / whitelist / custom_rules / redaction_methods /
// performance / audit）**不是**规则源：它们没有 `patterns:` 映射，检测器
// 也不解析它们。逐条理由见 configs/sensitive_patterns.yaml 里的就地注释，
// 以及 security/sanitize/r1039_yaml_rule_coverage_test.go 的白名单说明。
type patternConfig struct {
	PII       patternCategory `yaml:"pii"`
	Secret    patternCategory `yaml:"secret"`
	Financial patternCategory `yaml:"financial"`
	Medical   patternCategory `yaml:"medical"`
}

type patternCategory struct {
	Enabled  bool                         `yaml:"enabled"`
	Patterns map[string]patternDefinition `yaml:"patterns"`
}

type patternDefinition struct {
	Regex  string   `yaml:"regex"`
	Group  int      `yaml:"group"`
	Kind   string   `yaml:"kind"`
	Scopes []string `yaml:"scopes"`
	Action string   `yaml:"action"`
}

// NewPatternDetectorFromFile loads the shared sensitive-pattern configuration.
// Callers should fall back to NewPatternDetector when loading fails.
func NewPatternDetectorFromFile(path string) (*PatternDetector, error) {
	detector := NewPatternDetector()
	if err := detector.BuildFromFile(path); err != nil {
		return nil, err
	}
	return detector, nil
}

// BuildFromFile replaces detector rules only after the file fully validates.
func (d *PatternDetector) BuildFromFile(path string) error {
	if d.RuleManager() != nil {
		return fmt.Errorf("sensitive patterns are version managed; publish through rule management")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read sensitive pattern config failed: %w (path=%s)", err, path)
	}
	patterns, err := parsePatternConfig(data)
	if err != nil {
		return fmt.Errorf("load sensitive pattern config failed: %w (path=%s)", err, path)
	}
	d.mu.Lock()
	d.patterns = patterns
	d.configPath = path
	d.mu.Unlock()
	return nil
}

// ReloadFromFile atomically refreshes the detector from its prior config path.
func (d *PatternDetector) ReloadFromFile() error {
	if d == nil {
		return fmt.Errorf("reload sensitive pattern config failed: detector is nil")
	}
	d.mu.RLock()
	path := d.configPath
	d.mu.RUnlock()
	if path == "" {
		return fmt.Errorf("reload sensitive pattern config failed: no config path set")
	}
	return d.BuildFromFile(path)
}

func parsePatternConfig(data []byte) ([]patternEntry, error) {
	var config patternConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse YAML failed: %w", err)
	}
	entries := make([]patternEntry, 0)
	// Every family that declares `enabled` + `patterns` in the YAML is a rule
	// source. r1039 added `medical` here: the family was fully written in the
	// config file and never loaded, so medical PII passed through in
	// plaintext while the file claimed `enabled: true`.
	//
	// The generic guard (security/sanitize/r1039_yaml_rule_coverage_test.go)
	// fails when a family or a pattern in the YAML is missing from this list,
	// so the next silently-dead family is caught by a test instead of by an
	// audit.
	for _, category := range []struct {
		config patternCategory
		name   string
	}{
		{config.PII, "pii"},
		{config.Secret, "secret"},
		{config.Financial, "financial"},
		{config.Medical, "medical"},
	} {
		if !category.config.Enabled {
			continue
		}
		names := make([]string, 0, len(category.config.Patterns))
		for name := range category.config.Patterns {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			pattern := category.config.Patterns[name]
			if pattern.Regex == "" {
				continue
			}
			re, err := regexp.Compile(pattern.Regex)
			if err != nil {
				return nil, fmt.Errorf("compile pattern failed: %w (name=%s)", err, name)
			}
			if pattern.Group < 0 || pattern.Group > re.NumSubexp() {
				return nil, fmt.Errorf("invalid capture group (name=%s)", name)
			}
			// The shipped `secret.api_key` rule is the same vendor-key shape
			// as the built-in fallback (audit 244 F2): a bare sk-/ak- style
			// match whose 16-char floor and digit requirement live in
			// credibleSecretCandidate. Marking it here is what keeps the
			// YAML-loaded production detector and the built-in fallback
			// behaving identically.
			//
			// The test is on the pattern's own text, not on the rule name:
			// a deployment may legitimately name its own short-token rule
			// `api_key`, and that rule means exactly what it says.
			vendorKeyShape := isVendorKeyShapeRegex(pattern.Regex)
			kind, scopes, action, err := patternMetadata(category.name, name, pattern)
			if err != nil {
				return nil, err
			}
			entries = append(entries, patternEntry{
				sType:          sensitiveTypeForPattern(category.name, name),
				regex:          re,
				group:          pattern.Group,
				vendorKeyShape: vendorKeyShape,
				id:             category.name + "." + name, kind: kind, scopes: scopes, action: action,
			})
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no supported enabled patterns")
	}
	return entries, nil
}

func sensitiveTypeForPattern(category, name string) SensitiveType {
	switch name {
	case "phone":
		return TypePhone
	case "id_card":
		return TypeIDCard
	case "email":
		return TypeEmail
	case "bank_card":
		return TypeCreditCard
	case "account", "credential_pair_account":
		return TypeAccount
	case "server_ip", "server_ipv6":
		return TypeServerIP
	case "internal_ip":
		return TypeInternalIP
	}
	if category == "secret" {
		return TypeSecret
	}
	return TypeCustom
}
