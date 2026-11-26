package casdoor

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── 2026-11-27 L135 轮（断点①修复）userProvisioner.ensureLocalUser 判据钉 ──
//
// 三条路径 + 两个安全性质：
//   ① 已物化（metadata.users_id 在）⇒ 直接返回该 users.id，不再 INSERT；
//   ② 未物化 ⇒ INSERT users（role='user'、enabled、随机 bcrypt 串）
//     + 回写 metadata.users_id；
//   ③ 用户名撞 UNIQUE（23505）⇒ ErrUsernameConflict fail-closed（可 errors.Is）；
//   ④ 回写关联失败 ⇒ 删掉刚建的 users 行（幂等重试语义，不留半状态）；
//   ⑤ 变异守卫：判据能红 —— 把 metadata.users_id 预置值改掉，返回值跟着变
//     （证明 ① 不是恒绿的自证）。

// provisionFixture 组装 idMapper（真实现，走 pgxmock）与 provisioner。
func provisionFixture(mock pgxmock.PgxPoolIface) *userProvisioner {
	return newUserProvisioner(NewIDMapper(mock))
}

// stubAlreadyMapped 让 MapOrCreate 直接命中既有映射（existing 分支：
// SELECT 命中 + UPDATE cached fields）。
func stubExistingMapping(mock pgxmock.PgxPoolIface, mappingID int) {
	mock.ExpectQuery(`SELECT internal_id FROM auth_id_mapping`).
		WithArgs("casdoor", "uuid-123", "default").
		WillReturnRows(pgxmock.NewRows([]string{"internal_id"}).AddRow(mappingID))
	mock.ExpectExec(`UPDATE auth_id_mapping`).
		WithArgs(mappingID, "casdoor", "uuid-123", "显示名", "u@example.com", "testuser").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
}

