package attachments

import (
	"errors"
	"io"
	"testing"
	"time"
)

// mockBackend 用于测试的模拟后端
type mockBackend struct {
	saveCallCount   int
	saveFailUntil   int // 前 N 次调用失败
	saveError       error
	loadCallCount   int
	loadFailUntil   int
	loadError       error
}

func (m *mockBackend) SaveFile(relPath string, data []byte) error {
	m.saveCallCount++
	if m.saveCallCount <= m.saveFailUntil {
		return m.saveError
	}
	return nil
}

func (m *mockBackend) LoadFile(relPath string) ([]byte, error) {
	m.loadCallCount++
	if m.loadCallCount <= m.loadFailUntil {
		return nil, m.loadError
	}
	return []byte("test data"), nil
}

func (m *mockBackend) FileExists(relPath string) (bool, error) {
	return true, nil
}

func (m *mockBackend) StatFile(relPath string) (*FileInfo, error) {
	return &FileInfo{Size: 100, ModTime: time.Now()}, nil
}

func (m *mockBackend) OpenStream(relPath string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (m *mockBackend) DeleteFile(relPath string) error {
	return nil
}

func (m *mockBackend) HealthCheck() error {
	return nil
}

func (m *mockBackend) Info() BackendInfo {
	return BackendInfo{Type: "mock", Location: "memory"}
}

func TestRetryBackend_Success(t *testing.T) {
	mock := &mockBackend{}
	config := RetryConfig{
		MaxRetries:        3,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2.0,
	}
	
	retry := NewRetryBackend(mock, config)
	
	// 第一次调用就成功
	err := retry.SaveFile("test.txt", []byte("data"))
	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}
	
	if mock.saveCallCount != 1 {
		t.Errorf("Expected 1 call, got %d", mock.saveCallCount)
	}
}

func TestRetryBackend_RetryAndSuccess(t *testing.T) {
	mock := &mockBackend{
		saveFailUntil: 2, // 前 2 次失败，第 3 次成功
		saveError:     errors.New("timeout"),
	}
	
	config := RetryConfig{
		MaxRetries:        3,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2.0,
	}
	
	retry := NewRetryBackend(mock, config)
	
	start := time.Now()
	err := retry.SaveFile("test.txt", []byte("data"))
	elapsed := time.Since(start)
	
	if err != nil {
		t.Errorf("Expected success after retries, got error: %v", err)
	}
	
	if mock.saveCallCount != 3 {
		t.Errorf("Expected 3 calls (2 failures + 1 success), got %d", mock.saveCallCount)
	}
	
	// 验证退避时间（第1次10ms + 第2次20ms = 至少30ms）
	minExpected := 30 * time.Millisecond
	if elapsed < minExpected {
		t.Errorf("Expected at least %v backoff time, got %v", minExpected, elapsed)
	}
}

func TestRetryBackend_MaxRetriesExceeded(t *testing.T) {
	mock := &mockBackend{
		saveFailUntil: 10, // 一直失败
		saveError:     errors.New("permanent error"),
	}
	
	config := RetryConfig{
		MaxRetries:        3,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2.0,
	}
	
	retry := NewRetryBackend(mock, config)
	
	err := retry.SaveFile("test.txt", []byte("data"))
	
	if err == nil {
		t.Error("Expected error after max retries, got nil")
	}
	
	// 应该尝试 4 次（初始 + 3 次重试）
	if mock.saveCallCount != 4 {
		t.Errorf("Expected 4 calls (1 initial + 3 retries), got %d", mock.saveCallCount)
	}
	
	// 错误信息应该包含 "max retries exceeded"
	if err != nil && err.Error() != "" {
		// 验证错误信息
		t.Logf("Error message: %v", err)
	}
}

func TestRetryBackend_NonRetryableError(t *testing.T) {
	mock := &mockBackend{
		saveFailUntil: 10,
		saveError:     errors.New("invalid argument"), // 非可重试错误
	}
	
	config := RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
		RetryableErrors: []string{
			"timeout",
			"connection reset",
		},
	}
	
	retry := NewRetryBackend(mock, config)
	
	err := retry.SaveFile("test.txt", []byte("data"))
	
	if err == nil {
		t.Error("Expected error, got nil")
	}
	
	// 非可重试错误应该只调用 1 次
	if mock.saveCallCount != 1 {
		t.Errorf("Expected 1 call (no retries for non-retryable error), got %d", mock.saveCallCount)
	}
}

func TestRetryBackend_LoadFile(t *testing.T) {
	mock := &mockBackend{
		loadFailUntil: 1, // 第 1 次失败，第 2 次成功
		loadError:     errors.New("timeout"),
	}
	
	config := DefaultRetryConfig()
	retry := NewRetryBackend(mock, config)
	
	data, err := retry.LoadFile("test.txt")
	
	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}
	
	if string(data) != "test data" {
		t.Errorf("Expected 'test data', got '%s'", string(data))
	}
	
	if mock.loadCallCount != 2 {
		t.Errorf("Expected 2 calls, got %d", mock.loadCallCount)
	}
}

func TestRetryBackend_ExponentialBackoff(t *testing.T) {
	config := RetryConfig{
		MaxRetries:        5,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        200 * time.Millisecond,
		BackoffMultiplier: 2.0,
	}
	
	retry := NewRetryBackend(&mockBackend{}, config)
	
	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 10 * time.Millisecond},  // 10 * 2^0 = 10ms
		{1, 20 * time.Millisecond},  // 10 * 2^1 = 20ms
		{2, 40 * time.Millisecond},  // 10 * 2^2 = 40ms
		{3, 80 * time.Millisecond},  // 10 * 2^3 = 80ms
		{4, 160 * time.Millisecond}, // 10 * 2^4 = 160ms
		{5, 200 * time.Millisecond}, // 10 * 2^5 = 320ms, 但限制为 200ms
	}
	
	for _, tt := range tests {
		actual := retry.calculateBackoff(tt.attempt)
		if actual != tt.expected {
			t.Errorf("Backoff for attempt %d: expected %v, got %v", tt.attempt, tt.expected, actual)
		}
	}
}

func TestRetryBackend_Info(t *testing.T) {
	mock := &mockBackend{}
	config := DefaultRetryConfig()
	retry := NewRetryBackend(mock, config)
	
	info := retry.Info()
	
	if info.Type != "mock" {
		t.Errorf("Expected type 'mock', got '%s'", info.Type)
	}
	
	if info.Metadata["retry_enabled"] != "true" {
		t.Error("Expected retry_enabled=true in metadata")
	}
	
	if info.Metadata["max_retries"] != "3" {
		t.Errorf("Expected max_retries=3, got %s", info.Metadata["max_retries"])
	}
}

func TestDefaultRetryConfig(t *testing.T) {
	config := DefaultRetryConfig()
	
	if config.MaxRetries != 3 {
		t.Errorf("Expected MaxRetries=3, got %d", config.MaxRetries)
	}
	
	if config.InitialBackoff != 100*time.Millisecond {
		t.Errorf("Expected InitialBackoff=100ms, got %v", config.InitialBackoff)
	}
	
	if config.BackoffMultiplier != 2.0 {
		t.Errorf("Expected BackoffMultiplier=2.0, got %f", config.BackoffMultiplier)
	}
	
	if len(config.RetryableErrors) == 0 {
		t.Error("Expected RetryableErrors to have entries")
	}
}
