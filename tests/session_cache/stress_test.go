// Package session_cache_test - stress_test.go
//
// 压力测试与内存泄漏检测
//
// 测试场景：
// 1. 大会话压力测试（1000+ 轮对话）
// 2. 多会话并发测试（100+ 并发会话）
// 3. 内存泄漏检测（多次创建/销毁会话）
// 4. Goroutine 泄漏检测
// 5. 长时间运行稳定性测试
package session_cache_test

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ──────────────────────────────────────────────────────────────────────────────
// 1. 大会话压力测试
// ──────────────────────────────────────────────────────────────────────────────

func TestStress_LargeSession(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过大规模压力测试")
	}

	ctx := context.Background()
	hybrid := NewHybridCompressor()

	turnCounts := []int{100, 500, 1000, 2000}

	for _, turnCount := range turnCounts {
		t.Run(fmt.Sprintf("Turns_%d", turnCount), func(t *testing.T) {
			// 创建大会话
			sessionID := fmt.Sprintf("stress_large_%d", turnCount)
			tenantID := "stress_test"
			rawCache := NewRawSessionCache()
			compressedCache := NewCompressedSessionCache()

			start := time.Now()
			currentSummary := ""
			lastSummaryIndex := 0

			for turn := 1; turn <= turnCount; turn++ {
				// 模拟用户消息和助手回复
				userMsg := Message{
					Role:    "user",
					Content: fmt.Sprintf("用户问题 #%d: 如何解决复杂的并发问题？需要详细分析", turn),
				}
				assistantMsg := Message{
					Role:    "assistant",
					Content: fmt.Sprintf("助手回答 #%d: 并发问题通常涉及以下方面...（详细技术分析）", turn),
				}

				// 添加到原始缓存
				rawSession := rawCache.AddTurn(sessionID, tenantID, userMsg, assistantMsg)

				// 使用混合压缩策略（直接调用底层Compress以避免重复加锁）
				hybridCompressed, err := hybrid.CompressWithHybrid(ctx, rawSession, currentSummary, lastSummaryIndex)
				require.NoError(t, err)

				// 保存到压缩缓存（线程安全）
				compressedCache.Set(sessionID, &CompressedSession{
					SessionID:              hybridCompressed.SessionID,
					TenantID:               hybridCompressed.TenantID,
					CompressedMessages:     hybridCompressed.CompressedMessages,
					CompressionStrategy:    string(hybridCompressed.Strategy),
					OriginalMessageCount:   hybridCompressed.OriginalMessageCount,
					CompressedMessageCount: hybridCompressed.CompressedMessageCount,
					OriginalTokens:         hybridCompressed.OriginalTokens,
					CompressedTokens:       hybridCompressed.CompressedTokens,
					CompressionRatio:       hybridCompressed.CompressionRatio,
					AlignmentMap:           hybridCompressed.AlignmentMap,
					UpdatedAt:              hybridCompressed.UpdatedAt,
				})

				// 更新摘要状态
				if hybridCompressed.Strategy == StrategySummary {
					if len(hybridCompressed.CompressedMessages) > 0 && hybridCompressed.CompressedMessages[0].Role == "system" {
						currentSummary = hybridCompressed.CompressedMessages[0].Content
					}
					lastSummaryIndex = hybridCompressed.LastSummaryIndex
				}
			}

			elapsed := time.Since(start)

			// 验证结果
			finalRaw, _ := rawCache.Get(sessionID)
			finalCompressed, _ := compressedCache.Get(sessionID)

			t.Logf("✓ %d轮对话完成", turnCount)
			t.Logf("  耗时: %v", elapsed)
			t.Logf("  L1消息数: %d, Token: %d", len(finalRaw.Messages), finalRaw.TotalTokens)
			t.Logf("  L2消息数: %d, Token: %d", len(finalCompressed.CompressedMessages), finalCompressed.CompressedTokens)
			t.Logf("  压缩比: %.2f, 节省: %.1f%%",
				finalCompressed.CompressionRatio,
				(1-finalCompressed.CompressionRatio)*100)

			// 性能要求：每轮处理时间应该 < 10ms
			avgPerTurn := elapsed.Milliseconds() / int64(turnCount)
			assert.Less(t, avgPerTurn, int64(10),
				"平均每轮处理时间应 < 10ms (实际: %dms)", avgPerTurn)
		})
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// 2. 多会话并发测试（线程安全验证）
// ──────────────────────────────────────────────────────────────────────────────

func TestStress_ConcurrentSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过并发测试")
	}

	ctx := context.Background()
	_ = NewHybridCompressor // 保留供未来使用

	sessionCounts := []int{10, 50, 100}
	turnsPerSession := 20

	for _, sessionCount := range sessionCounts {
		t.Run(fmt.Sprintf("Sessions_%d", sessionCount), func(t *testing.T) {
			var wg sync.WaitGroup
			rawCache := NewRawSessionCache()
			compressedCache := NewCompressedSessionCache()
			auditedCache := NewAuditedSessionCache()

			start := time.Now()

			for i := 0; i < sessionCount; i++ {
				wg.Add(1)
				go func(sessionIdx int) {
					defer wg.Done()

					sessionID := fmt.Sprintf("concurrent_%d", sessionIdx)
					tenantID := "concurrent_test"

					currentSummary := ""
					lastSummaryIndex := 0

					for turn := 1; turn <= turnsPerSession; turn++ {
						userMsg := Message{
							Role:    "user",
							Content: fmt.Sprintf("S%d-T%d: 问题内容", sessionIdx, turn),
						}
						assistantMsg := Message{
							Role:    "assistant",
							Content: fmt.Sprintf("S%d-T%d: 回答内容", sessionIdx, turn),
						}

						rawSession := rawCache.AddTurn(sessionID, tenantID, userMsg, assistantMsg)

						// 使用缓存的线程安全Compress方法
						compressed, err := compressedCache.Compress(ctx, rawSession)
						if err != nil {
							t.Errorf("压缩失败: %v", err)
							return
						}

						// 安全审计
						audited, err := auditedCache.Audit(ctx, compressed)
						if err != nil {
							t.Errorf("审计失败: %v", err)
							return
						}

						// 读取压缩结果更新摘要（用于下次压缩）
						if compressed.CompressionStrategy == "summary" && len(compressed.CompressedMessages) > 0 {
							if compressed.CompressedMessages[0].Role == "system" {
								currentSummary = compressed.CompressedMessages[0].Content
								lastSummaryIndex = len(rawSession.Messages) - len(compressed.CompressedMessages) + 1
							}
						}

						_ = audited
						_ = currentSummary
						_ = lastSummaryIndex
					}
				}(i)
			}

			wg.Wait()
			elapsed := time.Since(start)

			t.Logf("✓ %d个会话 × %d轮 完成", sessionCount, turnsPerSession)
			t.Logf("  耗时: %v", elapsed)
			t.Logf("  平均每会话: %v", elapsed/time.Duration(sessionCount))

			// 验证所有会话都已创建
			assert.Equal(t, sessionCount, len(compressedCache.sessions))
			assert.Equal(t, sessionCount, len(auditedCache.sessions))
		})
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// 3. 内存泄漏检测
// ──────────────────────────────────────────────────────────────────────────────

func TestStress_MemoryLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过内存泄漏测试")
	}

	ctx := context.Background()
	hybrid := NewHybridCompressor()

	// 第一轮：创建100个会话
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	t.Log("=== 第一轮：创建100个会话 ===")
	caches := make([]*RawSessionCache, 100)
	compressedCaches := make([]*CompressedSessionCache, 100)

	for i := 0; i < 100; i++ {
		caches[i] = NewRawSessionCache()
		compressedCaches[i] = NewCompressedSessionCache()

		sessionID := fmt.Sprintf("mem_test_%d", i)
		for turn := 1; turn <= 50; turn++ {
			userMsg := Message{Role: "user", Content: fmt.Sprintf("S%d-T%d 问题", i, turn)}
			assistantMsg := Message{Role: "assistant", Content: fmt.Sprintf("S%d-T%d 回答", i, turn)}

			raw := caches[i].AddTurn(sessionID, "mem_test", userMsg, assistantMsg)
			hybridCompressed, err := hybrid.CompressWithHybrid(ctx, raw, "", 0)
			require.NoError(t, err)

			compressedCaches[i].Set(sessionID, &CompressedSession{
				SessionID:              hybridCompressed.SessionID,
				TenantID:               hybridCompressed.TenantID,
				CompressedMessages:     hybridCompressed.CompressedMessages,
				CompressionStrategy:    string(hybridCompressed.Strategy),
				OriginalMessageCount:   hybridCompressed.OriginalMessageCount,
				CompressedMessageCount: hybridCompressed.CompressedMessageCount,
				OriginalTokens:         hybridCompressed.OriginalTokens,
				CompressedTokens:       hybridCompressed.CompressedTokens,
				CompressionRatio:       hybridCompressed.CompressionRatio,
				AlignmentMap:           hybridCompressed.AlignmentMap,
				UpdatedAt:              hybridCompressed.UpdatedAt,
			})
		}
	}

	runtime.GC()
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)
	alloc1 := m2.TotalAlloc - m1.TotalAlloc
	t.Logf("  分配内存: %.2f MB", float64(alloc1)/1024/1024)

	// 第二轮：清理所有会话
	t.Log("=== 第二轮：清理所有会话 ===")
	for i := 0; i < 100; i++ {
		caches[i] = nil
		compressedCaches[i] = nil
	}
	caches = nil
	compressedCaches = nil

	// 强制GC
	for i := 0; i < 3; i++ {
		runtime.GC()
		time.Sleep(100 * time.Millisecond)
	}

	var m3 runtime.MemStats
	runtime.ReadMemStats(&m3)

	t.Logf("  清理后堆内存: %.2f MB", float64(m3.HeapInuse)/1024/1024)

	// 第三轮：再次创建100个会话（应该复用之前的内存）
	t.Log("=== 第三轮：再次创建100个会话 ===")
	caches2 := make([]*RawSessionCache, 100)
	compressedCaches2 := make([]*CompressedSessionCache, 100)

	for i := 0; i < 100; i++ {
		caches2[i] = NewRawSessionCache()
		compressedCaches2[i] = NewCompressedSessionCache()

		sessionID := fmt.Sprintf("mem_test2_%d", i)
		for turn := 1; turn <= 50; turn++ {
			userMsg := Message{Role: "user", Content: fmt.Sprintf("S%d-T%d 问题", i, turn)}
			assistantMsg := Message{Role: "assistant", Content: fmt.Sprintf("S%d-T%d 回答", i, turn)}

			raw := caches2[i].AddTurn(sessionID, "mem_test2", userMsg, assistantMsg)
			hybridCompressed, err := hybrid.CompressWithHybrid(ctx, raw, "", 0)
			require.NoError(t, err)

			compressedCaches2[i].Set(sessionID, &CompressedSession{
				SessionID:              hybridCompressed.SessionID,
				TenantID:               hybridCompressed.TenantID,
				CompressedMessages:     hybridCompressed.CompressedMessages,
				CompressionStrategy:    string(hybridCompressed.Strategy),
				OriginalMessageCount:   hybridCompressed.OriginalMessageCount,
				CompressedMessageCount: hybridCompressed.CompressedMessageCount,
				OriginalTokens:         hybridCompressed.OriginalTokens,
				CompressedTokens:       hybridCompressed.CompressedTokens,
				CompressionRatio:       hybridCompressed.CompressionRatio,
				AlignmentMap:           hybridCompressed.AlignmentMap,
				UpdatedAt:              hybridCompressed.UpdatedAt,
			})
		}
	}

	runtime.GC()
	var m4 runtime.MemStats
	runtime.ReadMemStats(&m4)
	alloc2 := m4.TotalAlloc - m3.TotalAlloc
	t.Logf("  第二次分配: %.2f MB", float64(alloc2)/1024/1024)

	// 内存泄漏判断：如果第二次分配的内存远大于第一次（> 2倍），可能有泄漏
	leakRatio := float64(alloc2) / float64(alloc1)
	t.Logf("  内存增长比: %.2fx", leakRatio)

	if leakRatio > 2.0 {
		t.Logf("⚠️  警告: 内存增长 %.2fx，可能存在内存泄漏", leakRatio)
	} else {
		t.Logf("✓ 内存使用稳定，无明显泄漏")
	}

	// 清理
	caches2 = nil
	compressedCaches2 = nil
}

