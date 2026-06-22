package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// AutoTitleGenerator handles automatic session title generation.
// It runs asynchronously after the first request in a session completes.
type AutoTitleGenerator struct {
	handler *Handler
	enabled bool
}

// NewAutoTitleGenerator creates a new auto title generator.
func NewAutoTitleGenerator(handler *Handler) *AutoTitleGenerator {
	return &AutoTitleGenerator{
		handler: handler,
		enabled: true, // TODO: make configurable via env var
	}
}

// MaybeGenerateTitle checks if a session needs a title and generates one.
// Called asynchronously after the first request in a session completes.
// This function is fire-and-forget and will not block the main request path.
func (g *AutoTitleGenerator) MaybeGenerateTitle(sessionID, tenantID string) {
	if !g.enabled || g.handler == nil || g.handler.db == nil {
		return
	}

	// Run in a separate goroutine to avoid blocking
	go g.generateTitleAsync(sessionID, tenantID)
}

func (g *AutoTitleGenerator) generateTitleAsync(sessionID, tenantID string) {
	// Use background context with timeout (not tied to the request context)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Add structured logging for observability
	logger := slog.With(
		"component", "auto_title_generator",
		"session_id", sessionID,
		"tenant_id", tenantID,
	)

	// Step 1: Check if title already exists (avoid duplicate work)
	hasTitle, err := g.checkSessionHasTitle(ctx, sessionID)
	if err != nil {
		logger.Warn("failed to check existing title", "error", err)
		return
	}
	if hasTitle {
		logger.Debug("session already has title, skipping")
		return
	}

	// Step 2: Wait for stream to complete (avoid generating title mid-stream)
	time.Sleep(3 * time.Second)

	// Step 3: Check if session has enough requests (at least 1 successful)
	count, err := g.countSessionRequests(ctx, sessionID, tenantID)
	if err != nil {
		logger.Warn("failed to count session requests", "error", err)
		return
	}
	if count < 1 {
		logger.Debug("session has no successful requests yet", "count", count)
		return
	}

	// Step 4: Generate title from first request
	title, err := g.generateTitleFromFirstRequest(ctx, sessionID, tenantID)
	if err != nil {
		logger.Warn("failed to generate title from first request", "error", err)
		return
	}

	// Step 5: Save title to database (with conflict handling)
	if err := g.saveSessionTitle(ctx, sessionID, title); err != nil {
		// Check if it's a duplicate key error (another goroutine already saved)
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			logger.Debug("title already saved by another goroutine")
			return
		}
		logger.Error("failed to save session title", "error", err)
		return
	}

	logger.Info("auto title generated successfully", "title", title, "length", len(title))
}

// checkSessionHasTitle checks if a session already has a title.
func (g *AutoTitleGenerator) checkSessionHasTitle(ctx context.Context, sessionID string) (bool, error) {
	var exists bool
	err := g.handler.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM session_titles 
			WHERE scoped_session_id = $1
		)
	`, sessionID).Scan(&exists)
	return exists, err
}

// countSessionRequests counts successful requests for a session.
func (g *AutoTitleGenerator) countSessionRequests(ctx context.Context, sessionID, tenantID string) (int, error) {
	var count int
	err := g.handler.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM request_logs
		WHERE gw_session_id = $1 
		  AND tenant_id = $2 
		  AND success = true
	`, sessionID, tenantID).Scan(&count)
	return count, err
}

// generateTitleFromFirstRequest extracts the first request and generates a title.
// Strategy: Use request_preview first 50 chars (simplified version).
// Future: Call LLM for intelligent summarization.
func (g *AutoTitleGenerator) generateTitleFromFirstRequest(ctx context.Context, sessionID, tenantID string) (string, error) {
	var requestPreview string
	err := g.handler.db.QueryRow(ctx, `
		SELECT COALESCE(request_preview, '') 
		FROM request_logs
		WHERE gw_session_id = $1 
		  AND tenant_id = $2
		  AND request_preview IS NOT NULL
		  AND request_preview != ''
		ORDER BY ts ASC
		LIMIT 1
	`, sessionID, tenantID).Scan(&requestPreview)
	if err != nil {
		return "", fmt.Errorf("failed to load first request: %w", err)
	}

	// Extract title from preview (simplified version)
	title := g.extractTitleFromPreview(requestPreview)
	if title == "" {
		return "", fmt.Errorf("failed to extract title from preview (empty)")
	}

	return title, nil
}

