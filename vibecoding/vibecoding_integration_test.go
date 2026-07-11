//go:build integration

package vibecoding

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVibecodingIntegration tests the complete VibeCoding flow:
// Create Project → Create Session → Add Messages → Code Review → Complete Session → Statistics
//
// To run:
//
//	export LLM_GATEWAY_PG_URL=<your-postgres-dsn>
//	go test -tags=integration ./vibecoding -v -run TestVibecodingIntegration
func TestVibecodingIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err, "failed to connect to database")
	defer pool.Close()

	store := NewPgxStore(pool)

	// Clean up test data
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM vibecoding_reviews WHERE session_id IN (SELECT id FROM vibecoding_sessions WHERE tenant_id LIKE 'test-tenant-%')")
		_, _ = pool.Exec(ctx, "DELETE FROM vibecoding_sessions WHERE tenant_id LIKE 'test-tenant-%'")
		_, _ = pool.Exec(ctx, "DELETE FROM vibecoding_projects WHERE tenant_id LIKE 'test-tenant-%'")
	}()

	t.Run("CompleteWorkflow", func(t *testing.T) {
		tenantID := "test-tenant-" + time.Now().Format("20060102-150405")

		// Step 1: Create Project
		project := &Project{
			TenantID:    tenantID,
			Name:        "AI-Powered E-commerce Platform",
			Description: "Building an e-commerce platform with AI recommendations",
			Language:    "Go",
			Framework:   "Echo",
			Status:      ProjectStatusActive,
			Settings: map[string]interface{}{
				"linter":         "golangci-lint",
				"test_framework": "testify",
				"coverage_min":   80,
			},
			CreatedBy: "developer@example.com",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		err := store.CreateProject(ctx, project)
		require.NoError(t, err, "CreateProject should succeed")
		assert.Greater(t, project.ID, int64(0), "Project ID should be assigned")

		// Step 2: Create Session
		sessionID := "session-" + time.Now().Format("20060102-150405-000")
		session := &Session{
			ProjectID: &project.ID,
			TenantID:  tenantID,
			SessionID: sessionID,
			TaskType:  "feature_development",
			Status:    SessionStatusActive,
			Messages:  []Message{},
			Metadata: map[string]interface{}{
				"feature":  "product_recommendation",
				"priority": "high",
			},
			CreatedAt: time.Now(),
		}

		err = store.CreateSession(ctx, session)
		require.NoError(t, err, "CreateSession should succeed")
		assert.Greater(t, session.ID, int64(0), "Session ID should be assigned")

		// Step 3: Add Messages (Conversation)
		messages := []Message{
			{
				Role:      "user",
				Content:   "I need to implement a product recommendation engine based on user browsing history",
				Timestamp: time.Now(),
			},
			{
				Role:      "assistant",
				Content:   "I'll help you create a recommendation engine. Let's start with the data model...",
				Timestamp: time.Now(),
			},
			{
				Role:      "user",
				Content:   "Can you implement collaborative filtering?",
				Timestamp: time.Now(),
			},
			{
				Role:      "assistant",
				Content:   "Sure! Here's the implementation using collaborative filtering algorithm...",
				Timestamp: time.Now(),
			},
		}

		session.Messages = messages
		err = store.UpdateSession(ctx, session)
		require.NoError(t, err, "UpdateSession with messages should succeed")

		// Verify messages saved
		retrieved, err := store.GetSession(ctx, session.ID)
		require.NoError(t, err)
		assert.Len(t, retrieved.Messages, 4, "Should have 4 messages")
		assert.Equal(t, "user", retrieved.Messages[0].Role)

		// Step 4: Code Review
		review := &Review{
			SessionID: &session.ID,
			TenantID:  tenantID,
			FilePath:  "internal/recommendation/engine.go",
			Language:  "go",
			OriginalCode: `package recommendation

import "math"

func CosineSimilarity(a, b []float64) float64 {
	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}`,
			ReviewResult: map[string]interface{}{
				"issues": []ReviewIssue{
					{
						Line:     5,
						Severity: "warning",
						Message:  "Consider adding bounds checking for array indices",
						Category: "safety",
					},
					{
						Line:     10,
						Severity: "info",
						Message:  "Consider adding documentation for the function",
						Category: "documentation",
					},
				},
				"suggestions": []string{
					"Add unit tests for edge cases (empty arrays, zero vectors)",
					"Consider adding error handling for division by zero",
					"Export the function if it's part of public API",
				},
				"summary":         "Good implementation of cosine similarity. Minor improvements suggested.",
				"complexity":      5,
				"maintainability": "high",
			},
			Score:     8.5,
			CreatedAt: time.Now(),
		}

		err = store.CreateReview(ctx, review)
		require.NoError(t, err, "CreateReview should succeed")
		assert.Greater(t, review.ID, int64(0))

		// Step 5: Add more reviews
		review2 := &Review{
			SessionID:    &session.ID,
			TenantID:     tenantID,
			FilePath:     "internal/recommendation/filter.go",
			Language:     "go",
			OriginalCode: "package recommendation\n\n// Filter implementation",
			ReviewResult: map[string]interface{}{
				"issues": []ReviewIssue{
					{
						Line:     3,
						Severity: "error",
						Message:  "Incomplete implementation",
						Category: "completeness",
					},
				},
				"suggestions":     []string{"Complete the filter implementation"},
				"summary":         "Incomplete code",
				"complexity":      1,
				"maintainability": "low",
			},
			Score:     4.0,
			CreatedAt: time.Now(),
		}

		err = store.CreateReview(ctx, review2)
		require.NoError(t, err)

		// Get all reviews for session
		sessionReviews, err := store.GetReviewsBySession(ctx, session.ID)
		require.NoError(t, err)
		assert.Len(t, sessionReviews, 2, "Should have 2 reviews")

		// Calculate average score
		var totalScore float64
		for _, r := range sessionReviews {
			totalScore += r.Score
		}
		avgScore := totalScore / float64(len(sessionReviews))
		t.Logf("Average review score: %.2f", avgScore)
		assert.InDelta(t, 6.25, avgScore, 0.1)

		// Step 6: Complete Session
		completedAt := time.Now()
		err = store.UpdateSessionStatus(ctx, session.ID, SessionStatusCompleted, &completedAt)
		require.NoError(t, err, "UpdateSessionStatus should succeed")

		// Verify completion
		completed, err := store.GetSession(ctx, session.ID)
		require.NoError(t, err)
		assert.Equal(t, SessionStatusCompleted, completed.Status)
		assert.NotNil(t, completed.CompletedAt)

		// Step 7: Query by SessionID
		bySessionID, err := store.GetSessionBySessionID(ctx, sessionID)
		require.NoError(t, err)
		assert.Equal(t, session.ID, bySessionID.ID)
		assert.Equal(t, sessionID, bySessionID.SessionID)
	})

	t.Run("ProjectManagement", func(t *testing.T) {
		tenantID := "test-tenant-pm-" + time.Now().Format("150405")

		// Create multiple projects
		projects := []Project{
			{
				TenantID:    tenantID,
				Name:        "Web Dashboard",
				Description: "Admin dashboard for monitoring",
				Language:    "TypeScript",
				Framework:   "React",
				Status:      ProjectStatusActive,
				CreatedBy:   "dev@example.com",
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			},
			{
				TenantID:    tenantID,
				Name:        "Mobile App",
				Description: "iOS and Android mobile application",
				Language:    "Dart",
				Framework:   "Flutter",
				Status:      ProjectStatusActive,
				CreatedBy:   "dev@example.com",
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			},
			{
				TenantID:    tenantID,
				Name:        "Legacy System",
				Description: "Old system to be archived",
				Language:    "Java",
				Framework:   "Spring",
				Status:      ProjectStatusArchived,
				CreatedBy:   "dev@example.com",
				CreatedAt:   time.Now().Add(-365 * 24 * time.Hour),
				UpdatedAt:   time.Now(),
			},
		}

		for i := range projects {
			err := store.CreateProject(ctx, &projects[i])
			require.NoError(t, err, "CreateProject should succeed for project %d", i)
		}

		// List active projects
		activeProjects, total, err := store.ListProjects(ctx, tenantID, ProjectStatusActive, 0, 10)
		require.NoError(t, err)
		assert.Equal(t, 2, len(activeProjects), "Should have 2 active projects")
		assert.Equal(t, 2, total)

		// List archived projects
		archivedProjects, archivedTotal, err := store.ListProjects(ctx, tenantID, ProjectStatusArchived, 0, 10)
		require.NoError(t, err)
		assert.Equal(t, 1, len(archivedProjects))
		assert.Equal(t, 1, archivedTotal)

		// Update project
		projects[0].Description = "Updated: Comprehensive admin dashboard"
		projects[0].UpdatedAt = time.Now()
		err = store.UpdateProject(ctx, &projects[0])
		require.NoError(t, err)

		// Verify update
		updated, err := store.GetProject(ctx, projects[0].ID)
		require.NoError(t, err)
		assert.Contains(t, updated.Description, "Updated:")

		// Delete project
		err = store.DeleteProject(ctx, projects[2].ID)
		require.NoError(t, err)

		// Verify deletion
		_, err = store.GetProject(ctx, projects[2].ID)
		assert.Error(t, err, "GetProject should fail after deletion")
	})

	t.Run("SessionFiltering", func(t *testing.T) {
		tenantID := "test-tenant-sf-" + time.Now().Format("150405")

		// Create project
		project := &Project{
			TenantID:    tenantID,
			Name:        "Test Project",
			Description: "Project for session filtering test",
			Language:    "Python",
			Framework:   "Django",
			Status:      ProjectStatusActive,
			CreatedBy:   "dev@example.com",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		err := store.CreateProject(ctx, project)
		require.NoError(t, err)

		// Create sessions with different statuses
		sessionStatuses := []SessionStatus{
			SessionStatusActive,
			SessionStatusCompleted,
			SessionStatusFailed,
			SessionStatusCancelled,
		}

		for i, status := range sessionStatuses {
			session := &Session{
				ProjectID: &project.ID,
				TenantID:  tenantID,
				SessionID: "session-" + time.Now().Format("150405") + "-" + string(rune('0'+i)),
				TaskType:  "test_task",
				Status:    status,
				Messages:  []Message{},
				Metadata:  map[string]interface{}{},
				CreatedAt: time.Now(),
			}

			if status != SessionStatusActive {
				now := time.Now()
				session.CompletedAt = &now
			}

			err := store.CreateSession(ctx, session)
			require.NoError(t, err)
			time.Sleep(10 * time.Millisecond)
		}

		// List sessions by project
		projectSessions, total, err := store.ListSessions(ctx, &project.ID, "", 0, 10)
		require.NoError(t, err)
		assert.Equal(t, 4, len(projectSessions), "Should have 4 sessions for project")
		assert.Equal(t, 4, total)

		// List only active sessions
		activeSessions, activeTotal, err := store.ListSessions(ctx, &project.ID, SessionStatusActive, 0, 10)
		require.NoError(t, err)
		assert.Equal(t, 1, len(activeSessions))
		assert.Equal(t, 1, activeTotal)

		// List completed sessions
		completedSessions, completedTotal, err := store.ListSessions(ctx, &project.ID, SessionStatusCompleted, 0, 10)
		require.NoError(t, err)
		assert.Equal(t, 1, len(completedSessions))
		assert.Equal(t, 1, completedTotal)

		// List all sessions for tenant (across projects)
		allSessions, allTotal, err := store.ListSessions(ctx, nil, "", 0, 100)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, allTotal, 4)
		assert.GreaterOrEqual(t, len(allSessions), 4)
	})

	t.Run("ReviewScoring", func(t *testing.T) {
		tenantID := "test-tenant-rs-" + time.Now().Format("150405")

		// Create session
		session := &Session{
			TenantID:  tenantID,
			SessionID: "session-scoring-" + time.Now().Format("150405"),
			TaskType:  "code_quality",
			Status:    SessionStatusActive,
			Messages:  []Message{},
			Metadata:  map[string]interface{}{},
			CreatedAt: time.Now(),
		}

		err := store.CreateSession(ctx, session)
		require.NoError(t, err)

		// Create reviews with different scores
		scores := []float64{9.5, 8.0, 7.5, 6.0, 8.5, 9.0}

		for i, score := range scores {
			review := &Review{
				SessionID:    &session.ID,
				TenantID:     tenantID,
				FilePath:     "file" + string(rune('0'+i)) + ".go",
				Language:     "go",
				OriginalCode: "package main",
				ReviewResult: map[string]interface{}{
					"summary": "Test review " + string(rune('0'+i)),
				},
				Score:     score,
				CreatedAt: time.Now(),
			}

			err := store.CreateReview(ctx, review)
			require.NoError(t, err)
		}

		// Get all reviews
		reviews, total, err := store.ListReviews(ctx, &session.ID, 0, 100)
		require.NoError(t, err)
		assert.Equal(t, len(scores), len(reviews))
		assert.Equal(t, len(scores), total)

		// Calculate statistics
		var (
			totalScore float64
			maxScore   float64
			minScore   = 10.0
		)

		for _, r := range reviews {
			totalScore += r.Score
			if r.Score > maxScore {
				maxScore = r.Score
			}
			if r.Score < minScore {
				minScore = r.Score
			}
		}

		avgScore := totalScore / float64(len(reviews))

		t.Logf("Review Statistics:")
		t.Logf("  Total reviews: %d", len(reviews))
		t.Logf("  Average score: %.2f", avgScore)
		t.Logf("  Max score: %.2f", maxScore)
		t.Logf("  Min score: %.2f", minScore)

		assert.InDelta(t, 8.08, avgScore, 0.1)
		assert.Equal(t, 9.5, maxScore)
		assert.Equal(t, 6.0, minScore)
	})

	t.Run("ComplexReviewResult", func(t *testing.T) {
		tenantID := "test-tenant-crr-" + time.Now().Format("150405")

		session := &Session{
			TenantID:  tenantID,
			SessionID: "session-complex-" + time.Now().Format("150405"),
			TaskType:  "refactoring",
			Status:    SessionStatusActive,
			Messages:  []Message{},
			Metadata:  map[string]interface{}{},
			CreatedAt: time.Now(),
		}

		err := store.CreateSession(ctx, session)
		require.NoError(t, err)

		// Create review with complex result
		complexResult := ReviewResult{
			Issues: []ReviewIssue{
				{Line: 10, Severity: "error", Message: "Unhandled error", Category: "error_handling"},
				{Line: 25, Severity: "warning", Message: "Variable shadowing", Category: "naming"},
				{Line: 40, Severity: "info", Message: "Consider using constants", Category: "best_practices"},
			},
			Suggestions: []string{
				"Extract repeated logic into helper function",
				"Add comprehensive error handling",
				"Improve variable naming for clarity",
				"Add unit tests for edge cases",
			},
			Summary:         "Code needs refactoring for better maintainability",
			Complexity:      12,
			Maintainability: "medium",
		}

		review := &Review{
			SessionID:    &session.ID,
			TenantID:     tenantID,
			FilePath:     "internal/service/processor.go",
			Language:     "go",
			OriginalCode: "// Complex code here",
			ReviewResult: mustMap(complexResult),
			Score:        6.5,
			CreatedAt:    time.Now(),
		}

		err = store.CreateReview(ctx, review)
		require.NoError(t, err)

		// Retrieve and verify
		retrieved, err := store.GetReview(ctx, review.ID)
		require.NoError(t, err)
		assert.Equal(t, review.FilePath, retrieved.FilePath)

		// Parse result back
		var parsedResult ReviewResult
		resultBytes, err := json.Marshal(retrieved.ReviewResult)
		require.NoError(t, err)
		err = json.Unmarshal(resultBytes, &parsedResult)
		require.NoError(t, err)

		assert.Len(t, parsedResult.Issues, 3)
		assert.Len(t, parsedResult.Suggestions, 4)
		assert.Equal(t, 12, parsedResult.Complexity)
		assert.Equal(t, "medium", parsedResult.Maintainability)
	})

	t.Run("PaginationAndLimits", func(t *testing.T) {
		tenantID := "test-tenant-pagination-" + time.Now().Format("150405")

		// Create multiple projects
		for i := 0; i < 15; i++ {
			project := &Project{
				TenantID:    tenantID,
				Name:        "Project " + string(rune('A'+i)),
				Description: "Test project for pagination",
				Language:    "Go",
				Framework:   "Echo",
				Status:      ProjectStatusActive,
				CreatedBy:   "dev@example.com",
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}

			err := store.CreateProject(ctx, project)
			require.NoError(t, err)
			time.Sleep(5 * time.Millisecond)
		}

		// Test pagination
		page1, total, err := store.ListProjects(ctx, tenantID, ProjectStatusActive, 0, 10)
		require.NoError(t, err)
		assert.Equal(t, 10, len(page1), "First page should have 10 items")
		assert.Equal(t, 15, total)

		page2, _, err := store.ListProjects(ctx, tenantID, ProjectStatusActive, 10, 10)
		require.NoError(t, err)
		assert.Equal(t, 5, len(page2), "Second page should have 5 items")

		// Verify no overlap
		page1IDs := make(map[int64]bool)
		for _, p := range page1 {
			page1IDs[p.ID] = true
		}

		for _, p := range page2 {
			assert.False(t, page1IDs[p.ID], "Pages should not overlap")
		}
	})
}

// Helper functions

func mustMap(v interface{}) map[string]interface{} {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		panic(err)
	}

	return result
}
