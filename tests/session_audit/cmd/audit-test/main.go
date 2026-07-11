//go:build tools

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// 会话输出审计完整测试脚本
// 包含：数据提取、审计测试、结果分析

const (
	DB252Host        = "172.16.2.210"
	DB252Port        = "5432"
	DB252Database    = "llm_gateway"
	DB252User        = "llm_gateway"
	DB252PasswordEnv = "DB_252_PASSWORD"
)

// ExtractedContent 提取的内容
type ExtractedContent struct {
	RequestID     string    `json:"request_id"`
	SessionID     string    `json:"session_id"`
	TenantID      string    `json:"tenant_id"`
	Content       string    `json:"content"`
	ContentHash   string    `json:"content_hash"`
	ContentLength int       `json:"content_length"`
	Model         string    `json:"model"`
	Provider      string    `json:"provider"`
	CreatedAt     time.Time `json:"created_at"`
}

// RequestLogRow 请求日志行
type RequestLogRow struct {
	RequestID    string          `json:"request_id"`
	SessionID    string          `json:"session_id"`
	TenantID     string          `json:"tenant_id"`
	OutboundBody json.RawMessage `json:"outbound_body"`
	ResponseBody json.RawMessage `json:"response_body"`
	CreatedAt    time.Time       `json:"created_at"`
	Model        string          `json:"model"`
	Provider     string          `json:"provider"`
}

// TestConfig 测试配置
type TestConfig struct {
	TestSensitiveWords struct {
		PoliticalCN         []string `yaml:"political_cn"`
		PoliticalEN         []string `yaml:"political_en"`
		PornographyViolence []string `yaml:"pornography_violence"`
		Contraband          []string `yaml:"contraband"`
		Discrimination      []string `yaml:"discrimination"`
		Fraud               []string `yaml:"fraud"`
		TestPII             []string `yaml:"test_pii"`
		TestInjection       []string `yaml:"test_injection"`
		TestJailbreak       []string `yaml:"test_jailbreak"`
	} `yaml:"test_sensitive_words"`

	PIIEnhanced struct {
		Enabled  bool `yaml:"enabled"`
		Patterns map[string]struct {
			Regex      string  `yaml:"regex"`
			Confidence float64 `yaml:"confidence"`
		} `yaml:"patterns"`
	} `yaml:"pii_enhanced"`

	PromptInjectionEnhanced struct {
		Enabled  bool                `yaml:"enabled"`
		Patterns map[string][]string `yaml:"patterns"`
	} `yaml:"prompt_injection_enhanced"`

	JailbreakEnhanced struct {
		Enabled  bool     `yaml:"enabled"`
		Patterns []string `yaml:"patterns"`
	} `yaml:"jailbreak_enhanced"`
}

// AuditResult 审计结果
type AuditResult struct {
	TestRunID       string                     `json:"test_run_id"`
	RequestID       string                     `json:"request_id"`
	SessionID       string                     `json:"session_id"`
	TenantID        string                     `json:"tenant_id"`
	Content         string                     `json:"content"`
	ContentLength   int                        `json:"content_length"`
	ContentHash     string                     `json:"content_hash"`
	DetectResult    *sessionaudit.DetectResult `json:"detect_result"`
	DetectLatencyMs int                        `json:"detect_latency_ms"`
	DetectLatencyUs int64                      `json:"detect_latency_us"`
	HasPromptInject bool                       `json:"has_prompt_inject"`
	HasPIILeak      bool                       `json:"has_pii_leak"`
	HasJailbreak    bool                       `json:"has_jailbreak"`
}