// TestEnsureLocalUser_AlreadyProvisioned 钉路径①：metadata.users_id 有值 ⇒
// 返回该值，不发 INSERT users、不回写。
func TestEnsureLocalUser_AlreadyProvisioned(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	p := provisionFixture(mock)
	stubExistingMapping(mock, 42)

	// metadata.users_id = 777 已物化（列名 SQL 里没有别名 ⇒ pgxmock 行列名
	// 用占位 "users_id"；Scan 目标是 *string ⇒ AddRow 直接给字符串）。
	mock.ExpectQuery(`SELECT COALESCE\(metadata->>'users_id', ''\) FROM auth_id_mapping`).
		WithArgs(42).
		WillReturnRows(pgxmock.NewRows([]string{"users_id"}).AddRow("777"))

	// profile 同步（best-effort；display_name/email 与既有值相同 ⇒ 无更新发生
	// 也无妨 —— Exec 期望保留以证明它确实走了同步路径）
	mock.ExpectExec(`UPDATE users SET display_name`).
		WithArgs(777, "显示名", "u@example.com").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	localID, err := p.ensureLocalUser(context.Background(), mock, "uuid-123", "default", "testuser", "显示名", "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, 777, localID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestEnsureLocalUser_AlreadyProvisioned_VariantIsCatchable 变异守卫：
// 同一用例把 metadata.users_id 换成另一个值 ⇒ 返回值必须跟着变。
// 这证明「返回值来自 metadata 反查」不是写死的（判据能红）。
func TestEnsureLocalUser_AlreadyProvisioned_VariantIsCatchable(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	p := provisionFixture(mock)
	stubExistingMapping(mock, 42)

	mock.ExpectQuery(`SELECT COALESCE\(metadata->>'users_id', ''\) FROM auth_id_mapping`).
		WithArgs(42).
		WillReturnRows(pgxmock.NewRows([]string{"users_id"}).AddRow("999"))

	mock.ExpectExec(`UPDATE users SET display_name`).
		WithArgs(999, "显示名", "u@example.com").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	localID, err := p.ensureLocalUser(context.Background(), mock, "uuid-123", "default", "testuser", "显示名", "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, 999, localID, "returned id must follow metadata.users_id, not a fixed value")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestEnsureLocalUser_ProvisionsNewUser 钉路径②：未物化 ⇒ INSERT users
// （角色 user、密码是随机 bcrypt 串）+ 回写 metadata.users_id ⇒ 返回新 id。
func TestEnsureLocalUser_ProvisionsNewUser(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	p := provisionFixture(mock)
	stubExistingMapping(mock, 42)

	// metadata 为 NULL ⇒ 未物化
	mock.ExpectQuery(`SELECT COALESCE\(metadata->>'users_id', ''\) FROM auth_id_mapping`).
		WithArgs(42).
		WillReturnRows(pgxmock.NewRows([]string{"users_id"}).AddRow(nil))

	// INSERT users 返回新 id=501
	mock.ExpectQuery(`INSERT INTO users`).
		WithArgs("default", "testuser", pgxmock.AnyArg(), "显示名", "u@example.com").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(501))

	// 回写 metadata.users_id
	mock.ExpectExec(`UPDATE auth_id_mapping`).
		WithArgs(42, 501).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	localID, err := p.ensureLocalUser(context.Background(), mock, "uuid-123", "default", "testuser", "显示名", "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, 501, localID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestEnsureLocalUser_UsernameConflictFailClosed 钉路径③：INSERT 撞 23505 ⇒
// ErrUsernameConflict（可 errors.Is 命中）—— fail-closed，不静默改名。
func TestEnsureLocalUser_UsernameConflictFailClosed(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	p := provisionFixture(mock)
	stubExistingMapping(mock, 42)

	mock.ExpectQuery(`SELECT COALESCE\(metadata->>'users_id', ''\) FROM auth_id_mapping`).
		WithArgs(42).
		WillReturnRows(pgxmock.NewRows([]string{"users_id"}).AddRow(nil))

	// 23505 unique violation（模拟 users.username UNIQUE）。
	// isUniqueViolation 用 errors.As 找 interface{ SQLState() string } ⇒
	// 用本文件的 pgConflict 桩即可命中。
	conflict := &pgConflict{msg: "duplicate key value violates unique constraint \"users_username_key\""}

	mock.ExpectQuery(`INSERT INTO users`).
		WithArgs("default", "testuser", pgxmock.AnyArg(), "显示名", "u@example.com").
		WillReturnError(conflict)

	_, err = p.ensureLocalUser(context.Background(), mock, "uuid-123", "default", "testuser", "显示名", "u@example.com")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUsernameConflict), "error must wrap ErrUsernameConflict, got: %v", err)
}

// TestEnsureLocalUser_LinkFailureRollsBackUser 钉路径④：回写 metadata 失败 ⇒
// 删掉刚建的 users 行 + 报错（本次登录失败、下次登录重走完整物化）。
func TestEnsureLocalUser_LinkFailureRollsBackUser(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	p := provisionFixture(mock)
	stubExistingMapping(mock, 42)

	mock.ExpectQuery(`SELECT COALESCE\(metadata->>'users_id', ''\) FROM auth_id_mapping`).
		WithArgs(42).
		WillReturnRows(pgxmock.NewRows([]string{"users_id"}).AddRow(nil))

	mock.ExpectQuery(`INSERT INTO users`).
		WithArgs("default", "testuser", pgxmock.AnyArg(), "显示名", "u@example.com").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(502))

	// 回写失败（0 行受影响 = mapping 消失）
	mock.ExpectExec(`UPDATE auth_id_mapping`).
		WithArgs(42, 502).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	// 回滚：DELETE 刚建的行
	mock.ExpectExec(`DELETE FROM users WHERE id`).
		WithArgs(502).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	_, err = p.ensureLocalUser(context.Background(), mock, "uuid-123", "default", "testuser", "显示名", "u@example.com")
	require.Error(t, err, "link failure must fail the login")
	assert.Contains(t, err.Error(), "link mapping")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestEnsureLocalUser_NilDBRejected 钉守卫：db 未装配（单测形态）⇒ 显式报错
// 而不是 panic —— auth_provider.go 里 `p.idMapper.db != nil` 的门就是靠它兜底。
func TestEnsureLocalUser_NilDBRejected(t *testing.T) {
	p := provisionFixture(nil)
	_, err := p.ensureLocalUser(context.Background(), nil, "uuid-123", "default", "testuser", "显示名", "u@example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no database handle")
}

// pgConflict 是一个能过 isUniqueViolation 的最小错误桩：
// isUniqueViolation 用 errors.As 找 interface{ SQLState() string }。
type pgConflict struct{ msg string }

func (e *pgConflict) Error() string  { return e.msg }
func (e *pgConflict) SQLState() string { return "23505" }

// 编译期锚点：确保 pgx.ErrNoRows 仍被引用（防 import 漂移告警）。
var _ = pgx.ErrNoRows
