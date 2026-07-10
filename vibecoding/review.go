package vibecoding

import (
	"context"
	"log/slog"
	"strings"
)

// ReviewManager 代码审查管理器
type ReviewManager struct {
	store Store
}

// NewReviewManager 创建代码审查管理器
func NewReviewManager(store Store) *ReviewManager {
	return &ReviewManager{
		store: store,
	}
}

// CreateReview 创建代码审查
func (m *ReviewManager) CreateReview(ctx context.Context, sessionID *int64, tenantID, filePath, language, code string) (*Review, error) {
	// 执行代码审查
	reviewResult := m.analyzeCode(code, language)

	review := &Review{
		SessionID:    sessionID,
		TenantID:     tenantID,
		FilePath:     filePath,
		Language:     language,
		OriginalCode: code,
		ReviewResult: reviewResult,
		Score:        m.calculateScore(reviewResult),
	}

	if err := m.store.CreateReview(ctx, review); err != nil {
		slog.Error("create review failed", "error", err)
		return nil, err
	}

	slog.Info("review created", "review_id", review.ID, "score", review.Score)
	return review, nil
}

// GetReview 获取代码审查
func (m *ReviewManager) GetReview(ctx context.Context, id int64) (*Review, error) {
	return m.store.GetReview(ctx, id)
}

// ListReviews 列出代码审查
func (m *ReviewManager) ListReviews(ctx context.Context, sessionID *int64, offset, limit int) ([]Review, int, error) {
	return m.store.ListReviews(ctx, sessionID, offset, limit)
}

// GetReviewsBySession 获取会话的所有审查
func (m *ReviewManager) GetReviewsBySession(ctx context.Context, sessionID int64) ([]Review, error) {
	return m.store.GetReviewsBySession(ctx, sessionID)
}

// analyzeCode 分析代码（简化版本，实际应调用AI模型）
func (m *ReviewManager) analyzeCode(code, language string) map[string]interface{} {
	issues := []ReviewIssue{}
	suggestions := []string{}

	// 基础静态分析
	lines := strings.Split(code, "\n")

	// 检查常见问题
	for i, line := range lines {
		lineNum := i + 1

		// 检查注释
		if strings.TrimSpace(line) == "" {
			continue
		}

		// 检查TODO
		if strings.Contains(line, "TODO") || strings.Contains(line, "FIXME") {
			issues = append(issues, ReviewIssue{
				Line:     lineNum,
				Severity: "info",
				Message:  "Found TODO/FIXME comment",
				Category: "documentation",
			})
		}

		// 检查过长行
		if len(line) > 120 {
			issues = append(issues, ReviewIssue{
				Line:     lineNum,
				Severity: "warning",
				Message:  "Line too long (>120 characters)",
				Category: "style",
			})
		}
	}

	// 通用建议
	if len(lines) > 500 {
		suggestions = append(suggestions, "Consider splitting this file into smaller modules")
	}

	if !strings.Contains(code, "package") && language == "go" {
		suggestions = append(suggestions, "Missing package declaration")
	}

	// 计算复杂度（简化）
	complexity := len(lines) / 10
	if complexity > 50 {
		complexity = 50
	}

	maintainability := "good"
	if complexity > 30 {
		maintainability = "needs improvement"
	} else if complexity > 40 {
		maintainability = "poor"
	}

	summary := "Code analysis completed"
	if len(issues) > 0 {
		summary = "Found " + string(rune(len(issues))) + " issues"
	}

	return map[string]interface{}{
		"issues":          issues,
		"suggestions":     suggestions,
		"summary":         summary,
		"complexity":      complexity,
		"maintainability": maintainability,
	}
}

// calculateScore 计算代码评分
func (m *ReviewManager) calculateScore(reviewResult map[string]interface{}) float64 {
	baseScore := 100.0

	// 从issues中扣分
	if issues, ok := reviewResult["issues"].([]ReviewIssue); ok {
		for _, issue := range issues {
			switch issue.Severity {
			case "error":
				baseScore -= 10.0
			case "warning":
				baseScore -= 5.0
			case "info":
				baseScore -= 1.0
			}
		}
	}

	// 从复杂度中扣分
	if complexity, ok := reviewResult["complexity"].(int); ok {
		if complexity > 30 {
			baseScore -= float64(complexity-30) * 0.5
		}
	}

	// 确保分数在0-100之间
	if baseScore < 0 {
		baseScore = 0
	}
	if baseScore > 100 {
		baseScore = 100
	}

	return baseScore
}

// GetReviewStats 获取审查统计
func (m *ReviewManager) GetReviewStats(ctx context.Context, sessionID *int64) (*ReviewStats, error) {
	reviews, _, err := m.store.ListReviews(ctx, sessionID, 0, 1000)
	if err != nil {
		return nil, err
	}

	stats := &ReviewStats{
		TotalReviews: len(reviews),
	}

	if len(reviews) == 0 {
		return stats, nil
	}

	// 计算平均分
	var totalScore float64
	for _, review := range reviews {
		totalScore += review.Score
	}
	stats.AverageScore = totalScore / float64(len(reviews))

	return stats, nil
}

// ReviewStats 审查统计
type ReviewStats struct {
	TotalReviews int     `json:"total_reviews"`
	AverageScore float64 `json:"average_score"`
}