// extractTitleFromPreview extracts a title from request_preview.
// Enhanced version: detect IDE source, extract user prompt, generate descriptive title.
func (g *AutoTitleGenerator) extractTitleFromPreview(preview string) string {
	// Remove leading/trailing whitespace
	preview = strings.TrimSpace(preview)
	if preview == "" {
		return ""
	}

	// Step 1: Detect IDE/tool source from system prompt
	idePrefix := g.detectIDESource(preview)

	// Step 2: Extract user prompt (skip system messages)
	userPrompt := g.extractUserPrompt(preview)

	// Step 3: Build title with IDE prefix if detected
	var title string
	if idePrefix != "" && userPrompt != "" {
		// Format: "[IDE] user prompt..."
		title = idePrefix + " " + userPrompt
	} else if userPrompt != "" {
		title = userPrompt
	} else {
		// Fallback: use raw preview (cleaned)
		title = preview
		title = strings.ReplaceAll(title, "\n", " ")
		title = strings.ReplaceAll(title, "\r", " ")
		title = strings.Join(strings.Fields(title), " ")
	}

	// Step 4: Truncate if needed (after combining IDE + prompt)
	maxLen := 80
	if len(title) > maxLen {
		cutoff := maxLen
		if idx := strings.LastIndex(title[:maxLen], " "); idx > 0 && idx > maxLen-20 {
			cutoff = idx
		}
		title = title[:cutoff] + "…"
	}

	// Step 5: Light normalization (remove quotes, excessive spaces)
	// Do NOT use normalizeSessionTitle yet - it removes brackets
	title = strings.Trim(title, `"'「」『』""`)
	title = strings.Join(strings.Fields(title), " ")

	// Step 6: Final validation (relaxed - allow brackets for IDE prefix)
	if len(strings.TrimSpace(title)) < 2 {
		// Fallback to simple prefix if too short
		runes := []rune(preview)
		if len(runes) > 20 {
			return string(runes[:20]) + "…"
		}
		return preview
	}

	return title
}

// detectIDESource detects the IDE/tool from system prompts in the request.
// Returns a short prefix like "[ZCode]", "[Cursor]", "[ZooCode]", etc.
func (g *AutoTitleGenerator) detectIDESource(preview string) string {
	lower := strings.ToLower(preview)

	// Check for known IDE signatures
	ideSignatures := map[string]string{
		"you are zcode":    "[ZCode]",
		"you are zoo":      "[ZooCode]",
		"you are cursor":   "[Cursor]",
		"you are opencode": "[OpenCode]",
		"you are claude":   "[Claude]",
		"you are windsurf": "[Windsurf]",
		"you are cline":    "[Cline]",
		"you are aider":    "[Aider]",
		"you are continue": "[Continue]",
		"you are copilot":  "[Copilot]",
		"you are kiro":     "[Kiro]",
	}

	for signature, prefix := range ideSignatures {
		if strings.Contains(lower, signature) {
			return prefix
		}
	}

	return ""
}

// extractUserPrompt extracts the actual user prompt from request preview.
// Skips system messages and focuses on user content.
func (g *AutoTitleGenerator) extractUserPrompt(preview string) string {
	// Common patterns to skip
	skipPatterns := []string{
		"[system]",
		"system:",
		"You are",
		"You're",
		"Your task is",
		"Your role is",
		"Act as",
		"Behave as",
	}

	lines := strings.Split(preview, "\n")
	var userLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Skip system-like lines
		isSystemLine := false
		for _, pattern := range skipPatterns {
			if strings.HasPrefix(line, pattern) {
				isSystemLine = true
				break
			}
		}

		if !isSystemLine && len(line) > 10 {
			// Extract content after common prefixes
			if strings.HasPrefix(line, "[user]") {
				line = strings.TrimSpace(strings.TrimPrefix(line, "[user]"))
			}
			if strings.HasPrefix(line, "user:") {
				line = strings.TrimSpace(strings.TrimPrefix(line, "user:"))
			}
			if line != "" {
				userLines = append(userLines, line)
			}
		}
	}

	if len(userLines) == 0 {
		return ""
	}

	// Take first meaningful user line
	userPrompt := userLines[0]

	// Truncate to 60 chars for the prompt part
	if len(userPrompt) > 60 {
		cutoff := 60
		if idx := strings.LastIndex(userPrompt[:60], " "); idx > 40 {
			cutoff = idx
		}
		userPrompt = userPrompt[:cutoff] + "…"
	}

	return userPrompt
}

// saveSessionTitle saves the auto-generated title to session_titles table.
// Uses task_id='auto' to indicate this was auto-generated.
func (g *AutoTitleGenerator) saveSessionTitle(ctx context.Context, sessionID, title string) error {
	_, err := g.handler.db.Exec(ctx, `
		INSERT INTO session_titles (
			task_id, 
			scoped_session_id, 
			title, 
			generated_at, 
			model, 
			api_key_id
		)
		VALUES ('auto', $1, $2, NOW(), 'auto-extract', 0)
		ON CONFLICT (task_id, scoped_session_id) DO NOTHING
	`, sessionID, title)
	return err
}
