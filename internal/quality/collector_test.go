package quality

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestNew 测试创建采集器
func TestNew(t *testing.T) {
	db := &sql.DB{}
	collector := New(db)

	if collector == nil {
		t.Fatal("New() returned nil")
	}
}

// TestNew_WithOptions 测试自定义配置
func TestNew_WithOptions(t *testing.T) {
	db := &sql.DB{}
	collector := New(db,
		WithMinuteInterval(2*time.Minute),
		WithHourInterval(2*time.Hour),
		WithTimeout(60*time.Second),
		WithEnabled(false),
	)

	if collector == nil {
		t.Fatal("New() with options returned nil")
	}
}

// TestStart_Disabled 测试禁用时不启动
func TestStart_Disabled(t *testing.T) {
	db := &sql.DB{}
	collector := New(db, WithEnabled(false))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := collector.Start(ctx)

	// 禁用时应立即返回 nil
	if err != nil {
		t.Errorf("Start() with disabled should return nil, got %v", err)
	}
}

// TestCollectMinuteMetrics_NoDatabase 测试无数据库时的行为
func TestCollectMinuteMetrics_NoDatabase(t *testing.T) {
	// 这个测试需要真实数据库连接，跳过
	t.Skip("需要真实数据库连接")

	db := &sql.DB{}
	collector := New(db)

	ctx := context.Background()
	err := collector.CollectMinuteMetrics(ctx)

	// 没有数据库应该返回错误
	if err == nil {
		t.Error("CollectMinuteMetrics() without database should return error")
	}
}

// TestCollectHourMetrics_NoDatabase 测试无数据库时的行为
func TestCollectHourMetrics_NoDatabase(t *testing.T) {
	// 这个测试需要真实数据库连接，跳过
	t.Skip("需要真实数据库连接")

	db := &sql.DB{}
	collector := New(db)

	ctx := context.Background()
	err := collector.CollectHourMetrics(ctx)

	// 没有数据库应该返回错误
	if err == nil {
		t.Error("CollectHourMetrics() without database should return error")
	}
}

// BenchmarkCollectMinuteMetrics 性能测试
func BenchmarkCollectMinuteMetrics(b *testing.B) {
	b.Skip("需要真实数据库连接")
}
