package bg

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func completeProbeJSONBody(body []byte, readErr error) string {
	if readErr != nil || len(body) == 0 || !json.Valid(body) {
		return ""
	}

	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return ""
	}
	changed := false
	value = redactProbeJSONValue(value, &changed)
	if changed {
		redacted, err := json.Marshal(value)
		if err != nil || !json.Valid(redacted) {
			return ""
		}
		return string(redacted)
	}
	return string(body)
}

func redactProbeJSONValue(value any, changed *bool) any {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if sensitiveProbeJSONKey(key) {
				if _, ok := child.(string); ok {
					value[key] = "<redacted>"
					*changed = true
					continue
				}
			}
			value[key] = redactProbeJSONValue(child, changed)
		}
		return value
	case []any:
		for index, child := range value {
			value[index] = redactProbeJSONValue(child, changed)
		}
		return value
	case string:
		redacted := string(errorsx.RedactCredentialShapes([]byte(value)))
		if redacted != value {
			*changed = true
		}
		return redacted
	default:
		return value
	}
}

func sensitiveProbeJSONKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
	switch key {
	case "token", "access_token", "refresh_token", "auth_token", "bearer_token", "api_key", "apikey", "authorization", "secret", "client_secret", "password", "credential":
		return true
	default:
		return strings.HasSuffix(key, "_api_key") || strings.HasSuffix(key, "_secret") || strings.HasSuffix(key, "_password")
	}
}
