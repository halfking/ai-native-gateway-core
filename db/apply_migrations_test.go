package db

import (
	"context"
	"testing"
)

// TestApplyMigrationsMethodExists 验证 *DB 有 ApplyMigrations 方法,
// 签名 func(context.Context) error。这是编译期断言。
// 真实行为由 db 现有集成测试覆盖(Open() 仍跑迁移)。
func TestApplyMigrationsMethodExists(t *testing.T) {
	var db *DB
	var _ func(context.Context) error = db.ApplyMigrations
}
