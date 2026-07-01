package disguise

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Profile struct {
	Name                    string   `yaml:"name"`
	DeviceIDNormalize       bool     `yaml:"device_id_normalize"`
	BillingHeaderStrip      bool     `yaml:"billing_header_strip"`
	EnvBlockNormalize       bool     `yaml:"env_block_normalize"`
	ProcessMetricsNormalize bool     `yaml:"process_metrics_normalize"`
	ExtraStripHeaders       []string `yaml:"extra_strip_headers"`
}

type ProfileConfig struct {
	Profiles map[string]Profile `yaml:"profiles"`
}

var billingStripHeaders = []string{
	"x-anthropic-billing-header",
	"x-stainless-arch",
	"x-stainless-lang",
	"x-stainless-os",
	"x-stainless-package-version",
	"x-stainless-runtime",
	"x-stainless-runtime-version",
}

var envBlockRe = regexp.MustCompile(`(?s)<env>.*?</env>`)

func LoadProfiles(path string) map[string]Profile {
	if path == "" {
		path = defaultDisguisePath()
	}
	if path == "" {
		return map[string]Profile{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Debug("disguise profiles not loaded", "path", path, "error", err)
		return map[string]Profile{}
	}
	var config ProfileConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		slog.Warn("disguise profiles parse error", "error", err)
		return map[string]Profile{}
	}
	return config.Profiles
}

func Apply(body []byte, headers map[string]string, profile *Profile, tenantID string, applicationID int) ([]byte, map[string]string) {
	if profile == nil {
		return body, headers
	}

	if profile.DeviceIDNormalize {
		body = deviceIDNormalize(body, tenantID, applicationID)
	}

	if profile.EnvBlockNormalize {
		body = envBlockNormalize(body)
	}

	if profile.ProcessMetricsNormalize {
		body = processMetricsNormalize(body)
	}

	if profile.BillingHeaderStrip {
		headers = billingStrip(headers, profile.ExtraStripHeaders)
	}

	return body, headers
}

func deviceIDNormalize(body []byte, tenantID string, applicationID int) []byte {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}

	metaRaw, ok := obj["metadata"]
	if !ok {
		return body
	}

	var meta map[string]any
	if json.Unmarshal(metaRaw, &meta) != nil {
		return body
	}

	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", tenantID, applicationID)))
	deterministicID := fmt.Sprintf("%x", hash[:16])

	if _, hasUserID := meta["user_id"]; hasUserID {
		meta["user_id"] = deterministicID
	}
	if _, hasUser := meta["user"]; hasUser {
		meta["user"] = deterministicID
	}

	obj["metadata"], _ = json.Marshal(meta)
	result, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return result
}

func envBlockNormalize(body []byte) []byte {
	canonicalEnv := "<env>\n  <platform>linux</platform>\n  <arch>x86_64</arch>\n  <cwd>/home/user</cwd>\n</env>"
	return envBlockRe.ReplaceAll(body, []byte(canonicalEnv))
}

func processMetricsNormalize(body []byte) []byte {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}

	metaRaw, ok := obj["metadata"]
	if !ok {
		return body
	}

	var meta map[string]json.RawMessage
	if json.Unmarshal(metaRaw, &meta) != nil {
		return body
	}

	processRaw, ok := meta["process"]
	if !ok {
		return body
	}

	var process map[string]any
	if json.Unmarshal(processRaw, &process) != nil {
		return body
	}

	clampMapValue(process, "memory_mb", 512, 4096)
	clampMapValue(process, "heap_mb", 256, 2048)
	clampMapValue(process, "rss_mb", 128, 1024)
	clampMapValue(process, "uptime_s", 0, 86400)

	meta["process"], _ = json.Marshal(process)
	obj["metadata"], _ = json.Marshal(meta)
	result, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return result
}

func clampMapValue(m map[string]any, key string, minVal, maxVal float64) {
	v, ok := m[key]
	if !ok {
		return
	}
	switch n := v.(type) {
	case float64:
		if n < minVal {
			m[key] = minVal
		} else if n > maxVal {
			m[key] = maxVal
		}
	case json.Number:
		if f, err := n.Float64(); err == nil {
			if f < minVal {
				m[key] = minVal
			} else if f > maxVal {
				m[key] = maxVal
			}
		}
	case string:
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			if f < minVal {
				m[key] = minVal
			} else if f > maxVal {
				m[key] = maxVal
			}
		}
	}
}

func billingStrip(headers map[string]string, extra []string) map[string]string {
	strip := billingStripHeaders
	if len(extra) > 0 {
		strip = append(strip, extra...)
	}
	result := make(map[string]string, len(headers))
	for k, v := range headers {
		lower := strings.ToLower(k)
		stripped := false
		for _, s := range strip {
			if lower == s || strings.HasPrefix(lower, s) {
				stripped = true
				break
			}
		}
		if !stripped {
			result[k] = v
		}
	}
	return result
}

func IsEnabled() bool {
	v := os.Getenv("LLM_GATEWAY_ENABLE_DISGUISE")
	return strings.EqualFold(v, "true") || strings.EqualFold(v, "1") || strings.EqualFold(v, "yes")
}

func ProfileName(transformProfile, clientProfile string) string {
	if transformProfile != "" {
		return transformProfile
	}
	return clientProfile
}

func defaultDisguisePath() string {
	if v := os.Getenv("LLM_GATEWAY_DISGUISE_PROFILES_PATH"); v != "" {
		return v
	}
	return ""
}

func ShouldApply(body []byte) bool {
	return bytes.Contains(body, []byte("metadata")) || bytes.Contains(body, []byte("<env>"))
}
