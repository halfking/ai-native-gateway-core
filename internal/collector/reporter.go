package collector

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultReportTimeout = 30 * time.Second

// HTTPReporter posts allowlisted metrics to the authority ingest endpoint.
type HTTPReporter struct {
	ReportURL string
	Token     string
	Client    *http.Client
	MaxRetry  int
}

// Report sends payload with bounded retries and exponential backoff.
func (r *HTTPReporter) Report(ctx context.Context, payload []byte) error {
	if r.ReportURL == "" {
		return fmt.Errorf("report url is empty")
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: defaultReportTimeout}
	}
	retries := r.MaxRetry
	if retries <= 0 {
		retries = 3
	}

	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
			}
		}
		if err := r.postOnce(ctx, client, payload); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func (r *HTTPReporter) postOnce(ctx context.Context, client *http.Client, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.ReportURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("ingest status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
