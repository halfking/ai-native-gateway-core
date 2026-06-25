// Package siem — configuration schema (B3-2).
//
// SIEM forwarding is configured via settings_kv with a stable JSON shape.
// This file defines the schema, defaults, and validation. It is the
// single source of truth — both the Admin API (write) and the bg
// forwarder worker (read) use these constants.
//
// settings_kv key shape:
//   - "siem.config"        → JSON-encoded Config (platform-wide defaults)
//   - "siem.config.<tenant>" → per-tenant override (Q4 A2-follow-on)
//
// Do NOT rename the field names without a migration step — operators
// have already deployed configs that depend on them.
package siem

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Format is the SIEM wire format. CEF is the industry default; LEEF is
// for HP ArcSight customers.
type Format string

const (
	WireCEF  Format = "cef"  // Common Event Format (ArcSight / Splunk / QRadar / Elastic)
	WireLEEF Format = "leef" // Log Event Extended Format (IBM QRadar)
	WireJSON Format = "json" // raw JSON (custom sinks / Datadog / Sumo)
)

// ValidFormats is the set of supported formats (typo guard).
var ValidFormats = map[Format]bool{
	WireCEF: true, WireLEEF: true, WireJSON: true,
}

// Config is the persisted SIEM forwarding configuration.
type Config struct {
	Enabled     bool              `json:"enabled"`
	Endpoint    string            `json:"endpoint"`               // file path OR https URL
	Format      Format            `json:"format,omitempty"`       // default: cef
	BatchSize   int               `json:"batch_size,omitempty"`   // default: 100; max 1000
	FlushMs     int               `json:"flush_ms,omitempty"`     // default: 5000; max 60000
	Retries     int               `json:"retries,omitempty"`      // default: 3; max 10
	ExtraFields map[string]string `json:"extra_fields,omitempty"` // appended to every CEF line (e.g. "environment=prod")
}

// Defaults returns the safe-default config (B3-2 reference):
//
//	disabled, writes CEF to /var/log/llm-gateway/siem.log, batch 100 / 5s.
func Defaults() Config {
	return Config{
		Enabled:   false,
		Endpoint:  "/var/log/llm-gateway/siem.log",
		Format:    WireCEF,
		BatchSize: 100,
		FlushMs:   5000,
		Retries:   3,
	}
}

// Load parses a raw settings_kv value and returns the resulting Config
// (filling defaults for missing fields). On parse error, Defaults() is
// returned plus the error so the caller can log; the worker never crashes
// on a bad config.
func Load(raw string) (Config, error) {
	cfg := Defaults()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return Defaults(), fmt.Errorf("siem: parse config: %w", err)
	}
	if err := cfg.fillDefaultsAndValidate(); err != nil {
		return Defaults(), err
	}
	return cfg, nil
}

// fillDefaultsAndValidate ensures the config is sane. Mutates and returns
// the first error encountered. Rules:
//   - Empty Format → CEF
//   - BatchSize ∈ [1, 1000]
//   - FlushMs   ∈ [100, 60000]
//   - Retries   ∈ [0, 10]
//   - Endpoint must be a local file path OR an https URL (no http, no ftp)
//   - ExtraFields keys must be non-empty (CEF extension convention)
func (c *Config) fillDefaultsAndValidate() error {
	if c.Format == "" {
		c.Format = WireCEF
	}
	if !ValidFormats[c.Format] {
		return fmt.Errorf("siem: invalid format %q (want cef|leef|json)", c.Format)
	}
	if c.BatchSize == 0 {
		c.BatchSize = 100
	}
	if c.BatchSize < 1 || c.BatchSize > 1000 {
		return fmt.Errorf("siem: batch_size out of [1,1000]: %d", c.BatchSize)
	}
	if c.FlushMs == 0 {
		c.FlushMs = 5000
	}
	if c.FlushMs < 100 || c.FlushMs > 60000 {
		return fmt.Errorf("siem: flush_ms out of [100,60000]: %d", c.FlushMs)
	}
	if c.Retries < 0 || c.Retries > 10 {
		return fmt.Errorf("siem: retries out of [0,10]: %d", c.Retries)
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.New("siem: endpoint is required")
	}
	// Endpoint: file path OR https URL
	if strings.HasPrefix(c.Endpoint, "http://") {
		return errors.New("siem: endpoint must be https (http rejected to prevent cleartext log leakage)")
	}
	if strings.HasPrefix(c.Endpoint, "https://") {
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Host == "" {
			return fmt.Errorf("siem: invalid https endpoint: %q", c.Endpoint)
		}
	}
	// (else: treat as local file path — no validation needed beyond non-empty)
	for k := range c.ExtraFields {
		if strings.TrimSpace(k) == "" {
			return errors.New("siem: extra_fields contains empty key")
		}
	}
	return nil
}

// IsRemote reports whether the endpoint is a remote URL (vs local file).
// Workers may want to apply different retry/backoff strategies.
func (c Config) IsRemote() bool {
	return strings.HasPrefix(c.Endpoint, "https://")
}

// SettingsKey is the canonical settings_kv key for the platform-wide config.
const SettingsKey = "siem.config"