func main() {
	log.Println("=== 会话输出审计完整测试 ===")

	// 步骤1：提取数据
	log.Println("\n[步骤 1/4] 数据提取...")
	data, err := extractData()
	if err != nil {
		log.Fatalf("❌ 数据提取失败: %v", err)
	}
	log.Printf("✅ 提取完成: %d 条记录", len(data))

	// 步骤2：加载配置
	log.Println("\n[步骤 2/4] 加载测试配置...")
	config, sensitiveWords, detector, err := setupDetector()
	if err != nil {
		log.Fatalf("❌ 配置加载失败: %v", err)
	}
	log.Printf("✅ 配置完成: %d 个敏感词", len(sensitiveWords))

	// 步骤3：执行审计
	log.Println("\n[步骤 3/4] 执行审计测试...")
	testRunID, results, err := runAudit(data, detector)
	if err != nil {
		log.Fatalf("❌ 审计失败: %v", err)
	}
	log.Printf("✅ 审计完成: %d 条结果", len(results))

	// 步骤4：保存结果
	log.Println("\n[步骤 4/4] 保存结果...")
	if err := saveResults(testRunID, config, sensitiveWords, results); err != nil {
		log.Fatalf("❌ 保存失败: %v", err)
	}
	log.Println("✅ 结果已保存到数据库")

	// 输出统计
	printSummary(testRunID, results)
}

// extractData 从数据库提取数据
func extractData() ([]ExtractedContent, error) {
	// 添加超时上下文
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dbURL := fmt.Sprintf(
		"postgresql://%s:%s@%s:%s/%s?sslmode=disable&connect_timeout=10",
		DB252User, os.Getenv(DB252PasswordEnv), DB252Host, DB252Port, DB252Database,
	)

	log.Printf("正在连接数据库: %s:%s/%s", DB252Host, DB252Port, DB252Database)
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	defer pool.Close()

	log.Println("正在验证数据库连接...")
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping 数据库失败: %w", err)
	}
	log.Println("数据库连接验证成功")

	// 检查是否为测试模式（从环境变量读取限制数量）
	limitStr := os.Getenv("TEST_LIMIT")
	limit := 10000
	if limitStr != "" {
		fmt.Sscanf(limitStr, "%d", &limit)
	}

	query := fmt.Sprintf(`
		SELECT 
			request_id,
			COALESCE(gw_session_id, '') AS session_id,
			COALESCE(tenant_id, 'default') AS tenant_id,
			outbound_body,
			response_body,
			ts AS created_at,
			COALESCE(client_model, '') AS model,
			COALESCE(provider_code, '') AS provider
		FROM request_logs
		WHERE ts >= NOW() - INTERVAL '7 days'
		  AND (
		      (outbound_body IS NOT NULL AND outbound_body != 'null'::jsonb)
		      OR 
		      (response_body IS NOT NULL AND response_body != 'null'::jsonb)
		  )
		  AND success = true
		ORDER BY ts DESC
		LIMIT %d
	`, limit)

	log.Println("正在执行查询...")
	rows, err := pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("查询失败: %w", err)
	}
	defer rows.Close()

	log.Println("正在提取数据...")
	var extracted []ExtractedContent
	processCount := 0
	for rows.Next() {
		processCount++
		if processCount%100 == 0 {
			log.Printf("  已扫描: %d 行", processCount)
		}
		var row RequestLogRow
		if err := rows.Scan(
			&row.RequestID, &row.SessionID, &row.TenantID,
			&row.OutboundBody, &row.ResponseBody, &row.CreatedAt,
			&row.Model, &row.Provider,
		); err != nil {
			continue
		}

		content := extractContent(row)
		if content == "" {
			continue
		}

		hash := sha256.Sum256([]byte(content))
		extracted = append(extracted, ExtractedContent{
			RequestID:     row.RequestID,
			SessionID:     row.SessionID,
			TenantID:      row.TenantID,
			Content:       content,
			ContentHash:   hex.EncodeToString(hash[:]),
			ContentLength: len(content),
			Model:         row.Model,
			Provider:      row.Provider,
			CreatedAt:     row.CreatedAt,
		})
	}

	return extracted, nil
}

