package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// openTestDB 在临时目录创建文件型 SQLite 数据库（WAL 不支持 :memory:）
// 并注册测试结束后的清理。建库走 OpenSQLite，同时完成 schema 初始化。
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestOpenSQLitePragmas 验证 PRAGMA 优化对连接池中的不同连接一致生效：
// journal_mode 应为 wal（DSN 参数），busy_timeout/cache_size/temp_store/
// foreign_keys 等连接级参数在池中每个连接上取值一致。
func TestOpenSQLitePragmas(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "pragma.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() { _ = db.Close() }()

	// journal_mode：任意连接查询都应为 wal。
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("查询 journal_mode 失败: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want \"wal\"", journalMode)
	}

	// 同时占住两个连接，验证连接级 PRAGMA 在每个池化连接上都生效。
	ctx := context.Background()
	conn1, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("获取连接 1 失败: %v", err)
	}
	defer func() { _ = conn1.Close() }()
	conn2, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("获取连接 2 失败: %v", err)
	}
	defer func() { _ = conn2.Close() }()

	for i, conn := range []*sql.Conn{conn1, conn2} {
		var busyTimeout, cacheSize, tempStore, foreignKeys int
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("连接 %d 查询 busy_timeout 失败: %v", i+1, err)
		}
		if busyTimeout != 5000 {
			t.Fatalf("连接 %d busy_timeout = %d, want 5000", i+1, busyTimeout)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA cache_size").Scan(&cacheSize); err != nil {
			t.Fatalf("连接 %d 查询 cache_size 失败: %v", i+1, err)
		}
		if cacheSize != -64000 {
			t.Fatalf("连接 %d cache_size = %d, want -64000", i+1, cacheSize)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA temp_store").Scan(&tempStore); err != nil {
			t.Fatalf("连接 %d 查询 temp_store 失败: %v", i+1, err)
		}
		// temp_store=2 对应 MEMORY。
		if tempStore != 2 {
			t.Fatalf("连接 %d temp_store = %d, want 2 (MEMORY)", i+1, tempStore)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("连接 %d 查询 foreign_keys 失败: %v", i+1, err)
		}
		if foreignKeys != 1 {
			t.Fatalf("连接 %d foreign_keys = %d, want 1", i+1, foreignKeys)
		}
	}
}

// TestInitSchemaIdempotent 验证重复执行 InitSchema 不报错。
func TestInitSchemaIdempotent(t *testing.T) {
	db := openTestDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("重复执行 InitSchema 失败: %v", err)
	}
}

// TestMergeAndNormalizePragmas 覆盖 PRAGMA 合并与值渲染的边界情况。
func TestMergeAndNormalizePragmas(t *testing.T) {
	merged := mergePragmas(
		[]Pragma{{Name: "a", Value: "1"}, {Name: "b", Value: "x"}},
		[]Pragma{{Name: "b", Value: "y"}, {Name: "c", Value: "2"}},
	)
	if len(merged) != 3 {
		t.Fatalf("mergePragmas 长度 = %d, want 3", len(merged))
	}
	if merged[1].Value != "y" {
		t.Fatalf("同名 PRAGMA 未被覆盖: %+v", merged[1])
	}

	if got := pragmaValue("-64000"); got != "-64000" {
		t.Fatalf("pragmaValue(-64000) = %q, want 原样数字", got)
	}
	if got := pragmaValue("MEMORY"); got != "'MEMORY'" {
		t.Fatalf("pragmaValue(MEMORY) = %q, want 带引号字符串", got)
	}
	if got := pragmaValue("it's"); got != "'it''s'" {
		t.Fatalf("pragmaValue(it's) = %q, want 单引号转义", got)
	}
}

// TestValidPragmaName 回归：PRAGMA 名直接拼进 "PRAGMA <name> = ..." 语句，
// 必须白名单校验防配置注入（审计 P2）。
func TestValidPragmaName(t *testing.T) {
	for _, ok := range []string{"journal_mode", "busy_timeout", "_cache_size", "CacheSize2"} {
		if !validPragmaName(ok) {
			t.Errorf("validPragmaName(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "1abc", "a b", "a;b", "a-b", "a'b", "a\"b"} {
		if validPragmaName(bad) {
			t.Errorf("validPragmaName(%q) = true, want false", bad)
		}
	}
}

// TestOpenSQLiteRejectsInvalidPragmaName 端到端：OpenSQLite 必须在开库前拒绝非法 PRAGMA 名。
func TestOpenSQLiteRejectsInvalidPragmaName(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenSQLite(dir+"/bad.db", Pragma{Name: "journal; DROP TABLE users", Value: "WAL"}); err == nil {
		t.Fatal("OpenSQLite 应拒绝含注入片段的 PRAGMA 名")
	}
}
