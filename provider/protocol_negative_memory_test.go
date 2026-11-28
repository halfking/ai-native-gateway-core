package provider

import (
	"testing"
	"time"
)

// TestProtocolNegativeMemory 验证负向记忆的基本功能。
func TestProtocolNegativeMemory(t *testing.T) {
	resetProtocolNegativeMemoryForTest()

	providerID := 123
	model := "test-model"
	protocol := "anthropic-messages"

	// 初始状态：未记录，应返回 false
	if isProtocolUnsupported(providerID, model, protocol) {
		t.Error("expected false for unrecorded entry")
	}

	// 记录负向证据
	recordProtocolUnsupported(providerID, model, protocol, 100*time.Millisecond)

	// 记录后应返回 true
	if !isProtocolUnsupported(providerID, model, protocol) {
		t.Error("expected true after recording")
	}

	// 不同的组合应返回 false
	if isProtocolUnsupported(providerID, "other-model", protocol) {
		t.Error("expected false for different model")
	}
	if isProtocolUnsupported(999, model, protocol) {
		t.Error("expected false for different provider")
	}
	if isProtocolUnsupported(providerID, model, "other-protocol") {
		t.Error("expected false for different protocol")
	}

	// 等待过期
	time.Sleep(150 * time.Millisecond)

	// 过期后应返回 false
	if isProtocolUnsupported(providerID, model, protocol) {
		t.Error("expected false after expiration")
	}
}

// TestProtocolNegativeMemoryTTL 验证 TTL 默认值和自定义值。
func TestProtocolNegativeMemoryTTL(t *testing.T) {
	resetProtocolNegativeMemoryForTest()

	providerID := 456
	model := "gpt-4"
	protocol := "gemini-generate"

	// 使用默认 TTL（应该是 24 小时，测试时用 0 触发默认）
	recordProtocolUnsupported(providerID, model, protocol, 0)

	if !isProtocolUnsupported(providerID, model, protocol) {
		t.Error("expected true with default TTL")
	}

	// 自定义短 TTL
	resetProtocolNegativeMemoryForTest()
	recordProtocolUnsupported(providerID, model, protocol, 50*time.Millisecond)

	if !isProtocolUnsupported(providerID, model, protocol) {
		t.Error("expected true before custom TTL expires")
	}

	time.Sleep(60 * time.Millisecond)

	if isProtocolUnsupported(providerID, model, protocol) {
		t.Error("expected false after custom TTL expires")
	}
}

// TestProtocolNegativeMemoryCleanup 验证过期条目清理。
func TestProtocolNegativeMemoryCleanup(t *testing.T) {
	resetProtocolNegativeMemoryForTest()

	// 记录多个条目，部分已过期
	recordProtocolUnsupported(1, "model-1", "proto-1", 10*time.Millisecond)
	recordProtocolUnsupported(2, "model-2", "proto-2", 1*time.Hour) // 长期有效

	time.Sleep(20 * time.Millisecond)

	// 手动触发清理
	cleanExpiredProtocolNegativeEntries()

	// 过期的应被删除
	if isProtocolUnsupported(1, "model-1", "proto-1") {
		t.Error("expected expired entry to be cleaned up")
	}

	// 未过期的应保留
	if !isProtocolUnsupported(2, "model-2", "proto-2") {
		t.Error("expected non-expired entry to remain")
	}
}

// TestProtocolNegativeMemoryConcurrency 验证并发安全性。
func TestProtocolNegativeMemoryConcurrency(t *testing.T) {
	resetProtocolNegativeMemoryForTest()

	done := make(chan bool)
	concurrency := 10

	// 并发写入
	for i := 0; i < concurrency; i++ {
		go func(id int) {
			recordProtocolUnsupported(id, "model", "protocol", 1*time.Hour)
			done <- true
		}(i)
	}

	// 等待所有写入完成
	for i := 0; i < concurrency; i++ {
		<-done
	}

	// 并发读取
	for i := 0; i < concurrency; i++ {
		go func(id int) {
			_ = isProtocolUnsupported(id, "model", "protocol")
			done <- true
		}(i)
	}

	// 等待所有读取完成
	for i := 0; i < concurrency; i++ {
		<-done
	}

	// 验证所有条目都存在
	for i := 0; i < concurrency; i++ {
		if !isProtocolUnsupported(i, "model", "protocol") {
			t.Errorf("expected entry %d to exist after concurrent writes", i)
		}
	}
}
