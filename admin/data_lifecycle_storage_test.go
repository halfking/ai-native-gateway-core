// Unit tests for the storage overview helpers (2026-07-01).
//
// We test pure functions (humanBytes, queryFilesystem) without a real DB.
// The DB-bound functions (queryDatabaseStorage, queryTableSizes) require
// a live pgxpool.Pool and are exercised via the existing data lifecycle
// partition_test.go in CI with the test harness.

package admin

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1 MB"},
		{2 * 1024 * 1024 * 1024, "2 GB"},
		{5 * 1024 * 1024 * 1024 * 1024, "5 TB"},
	}
	for _, tt := range tests {
		got := humanBytes(tt.in)
		if got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHumanBytesExportAlias(t *testing.T) {
	// humanBytesExport should be the same as humanBytes
	if humanBytesExport(1024) != humanBytes(1024) {
		t.Error("export alias mismatch")
	}
}

func TestFormatInt(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{12345, "12345"},
		{-7, "-7"},
	}
	for _, tt := range tests {
		got := formatInt(tt.in)
		if got != tt.want {
			t.Errorf("formatInt(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatFloat(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0.0"},
		{1.5, "1.5"},
		{10.25, "10.2"},
		{1234.5, "1234.5"},
	}
	for _, tt := range tests {
		got := formatFloat(tt.in)
		if got != tt.want {
			t.Errorf("formatFloat(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestQueryFilesystem(t *testing.T) {
	// 探测当前目录（保证测试在所有环境都能跑）
	info, err := queryFilesystem(".")
	if err != nil {
		t.Fatalf("queryFilesystem failed: %v", err)
	}
	if info.TotalBytes <= 0 {
		t.Errorf("expected TotalBytes > 0, got %d", info.TotalBytes)
	}
	if info.FreeBytes < 0 {
		t.Errorf("expected FreeBytes >= 0, got %d", info.FreeBytes)
	}
	if info.UsedBytes+info.FreeBytes > info.TotalBytes {
		t.Errorf("used(%d) + free(%d) > total(%d)",
			info.UsedBytes, info.FreeBytes, info.TotalBytes)
	}
	if info.UsedPercent < 0 || info.UsedPercent > 100 {
		t.Errorf("UsedPercent out of range: %d", info.UsedPercent)
	}
	if !strings.HasPrefix(info.Path, "/") {
		t.Errorf("Path should be absolute, got %q", info.Path)
	}
}

func TestQueryDirectory(t *testing.T) {
	// 不存在的路径
	info := queryDirectory("/this/path/should/not/exist/1234567")
	if info.Exists {
		t.Error("expected Exists=false for non-existent path")
	}
	if info.Files != 0 || info.SizeBytes != 0 {
		t.Errorf("expected zero counts, got files=%d bytes=%d", info.Files, info.SizeBytes)
	}

	// "." 一定存在
	cwd := queryDirectory(".")
	if !cwd.Exists {
		t.Error("expected Exists=true for current dir")
	}
	if !strings.HasPrefix(cwd.Path, "/") {
		t.Errorf("Path should be absolute, got %q", cwd.Path)
	}
}

func TestSortTablesDesc(t *testing.T) {
	tables := []tableSizeInfo{
		{Table: "a", TotalBytes: 100},
		{Table: "b", TotalBytes: 500},
		{Table: "c", TotalBytes: 200},
	}
	sortTablesDesc(tables)
	if tables[0].Table != "b" || tables[1].Table != "c" || tables[2].Table != "a" {
		t.Errorf("sort failed: %v", []string{tables[0].Table, tables[1].Table, tables[2].Table})
	}
}

// TestQueryColumnarStorageSafeNilHandler 验证 nil/空 handler 安全降级
//
// 2026-07-03: 列存查询失败应降级而非阻塞主流程。安全包装在 h/db 为 nil 时
// 返回带提示的默认列存信息，且不返回 error。
func TestQueryColumnarStorageSafeNilHandler(t *testing.T) {
	// nil handler
	out, err := queryColumnarStorageSafe(context.Background(), nil)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if out.Available {
		t.Error("expected Available=false for nil handler")
	}
	if !strings.Contains(out.Note, "数据库连接未就绪") {
		t.Errorf("expected note to mention '数据库连接未就绪', got %q", out.Note)
	}

	// handler with nil db
	h := &Handler{}
	out, err = queryColumnarStorageSafe(context.Background(), h)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if out.Available {
		t.Error("expected Available=false for nil db")
	}
	if !strings.Contains(out.Note, "数据库连接未就绪") {
		t.Errorf("expected note to mention '数据库连接未就绪', got %q", out.Note)
	}
}

// TestColumnarStorageInfoDefaults 验证默认结构体字段（前端 fallback）
//
// 2026-07-03: 即便后端没有返回 columnar 字段，前端 TS 类型也应能编译。
// 这里验证结构体的零值是合理的"未安装"状态。
func TestColumnarStorageInfoDefaults(t *testing.T) {
	var out columnarStorageInfo
	if out.Available {
		t.Error("zero-value should be Available=false")
	}
	if out.TableCount != 0 || out.TotalColumns != 0 || out.TotalBytes != 0 {
		t.Errorf("zero-value counts should be 0, got %+v", out)
	}
	if out.Note != "" {
		t.Errorf("zero-value Note should be empty, got %q", out.Note)
	}
}

// TestQueryDatabaseStorageAgainstLocalDB 端到端验证：连接真实 Citus 数据库
//
// 2026-07-03: 验证 queryDatabaseStorage 不再调用 Citus 不存在的
// pg_total_database_size(name)。该测试需要本地 r112_postgres
// 跑着，否则跳过。
//
// 启用方法：
//
//	TEST_DATABASE_URL="postgres://kxuser:kxpass@127.0.0.1:5432/llm_gateway?sslmode=disable" \
//	go test ./admin -run TestQueryDatabaseStorageAgainstLocalDB -v
func TestQueryDatabaseStorageAgainstLocalDB(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	h := &Handler{db: pool}
	info, err := queryDatabaseStorage(ctx, h)
	if err != nil {
		t.Fatalf("queryDatabaseStorage: %v", err)
	}
	if info == nil {
		t.Fatal("nil info")
	}
	if info.DatabaseBytes <= 0 {
		t.Errorf("expected DatabaseBytes > 0, got %d", info.DatabaseBytes)
	}
	if info.TotalBytes <= 0 {
		t.Errorf("expected TotalBytes > 0, got %d", info.TotalBytes)
	}
	if info.ServerVersion == "" {
		t.Error("ServerVersion should be populated")
	}
	t.Logf("DB: %s (db_human=%s, total_human=%s, v=%s)",
		info.DatabaseHuman, info.DatabaseHuman, info.TotalHuman, info.ServerVersion)
}

// TestQueryColumnarStorageAgainstLocalDB 端到端验证：列存查询在真实 Citus 上工作
//
// 2026-07-03: 验证 queryColumnarStorage 能正确识别列存表。
func TestQueryColumnarStorageAgainstLocalDB(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	h := &Handler{db: pool}
	out := queryColumnarStorage(ctx, h)

	// 区分三种场景：扩展未装 / 装了但无表 / 有表
	if !out.Available {
		if out.Note == "" {
			t.Errorf("Available=false should have a Note explaining why")
		}
		t.Skipf("citus_columnar not available: %s", out.Note)
	}
	t.Logf("Columnar: tables=%d cols=%d bytes=%d human=%s note=%q",
		out.TableCount, out.TotalColumns, out.TotalBytes, out.TotalHuman, out.Note)
	if out.TableCount > 0 && out.TotalBytes <= 0 {
		t.Error("found columnar tables but TotalBytes is 0")
	}
	if out.TableCount > 0 && out.TotalColumns <= 0 {
		t.Error("found columnar tables but TotalColumns is 0")
	}
	// 当有表时 Note 应该不包含 "未安装"
	if out.TableCount > 0 && strings.Contains(out.Note, "未安装") {
		t.Errorf("inconsistent: TableCount=%d but Note says 'not installed'", out.TableCount)
	}
}
