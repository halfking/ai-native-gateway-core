package licensing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

// AutoRefreshToken 自动刷新 instance_token。
// 读取 refreshTokenPath，调用 masterURL/api/v1/instances/refresh，
// 成功后将新 instance_token 写入 instanceTokenPath。
// 失败时重试 3 次（延迟 5s / 30s / 120s）。
func AutoRefreshToken(ctx context.Context, masterURL, refreshTokenPath, instanceTokenPath string) error {
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

	// 重试策略：5s / 30s / 120s
	retryDelays := []time.Duration{5 * time.Second, 30 * time.Second, 120 * time.Second}
	var lastErr error

	for attempt := 0; attempt <= len(retryDelays); attempt++ {
		if attempt > 0 {
			delay := retryDelays[attempt-1]
			slog.Info("token_refresh: retrying after delay",
				"attempt", attempt+1,
				"delay", delay.String())
			select {
			case <-ctx.Done():
				return fmt.Errorf("token refresh cancelled: %w", ctx.Err())
			case <-time.After(delay):
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
				"attempt", attempt+1)
			return nil
		}

		lastErr = err
		// 如果是过期错误，不重试
		if errors.Is(err, ErrRefreshTokenExpired) {
			return err
		}

		slog.Warn("token_refresh: attempt failed",
			"attempt", attempt+1,
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
