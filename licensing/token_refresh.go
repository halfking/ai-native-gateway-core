package licensing

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

var (
	// ErrRefreshTokenNotFound 表示 refresh_token 文件不存在
	ErrRefreshTokenNotFound = errors.New("refresh_token file not found")
	// ErrRefreshTokenExpired 表示 refresh_token 已过期（需重新 register）
	ErrRefreshTokenExpired = errors.New("refresh_token expired, re-registration required")
	// ErrRefreshFailed 表示刷新请求失败（网络/服务端错误）
	ErrRefreshFailed = errors.New("token refresh request failed")
)

// RefreshTokenRequest 是发送给主控端 /api/v1/instances/refresh 的请求体
type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// RefreshTokenResponse 是主控端返回的响应体
type RefreshTokenResponse struct {
	Success       bool   `json:"success"`
	InstanceToken string `json:"instance_token,omitempty"`
	Message       string `json:"message,omitempty"`
}

// DefaultBackoffConfig is the fallback retry configuration used when
// AutoRefreshToken is called without an explicit config. Exponential
// backoff with jitter, capped at 5 minutes, max 6 attempts:
//
//	attempt 1 (initial):  0s delay
//	attempt 2 (retry 1):  ~5s  (5s base × 2^0)
//	attempt 3 (retry 2):  ~10s
//	attempt 4 (retry 3):  ~20s
//	attempt 5 (retry 4):  ~40s
//	attempt 6 (retry 5):  ~80s (capped before 5min)
//
// Total wall time for a fully-failed cycle: ≈155s, well within the
// typical refresh-window budget.
var DefaultBackoffConfig = BackoffConfig{
	BaseDelay:      5 * time.Second,
	MaxDelay:       5 * time.Minute,
	MaxAttempts:    6,
	JitterFraction: 0.2,
}

// BackoffConfig controls AutoRefreshToken's retry behaviour. Production
// callers pass DefaultBackoffConfig; tests pass custom configs with
// shorter delays so the suite runs in <1s.
type BackoffConfig struct {
	// BaseDelay is the per-attempt delay multiplied by 2^(attempt-1).
	// First retry waits BaseDelay, second waits BaseDelay*2, etc.
	BaseDelay time.Duration

	// MaxDelay caps the per-attempt delay so a misconfigured loop
	// can't sleep for hours.
	MaxDelay time.Duration

	// MaxAttempts is the total number of HTTP requests (1 = initial
	// + N retries). Set to 1 to disable retries entirely.
	MaxAttempts int

	// JitterFraction is 0..1 — proportion of BaseDelay added as
	// random jitter to avoid thundering herd. 0 = no jitter.
	JitterFraction float64

	// Rand is the random source for jitter. Production callers leave
	// this nil so the package-level crypto/rand is used.
	Rand func(n int) int

	// Sleep is the wait primitive. Production callers leave this nil
	// so time.After / ctx-aware wait is used. Tests pass a no-op to
	// keep the suite fast.
	Sleep func(ctx context.Context, d time.Duration) error
}

// nextDelay computes the delay before attempt N (1-indexed). delay
// for attempt N is BaseDelay * 2^(N-2), capped at MaxDelay, plus
// uniform random jitter.
func (b BackoffConfig) nextDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 0
	}
	exp := attempt - 2
	delay := b.BaseDelay * time.Duration(1<<uint(math.Min(float64(exp), 16)))
	if delay > b.MaxDelay || delay < 0 {
		delay = b.MaxDelay
	}
	if b.JitterFraction > 0 {
		// pick a deterministic-ish random int for jitter.
		var offset int64
		if b.Rand != nil {
			offset = int64(b.Rand(int(float64(delay)*b.JitterFraction)) + 1)
		} else {
			var rndBuf [8]byte
			_, _ = rand.Read(rndBuf[:])
			_, num := binary.Uvarint(rndBuf[:])
			offset = int64(num)
		}
		jitterRange := int64(float64(delay) * b.JitterFraction)
		if jitterRange > 0 {
			offset = offset % jitterRange
			// 50/50 add or subtract
			if offset%2 == 0 {
				delay += time.Duration(offset)
			} else {
				delay -= time.Duration(offset)
			}
			if delay < 0 {
				delay = 0
			}
		}
	}
	return delay
}

