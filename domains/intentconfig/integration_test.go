package intentconfig

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
)

// TestIntegrationAnalyzer 集成测试（需要真实数据库连接）
// 运行: DATABASE_URL=postgres://... go test -v -run TestIntegration
func TestIntegrationAnalyzer(t *testing.T) {
	// 跳过单元测试模式
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// 连接数据库
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	// 创建配置管理器
	configMgr := NewManager(pool, logger)
	if err := configMgr.Start(ctx); err != nil {
		t.Fatalf("Failed to start config manager: %v", err)
	}
	defer configMgr.Stop()

	// 等待初始加载
	time.Sleep(100 * time.Millisecond)

	// 创建存储层
	evolutionStore := NewPGEvolutionStore(pool, logger)
	feedbackStore := NewPGFeedbackStore(pool, logger)

	// 创建分析器
	analyzer := NewAnalyzer(configMgr, evolutionStore, logger)

	// 测试会话ID
	sessionID := "integration_test_session_" + time.Now().Format("20060102_150405")
	tenantID := "test_tenant"

	t.Run("第1轮-代码意图", func(t *testing.T) {
		result, err := analyzer.Analyze(ctx, AnalysisRequest{
			SessionID:     sessionID,
			RequestID:     "req_001",
			TenantID:      tenantID,
			UserContent:   "请帮我实现一个快速排序算法",
			ContextLength: 100,
			HasImages:     false,
			ToolCount:     0,
			StoreContent:  false,
		})

		if err != nil {
			t.Fatalf("Analysis failed: %v", err)
		}

		// 验证结果
		if result.PrimaryIntent.Kind != IntentCode {
			t.Errorf("Expected Code intent, got %s", result.PrimaryIntent.Kind)
		}

		if result.PrimaryConfidence < 0.4 {
			t.Errorf("Expected confidence >= 0.4, got %.2f", result.PrimaryConfidence)
		}

		if result.TurnNumber != 1 {
			t.Errorf("Expected turn 1, got %d", result.TurnNumber)
		}

		if result.IntentDriftScore > 0.1 {
			t.Errorf("Expected low drift on first turn, got %.2f", result.IntentDriftScore)
		}

		t.Logf("✓ 第1轮: %s (置信度: %.2f)", result.PrimaryIntent.Kind, result.PrimaryConfidence)
	})

	t.Run("第2轮-继续代码", func(t *testing.T) {
		result, err := analyzer.Analyze(ctx, AnalysisRequest{
			SessionID:     sessionID,
			RequestID:     "req_002",
			TenantID:      tenantID,
			UserContent:   "```python\ndef quicksort(arr):\n    return arr\n```",
			ContextLength: 150,
			HasImages:     false,
			ToolCount:     0,
			StoreContent:  false,
		})

		if err != nil {
			t.Fatalf("Analysis failed: %v", err)
		}

		if result.PrimaryIntent.Kind != IntentCode {
			t.Errorf("Expected Code intent, got %s", result.PrimaryIntent.Kind)
		}

		if result.TurnNumber != 2 {
			t.Errorf("Expected turn 2, got %d", result.TurnNumber)
		}

		// 意图稳定，漂移应该很低
		if result.IntentDriftScore > 0.3 {
			t.Errorf("Expected low drift for stable intent, got %.2f", result.IntentDriftScore)
		}

		if result.IsIntentChanged {
			t.Error("Expected no intent change")
		}

		t.Logf("✓ 第2轮: %s (漂移: %.2f)", result.PrimaryIntent.Kind, result.IntentDriftScore)
	})

	t.Run("第3轮-意图切换到推理", func(t *testing.T) {
		result, err := analyzer.Analyze(ctx, AnalysisRequest{
			SessionID:     sessionID,
			RequestID:     "req_003",
			TenantID:      tenantID,
			UserContent:   "请证明快速排序的平均时间复杂度是O(n log n)",
			ContextLength: 200,
			HasImages:     false,
			ToolCount:     0,
			StoreContent:  false,
		})

		if err != nil {
			t.Fatalf("Analysis failed: %v", err)
		}

		if result.PrimaryIntent.Kind != IntentReasoning {
			t.Errorf("Expected Reasoning intent, got %s", result.PrimaryIntent.Kind)
		}

		if result.TurnNumber != 3 {
			t.Errorf("Expected turn 3, got %d", result.TurnNumber)
		}

		// 意图切换，漂移应该较高
		if result.IntentDriftScore < 0.3 {
			t.Logf("Warning: Expected high drift for intent change, got %.2f", result.IntentDriftScore)
		}

		if !result.IsIntentChanged {
			t.Error("Expected intent change detection")
		}

		t.Logf("✓ 第3轮: %s (漂移: %.2f, 切换: %s)", 
			result.PrimaryIntent.Kind, 
			result.IntentDriftScore,
			result.IntentShiftType)
	})

	t.Run("获取会话摘要", func(t *testing.T) {
		summary, err := analyzer.GetSessionSummary(ctx, sessionID, tenantID)
		if err != nil {
			t.Fatalf("Get summary failed: %v", err)
		}

		if summary.TotalTurns != 3 {
			t.Errorf("Expected 3 turns, got %d", summary.TotalTurns)
		}

		if summary.SwitchCount != 1 {
			t.Errorf("Expected 1 switch, got %d", summary.SwitchCount)
		}

		if summary.LatestIntent != string(IntentReasoning) {
			t.Errorf("Expected latest intent Reasoning, got %s", summary.LatestIntent)
		}

		t.Logf("✓ 会话摘要: %d轮, 主导意图=%s, 切换=%d次", 
			summary.TotalTurns,
			summary.DominantIntent,
			summary.SwitchCount)
	})

	t.Run("保存反馈", func(t *testing.T) {
		feedback := &Feedback{
			SessionID:           sessionID,
			RequestID:           "req_001",
			TenantID:            tenantID,
			PredictedIntent:     string(IntentCode),
			PredictedConfidence: 0.85,
			UserAcceptedModel:   boolPtr(true),
			UserRetryCount:      0,
		}

		if err := feedbackStore.Save(ctx, feedback); err != nil {
			t.Fatalf("Save feedback failed: %v", err)
		}

		if feedback.ID == 0 {
			t.Error("Expected feedback ID to be set")
		}

		t.Logf("✓ 反馈已保存 (ID: %d)", feedback.ID)
	})

	t.Run("更新配置", func(t *testing.T) {
		cfg := configMgr.GetConfig(tenantID)
		originalThreshold := cfg.DriftThreshold

		// 修改配置
		cfg.DriftThreshold = 0.2
		if err := configMgr.UpdateConfig(ctx, cfg); err != nil {
			t.Fatalf("Update config failed: %v", err)
		}

		// 立即重载
		time.Sleep(100 * time.Millisecond)

		// 验证更新
		newCfg := configMgr.GetConfig(tenantID)
		if newCfg.DriftThreshold != 0.2 {
			t.Errorf("Expected drift_threshold=0.2, got %.2f", newCfg.DriftThreshold)
		}

		// 恢复原值
		cfg.DriftThreshold = originalThreshold
		if err := configMgr.UpdateConfig(ctx, cfg); err != nil {
			t.Fatalf("Restore config failed: %v", err)
		}

		t.Logf("✓ 配置更新成功")
	})

	t.Logf("\n=== 集成测试完成 ===")
	t.Logf("会话ID: %s", sessionID)
	t.Logf("数据库: %s", os.Getenv("DATABASE_URL"))
}

func boolPtr(b bool) *bool {
	return &b
}
