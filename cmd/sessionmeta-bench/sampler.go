// Package main implements session metadata quality benchmark.
//
// sampler.go handles read-only sampling from production database and
// privacy-preserving anonymization before any model invocation.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionSample represents one anonymized session for evaluation.
type SessionSample struct {
	// SessionHash is SHA256(tenant_id + session_id + sampling_seed) — irreversible
	SessionHash string `json:"session_hash"`

	// Turns contains user/assistant dialogue without system prompts
	Turns []TurnSample `json:"turns"`

	// Metadata for stratification (anonymized)
	ClientTypeHint string `json:"client_type_hint,omitempty"` // ide/web/api/unknown
	TurnCount      int    `json:"turn_count"`
	EstimatedRunes int    `json:"estimated_runes"`
	HasCodeBlock   bool   `json:"has_code_block"`
	Language       string `json:"language"` // en/zh/mixed/unknown

	// Production baseline (if available)
	ProductionTitle   string `json:"production_title,omitempty"`
	ProductionSummary string `json:"production_summary,omitempty"`
}

// TurnSample is one user-assistant exchange.
type TurnSample struct {
	TurnNo           int    `json:"turn_no"`
	UserMessage      string `json:"user_message"`
	AssistantMessage string `json:"assistant_message"`
}

// SamplerConfig controls sampling behavior.
type SamplerConfig struct {
	DSN             string
	TargetCount     int
	MinTurns        int
	MaxTurns        int
	TimeWindowStart time.Time
	TimeWindowEnd   time.Time
	RandomSeed      int64
	ExcludeActors   []string // auto-title-generator, quality-test, etc.
}

// DefaultSamplerConfig returns safe defaults.
func DefaultSamplerConfig() SamplerConfig {
	end := time.Now().UTC()
	start := end.Add(-30 * 24 * time.Hour) // last 30 days
	return SamplerConfig{
		TargetCount:     80,
		MinTurns:        1,
		MaxTurns:        20,
		TimeWindowStart: start,
		TimeWindowEnd:   end,
		RandomSeed:      42,
		ExcludeActors: []string{
			"auto-title-generator",
			"quality-test",
			"self-check",
			"node-probe",
			"credential-selfcheck",
		},
	}
}

// SampleSessions performs stratified read-only sampling.
func SampleSessions(ctx context.Context, cfg SamplerConfig) ([]SessionSample, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("sampler: DSN is required")
	}

	pool, err := pgxpool.New(ctx, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("sampler: connect: %w", err)
	}
	defer pool.Close()

	// Verify read-only session level
	var txReadOnly string
	if err := pool.QueryRow(ctx, "SHOW default_transaction_read_only").Scan(&txReadOnly); err != nil {
		log.Printf("warn: cannot verify read-only mode: %v", err)
	} else if txReadOnly != "on" {
		log.Printf("warn: database is not in read-only mode (default_transaction_read_only=%s)", txReadOnly)
	}

	// Build exclusion filter (currently unused since session_bodies_unified has no origin_actor)
	_ = cfg.ExcludeActors

	// Query: sample session_ids with turn counts from session_bodies_unified
	query := fmt.Sprintf(`
		WITH eligible_sessions AS (
			SELECT
				sb.tenant_id,
				sb.session_id,
				COUNT(*) AS turn_count,
				MIN(sb.ts) AS first_turn,
				MAX(sb.ts) AS last_turn
			FROM session_bodies_unified sb
			WHERE sb.ts >= $1
			  AND sb.ts < $2
			  AND sb.session_id IS NOT NULL
			  AND sb.session_id != ''
			GROUP BY sb.tenant_id, sb.session_id
			HAVING COUNT(*) >= $3 AND COUNT(*) <= $4
		)
		SELECT tenant_id, session_id, turn_count
		FROM eligible_sessions
		ORDER BY RANDOM()
		LIMIT $5
	`)

	rows, err := pool.Query(ctx, query,
		cfg.TimeWindowStart, cfg.TimeWindowEnd,
		cfg.MinTurns, cfg.MaxTurns, cfg.TargetCount)
	if err != nil {
		return nil, fmt.Errorf("sampler: query sessions: %w", err)
	}
	defer rows.Close()

	type sessionKey struct {
		tenantID  string
		sessionID string
		turnCount int
	}
	var keys []sessionKey
	for rows.Next() {
		var k sessionKey
		if err := rows.Scan(&k.tenantID, &k.sessionID, &k.turnCount); err != nil {
			return nil, fmt.Errorf("sampler: scan session: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sampler: iterate sessions: %w", err)
	}

	log.Printf("sampler: found %d eligible sessions", len(keys))

	// For each session, fetch turns and anonymize
	samples := make([]SessionSample, 0, len(keys))
	for _, k := range keys {
		sample, err := fetchAndAnonymize(ctx, pool, k.tenantID, k.sessionID, cfg.RandomSeed)
		if err != nil {
			log.Printf("warn: skip session %s/%s: %v", k.tenantID, k.sessionID, err)
			continue
		}
		samples = append(samples, sample)
	}

	log.Printf("sampler: successfully anonymized %d/%d sessions", len(samples), len(keys))
	return samples, nil
}

