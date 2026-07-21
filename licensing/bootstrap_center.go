package licensing

import (
	"net/http"
	"sync"
	"time"
)

// centerAgentLoop keeps register + heartbeat retries in the background.
type centerAgentLoop struct {
	mu   sync.Mutex
	stop chan struct{}
}

func (h *BootstrapHandler) startCenterAgent(instanceID, licenseKey, hardwareHash string) {
	if instanceID == "" || h.centerURL == "" {
		return
	}
	h.agent.mu.Lock()
	defer h.agent.mu.Unlock()
	if h.agent.stop != nil {
		close(h.agent.stop)
	}
	stop := make(chan struct{})
	h.agent.stop = stop
	go func() {
		backoff := 2 * time.Second
		for {
			select {
			case <-stop:
				return
			default:
			}
			if h.probeCenter() {
				ok, _, _ := h.registerOnce(instanceID, licenseKey, hardwareHash, gatewayHostname(), gatewayVersionEnv(), "")
				if ok {
					_, _, _ = bootstrapPostJSON(h.centerURL+"/maintain-api/public/instances/heartbeat", map[string]any{
						"instance_id":   instanceID,
						"license_key":   licenseKey,
						"hardware_hash": hardwareHash,
						"status":        "online",
						"version":       gatewayVersionEnv(),
					})
					backoff = 30 * time.Second
				}
			}
			timer := time.NewTimer(backoff)
			select {
			case <-stop:
				timer.Stop()
				return
			case <-timer.C:
			}
			if backoff < 5*time.Minute {
				backoff *= 2
			}
		}
	}()
}

func (h *BootstrapHandler) registerOnce(instanceID, licenseKey, hardwareHash, host, version, region string) (bool, any, error) {
	return bootstrapPostJSON(h.centerURL+"/maintain-api/public/instances/register", map[string]any{
		"instance_id":   instanceID,
		"license_key":   licenseKey,
		"hardware_hash": hardwareHash,
		"hostname":      host,
		"version":       version,
		"region":        region,
		"status":        "online",
	})
}

func (h *BootstrapHandler) probeCenter() bool {
	if h.centerURL == "" {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(h.centerURL + "/maintain-api/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
