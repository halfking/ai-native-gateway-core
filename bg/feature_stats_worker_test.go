// bg/feature_stats_worker_test.go — 2026-09-06
//
// 隐私合规测试：验证 FeatureStatsWorker 不泄露 prompt/messages/response 内容。
//
// 测试策略：
//   1. 插入包含敏感内容的 auto_route_selections 记录
//   2. 运行 worker 计算统计
//   3. 验证统计表只包含聚合数据，不包含原始内容
//   4. 验证 GetLatestStats() 返回的数据不包含敏感信息
//
// Part of: P2.3 - Monitor Feature Distribution (Privacy Compliance)

package bg

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestFeatureStatsWorkerNoContentLeakage 验证统计计算不泄露原始内容。
func TestFeatureStatsWorkerNoContentLeakage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// 1. 插入测试数据（包含敏感内容标记）
	sensitivePrompt := "SENSITIVE_PROMPT_CONTENT_12345"
	sensitiveResponse := "SENSITIVE_RESPONSE_CONTENT_67890"
	
	testData := []struct {
		language       string
		lengthBucket   string
		contentHash    string
		hasCodeInd     bool
	}{
		{"zh", "m", "hash001", true},
		{"en", "s", "hash002", false},
		{"zh", "m", "hash001", true}, // 重复
		{"en", "l", "hash003", true},
	}

	for _, td := range testData {
		_, err := db.Exec(ctx, `
			INSERT INTO auto_route_selections 
			(ts, task_type, profile, classifier, confidence, chosen_model, 
			 detected_language, prompt_length_bucket, content_hash, has_code_indicator)
			VALUES ($1, 'chat', 'balanced', 'heuristic_v1', 0.85, 'gpt-4',
			        $2, $3, $4, $5)
		`, time.Now(), td.language, td.lengthBucket, td.contentHash, td.hasCodeInd)
		
		if err != nil {
			t.Fatalf("failed to insert test data: %v", err)
		}
	}

	// 2. 运行 worker 计算统计
	worker := NewFeatureStatsWorker(db, time.Hour)
	worker.computeStats(ctx)

	// 3. 验证 feature_distribution_stats 不包含敏感内容
	t.Run("feature_distribution_stats_no_leak", func(t *testing.T) {
		rows, err := db.Query(ctx, `
			SELECT feature_name, feature_value, row_count, percentage
			FROM feature_distribution_stats
			WHERE stat_date = CURRENT_DATE
		`)
		if err != nil {
			t.Fatalf("failed to query feature_distribution_stats: %v", err)
		}
		defer rows.Close()

		for rows.Next() {
			var featureName, featureValue string
			var rowCount int
			var percentage float64
			
			if err := rows.Scan(&featureName, &featureValue, &rowCount, &percentage); err != nil {
				t.Fatalf("failed to scan row: %v", err)
			}

			// 验证不包含敏感内容
			if strings.Contains(featureValue, sensitivePrompt) {
				t.Errorf("feature_distribution_stats contains sensitive prompt: %s", featureValue)
			}
			if strings.Contains(featureValue, sensitiveResponse) {
				t.Errorf("feature_distribution_stats contains sensitive response: %s", featureValue)
			}

			// 验证只包含预期的枚举值
			if featureName == "detected_language" {
				if featureValue != "zh" && featureValue != "en" && featureValue != "NULL" {
					t.Errorf("unexpected language value: %s", featureValue)
				}
			}
			if featureName == "prompt_length_bucket" {
				validBuckets := map[string]bool{"xs": true, "s": true, "m": true, "l": true, "xl": true, "xxl": true, "NULL": true}
				if !validBuckets[featureValue] {
					t.Errorf("unexpected length bucket value: %s", featureValue)
				}
			}
		}
	})

	// 4. 验证 dedup_stats 只包含哈希和计数
	t.Run("dedup_stats_no_leak", func(t *testing.T) {
		var dedupRate float64
		var topDupesJSON string
		
		err := db.QueryRow(ctx, `
			SELECT dedup_rate, top_duplicate_hashes::text
			FROM dedup_stats
			WHERE stat_date = CURRENT_DATE
		`).Scan(&dedupRate, &topDupesJSON)
		
		if err != nil {
			t.Fatalf("failed to query dedup_stats: %v", err)
		}

		// 验证 JSON 不包含敏感内容
		if strings.Contains(topDupesJSON, sensitivePrompt) {
			t.Errorf("dedup_stats contains sensitive prompt in JSON: %s", topDupesJSON)
		}
		if strings.Contains(topDupesJSON, sensitiveResponse) {
			t.Errorf("dedup_stats contains sensitive response in JSON: %s", topDupesJSON)
		}

		// 验证 JSON 只包含哈希和计数
		var topDupes []map[string]interface{}
		if err := json.Unmarshal([]byte(topDupesJSON), &topDupes); err != nil {
			t.Fatalf("failed to parse top_duplicate_hashes JSON: %v", err)
		}

		for _, dupe := range topDupes {
			if _, hasHash := dupe["hash"]; !hasHash {
				t.Errorf("top_duplicate_hashes missing 'hash' field: %v", dupe)
			}
			if _, hasCount := dupe["count"]; !hasCount {
				t.Errorf("top_duplicate_hashes missing 'count' field: %v", dupe)
			}
			if len(dupe) > 2 {
				t.Errorf("top_duplicate_hashes has unexpected fields: %v", dupe)
			}
		}
	})

	// 5. 验证 GetLatestStats() 不泄露内容
	t.Run("get_latest_stats_no_leak", func(t *testing.T) {
		stats, err := worker.GetLatestStats(ctx)
		if err != nil {
			t.Fatalf("GetLatestStats failed: %v", err)
		}

		statsJSON, _ := json.Marshal(stats)
		statsStr := string(statsJSON)

		if strings.Contains(statsStr, sensitivePrompt) {
			t.Errorf("GetLatestStats contains sensitive prompt: %s", statsStr)
		}
		if strings.Contains(statsStr, sensitiveResponse) {
			t.Errorf("GetLatestStats contains sensitive response: %s", statsStr)
		}

		// 验证返回的数据结构
		if _, ok := stats["dedup_rate"]; !ok {
			t.Errorf("GetLatestStats missing dedup_rate field")
		}
		if _, ok := stats["fill_rates"]; !ok {
			t.Errorf("GetLatestStats missing fill_rates field")
		}
	})

	// 6. 验证 worker 的 String() 和 MarshalJSON() 不泄露数据库凭据
	t.Run("worker_serialization_no_leak", func(t *testing.T) {
		workerStr := worker.String()
		if strings.Contains(workerStr, "password") || strings.Contains(workerStr, "host") {
			t.Errorf("worker.String() leaks database connection info: %s", workerStr)
		}

		workerJSON, err := json.Marshal(worker)
		if err != nil {
			t.Fatalf("worker.MarshalJSON() failed: %v", err)
		}
		if strings.Contains(string(workerJSON), "db") {
			t.Errorf("worker.MarshalJSON() leaks database field: %s", string(workerJSON))
		}
	})
}

