package sanitize

import (
	"fmt"
	"os"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

type patternConfig struct {
	PII       patternCategory `yaml:"pii"`
	Secret    patternCategory `yaml:"secret"`
	Financial patternCategory `yaml:"financial"`
}

type patternCategory struct {
	Enabled  bool                         `yaml:"enabled"`
	Patterns map[string]patternDefinition `yaml:"patterns"`
}

type patternDefinition struct {
	Regex string `yaml:"regex"`
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
	for _, category := range []struct {
		config patternCategory
		name   string
	}{
		{config.PII, "pii"},
		{config.Secret, "secret"},
		{config.Financial, "financial"},
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
			entries = append(entries, patternEntry{sType: sensitiveTypeForPattern(category.name, name), regex: re})
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
	}
	if category == "secret" {
		return TypeSecret
	}
	return TypeCustom
}
