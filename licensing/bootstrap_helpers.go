package licensing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultCenterURL = "https://llm.kxpms.cn"

func resolveCenterURL() string {
	for _, key := range []string{
		"LLM_GATEWAY_CENTER_URL",
		"LICENSE_AUTHORITY_URL",
		"MAINTAIN_SERVICE_URL",
		"OPS_COLLECT_URL",
	} {
		if v := strings.TrimRight(strings.TrimSpace(os.Getenv(key)), "/"); v != "" {
			return v
		}
	}
	return defaultCenterURL
}

// instanceIDPath returns the on-disk file path that holds the local instance ID.
// One physical machine == one instance ID (one license seat).
// The file is generated on first install (or first bootstrap call) and persisted
// to /var/lib/kx-gateway/instance.id, $LLM_GATEWAY_DATA_DIR/instance.id, or
// ~/.local/share/kx-gateway/instance.id in that order.
func instanceIDPath() string {
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_DATA_DIR")); v != "" {
		return filepath.Join(v, "instance.id")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "kx-gateway", "instance.id")
	}
	return "/var/lib/kx-gateway/instance.id"
}

// resolveLocalInstanceID returns the locally persisted instance ID, or generates
// and persists one if absent. The ID is derived from the hardware fingerprint
// hash so that reinstalls on the same machine keep the same instance seat.
// One physical device == one instance ID == one license seat.
func resolveLocalInstanceID() string {
	path := instanceIDPath()
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}
	fp, err := GenerateFingerprint()
	if err != nil || fp == nil {
		// Fall back to a random UUID; uniqueness still holds across installs.
		return persistInstanceID(path, fallbackUUID())
	}
	id := deriveInstanceIDFromFingerprint(fp.Hash())
	return persistInstanceID(path, id)
}

func fallbackUUID() string {
	b := make([]byte, 16)
	now := time.Now().UnixNano()
	for i := 0; i < 8; i++ {
		b[i] = byte(now >> (8 * i))
	}
	for i := 0; i < 8; i++ {
		b[8+i] = byte(now >> (8 * (7 - i)))
	}
	return "inst-" + hex.EncodeToString(b)
}

// deriveInstanceIDFromFingerprint makes the instance ID human-readable and
// stable per machine. Format: gw-<8 hex>. Changing the format would orphan
// existing instances — keep prefix "gw-" for backwards compatibility.
func deriveInstanceIDFromFingerprint(fpHash string) string {
	h := sha256.Sum256([]byte("instance|" + fpHash))
	return "gw-" + hex.EncodeToString(h[:8])
}

func persistInstanceID(path, id string) string {
	if path == "" {
		return id
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return id
	}
	if err := os.WriteFile(path, []byte(id), 0o644); err != nil {
		return id
	}
	return id
}

func bootstrapPostJSON(url string, payload any) (bool, any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return false, nil, err
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return false, nil, err
	}
	defer resp.Body.Close()
	var decoded any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode >= 200 && resp.StatusCode < 300, decoded, nil
}

func bootstrapErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func maskBootstrapLicenseKey(k string) string {
	if len(k) <= 8 {
		return k
	}
	return k[:4] + "••••" + k[len(k)-4:]
}

func primaryIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return ""
}

func gatewayHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func gatewayVersionEnv() string {
	if v := os.Getenv("LLM_GATEWAY_VERSION"); v != "" {
		return v
	}
	return "dev"
}

func mapBodyString(body any, key string) string {
	m, ok := body.(map[string]any)
	if !ok {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}