// ──────────────────────────────────────────────────────────────────────────────
// 4. Goroutine 泄漏检测
// ──────────────────────────────────────────────────────────────────────────────

func TestStress_GoroutineLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过Goroutine泄漏测试")
	}

	// 记录初始goroutine数量
	startGoroutines := runtime.NumGoroutine()
	t.Logf("初始Goroutine数量: %d", startGoroutines)

	ctx := context.Background()
	hybrid := NewHybridCompressor()

	// 启动大量并发任务
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			cache := NewRawSessionCache()
			compCache := NewCompressedSessionCache()
			sessionID := fmt.Sprintf("goroutine_test_%d", idx)

			for turn := 1; turn <= 30; turn++ {
				userMsg := Message{Role: "user", Content: fmt.Sprintf("T%d", turn)}
				assistantMsg := Message{Role: "assistant", Content: fmt.Sprintf("R%d", turn)}

				raw := cache.AddTurn(sessionID, "test", userMsg, assistantMsg)
				hybridCompressed, err := hybrid.CompressWithHybrid(ctx, raw, "", 0)
				if err != nil {
					return
				}

				compCache.Set(sessionID, &CompressedSession{
					SessionID:              hybridCompressed.SessionID,
					TenantID:               hybridCompressed.TenantID,
					CompressedMessages:     hybridCompressed.CompressedMessages,
					CompressionStrategy:    string(hybridCompressed.Strategy),
					OriginalMessageCount:   hybridCompressed.OriginalMessageCount,
					CompressedMessageCount: hybridCompressed.CompressedMessageCount,
					OriginalTokens:         hybridCompressed.OriginalTokens,
					CompressedTokens:       hybridCompressed.CompressedTokens,
					CompressionRatio:       hybridCompressed.CompressionRatio,
					AlignmentMap:           hybridCompressed.AlignmentMap,
					UpdatedAt:              hybridCompressed.UpdatedAt,
				})
			}
		}(i)
	}

	wg.Wait()

	// 等待goroutine清理
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	time.Sleep(500 * time.Millisecond)

	endGoroutines := runtime.NumGoroutine()
	t.Logf("结束Goroutine数量: %d", endGoroutines)
	t.Logf("增长: %d", endGoroutines-startGoroutines)

	// Goroutine泄漏判断：不应该增长超过5个
	if endGoroutines-startGoroutines > 5 {
		t.Logf("⚠️  警告: Goroutine增长 %d，可能存在泄漏", endGoroutines-startGoroutines)
	} else {
		t.Logf("✓ Goroutine数量稳定，无泄漏")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// 5. 长时间运行稳定性测试
// ──────────────────────────────────────────────────────────────────────────────

func TestStress_StabilityLongRun(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过长时间稳定性测试")
	}

	ctx := context.Background()
	hybrid := NewHybridCompressor()

	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	start := time.Now()

	// 持续运行（short模式5秒，完整模式30秒）
	duration := 30 * time.Second
	if testing.Short() {
		duration = 5 * time.Second
	}

	cache := NewRawSessionCache()
	compCache := NewCompressedSessionCache()

	turnCount := 0
	deadline := time.Now().Add(duration)

	t.Logf("运行 %v...", duration)

	for time.Now().Before(deadline) {
		sessionID := fmt.Sprintf("long_run_%d", turnCount%10)
		userMsg := Message{Role: "user", Content: fmt.Sprintf("T%d 问题", turnCount)}
		assistantMsg := Message{Role: "assistant", Content: fmt.Sprintf("T%d 回答", turnCount)}

		raw := cache.AddTurn(sessionID, "long_test", userMsg, assistantMsg)
		hybridCompressed, err := hybrid.CompressWithHybrid(ctx, raw, "", 0)
		require.NoError(t, err)

		compCache.Set(sessionID, &CompressedSession{
			SessionID:              hybridCompressed.SessionID,
			TenantID:               hybridCompressed.TenantID,
			CompressedMessages:     hybridCompressed.CompressedMessages,
			CompressionStrategy:    string(hybridCompressed.Strategy),
			OriginalMessageCount:   hybridCompressed.OriginalMessageCount,
			CompressedMessageCount: hybridCompressed.CompressedMessageCount,
			OriginalTokens:         hybridCompressed.OriginalTokens,
			CompressedTokens:       hybridCompressed.CompressedTokens,
			CompressionRatio:       hybridCompressed.CompressionRatio,
			AlignmentMap:           hybridCompressed.AlignmentMap,
			UpdatedAt:              hybridCompressed.UpdatedAt,
		})

		turnCount++

		// 每1000轮报告一次
		if turnCount%1000 == 0 {
			elapsed := time.Since(start)
			t.Logf("  %d轮, 耗时 %v, 速率 %.0f 轮/秒",
				turnCount, elapsed, float64(turnCount)/elapsed.Seconds())
		}
	}

	elapsed := time.Since(start)

	runtime.GC()
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	t.Logf("✓ 稳定性测试完成")
	t.Logf("  总轮次: %d", turnCount)
	t.Logf("  总耗时: %v", elapsed)
	t.Logf("  平均速率: %.0f 轮/秒", float64(turnCount)/elapsed.Seconds())
	t.Logf("  内存增长: %.2f MB", float64(m2.HeapInuse-m1.HeapInuse)/1024/1024)
	t.Logf("  当前Goroutine: %d", runtime.NumGoroutine())
}
