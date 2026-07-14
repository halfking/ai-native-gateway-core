package collector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// forbiddenKeyFragments blocks sensitive business/session/credential fields.
var forbiddenKeyFragments = []string{
	"prompt", "completion", "conversation_id", "message_id", "session_id",
	"api_key", "secret_key", "access_key", "password", "authorization",
	"hostname", "ip_address", "mac_address", "email",
}

// ValidatePayload rejects unknown or forbidden telemetry fields before ingest.
func ValidatePayload(data []byte) error {
	if err := rejectForbiddenKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var payload RuntimeMetrics
	if err := dec.Decode(&payload); err != nil {
		return fmt.Errorf("payload does not match allowlist schema: %w", err)
	}
	if payload.InstanceID == "" {
		return fmt.Errorf("instance_id is required")
	}
	if payload.Version == "" {
		return fmt.Errorf("version is required")
	}
	return nil
}

func rejectForbiddenKeys(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return walkForbiddenKeys("", raw)
}

func walkForbiddenKeys(prefix string, value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			fullKey := key
			if prefix != "" {
				fullKey = prefix + "." + key
			}
			lower := strings.ToLower(key)
			for _, fragment := range forbiddenKeyFragments {
				if strings.Contains(lower, fragment) {
					return fmt.Errorf("forbidden field %q", fullKey)
				}
			}
			if err := walkForbiddenKeys(fullKey, child); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range typed {
			if err := walkForbiddenKeys(fmt.Sprintf("%s[%d]", prefix, i), child); err != nil {
				return err
			}
		}
	}
	return nil
}