// wait sleeps for d, returning early on context cancellation. The
// Sleep hook is overridable for tests so they don't pay the real
// backoff cost.
func (b BackoffConfig) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if b.Sleep != nil {
		return b.Sleep(ctx, d)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// AutoRefreshToken 自动刷新 instance_token。读取 refreshTokenPath，
// 调用 masterURL/api/v1/instances/refresh，成功后将新
// instance_token 写入 instanceTokenPath。
//
// The retry policy is governed by cfg; pass nil to use
// DefaultBackoffConfig. ErrRefreshTokenExpired short-circuits retries
// because the master has revoked the token — no amount of waiting
// will fix it.
func AutoRefreshToken(ctx context.Context, masterURL, refreshTokenPath, instanceTokenPath string) error {
	return AutoRefreshTokenWithConfig(ctx, masterURL, refreshTokenPath, instanceTokenPath, DefaultBackoffConfig)
}

// AutoRefreshTokenWithConfig is the policy-aware version. Exposed so
// tests and enterprise callers can tune the retry curve without
// affecting the default.
func AutoRefreshTokenWithConfig(
	ctx context.Context,
	masterURL, refreshTokenPath, instanceTokenPath string,
	cfg BackoffConfig,
) error {
	// 读取 refresh_token
	refreshToken, err := os.ReadFile(refreshTokenPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrRefreshTokenNotFound, refreshTokenPath)
		}
		return fmt.Errorf("failed to read refresh_token: %w", err)
	}

	token := string(bytes.TrimSpace(refreshToken))
	if token == "" {
		return fmt.Errorf("refresh_token is empty")
	}

	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultBackoffConfig.MaxAttempts
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = DefaultBackoffConfig.BaseDelay
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = DefaultBackoffConfig.MaxDelay
	}

	var lastErr error

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		// Wait the per-attempt delay BEFORE running attempt N (no
		// wait before attempt 1).
		if attempt > 1 {
			delay := cfg.nextDelay(attempt)
			slog.Info("token_refresh: retrying after delay",
				"attempt", attempt,
				"delay", delay.String())
			if err := cfg.wait(ctx, delay); err != nil {
				return fmt.Errorf("token refresh cancelled: %w", err)
			}
		}

		instanceToken, err := callRefreshAPI(ctx, masterURL, token)
		if err == nil {
			// 成功：写入新 token
			if writeErr := writeInstanceToken(instanceTokenPath, instanceToken); writeErr != nil {
				return fmt.Errorf("failed to write instance_token: %w", writeErr)
			}
			slog.Info("token_refresh: success",
				"instance_token_path", instanceTokenPath,
				"attempt", attempt)
			return nil
		}

		lastErr = err
		// ErrRefreshTokenExpired short-circuits — no retries help.
		if errors.Is(err, ErrRefreshTokenExpired) {
			return err
		}

		slog.Warn("token_refresh: attempt failed",
			"attempt", attempt,
			"max", cfg.MaxAttempts,
			"error", err)
	}

	return fmt.Errorf("%w: %v", ErrRefreshFailed, lastErr)
}

// callRefreshAPI 调用主控端刷新 API
func callRefreshAPI(ctx context.Context, masterURL, refreshToken string) (string, error) {
	endpoint := masterURL + "/api/v1/instances/refresh"

	reqBody := RefreshTokenRequest{
		RefreshToken: refreshToken,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		// 401 表示 refresh_token 过期
		return "", fmt.Errorf("%w: status 401", ErrRefreshTokenExpired)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var refreshResp RefreshTokenResponse
	if err := json.Unmarshal(body, &refreshResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if !refreshResp.Success {
		return "", fmt.Errorf("refresh failed: %s", refreshResp.Message)
	}

	if refreshResp.InstanceToken == "" {
		return "", fmt.Errorf("instance_token is empty in response")
	}

	return refreshResp.InstanceToken, nil
}

// writeInstanceToken 将 instance_token 写入文件（创建目录 + 原子写入）
func writeInstanceToken(path, token string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// 原子写入：写到临时文件再重命名
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(token), 0600); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}
