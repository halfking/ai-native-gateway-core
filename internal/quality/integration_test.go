package quality

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// getTestDB 获取测试数据库连接
func getTestDB(t *testing.T) *sql.DB {
	// 优先使用环境变量
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		// 默认使用本地 Docker
		dsn = "postgres://kxuser:kxpass@localhost:15432/llm_gateway?sslmode=disable"
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("无法连接测试数据库: %v", err)
	}

	// 测试连接
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Skipf("测试数据库不可达: %v", err)
	}

	return db
}

// cleanupTestData 清理测试数据
func cleanupTestData(t *testing.T, db *sql.DB) {
	ctx := context.Background()

	queries := []string{
		"DELETE FROM provider_metrics_minute WHERE bucket >= NOW() - INTERVAL '1 hour'",
		"DELETE FROM provider_metrics_hour WHERE bucket >= NOW() - INTERVAL '2 hours'",
	}

	for _, q := range queries {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Logf("清理测试数据失败: %v", err)
		}
	}
}

// TestCollectMinuteMetrics_Integration 集成测试：分钟级聚合
func TestCollectMinuteMetrics_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试（-short 模式）")
	}

	db := getTestDB(t)
	defer db.Close()
	defer cleanupTestData(t, db)

	collector := New(db, WithTimeout(10*time.Second))

	ctx := context.Background()

	// 执行聚合
	err := collector.CollectMinuteMetrics(ctx)
	if err != nil {
		t.Fatalf("CollectMinuteMetrics() failed: %v", err)
	}

	// 验证数据是否写入（可能为 0 行，如果 request_logs 为空）
	var count int
	query := `SELECT COUNT(*) FROM provider_metrics_minute 
	          WHERE bucket >= NOW() - INTERVAL '5 minutes'`
	err = db.QueryRowContext(ctx, query).Scan(&count)
	if err != nil {
		t.Fatalf("查询聚合结果失败: %v", err)
	}

	t.Logf("分钟级聚合成功，插入/更新 %d 行", count)
}

// TestCollectHourMetrics_Integration 集成测试：小时级聚合
func TestCollectHourMetrics_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试（-short 模式）")
	}

	db := getTestDB(t)
	defer db.Close()
	defer cleanupTestData(t, db)

	collector := New(db, WithTimeout(10*time.Second))

	ctx := context.Background()

	// 执行聚合
	err := collector.CollectHourMetrics(ctx)
	if err != nil {
		t.Fatalf("CollectHourMetrics() failed: %v", err)
	}

	// 验证数据是否写入
	var count int
	query := `SELECT COUNT(*) FROM provider_metrics_hour 
	          WHERE bucket >= NOW() - INTERVAL '2 hours'`
	err = db.QueryRowContext(ctx, query).Scan(&count)
	if err != nil {
		t.Fatalf("查询聚合结果失败: %v", err)
	}

	t.Logf("小时级聚合成功，插入/更新 %d 行", count)
}

// TestCollectMinuteMetrics_Idempotent 测试幂等性
func TestCollectMinuteMetrics_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试（-short 模式）")
	}

	db := getTestDB(t)
	defer db.Close()
	defer cleanupTestData(t, db)

	collector := New(db, WithTimeout(10*time.Second))
	ctx := context.Background()

	// 第一次执行
	err1 := collector.CollectMinuteMetrics(ctx)
	if err1 != nil {
		t.Fatalf("第一次聚合失败: %v", err1)
	}

	// 查询第一次结果
	var count1 int
	query := `SELECT COUNT(*) FROM provider_metrics_minute 
	          WHERE bucket >= NOW() - INTERVAL '5 minutes'`
	db.QueryRowContext(ctx, query).Scan(&count1)

	// 第二次执行（应该幂等）
	err2 := collector.CollectMinuteMetrics(ctx)
	if err2 != nil {
		t.Fatalf("第二次聚合失败: %v", err2)
	}

	// 查询第二次结果
	var count2 int
	db.QueryRowContext(ctx, query).Scan(&count2)

	// 两次结果应该相同（幂等）
	if count1 != count2 {
		t.Errorf("幂等性测试失败: 第一次 %d 行，第二次 %d 行", count1, count2)
	}

	t.Logf("幂等性测试通过：两次执行均为 %d 行", count1)
}

// TestCollect_WithTestData 使用测试数据验证聚合逻辑
func TestCollect_WithTestData(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试（-short 模式）")
	}

	db := getTestDB(t)
	defer db.Close()
	defer cleanupTestData(t, db)

	ctx := context.Background()

	// 插入测试数据到 request_logs
	testData := `
INSERT INTO request_logs (
    provider_id, model_name, endpoint, created_at,
    status_code, latency_ms, input_tokens, output_tokens, cost
) VALUES 
    (1, 'test-model', 'chat', date_trunc('minute', NOW() - INTERVAL '30 seconds'), 200, 1000, 100, 50, 0.01),
    (1, 'test-model', 'chat', date_trunc('minute', NOW() - INTERVAL '30 seconds'), 200, 1200, 120, 60, 0.012),
    (1, 'test-model', 'chat', date_trunc('minute', NOW() - INTERVAL '30 seconds'), 500, 5000, 100, 0, 0.01)
ON CONFLICT DO NOTHING;
`
	_, err := db.ExecContext(ctx, testData)
	if err != nil {
		t.Skipf("插入测试数据失败（可能表结构不匹配）: %v", err)
	}

	// 执行聚合
	collector := New(db, WithTimeout(10*time.Second))
	err = collector.CollectMinuteMetrics(ctx)
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}

	// 验证聚合结果
	var totalRequests, successfulRequests, error5xx int
	query := `
SELECT total_requests, successful_requests, error_5xx
FROM provider_metrics_minute
WHERE provider_id = 1
  AND model_name = 'test-model'
  AND endpoint = 'chat'
  AND bucket = date_trunc('minute', NOW() - INTERVAL '30 seconds')
`
	err = db.QueryRowContext(ctx, query).Scan(&totalRequests, &successfulRequests, &error5xx)
	if err != nil {
		t.Fatalf("查询聚合结果失败: %v", err)
	}

	// 验证数据正确性
	if totalRequests != 3 {
		t.Errorf("total_requests = %d, want 3", totalRequests)
	}
	if successfulRequests != 2 {
		t.Errorf("successful_requests = %d, want 2", successfulRequests)
	}
	if error5xx != 1 {
		t.Errorf("error_5xx = %d, want 1", error5xx)
	}

	t.Logf("测试数据验证通过: %d 请求, %d 成功, %d 5xx", totalRequests, successfulRequests, error5xx)

	// 清理测试数据
	db.ExecContext(ctx, "DELETE FROM request_logs WHERE model_name = 'test-model'")
}

// Example_usage 使用示例
func Example_usage() {
	// 连接数据库
	db, _ := sql.Open("pgx", "postgres://user:pass@localhost/db")
	defer db.Close()

	// 创建采集器
	collector := New(db,
		WithMinuteInterval(1*time.Minute),
		WithHourInterval(1*time.Hour),
		WithTimeout(30*time.Second),
	)

	// 启动采集器
	ctx := context.Background()
	go collector.Start(ctx)

	// 或手动触发
	collector.CollectMinuteMetrics(ctx)
	collector.CollectHourMetrics(ctx)

	fmt.Println("质量指标采集器已启动")
	// Output: 质量指标采集器已启动
}