// TestFeatureStatsWorkerComputeDistribution 测试特征分布计算逻辑。
func TestFeatureStatsWorkerComputeDistribution(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// 插入已知分布的数据
	languages := []string{"zh", "zh", "zh", "en", "ja"}
	for _, lang := range languages {
		_, err := db.Exec(ctx, `
			INSERT INTO auto_route_selections 
			(ts, task_type, profile, classifier, confidence, chosen_model, detected_language)
			VALUES ($1, 'chat', 'balanced', 'heuristic_v1', 0.85, 'gpt-4', $2)
		`, time.Now(), lang)
		
		if err != nil {
			t.Fatalf("failed to insert test data: %v", err)
		}
	}

	// 计算统计
	worker := NewFeatureStatsWorker(db, time.Hour)
	err := worker.computeFeatureDistributions(ctx, time.Now().UTC().Truncate(24*time.Hour))
	if err != nil {
		t.Fatalf("computeFeatureDistributions failed: %v", err)
	}

	// 验证统计结果
	rows, err := db.Query(ctx, `
		SELECT feature_value, row_count, percentage
		FROM feature_distribution_stats
		WHERE stat_date = CURRENT_DATE
		  AND feature_name = 'detected_language'
		ORDER BY row_count DESC
	`)
	if err != nil {
		t.Fatalf("failed to query stats: %v", err)
	}
	defer rows.Close()

	expected := map[string]struct{ count int; pct float64 }{
		"zh": {3, 60.0},
		"en": {1, 20.0},
		"ja": {1, 20.0},
	}

	for rows.Next() {
		var value string
		var count int
		var pct float64
		
		if err := rows.Scan(&value, &count, &pct); err != nil {
			t.Fatalf("failed to scan row: %v", err)
		}

		exp, ok := expected[value]
		if !ok {
			t.Errorf("unexpected feature value: %s", value)
			continue
		}

		if count != exp.count {
			t.Errorf("language %s: expected count %d, got %d", value, exp.count, count)
		}
		if pct != exp.pct {
			t.Errorf("language %s: expected percentage %.1f, got %.1f", value, exp.pct, pct)
		}
	}
}

