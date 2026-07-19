package pluginruntime

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// CheckHealth 调用 GET {base}{healthPath}，200 视为健康。
func CheckHealth(client *http.Client, base, healthPath string) error {
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, base+healthPath, nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	return nil
}
