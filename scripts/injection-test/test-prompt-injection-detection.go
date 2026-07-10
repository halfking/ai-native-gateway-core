package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// Threat 威胁信息
type Threat struct {
	Type     string `json:"type"`
	Severity int    `json:"severity"`
	Evidence string `json:"evidence"`
}

// DetectResult 检测结果
type DetectResult struct {
	Score          int      `json:"score"`
	Decision       string   `json:"decision"`
	Threats        []Threat `json:"threats"`
	SensitiveWords []string `json:"sensitive_words"`
	LatencyMs      int      `json:"latency_ms"`
}

// TestResult 测试结果
type TestResult struct {
	ID                int64    `json:"id"`
	RequestID         string   `json:"request_id"`
	SessionKey        string   `json:"session_key"`
	Content           string   `json:"content"`
	OriginalScore     int      `json:"original_score"`
	OriginalDecision  string   `json:"original_decision"`
	OriginalThreats   string   `json:"original_threats"`
	NewScore          int      `json:"new_score"`
	NewDecision       string   `json:"new_decision"`
	NewThreats        string   `json:"new_threats"`
	NewSensitiveWords []string `json:"new_sensitive_words"`
	LatencyMs         int      `json:"latency_ms"`
	MatchedPatterns   []string `json:"matched_patterns"`
	IsTruePositive    bool     `json:"is_true_positive"`
	IsFalsePositive   bool     `json:"is_false_positive"`
	IsFalseNegative   bool     `json:"is_false_negative"`
}

// AnalysisResult 分析结果
type AnalysisResult struct {
	TotalRecords         int            `json:"total_records"`
	TotalDetections      int            `json:"total_detections"`
	TruePositives        int            `json:"true_positives"`
	FalsePositives       int            `json:"false_positives"`
	FalseNegatives       int            `json:"false_negatives"`
	TrueNegatives        int            `json:"true_negatives"`
	AvgLatencyMs         float64        `json:"avg_latency_ms"`
	MaxLatencyMs         int            `json:"max_latency_ms"`
	MinLatencyMs         int            `json:"min_latency_ms"`
	Accuracy             float64        `json:"accuracy"`
	Precision            float64        `json:"precision"`
	Recall               float64        `json:"recall"`
	F1Score              float64        `json:"f1_score"`
	ThreatDistribution   map[string]int `json:"threat_distribution"`
	DecisionDistribution map[string]int `json:"decision_distribution"`
}

// Detector 检测器
type Detector struct {
	sensitiveWords    []string
	injectionPatterns []*regexp.Regexp
	piiPatterns       []*regexp.Regexp
	jailbreakPatterns []*regexp.Regexp
}

// NewDetector 创建检测器
func NewDetector(sensitiveWords, injectionPatterns, piiPatterns, jailbreakPatterns []string) *Detector {
	return &Detector{
		sensitiveWords:    sensitiveWords,
		injectionPatterns: compileRegexList(injectionPatterns),
		piiPatterns:       compileRegexList(piiPatterns),
		jailbreakPatterns: compileRegexList(jailbreakPatterns),
	}
}

// Detect 执行检测
func (d *Detector) Detect(ctx context.Context, content string) (*DetectResult, error) {
	start := time.Now()

	result := &DetectResult{
		SensitiveWords: []string{},
		Threats:        []Threat{},
		Score:          0,
	}

	// 1. 敏感词扫描
	contentLower := strings.ToLower(content)
	for _, word := range d.sensitiveWords {
		if strings.Contains(contentLower, strings.ToLower(word)) {
			result.SensitiveWords = append(result.SensitiveWords, word)
			result.Score += 2
		}
	}

	// 2. 注入检测
	for _, rule := range d.injectionPatterns {
		if matches := rule.FindStringSubmatch(content); matches != nil {
			result.Threats = append(result.Threats, Threat{
				Type:     "prompt_inject",
				Severity: 8,
				Evidence: truncate(matches[0], 50),
			})
			result.Score += 5
		}
	}

	// 3. PII 检测
	for _, rule := range d.piiPatterns {
		if matches := rule.FindStringSubmatch(content); matches != nil {
			result.Threats = append(result.Threats, Threat{
				Type:     "pii_leak",
				Severity: 9,
				Evidence: "[REDACTED]",
			})
			result.Score += 6
		}
	}

	// 4. 越狱检测
	for _, rule := range d.jailbreakPatterns {
		if matches := rule.FindStringSubmatch(content); matches != nil {
			result.Threats = append(result.Threats, Threat{
				Type:     "jailbreak",
				Severity: 10,
				Evidence: truncate(matches[0], 50),
			})
			result.Score += 7
		}
	}

	// 5. 限制最高分
	if result.Score > 10 {
		result.Score = 10
	}

	// 6. 决策逻辑
	maxSeverity := 0
	for _, th := range result.Threats {
		if th.Severity > maxSeverity {
			maxSeverity = th.Severity
		}
	}

	switch {
	case maxSeverity >= 8 || result.Score >= 8:
		result.Decision = "need_approval"
	case result.Score >= 5:
		result.Decision = "warn"
	default:
		result.Decision = "pass"
	}

	result.LatencyMs = int(time.Since(start) / time.Millisecond)
	return result, nil
}

