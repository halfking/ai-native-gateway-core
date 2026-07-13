package envinjector

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SOPSDecrypter is the interface for decrypting SOPS envelopes.
type SOPSDecrypter interface {
	Decrypt(encPath string) ([]byte, error)
}

// DefaultSOPSDecrypter shells out to the sops binary.
type DefaultSOPSDecrypter struct{}

// Decrypt runs `sops --decrypt` on the given file and returns raw plaintext.
// It auto-detects the age private key at the standard SOPS location
// (~/.config/sops/age/keys.txt) if SOPS_AGE_KEY_FILE is not already set.
func (d *DefaultSOPSDecrypter) Decrypt(encPath string) ([]byte, error) {
	if _, err := exec.LookPath("sops"); err != nil {
		return nil, fmt.Errorf("sops binary not found in PATH: %w", err)
	}
	cmd := exec.Command("sops", "--decrypt", encPath)
	cmd.Stderr = os.Stderr
	// Auto-detect age key location if not explicitly set
	if os.Getenv("SOPS_AGE_KEY_FILE") == "" {
		if home, err := os.UserHomeDir(); err == nil {
			defaultKey := filepath.Join(home, ".config", "sops", "age", "keys.txt")
			if _, err := os.Stat(defaultKey); err == nil {
				cmd.Env = append(os.Environ(), "SOPS_AGE_KEY_FILE="+defaultKey)
			}
		}
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sops --decrypt %s failed: %w", encPath, err)
	}
	return out, nil
}

// sopsEnvelope is the JSON structure of a SOPS .enc file after decryption.
// The "data" field holds the actual credential payload (KEY=VALUE lines).
type sopsEnvelope struct {
	Data string `json:"data"`
}

// ParseDecrypted extracts KEY=VALUE pairs from the decrypted SOPS output.
// The output may be:
//   - JSON with a "data" field containing newline-separated KEY=VALUE lines
//   - Direct KEY=VALUE lines (dotenv format)
//   - JSON with top-level key-value pairs (each key is a credential)
func ParseDecrypted(raw []byte) (map[string]string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fmt.Errorf("decrypted payload is empty")
	}

	// Try JSON first (SOPS decrypts JSON files to JSON)
	if trimmed[0] == '{' {
		return parseJSON(trimmed)
	}

	// Fall back to dotenv format
	return parseDotenv(trimmed), nil
}

func parseJSON(s string) (map[string]string, error) {
	// Try single-data-field envelope first
	var env sopsEnvelope
	if err := json.Unmarshal([]byte(s), &env); err == nil && env.Data != "" {
		return parseDotenv(env.Data), nil
	}

	// Try flat key-value JSON
	var flat map[string]string
	if err := json.Unmarshal([]byte(s), &flat); err == nil && len(flat) > 0 {
		return flat, nil
	}

	// Try map[string]any and stringify values
	var raw map[string]any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("cannot parse decrypted JSON: %w", err)
	}
	result := make(map[string]string, len(raw))
	for k, v := range raw {
		if k == "sops" {
			continue // skip SOPS metadata
		}
		result[k] = fmt.Sprintf("%v", v)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("decrypted JSON has no credential keys (excluding sops metadata)")
	}
	return result, nil
}

func parseDotenv(s string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		// Strip surrounding quotes
		val = strings.Trim(val, `"'`)
		if key != "" {
			result[key] = val
		}
	}
	if len(result) == 0 {
		return nil // caller checks nil
	}
	return result
}

// FormatEval emits shell-compatible export statements for use with eval.
func FormatEval(vars map[string]string) string {
	var b strings.Builder
	for k, v := range vars {
		// Escape single quotes in values for safe shell embedding
		safe := strings.ReplaceAll(v, "'", `'\''`)
		fmt.Fprintf(&b, "export %s='%s'\n", k, safe)
	}
	return b.String()
}

// FormatJSON emits a JSON object of the variables.
func FormatJSON(vars map[string]string) (string, error) {
	b, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// FormatDotenv emits KEY=VALUE lines (no export prefix).
func FormatDotenv(vars map[string]string) string {
	var b strings.Builder
	for k, v := range vars {
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	return b.String()
}