// extractContent 提取内容
func extractContent(row RequestLogRow) string {
	if len(row.OutboundBody) > 0 && string(row.OutboundBody) != "null" {
		var messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if json.Unmarshal(row.OutboundBody, &messages) == nil {
			var contents []string
			for _, msg := range messages {
				if msg.Role == "assistant" && msg.Content != "" {
					contents = append(contents, msg.Content)
				}
			}
			if len(contents) > 0 {
				return strings.Join(contents, "\n\n")
			}
		}
	}

	if len(row.ResponseBody) > 0 && string(row.ResponseBody) != "null" {
		var openaiResp struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(row.ResponseBody, &openaiResp) == nil {
			if len(openaiResp.Choices) > 0 {
				return openaiResp.Choices[0].Message.Content
			}
		}
	}

	return ""
}

// setupDetector 设置检测器
func setupDetector() (*TestConfig, []string, *sessionaudit.FastDetector, error) {
	configData, err := os.ReadFile("02_sensitive_words_test.yaml")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("读取配置失败: %w", err)
	}

	var config TestConfig
	if err := yaml.Unmarshal(configData, &config); err != nil {
		return nil, nil, nil, fmt.Errorf("解析配置失败: %w", err)
	}

	// 构建敏感词列表
	words := []string{}
	words = append(words, config.TestSensitiveWords.PoliticalCN...)
	words = append(words, config.TestSensitiveWords.PoliticalEN...)
	words = append(words, config.TestSensitiveWords.PornographyViolence...)
	words = append(words, config.TestSensitiveWords.Contraband...)
	words = append(words, config.TestSensitiveWords.Discrimination...)
	words = append(words, config.TestSensitiveWords.Fraud...)
	words = append(words, config.TestSensitiveWords.TestPII...)
	words = append(words, config.TestSensitiveWords.TestInjection...)
	words = append(words, config.TestSensitiveWords.TestJailbreak...)

	// 构建检测器配置
	var piiPatterns []string
	for _, pattern := range config.PIIEnhanced.Patterns {
		piiPatterns = append(piiPatterns, pattern.Regex)
	}

	var injectionPatterns []string
	for _, patterns := range config.PromptInjectionEnhanced.Patterns {
		injectionPatterns = append(injectionPatterns, patterns...)
	}

	detectorConfig := &sessionaudit.DetectorConfig{
		SensitiveWords:    words,
		InjectionPatterns: injectionPatterns,
		PIIPatterns:       piiPatterns,
		JailbreakPatterns: config.JailbreakEnhanced.Patterns,
		MaxContentLen:     50000,
	}

	detector := sessionaudit.NewFastDetector(detectorConfig)

	return &config, words, detector, nil
}

// runAudit 执行审计
func runAudit(data []ExtractedContent, detector *sessionaudit.FastDetector) (string, []AuditResult, error) {
	testRunID := fmt.Sprintf("test_%s", uuid.New().String()[:8])
	ctx := context.Background()

	var results []AuditResult
	for i, item := range data {
		start := time.Now()
		detectResult, err := detector.Detect(ctx, item.Content)
		duration := time.Since(start)

		if err != nil {
			log.Printf("⚠️  [%d/%d] 审计失败: %v", i+1, len(data), err)
			continue
		}

		hasPromptInject := false
		hasPIILeak := false
		hasJailbreak := false
		for _, threat := range detectResult.Threats {
			switch threat.Type {
			case "prompt_inject":
				hasPromptInject = true
			case "pii_leak":
				hasPIILeak = true
			case "jailbreak":
				hasJailbreak = true
			}
		}

		results = append(results, AuditResult{
			TestRunID:       testRunID,
			RequestID:       item.RequestID,
			SessionID:       item.SessionID,
			TenantID:        item.TenantID,
			Content:         item.Content,
			ContentLength:   item.ContentLength,
			ContentHash:     item.ContentHash,
			DetectResult:    detectResult,
			DetectLatencyMs: int(duration.Milliseconds()),
			DetectLatencyUs: duration.Microseconds(),
			HasPromptInject: hasPromptInject,
			HasPIILeak:      hasPIILeak,
			HasJailbreak:    hasJailbreak,
		})

		if (i+1)%100 == 0 {
			log.Printf("  进度: %d/%d (%.1f%%)", i+1, len(data), float64(i+1)/float64(len(data))*100)
		}
	}

	return testRunID, results, nil
}