func compileRegexList(patterns []string) []*regexp.Regexp {
	var rules []*regexp.Regexp
	for _, p := range patterns {
		if r, err := regexp.Compile(p); err == nil {
			rules = append(rules, r)
		}
	}
	return rules
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func main() {
	// 1. 连接数据库
	dbURL := os.Getenv("LLM_GATEWAY_DATABASE_URL")
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
	}
	if dbURL == "" {
		// 使用默认的252数据库连接
		dbURL = "postgres://llm_gateway:4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg@172.16.2.210:5432/llm_gateway?sslmode=disable"
		log.Printf("使用默认数据库连接: %s", dbURL)
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer db.Close()

	// 测试连接
	if err := db.Ping(); err != nil {
		log.Fatalf("数据库连接失败: %v", err)
	}
	log.Println("数据库连接成功")

	// 2. 创建临时表
	ctx := context.Background()
	if err := createTempTables(ctx, db); err != nil {
		log.Fatalf("创建临时表失败: %v", err)
	}

	// 3. 获取请求数据
	records, err := fetchRequestRecords(ctx, db)
	if err != nil {
		log.Fatalf("获取请求数据失败: %v", err)
	}
	log.Printf("获取到 %d 条记录", len(records))

	// 4. 配置多样化敏感词
	sensitiveWords := getDiverseSensitiveWords()
	log.Printf("配置了 %d 个敏感词", len(sensitiveWords))

	// 5. 创建检测器
	detector := NewDetector(
		sensitiveWords,
		getEnhancedInjectionPatterns(),
		getEnhancedPIIPatterns(),
		getEnhancedJailbreakPatterns(),
	)

	// 6. 逐个进行会话注入检测
	var results []*TestResult
	totalLatency := 0
	maxLatency := 0
	minLatency := int(^uint(0) >> 1)

	for i, record := range records {
		if (i+1)%100 == 0 {
			log.Printf("处理进度: %d/%d", i+1, len(records))
		}

		result, err := processRecord(ctx, detector, record)
		if err != nil {
			log.Printf("处理记录失败: %v", err)
			continue
		}

		results = append(results, result)
		totalLatency += result.LatencyMs
		if result.LatencyMs > maxLatency {
			maxLatency = result.LatencyMs
		}
		if result.LatencyMs < minLatency {
			minLatency = result.LatencyMs
		}

		// 记录到临时表
		if err := insertTestResult(ctx, db, result); err != nil {
			log.Printf("插入测试结果失败: %v", err)
		}
	}

	// 7. 进行全局分析
	analysis := analyzeResults(results, totalLatency, maxLatency, minLatency)

	// 8. 输出分析报告
	printAnalysisReport(analysis)

	// 9. 生成优化建议
	generateOptimizationSuggestions(analysis)
}

// createTempTables 创建临时表
func createTempTables(ctx context.Context, db *sql.DB) error {
	queries := []string{
		// 测试结果临时表
		`CREATE TEMPORARY TABLE IF NOT EXISTS test_injection_results (
			id BIGSERIAL PRIMARY KEY,
			request_id VARCHAR(255),
			session_key VARCHAR(255),
			content TEXT,
			original_score INT,
			original_decision VARCHAR(50),
			original_threats JSONB,
			new_score INT,
			new_decision VARCHAR(50),
			new_threats JSONB,
			new_sensitive_words JSONB,
			latency_ms INT,
			matched_patterns JSONB,
			is_true_positive BOOLEAN,
			is_false_positive BOOLEAN,
			is_false_negative BOOLEAN,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,

		// 分析结果临时表
		`CREATE TEMPORARY TABLE IF NOT EXISTS test_analysis_results (
			id BIGSERIAL PRIMARY KEY,
			total_records INT,
			total_detections INT,
			true_positives INT,
			false_positives INT,
			false_negatives INT,
			true_negatives INT,
			avg_latency_ms FLOAT,
			max_latency_ms INT,
			min_latency_ms INT,
			accuracy FLOAT,
			precision_score FLOAT,
			recall FLOAT,
			f1_score FLOAT,
			threat_distribution JSONB,
			decision_distribution JSONB,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,

		// 敏感词配置临时表
		`CREATE TEMPORARY TABLE IF NOT EXISTS test_sensitive_words_config (
			id BIGSERIAL PRIMARY KEY,
			word VARCHAR(255),
			category VARCHAR(100),
			is_known_sensitive BOOLEAN,
			description TEXT,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
	}

	for _, query := range queries {
		if _, err := db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("执行SQL失败: %w", err)
		}
	}

	log.Println("临时表创建成功")
	return nil
}

// RequestRecord 请求记录结构
type RequestRecord struct {
	ID              int64
	SessionID       string
	TenantID        string
	RequestID       string
	ClientIP        string
	ClientUserAgent string
	ClientModel     string
	ContentSummary  string
	ContentHash     string
	DetectScore     int
	DetectDecision  string
	Threats         string
	SensitiveWords  string
	CreatedAt       time.Time
}

// fetchRequestRecords 获取请求记录
func fetchRequestRecords(ctx context.Context, db *sql.DB) ([]RequestRecord, error) {
	// 从 session_audit_records 表获取数据
	query := `
		SELECT 
			id,
			COALESCE(session_id, ''),
			COALESCE(tenant_id, ''),
			COALESCE(request_id, ''),
			COALESCE(client_ip, ''),
			COALESCE(client_user_agent, ''),
			COALESCE(client_model, ''),
			COALESCE(content_summary, ''),
			COALESCE(content_hash, ''),
			COALESCE(detect_score, 0),
			COALESCE(detect_decision, 'pass'),
			COALESCE(threats::text, '[]'),
			COALESCE(sensitive_words::text, '[]'),
			created_at
		FROM session_audit_records
		WHERE created_at >= NOW() - INTERVAL '7 days'
		ORDER BY created_at DESC
		LIMIT 1000
	`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("查询失败: %w", err)
	}
	defer rows.Close()

	var records []RequestRecord
	for rows.Next() {
		var record RequestRecord
		var threats, sensitiveWords string

		err := rows.Scan(
			&record.ID,
			&record.SessionID,
			&record.TenantID,
			&record.RequestID,
			&record.ClientIP,
			&record.ClientUserAgent,
			&record.ClientModel,
			&record.ContentSummary,
			&record.ContentHash,
			&record.DetectScore,
			&record.DetectDecision,
			&threats,
			&sensitiveWords,
			&record.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("扫描行失败: %w", err)
		}

		record.Threats = threats
		record.SensitiveWords = sensitiveWords
		records = append(records, record)
	}

	return records, nil
}

// processRecord 处理单条记录
func processRecord(ctx context.Context, detector *Detector, record RequestRecord) (*TestResult, error) {
	content := record.ContentSummary
	if content == "" {
		content = "测试内容" // 默认测试内容
	}

	// 执行新的检测
	start := time.Now()
	result, err := detector.Detect(ctx, content)
	if err != nil {
		return nil, fmt.Errorf("检测失败: %w", err)
	}
	latencyMs := int(time.Since(start) / time.Millisecond)

	// 提取匹配的模式
	var matchedPatterns []string
	for _, threat := range result.Threats {
		matchedPatterns = append(matchedPatterns, threat.Evidence)
	}

	// 转换威胁为JSON
	threatsJSON, _ := json.Marshal(result.Threats)

	// 获取原始检测结果
	originalScore := record.DetectScore
	originalDecision := record.DetectDecision
	originalThreats := record.Threats

	// 判断真阳性/假阳性/假阴性
	isTruePositive, isFalsePositive, isFalseNegative := evaluateDetection(
		content, originalScore, originalDecision,
		result.Score, result.Decision, result.Threats,
	)

	return &TestResult{
		RequestID:         record.RequestID,
		SessionKey:        record.SessionID,
		Content:           content,
		OriginalScore:     originalScore,
		OriginalDecision:  originalDecision,
		OriginalThreats:   originalThreats,
		NewScore:          result.Score,
		NewDecision:       result.Decision,
		NewThreats:        string(threatsJSON),
		NewSensitiveWords: result.SensitiveWords,
		LatencyMs:         latencyMs,
		MatchedPatterns:   matchedPatterns,
		IsTruePositive:    isTruePositive,
		IsFalsePositive:   isFalsePositive,
		IsFalseNegative:   isFalseNegative,
	}, nil
}

// evaluateDetection 评估检测结果
func evaluateDetection(
	content string,
	origScore int, origDecision string,
	newScore int, newDecision string,
	threats []Threat,
) (isTruePositive, isFalsePositive, isFalseNegative bool) {
	// 简化评估逻辑
	contentLower := strings.ToLower(content)

	// 检查是否包含注入特征
	injectionPatterns := []string{
		"ignore previous", "ignore all", "ignore above",
		"disregard", "you are now", "system:",
		"jailbreak", "dan mode", "developer mode",
	}

	hasInjection := false
	for _, pattern := range injectionPatterns {
		if strings.Contains(contentLower, pattern) {
			hasInjection = true
			break
		}
	}

	// 检查是否包含敏感词
	sensitivePatterns := []string{
		"政变", "六四", "法轮功", "色情", "暴力",
		"毒品", "枪支", "炸药", "黑客", "漏洞",
	}

	hasSensitive := false
	for _, pattern := range sensitivePatterns {
		if strings.Contains(contentLower, pattern) {
			hasSensitive = true
			break
		}
	}

	// 评估新检测器的结果
	if hasInjection || hasSensitive {
		// 应该检测到威胁
		if newScore > 0 || len(threats) > 0 {
			isTruePositive = true
		} else {
			isFalseNegative = true
		}
	} else {
		// 不应该检测到威胁
		if newScore > 0 || len(threats) > 0 {
			isFalsePositive = true
		}
	}

	return
}

// insertTestResult 插入测试结果
func insertTestResult(ctx context.Context, db *sql.DB, result *TestResult) error {
	query := `
		INSERT INTO test_injection_results (
			request_id, session_key, content, original_score, original_decision,
			original_threats, new_score, new_decision, new_threats,
			new_sensitive_words, latency_ms, matched_patterns,
			is_true_positive, is_false_positive, is_false_negative
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`

	newThreatsJSON, _ := json.Marshal(result.NewThreats)
	newSensitiveWordsJSON, _ := json.Marshal(result.NewSensitiveWords)
	matchedPatternsJSON, _ := json.Marshal(result.MatchedPatterns)

	_, err := db.ExecContext(ctx, query,
		result.RequestID, result.SessionKey, result.Content,
		result.OriginalScore, result.OriginalDecision, result.OriginalThreats,
		result.NewScore, result.NewDecision, string(newThreatsJSON),
		string(newSensitiveWordsJSON), result.LatencyMs, string(matchedPatternsJSON),
		result.IsTruePositive, result.IsFalsePositive, result.IsFalseNegative,
	)

	return err
}

// analyzeResults 分析结果
func analyzeResults(results []*TestResult, totalLatency, maxLatency, minLatency int) *AnalysisResult {
	analysis := &AnalysisResult{
		TotalRecords:         len(results),
		ThreatDistribution:   make(map[string]int),
		DecisionDistribution: make(map[string]int),
	}

	if len(results) == 0 {
		return analysis
	}

	// 统计各项指标
	for _, result := range results {
		if result.NewScore > 0 {
			analysis.TotalDetections++
		}

		if result.IsTruePositive {
			analysis.TruePositives++
		}
		if result.IsFalsePositive {
			analysis.FalsePositives++
		}
		if result.IsFalseNegative {
			analysis.FalseNegatives++
		}
		if !result.IsTruePositive && !result.IsFalsePositive && !result.IsFalseNegative {
			analysis.TrueNegatives++
		}

		// 统计威胁分布
		var threats []Threat
		if err := json.Unmarshal([]byte(result.NewThreats), &threats); err == nil {
			for _, threat := range threats {
				analysis.ThreatDistribution[threat.Type]++
			}
		}

		// 统计决策分布
		analysis.DecisionDistribution[result.NewDecision]++
	}

	// 计算平均延迟
	analysis.AvgLatencyMs = float64(totalLatency) / float64(len(results))
	analysis.MaxLatencyMs = maxLatency
	analysis.MinLatencyMs = minLatency

	// 计算准确率、精确率、召回率、F1分数
	total := analysis.TruePositives + analysis.FalsePositives + analysis.FalseNegatives + analysis.TrueNegatives
	if total > 0 {
		analysis.Accuracy = float64(analysis.TruePositives+analysis.TrueNegatives) / float64(total)
	}

	if analysis.TruePositives+analysis.FalsePositives > 0 {
		analysis.Precision = float64(analysis.TruePositives) / float64(analysis.TruePositives+analysis.FalsePositives)
	}

	if analysis.TruePositives+analysis.FalseNegatives > 0 {
		analysis.Recall = float64(analysis.TruePositives) / float64(analysis.TruePositives+analysis.FalseNegatives)
	}

	if analysis.Precision+analysis.Recall > 0 {
		analysis.F1Score = 2 * analysis.Precision * analysis.Recall / (analysis.Precision + analysis.Recall)
	}

	return analysis
}

// printAnalysisReport 打印分析报告
func printAnalysisReport(analysis *AnalysisResult) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("会话注入检测测试分析报告")
	fmt.Println(strings.Repeat("=", 60))

	fmt.Printf("\n总记录数: %d\n", analysis.TotalRecords)
	fmt.Printf("总检测数: %d\n", analysis.TotalDetections)

	fmt.Println("\n--- 评估指标 ---")
	fmt.Printf("真阳性 (True Positives): %d\n", analysis.TruePositives)
	fmt.Printf("假阳性 (False Positives): %d\n", analysis.FalsePositives)
	fmt.Printf("假阴性 (False Negatives): %d\n", analysis.FalseNegatives)
	fmt.Printf("真阴性 (True Negatives): %d\n", analysis.TrueNegatives)

	fmt.Println("\n--- 性能指标 ---")
	fmt.Printf("平均延迟: %.2f ms\n", analysis.AvgLatencyMs)
	fmt.Printf("最大延迟: %d ms\n", analysis.MaxLatencyMs)
	fmt.Printf("最小延迟: %d ms\n", analysis.MinLatencyMs)

	fmt.Println("\n--- 准确性指标 ---")
	fmt.Printf("准确率 (Accuracy): %.2f%%\n", analysis.Accuracy*100)
	fmt.Printf("精确率 (Precision): %.2f%%\n", analysis.Precision*100)
	fmt.Printf("召回率 (Recall): %.2f%%\n", analysis.Recall*100)
	fmt.Printf("F1 分数: %.2f%%\n", analysis.F1Score*100)

	fmt.Println("\n--- 威胁类型分布 ---")
	for threatType, count := range analysis.ThreatDistribution {
		fmt.Printf("%s: %d\n", threatType, count)
	}

	fmt.Println("\n--- 决策分布 ---")
	for decision, count := range analysis.DecisionDistribution {
		fmt.Printf("%s: %d\n", decision, count)
	}

	fmt.Println(strings.Repeat("=", 60))
}

// generateOptimizationSuggestions 生成优化建议
func generateOptimizationSuggestions(analysis *AnalysisResult) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("优化建议")
	fmt.Println(strings.Repeat("=", 60))

	suggestions := []string{}

	// 基于假阳性率
	if analysis.FalsePositives > 0 {
		falsePositiveRate := float64(analysis.FalsePositives) / float64(analysis.TotalRecords)
		if falsePositiveRate > 0.1 {
			suggestions = append(suggestions,
				fmt.Sprintf("假阳性率过高 (%.2f%%)，建议：\n"+
					"  1. 调整检测阈值，提高 decision 阈值\n"+
					"  2. 添加白名单机制，排除已知安全内容\n"+
					"  3. 优化正则表达式，减少误匹配", falsePositiveRate*100))
		}
	}

	// 基于假阴性率
	if analysis.FalseNegatives > 0 {
		falseNegativeRate := float64(analysis.FalseNegatives) / float64(analysis.TotalRecords)
		if falseNegativeRate > 0.05 {
			suggestions = append(suggestions,
				fmt.Sprintf("假阴性率过高 (%.2f%%)，建议：\n"+
					"  1. 增加检测规则，覆盖更多攻击模式\n"+
					"  2. 降低检测阈值，提高敏感度\n"+
					"  3. 添加启发式检测，识别新型攻击", falseNegativeRate*100))
		}
	}

	// 基于性能
	if analysis.AvgLatencyMs > 5 {
		suggestions = append(suggestions,
			fmt.Sprintf("平均延迟过高 (%.2f ms)，建议：\n"+
				"  1. 优化正则表达式，使用更高效的匹配算法\n"+
				"  2. 实现缓存机制，避免重复计算\n"+
				"  3. 考虑使用 Trie 树替代正则表达式", analysis.AvgLatencyMs))
	}

	// 基于F1分数
	if analysis.F1Score < 0.8 {
		suggestions = append(suggestions,
			fmt.Sprintf("F1分数偏低 (%.2f%%)，建议：\n"+
				"  1. 平衡精确率和召回率\n"+
				"  2. 调整检测规则权重\n"+
				"  3. 考虑使用机器学习模型辅助检测", analysis.F1Score*100))
	}

	if len(suggestions) == 0 {
		suggestions = append(suggestions, "检测性能良好，无需特别优化")
	}

	for i, suggestion := range suggestions {
		fmt.Printf("\n%d. %s\n", i+1, suggestion)
	}

	fmt.Println(strings.Repeat("=", 60))
}

// getDiverseSensitiveWords 获取多样化敏感词
func getDiverseSensitiveWords() []string {
	return []string{
		// 政治敏感词
		"政变", "六四", "法轮功", "天安门事件", "六四事件",
		"民主运动", "政治迫害", "人权", "自由",

		// 色情暴力
		"色情", "暴力", "血腥", "裸体", "性行为",
		"强奸", "谋杀", "自杀", "自残",

		// 违禁品
		"毒品", "枪支", "炸药", "海洛因", "可卡因",
		"大麻", "冰毒", "摇头丸",

		// 网络安全
		"黑客", "漏洞", "入侵", "攻击", "恶意软件",
		"病毒", "木马", "钓鱼", "勒索软件",

		// 金融犯罪
		"洗钱", "诈骗", "非法集资", "传销", "庞氏骗局",

		// 其他敏感词
		"恐怖主义", "极端主义", "歧视", "仇恨",
		"虐待", "剥削", "拐卖", "走私",

		// 测试用敏感词
		"测试敏感词1", "测试敏感词2", "测试敏感词3",
	}
}

// getEnhancedInjectionPatterns 获取增强的注入检测模式
func getEnhancedInjectionPatterns() []string {
	return []string{
		// 基础注入模式
		`(?i)ignore\s+(previous|all|above)\s+instructions?`,
		`(?i)disregard\s+(previous|all)\s+(instructions?|prompts?)`,
		`(?i)you\s+are\s+now\s+a\s+different`,
		`(?i)system:\s*`,
		`(?i)<\|im_start\|>`,
		`(?i)__SYSTEM__`,

		// 增强注入模式
		`(?i)forget\s+everything\s+you\s+know`,
		`(?i)new\s+instructions?:`,
		`(?i)override\s+system\s+prompt`,
		`(?i)admin\s+mode\s+enabled`,
		`(?i)developer\s+console\s+activated`,
		`(?i)debug\s+mode\s+on`,
		`(?i)root\s+access\s+granted`,
		`(?i)sudo\s+mode`,
		`(?i)elevated\s+privileges`,
		`(?i)unrestricted\s+access`,

		// 多语言绕过
		`(?i)ignorez\s+les\s+instructions`,
		`(?i)ignoriere\s+die\s+Anweisungen`,
		`(?i)無視\s+指令`,
		`(?i)指示を\s+無視`,

		// 编码绕过
		`(?i)base64\s+encoded\s+instruction`,
		`(?i)hex\s+encoded\s+command`,
		`(?i)rot13\s+encoded\s+message`,

		// 特殊字符绕过
		`(?i)system\s*\|\s*user`,
		`(?i)assistant\s*\|\s*system`,
		`(?i)\[INST\].*\[/INST\]`,
		`(?i)<system>.*</system>`,

		// 角色切换
		`(?i)you\s+are\s+now\s+a\s+(hacker|malicious|evil)`,
		`(?i)pretend\s+you\s+are\s+(evil|malicious)`,
		`(?i)act\s+as\s+if\s+you\s+have\s+no\s+restrictions`,
	}
}

// getEnhancedPIIPatterns 获取增强的PII检测模式
func getEnhancedPIIPatterns() []string {
	return []string{
		// 信用卡号
		`\b\d{4}[\s-]?\d{4}[\s-]?\d{4}[\s-]?\d{4}\b`,
		`\b\d{4}\s\d{4}\s\d{4}\s\d{4}\b`,
		`\b\d{4}-\d{4}-\d{4}-\d{4}\b`,

		// 身份证号
		`\b\d{17}[\dXx]\b`,
		`\b\d{6}\s\d{4}\s\d{4}\s\d{3}[\dXx]\b`,

		// 手机号
		`\b1[3-9]\d{9}\b`,
		`\b1[3-9]\d[\s-]?\d{4}[\s-]?\d{4}\b`,

		// 邮箱
		`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`,

		// 银行卡号
		`\b\d{16,19}\b`,
		`\b\d{4}\s\d{4}\s\d{4}\s\d{4}\s\d{0,3}\b`,

		// 地址
		`\b\d{1,5}\s\w+\s(?:Street|St|Avenue|Ave|Road|Rd|Boulevard|Blvd|Drive|Dr|Court|Ct|Lane|Ln)\b`,

		// 护照号
		`\b[A-Z]\d{8}\b`,
	}
}

// getEnhancedJailbreakPatterns 获取增强的越狱检测模式
func getEnhancedJailbreakPatterns() []string {
	return []string{
		// 基础越狱模式
		`(?i)\bDAN\b`,
		`(?i)jailbreak`,
		`(?i)no\s+restrictions?`,
		`(?i)pretend\s+you\s+(are|can)`,
		`(?i)developer\s+mode`,

		// 增强越狱模式
		`(?i)do\s+anything\s+now`,
		`(?i)unlimited\s+mode`,
		`(?i)god\s+mode`,
		`(?i)admin\s+mode`,
		`(?i)root\s+mode`,
		`(?i)debug\s+mode`,
		`(?i)developer\s+console`,
		`(?i)elevated\s+access`,
		`(?i)unrestricted\s+mode`,
		`(?i)no\s+boundaries`,
		`(?i)without\s+limitations`,
		`(?i)ignore\s+safety`,
		`(?i)bypass\s+filter`,
		`(?i)disable\s+safety`,
		`(?i)turn\s+off\s+filter`,
		`(?i)remove\s+restrictions`,
		`(?i)break\s+rules`,
		`(?i)violate\s+guidelines`,
		`(?i)against\s+policy`,
		`(?i)forbidden\s+knowledge`,
		`(?i)secret\s+information`,
		`(?i)hidden\s+abilities`,
		`(?i)special\s+powers`,
		`(?i)ultimate\s+mode`,
		`(?i)chaos\s+mode`,
		`(?i)anarchy\s+mode`,
		`(?i)rebel\s+mode`,
		`(?i)pirate\s+mode`,
		`(?i)ninja\s+mode`,
		`(?i)samurai\s+mode`,
		`(?i)warrior\s+mode`,
		`(?i)assassin\s+mode`,
		`(?i)hitman\s+mode`,
		`(?i)killer\s+mode`,
		`(?i)murderer\s+mode`,
		`(?i)terrorist\s+mode`,
		`(?i)hacker\s+mode`,
		`(?i)cracker\s+mode`,
		`(?i)phreaker\s+mode`,
		`(?i)script\s+kiddie`,
		`(?i)black\s+hat`,
		`(?i)grey\s+hat`,
		`(?i)white\s+hat`,
		`(?i)security\s+researcher`,
		`(?i)penetration\s+tester`,
		`(?i)ethical\s+hacker`,
		`(?i)cyber\s+criminal`,
		`(?i)malicious\s+actor`,
		`(?i)threat\s+actor`,
		`(?i)advanced\s+persistent\s+threat`,
		`(?i)apt\s+group`,
		`(?i)state\s+sponsored`,
		`(?i)nation\s+state`,
	}
}
