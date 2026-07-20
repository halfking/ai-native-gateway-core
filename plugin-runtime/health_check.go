package pluginruntime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// PreciseHealthCheck returns a health-check function that GETs healthPath over
// the plugin's unix socket. More precise than socket-only dial: catches a
// process that has the socket open but is wedged (returns non-200).
func PreciseHealthCheck(socketPath, healthPath string, timeout time.Duration) func() error {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport, Timeout: timeout}
	return func() error {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://unix"+healthPath, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("health check: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("health check status %d", resp.StatusCode)
		}
		return nil
	}
}