// saveResults 保存结果到数据库
func saveResults(testRunID string, config *TestConfig, sensitiveWords []string, results []AuditResult) error {
	ctx := context.Background()
	dbURL := fmt.Sprintf(
		"postgresql://%s:%s@%s:%s/%s?sslmode=disable",
		DB252User, os.Getenv(DB252PasswordEnv), DB252Host, DB252Port, DB252Database,
	)

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	defer pool.Close()

	// 保存测试批次元数据
	configJSON, _ := json.Marshal(config)
	_, err = pool.Exec(ctx, `
		INSERT INTO audit_test_runs (
			test_run_id, description, detector_config, sensitive_words, 
			test_data_source, total_records, started_at
		) VALUES ($1, $2, $3, $4, $5, $6, NOW())
	`, testRunID,
		fmt.Sprintf("会话输出审计测试 - %s", time.Now().Format("2006-01-02 15:04:05")),
		configJSON, sensitiveWords,
		"252 数据库 request_logs 表 (最近 7 天)", len(results))

	if err != nil {
		return fmt.Errorf("保存测试批次元数据失败: %w", err)
	}

	// 保存审计结果
	for _, result := range results {
		threatsJSON, _ := json.Marshal(result.DetectResult.Threats)
		sensitiveWordsJSON, _ := json.Marshal(result.DetectResult.SensitiveWords)

		_, err := pool.Exec(ctx, `
			INSERT INTO audit_test_results (
				test_run_id, request_id, session_id, tenant_id,
				content, content_length, content_hash,
				detect_score, decision, reason,
				sensitive_words, sensitive_count,
				threats, threat_count,
				has_prompt_inject, has_pii_leak, has_jailbreak,
				detect_latency_ms, detect_latency_us,
				created_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
				$11, $12, $13, $14, $15, $16, $17, $18, $19, NOW()
			)
		`, testRunID, result.RequestID, result.SessionID, result.TenantID,
			result.Content, result.ContentLength, result.ContentHash,
			result.DetectResult.Score, string(result.DetectResult.Decision), result.DetectResult.Reason,
			sensitiveWordsJSON, len(result.DetectResult.SensitiveWords),
			threatsJSON, len(result.DetectResult.Threats),
			result.HasPromptInject, result.HasPIILeak, result.HasJailbreak,
			result.DetectLatencyMs, result.DetectLatencyUs)

		if err != nil {
			log.Printf("⚠️  保存结果失败: %v", err)
		}
	}

	// 更新统计
	return updateStats(ctx, pool, testRunID, results)
}

// updateStats 更新统计
func updateStats(ctx context.Context, pool *pgxpool.Pool, testRunID string, results []AuditResult) error {
	var latencies []int
	decisionCounts := make(map[sessionaudit.Decision]int)
	threatInjection, threatPII, threatJailbreak := 0, 0, 0

	for _, r := range results {
		latencies = append(latencies, r.DetectLatencyMs)
		decisionCounts[r.DetectResult.Decision]++
		if r.HasPromptInject {
			threatInjection++
		}
		if r.HasPIILeak {
			threatPII++
		}
		if r.HasJailbreak {
			threatJailbreak++
		}
	}

	// 排序计算分位数
	for i := 0; i < len(latencies); i++ {
		for j := i + 1; j < len(latencies); j++ {
			if latencies[j] < latencies[i] {
				latencies[i], latencies[j] = latencies[j], latencies[i]
			}
		}
	}

	sum := 0
	for _, l := range latencies {
		sum += l
	}
	avgLatency := float64(sum) / float64(len(latencies))

	_, err := pool.Exec(ctx, `
		UPDATE audit_test_runs SET
			completed_records = $1,
			avg_latency_ms = $2,
			p50_latency_ms = $3,
			p95_latency_ms = $4,
			p99_latency_ms = $5,
			max_latency_ms = $6,
			min_latency_ms = $7,
			decision_pass = $8,
			decision_warn = $9,
			decision_block = $10,
			decision_approval = $11,
			threat_injection = $12,
			threat_pii = $13,
			threat_jailbreak = $14,
			completed_at = NOW(),
			duration_seconds = EXTRACT(EPOCH FROM (NOW() - started_at))::INT
		WHERE test_run_id = $15
	`, len(results), avgLatency,
		latencies[len(latencies)/2], latencies[len(latencies)*95/100], latencies[len(latencies)*99/100],
		latencies[len(latencies)-1], latencies[0],
		decisionCounts[sessionaudit.DecisionPass], decisionCounts[sessionaudit.DecisionWarn],
		decisionCounts[sessionaudit.DecisionBlock], decisionCounts[sessionaudit.DecisionNeedApproval],
		threatInjection, threatPII, threatJailbreak, testRunID)

	return err
}