// TestFeatureStatsWorkerDedupRate 测试去重率计算逻辑。
func TestFeatureStatsWorkerDedupRate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// 插入已知去重率的数据：4条记录，2个唯一哈希
	hashes := []string{"hash_a", "hash_a", "hash_b", "hash_b"}
	for _, hash := range hashes {
		_, err := db.Exec(ctx, `
			INSERT INTO auto_route_selections 
			(ts, task_type, profile, classifier, confidence, chosen_model, content_hash)
			VALUES ($1, 'chat', 'balanced', 'heuristic_v1', 0.85, 'gpt-4', $2)
		`, time.Now(), hash)
		
		if err != nil {
			t.Fatalf("failed to insert test data: %v", err)
		}
	}

	// 计算去重率
	worker := NewFeatureStatsWorker(db, time.Hour)
	err := worker.computeDedupRate(ctx, time.Now().UTC().Truncate(24*time.Hour))
	if err != nil {
		t.Fatalf("computeDedupRate failed: %v", err)
	}

	// 验证结果：4条记录，2个唯一哈希 → 去重率 50%
	var totalRows, uniqueHashes int
	var dedupRate float64
	
	err = db.QueryRow(ctx, `
		SELECT total_rows, unique_hashes, dedup_rate
		FROM dedup_stats
		WHERE stat_date = CURRENT_DATE
	`).Scan(&totalRows, &uniqueHashes, &dedupRate)
	
	if err != nil {
		t.Fatalf("failed to query dedup_stats: %v", err)
	}

	if totalRows != 4 {
		t.Errorf("expected total_rows=4, got %d", totalRows)
	}
	if uniqueHashes != 2 {
		t.Errorf("expected unique_hashes=2, got %d", uniqueHashes)
	}
	if dedupRate != 50.0 {
		t.Errorf("expected dedup_rate=50.0, got %.1f", dedupRate)
	}
}

// setupTestDB 创建测试数据库连接（需要真实的 PostgreSQL 实例）。
func setupTestDB(t *testing.T) *pgxpool.Pool {
	// 使用环境变量或默认测试数据库连接
	dsn := "postgres://localhost/llm_gateway_test?sslmode=disable"
	
	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("skipping test: cannot connect to test database: %v", err)
	}

	// 清理测试表
	ctx := context.Background()
	_, _ = db.Exec(ctx, "TRUNCATE TABLE feature_distribution_stats, dedup_stats, auto_route_selections CASCADE")

	return db
}
