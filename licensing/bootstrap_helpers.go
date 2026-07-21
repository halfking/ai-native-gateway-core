package licensing

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func resolveCenterURL() string {
	for _, key := range []string{"LLM_GATEWAY_CENTER_URL", "LICENSE_AUTHORITY_URL", "MAINTAIN_SERVICE_URL"} {
		if v := strings.TrimRight(strings.TrimSpace(os.Getenv(key)), "/"); v != "" {
			return v
		}
	}
	return ""
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