// printSummary 打印摘要
func printSummary(testRunID string, results []AuditResult) {
	log.Println("\n=== 测试摘要 ===")
	log.Printf("测试批次 ID: %s", testRunID)
	log.Printf("总记录数: %d", len(results))

	// 决策分布
	decisionCounts := make(map[sessionaudit.Decision]int)
	for _, r := range results {
		decisionCounts[r.DetectResult.Decision]++
	}

	log.Println("\n决策分布:")
	log.Printf("  Pass:         %d (%.1f%%)", decisionCounts[sessionaudit.DecisionPass], float64(decisionCounts[sessionaudit.DecisionPass])/float64(len(results))*100)
	log.Printf("  Warn:         %d (%.1f%%)", decisionCounts[sessionaudit.DecisionWarn], float64(decisionCounts[sessionaudit.DecisionWarn])/float64(len(results))*100)
	log.Printf("  Block:        %d (%.1f%%)", decisionCounts[sessionaudit.DecisionBlock], float64(decisionCounts[sessionaudit.DecisionBlock])/float64(len(results))*100)
	log.Printf("  NeedApproval: %d (%.1f%%)", decisionCounts[sessionaudit.DecisionNeedApproval], float64(decisionCounts[sessionaudit.DecisionNeedApproval])/float64(len(results))*100)

	// 威胁统计
	threatInjection, threatPII, threatJailbreak := 0, 0, 0
	for _, r := range results {
		if r.HasPromptInject {
			threatInjection++
		}
		if r.HasPIILeak {
			threatPII++
		}
		if r.HasJailbreak {
			threatJailbreak++
		}
	}

	log.Println("\n威胁检测:")
	log.Printf("  Prompt Injection: %d (%.1f%%)", threatInjection, float64(threatInjection)/float64(len(results))*100)
	log.Printf("  PII Leak:         %d (%.1f%%)", threatPII, float64(threatPII)/float64(len(results))*100)
	log.Printf("  Jailbreak:        %d (%.1f%%)", threatJailbreak, float64(threatJailbreak)/float64(len(results))*100)

	// 性能统计
	var latencies []int
	for _, r := range results {
		latencies = append(latencies, r.DetectLatencyMs)
	}
	for i := 0; i < len(latencies); i++ {
		for j := i + 1; j < len(latencies); j++ {
			if latencies[j] < latencies[i] {
				latencies[i], latencies[j] = latencies[j], latencies[i]
			}
		}
	}

	log.Println("\n性能指标:")
	log.Printf("  P50: %d ms", latencies[len(latencies)/2])
	log.Printf("  P95: %d ms", latencies[len(latencies)*95/100])
	log.Printf("  P99: %d ms", latencies[len(latencies)*99/100])
	log.Printf("  Max: %d ms", latencies[len(latencies)-1])
	log.Printf("  Min: %d ms", latencies[0])

	log.Println("\n💡 查询结果:")
	log.Printf("  psql -h %s -p %s -U %s -d %s", DB252Host, DB252Port, DB252User, DB252Database)
	log.Printf("  SELECT * FROM v_audit_performance_summary WHERE test_run_id = '%s';", testRunID)
}