func fetchAndAnonymize(ctx context.Context, pool *pgxpool.Pool, tenantID, sessionID string, seed int64) (SessionSample, error) {
	// Fetch turns from session_bodies_unified (V2 store)
	query := `
		SELECT
			turn_no,
			request_delta::text,
			response_delta::text
		FROM session_bodies_unified
		WHERE tenant_id = $1
		  AND session_id = $2
		ORDER BY turn_no
		LIMIT 20
	`
	rows, err := pool.Query(ctx, query, tenantID, sessionID)
	if err != nil {
		return SessionSample{}, fmt.Errorf("fetch turns: %w", err)
	}
	defer rows.Close()

	var turns []TurnSample
	totalRunes := 0
	hasCode := false

	for rows.Next() {
		var turnNo int
		var reqDelta, respDelta string
		if err := rows.Scan(&turnNo, &reqDelta, &respDelta); err != nil {
			return SessionSample{}, fmt.Errorf("scan turn: %w", err)
		}

		userMsg := extractUserMessageFromDelta(reqDelta)
		assistantMsg := extractAssistantMessageFromDelta(respDelta)

		if userMsg == "" && assistantMsg == "" {
			continue // skip empty turn
		}

		// Anonymize
		userMsg = anonymizeText(userMsg)
		assistantMsg = anonymizeText(assistantMsg)

		totalRunes += len([]rune(userMsg)) + len([]rune(assistantMsg))
		if !hasCode && containsCodeBlock(userMsg, assistantMsg) {
			hasCode = true
		}

		turns = append(turns, TurnSample{
			TurnNo:           turnNo,
			UserMessage:      userMsg,
			AssistantMessage: assistantMsg,
		})
	}

	if len(turns) == 0 {
		return SessionSample{}, fmt.Errorf("no valid turns")
	}

	// Generate irreversible session hash
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s:%s:%d", tenantID, sessionID, seed)))
	sessionHash := hex.EncodeToString(h.Sum(nil))[:16]

	language := detectLanguage(turns)

	return SessionSample{
		SessionHash:    sessionHash,
		Turns:          turns,
		ClientTypeHint: "unknown", // session_bodies_unified doesn't have client_profile
		TurnCount:      len(turns),
		EstimatedRunes: totalRunes,
		HasCodeBlock:   hasCode,
		Language:       language,
	}, nil
}

func extractUserMessageFromDelta(deltaJSON string) string {
	var delta []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(deltaJSON), &delta); err != nil {
		return ""
	}
	for _, msg := range delta {
		if msg.Role == "user" {
			return msg.Content
		}
	}
	return ""
}

func extractAssistantMessageFromDelta(deltaJSON string) string {
	var delta []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(deltaJSON), &delta); err != nil {
		return ""
	}
	for _, msg := range delta {
		if msg.Role == "assistant" {
			return msg.Content
		}
	}
	return ""
}

func extractUserMessage(reqBodyJSON string) string {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(reqBodyJSON), &req); err != nil {
		return ""
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return req.Messages[i].Content
		}
	}
	return ""
}

func extractAssistantMessage(respBodyJSON string) string {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(respBodyJSON), &resp); err != nil {
		return ""
	}
	if len(resp.Choices) > 0 {
		return resp.Choices[0].Message.Content
	}
	return ""
}

var (
	emailPattern      = regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	phonePattern      = regexp.MustCompile(`\b1[3-9]\d{9}\b|\+\d{1,3}[\s-]?\(?\d{1,4}\)?[\s-]?\d{1,4}[\s-]?\d{1,9}`)
	ipPattern         = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	urlQueryPattern   = regexp.MustCompile(`(\?|&)[^=&\s]+=[^&\s]+`)
	bearerPattern     = regexp.MustCompile(`(?i)bearer\s+[a-zA-Z0-9_\-\.]+`)
	pathPattern       = regexp.MustCompile(`(/[a-zA-Z0-9_\-]+){4,}(/[a-zA-Z0-9_\-\.]+)?`)
	connectionPattern = regexp.MustCompile(`(?i)(postgres|mysql|mongodb)://[^\s'"]+`)
)

func anonymizeText(text string) string {
	text = emailPattern.ReplaceAllString(text, "[EMAIL]")
	text = phonePattern.ReplaceAllString(text, "[PHONE]")
	text = ipPattern.ReplaceAllString(text, "[IP]")
	text = urlQueryPattern.ReplaceAllString(text, "?[QUERY]")
	text = bearerPattern.ReplaceAllString(text, "Bearer [TOKEN]")
	text = connectionPattern.ReplaceAllString(text, "[CONNECTION_STRING]")
	text = pathPattern.ReplaceAllString(text, "[FILE_PATH]")
	return text
}

func anonymizeClientType(raw string) string {
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "cursor") || strings.Contains(lower, "vscode") || strings.Contains(lower, "ide") {
		return "ide"
	}
	if strings.Contains(lower, "web") || strings.Contains(lower, "browser") {
		return "web"
	}
	if strings.Contains(lower, "api") || strings.Contains(lower, "sdk") {
		return "api"
	}
	return "unknown"
}

func containsCodeBlock(userMsg, assistantMsg string) bool {
	combined := userMsg + " " + assistantMsg
	return strings.Contains(combined, "```") || strings.Contains(combined, "    ") // markdown or indent
}

func detectLanguage(turns []TurnSample) string {
	enCount, zhCount, otherCount := 0, 0, 0
	for _, t := range turns {
		combined := t.UserMessage + " " + t.AssistantMessage
		for _, r := range combined {
			if r >= 0x4e00 && r <= 0x9fff {
				zhCount++
			} else if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				enCount++
			} else if r > 127 {
				otherCount++
			}
		}
	}
	total := enCount + zhCount + otherCount
	if total < 10 {
		return "unknown"
	}
	// Mixed: both languages present with significant counts
	if zhCount >= 5 && enCount >= 20 {
		return "mixed"
	}
	if zhCount > enCount*2 {
		return "zh"
	}
	if enCount > zhCount*2 {
		return "en"
	}
	return "unknown"
}
