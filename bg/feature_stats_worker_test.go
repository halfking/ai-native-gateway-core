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
	"fmt"
	"os"
	"regexp"
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

	// ★ 用例级前置守卫：关系不在 ⇒ 诚实 SKIP 并点名缺哪个（靶子不对 ≠ 产品缺陷）。
	requireFeatureStatsRelations(t, db, "auto_route_selections", "feature_distribution_stats", "dedup_stats")

	ctx := context.Background()

	// 1. 插入测试数据（包含敏感内容标记）
	sensitivePrompt := "SENSITIVE_PROMPT_CONTENT_12345"
	sensitiveResponse := "SENSITIVE_RESPONSE_CONTENT_67890"

	testData := []struct {
		language     string
		lengthBucket string
		contentHash  string
		hasCodeInd   bool
	}{
		{"zh", "m", "hash001", true},
		{"en", "s", "hash002", false},
		{"zh", "m", "hash001", true}, // 重复
		{"en", "l", "hash003", true},
	}

	// ★★ R85 轮：种子必须落在 worker 的**统计日窗口**内，否则 worker 读到**空集**、
	//   断言变成空转。worker 的窗口是 `[statDateUTC, +1 day)`（`computeFeatureDistributions`），
	//   而旧夹具写的是 `time.Now()`（**本地时区**）⇒ 本机 UTC+8 时 06:00 本地 = **前一天 22:00 UTC**
	//   ⇒ 落在窗口外 ⇒ worker 什么都没算 ⇒ 「通过」是假的。
	//   ⇒ 这里取「今天 UTC 零点 + 1 分钟」⇒ 恒在 [今天 UTC, 明天 UTC) 内。
	seedTS := time.Now().UTC().Truncate(24 * time.Hour).Add(time.Minute)
	for i, td := range testData {
		_, err := db.Exec(ctx, `
			INSERT INTO auto_route_selections 
			(request_id, ts, task_type, profile, classifier, confidence, chosen_model, 
			 detected_language, prompt_length_bucket, content_hash, has_code_indicator)
			VALUES ($1, $2, 'chat', 'smart', 'heuristic_v1', 0.85, 'gpt-4',
			        $3, $4, $5, $6)
		`, fmt.Sprintf("r85fs-%d", i), seedTS, td.language, td.lengthBucket, td.contentHash, td.hasCodeInd)

		if err != nil {
			// ★★ R85 轮（R80-B 收敛）：插入失败 ⇒ **红**，不再 SKIP。
			//   「关系在不在」已由**用例级前置守卫**（requireFeatureStatsRelations）在插入**之前**问过了
			//   ⇒ ⇒ 此时的任何失败都是**夹具漂移或真缺陷** ⇒ 必须红。
			//   （旧形态：isSchemaShapeMismatch 把失败吞成 SKIP ⇒ 门永远绿、什么都证明不了。）
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
			ORDER BY stat_date DESC
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

		// ★★ R85 轮：原来写 `WHERE stat_date = CURRENT_DATE` —— 那是**DB 会话本地日期**，
		//   而 worker 写入的是 **UTC 日期**（`time.Now().UTC().Truncate(24h)`）
		//   ⇒ ⇒ 两者只在「本地日期 == UTC 日期」的 8 小时里相等
		//   ⇒ ⇒ **这道门按挂钟小时变化**（本机 UTC+8 ⇒ 00:00~08:00 必红）★★
		//   ⇒ 改成「取最新一行」⇒ 与产品 `GetLatestStats`（`ORDER BY stat_date DESC LIMIT 1`）
		//   同语义 ⇒ ⇒ 既与时区无关，也没有跨零点的竞态。
		err := db.QueryRow(ctx, `
			SELECT dedup_rate, top_duplicate_hashes::text
			FROM dedup_stats
			ORDER BY stat_date DESC
			LIMIT 1
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

	requireFeatureStatsRelations(t, db, "auto_route_selections", "feature_distribution_stats")

	ctx := context.Background()

	// 插入已知分布的数据
	languages := []string{"zh", "zh", "zh", "en", "ja"}
	seedTS := time.Now().UTC().Truncate(24 * time.Hour).Add(time.Minute)
	for i, lang := range languages {
		_, err := db.Exec(ctx, `
			INSERT INTO auto_route_selections 
			(request_id, ts, task_type, profile, classifier, confidence, chosen_model, detected_language)
			VALUES ($1, $2, 'chat', 'smart', 'heuristic_v1', 0.85, 'gpt-4', $3)
		`, fmt.Sprintf("r85fs-dist-%d", i), seedTS, lang)

		if err != nil {
			// ★★ R85 轮（R80-B 收敛）：插入失败 ⇒ **红**，不再 SKIP。
			//   「关系在不在」已由**用例级前置守卫**（requireFeatureStatsRelations）在插入**之前**问过了
			//   ⇒ ⇒ 此时的任何失败都是**夹具漂移或真缺陷** ⇒ 必须红。
			//   （旧形态：isSchemaShapeMismatch 把失败吞成 SKIP ⇒ 门永远绿、什么都证明不了。）
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
		WHERE feature_name = 'detected_language'
		ORDER BY stat_date DESC, row_count DESC
	`)
	if err != nil {
		t.Fatalf("failed to query stats: %v", err)
	}
	defer rows.Close()

	expected := map[string]struct {
		count int
		pct   float64
	}{
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

	requireFeatureStatsRelations(t, db, "auto_route_selections", "dedup_stats")

	ctx := context.Background()

	// 插入已知去重率的数据：4条记录，2个唯一哈希
	hashes := []string{"hash_a", "hash_a", "hash_b", "hash_b"}
	seedTS := time.Now().UTC().Truncate(24 * time.Hour).Add(time.Minute)
	for i, hash := range hashes {
		_, err := db.Exec(ctx, `
			INSERT INTO auto_route_selections 
			(request_id, ts, task_type, profile, classifier, confidence, chosen_model, content_hash)
			VALUES ($1, $2, 'chat', 'smart', 'heuristic_v1', 0.85, 'gpt-4', $3)
		`, fmt.Sprintf("r85fs-dedup-%d", i), seedTS, hash)

		if err != nil {
			// ★★ R85 轮（R80-B 收敛）：插入失败 ⇒ **红**，不再 SKIP。
			//   「关系在不在」已由**用例级前置守卫**（requireFeatureStatsRelations）在插入**之前**问过了
			//   ⇒ ⇒ 此时的任何失败都是**夹具漂移或真缺陷** ⇒ 必须红。
			//   （旧形态：isSchemaShapeMismatch 把失败吞成 SKIP ⇒ 门永远绿、什么都证明不了。）
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
		ORDER BY stat_date DESC
		LIMIT 1
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
// scratchDBName = 一次性库的库名约定（破坏性夹具只对这类库跑）。
var scratchDBName = regexp.MustCompile(`(?i)(itgate|scratch|test|tmp|bench|pg_)`)

func setupTestDB(t *testing.T) *pgxpool.Pool {
	// 使用环境变量或默认测试数据库连接。pgxpool.New 是懒连接，构造不报错，
	// 必须 Ping 一次让无库/拒认证环境走 Skip（否则首个 Exec 才炸出连接错误）。
	dsn := os.Getenv("LLM_GATEWAY_FEATURE_STATS_TEST_DSN")
	if dsn == "" {
		// ★ R70 轮：原先 fallback 到写死的 `postgres://localhost/llm_gateway_test` ⇒
		//   连不上 ⇒ 静默 SKIP 被 `go test` 记成绿（R69 §19.18 记的两种形态之一）⇒ 不再 fallback。
		//
		// ★★ R89 轮订正（R86 轮回执要求的第 ① 条）：**原先那句提示已经过期。**
		//   它让人「source scripts/audit/datasource_env.sh」⇒ 但 R86 轮把这个变量
		//   **移出了事实源**（`datasource_env.sh:89` 起逐条写明「不在事实源里」）
		//   ⇒ ⇒ **source 那个脚本也拿不到它** ⇒ 照旧提示做的人会**永远卡在这条 SKIP 上**。
		//   ⇒ 事实源自己的注释（`datasource_env.sh:98`）已经把正确做法写出来了：
		//   「★ 真要跑它 ⇒ 显式 export 一个指向一次性库的 DSN。」⇒ 这里对齐它。
		//   ⚠ 而「指向一次性库」不是客气话：本函数会 `TRUNCATE … CASCADE` 三张表，
		//     而下面的库名守卫只放行 itgate|scratch|test|tmp|bench|pg_ ⇒
		//     **库名 llm_gateway 必被拒绝**（这是安全拒绝，不是缺陷）。
		t.Skip("LLM_GATEWAY_FEATURE_STATS_TEST_DSN not set —— ★ 它是 dsn_exclusive（专用值），" +
			"**不在事实源里**（scripts/audit/datasource_env.sh 不导出它 ⇒ source 那个脚本也拿不到）⇒ " +
			"真要跑这条腿 ⇒ 显式 export 一个指向一次性库的 DSN" +
			"（库名须含 itgate|scratch|test|tmp|bench|pg_，否则下面的库名守卫会拒绝）；" +
			"未设时本门不构成证据")
	}

	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("skipping test: cannot connect to test database: %v", err)
	}
	if err := db.Ping(context.Background()); err != nil {
		db.Close()
		t.Skipf("skipping test: test database unreachable: %v", err)
	}

	// ★★★★ R70 轮：**破坏性守卫**（本轮最重要的一条修法）。
	//   下面这句是 `TRUNCATE ... CASCADE` ⇒ **它会清空三张表**。
	//   ⇒ ★★★ 而 R70 轮把本函数的 DSN 从「写死的 `localhost/llm_gateway_test`」
	//   改成了「认事实源」⇒ ⇒ ★★ **若事实源指向的是共享真库，这条腿一跑就会清空它**
	//   ⇒ ⇒ ⇒ **⇒「让一条真库腿认事实源」有前提：前提是它的写操作不破坏共享数据。**
	//   ⇒ ⇒ ⇒ 本仓既有纪律：**破坏性夹具只对一次性库跑**
	//   （gate 脚本自己建 `itgate_*` 库；R59 §17.7-4 记过这条库名约定）。
	//   ⇒ ⇒ ⇒ 所以这里显式化成**可执行的守卫**：库名不合约定 ⇒ 明确 SKIP 并说明。
	var dbName string
	if err := db.QueryRow(context.Background(), `SELECT current_database()`).Scan(&dbName); err != nil {
		t.Skipf("cannot read current database name: %v", err)
	}
	if !scratchDBName.MatchString(dbName) {
		t.Skipf("当前库 %q 不是一次性库（命名须含 itgate|scratch|test|tmp|bench|pg_）—— "+
			"本门会 TRUNCATE 三张表，**只允许对一次性库跑**；未设 LLM_GATEWAY_FEATURE_STATS_TEST_DSN 时也不构成证据", dbName)
	}

	// 清理测试表
	ctx := context.Background()
	_, _ = db.Exec(ctx, "TRUNCATE TABLE feature_distribution_stats, dedup_stats, auto_route_selections CASCADE")

	return db
}

// requireFeatureStatsRelations 是**用例级**前置守卫：关系不在 ⇒ 诚实 SKIP 并点名缺哪个。
//
// ★★ R85 轮（R80-B 收敛）：它**取代**了本文件原先那份 `isSchemaShapeMismatch` 副本。
//
//	★ 两条边界**互补**，不是同一条：
//	  - 「关系不在」⇒ **靶子不对**（环境不是产品形态）⇒ **诚实 SKIP**；
//	  - 「关系在、但插入失败」⇒ **夹具漂移或真缺陷**⇒ **必须红**。
//	⇒ ⇒ 旧形态把两者**混在一个判据里**（插入失败 ⇒ SKIP）⇒ ⇒ 门永远绿、什么都证明不了。
func requireFeatureStatsRelations(t *testing.T, db *pgxpool.Pool, rels ...string) {
	t.Helper()
	ctx := context.Background()
	for _, rel := range rels {
		var got string
		if err := db.QueryRow(ctx,
			`SELECT COALESCE(to_regclass('public.' || $1)::text, '')`, rel).Scan(&got); err != nil {
			t.Fatalf("探测关系 public.%s 失败: %v", rel, err)
		}
		if got == "" {
			t.Skipf("关系 public.%s 不存在 —— 本用例的取数源不在当前库里，"+
				"本次**没有**跑真库行为验证（不是通过）", rel)
		}
	}
}
