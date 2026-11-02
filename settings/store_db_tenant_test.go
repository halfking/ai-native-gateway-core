package settings

// store_db_tenant_test.go —— 租户级设置写路径（2026-11-02）。
//
// ★ 病：SetTenant 原先用 `INSERT ... ON CONFLICT (tenant_id, key) DO UPDATE`，
//   而 tenant_settings_kv 在【生产库与全新建库基线】上**都没有**那条唯一约束
//   ⇒ 每次租户级写入都 42P10 ⇒ **所有租户级设置写入全废**
//   （含「会话正文录制同意」开关 ⇒ 录制同意永远关着 ⇒ 会话正文永不落库
//     ⇒ 前端「对话」页四个角色页签全空 —— 本轮报障的根修链一环）。
//
// ★ 修法：与平台级 Set 同款「先 UPDATE，未命中再 INSERT」（不依赖那条约束）。
// ★ 本门钉住四件事：
//   ① 已有行 ⇒ 走 UPDATE 并回传旧值（prev_value 语义）；
//   ② 没有行 ⇒ 走 INSERT（不报错、不返回旧值）；
//   ③ **写前取事务级咨询锁**（pg_advisory_xact_lock）⇒ 同一 (tenant_id,key)
//     的并发写被串行化 ⇒ 不会落重复行（无唯一约束 ⇒ 必须自己保证）；
//   ④ 不得再出现 ON CONFLICT（生产库没有那条约束 ⇒ 改回去就是 42P10）。

import (
	"os"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func registerConsentSpecForTest(t *testing.T) {
	t.Helper()
	prev := Global
	reg := NewRegistry()
	for _, sp := range RecordingConsentSpecs() {
		reg.MustRegisterSpec(sp)
	}
	Global = reg
	t.Cleanup(func() { Global = prev })
}

func TestSetTenantUpdatesExistingRowAndReturnsPreviousValue(t *testing.T) {
	registerConsentSpecForTest(t)
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectBegin()
	// ★ 咨询锁**必须先于**任何写语句（pgxmock 默认按序匹配 ⇒ 顺序即断言）。
	mock.ExpectExec(`pg_advisory_xact_lock`).
		WithArgs("tenant_settings_kv:default:" + KeySessionRecordingConsent).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`UPDATE tenant_settings_kv`).
		WithArgs("default", KeySessionRecordingConsent, "true", "bool", "session").
		WillReturnRows(pgxmock.NewRows([]string{"prev_value"}).AddRow([]byte("false")))
	mock.ExpectCommit()

	store := newStoreDBForTest(mock)
	prev, err := store.SetTenant("default", KeySessionRecordingConsent, true)
	require.NoError(t, err)
	require.JSONEq(t, `false`, string(prev))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSetTenantInsertsWhenRowMissing(t *testing.T) {
	registerConsentSpecForTest(t)
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// UPDATE 未命中 ⇒ 空结果 ⇒ 走 INSERT。
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).
		WithArgs("tenant_settings_kv:default:" + KeySessionRecordingConsent).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`UPDATE tenant_settings_kv`).
		WithArgs("default", KeySessionRecordingConsent, "true", "bool", "session").
		WillReturnRows(pgxmock.NewRows([]string{"prev_value"}))
	mock.ExpectExec(`INSERT INTO tenant_settings_kv`).
		WithArgs("default", KeySessionRecordingConsent, "true", "bool", "session").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	store := newStoreDBForTest(mock)
	prev, err := store.SetTenant("default", KeySessionRecordingConsent, true)
	require.NoError(t, err)
	require.Empty(t, prev, "首次写入没有旧值")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSetTenantRollsBackWhenInsertFails 钉住失败语义：INSERT 报错 ⇒ 整体回滚
// （★ 没有回滚的话，事务外的半截状态会留在库里）。
func TestSetTenantRollsBackWhenInsertFails(t *testing.T) {
	registerConsentSpecForTest(t)
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).
		WithArgs("tenant_settings_kv:default:" + KeySessionRecordingConsent).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`UPDATE tenant_settings_kv`).
		WithArgs("default", KeySessionRecordingConsent, "true", "bool", "session").
		WillReturnRows(pgxmock.NewRows([]string{"prev_value"}))
	mock.ExpectExec(`INSERT INTO tenant_settings_kv`).
		WithArgs("default", KeySessionRecordingConsent, "true", "bool", "session").
		WillReturnError(os.ErrDeadlineExceeded)
	mock.ExpectRollback()

	store := newStoreDBForTest(mock)
	_, err = store.SetTenant("default", KeySessionRecordingConsent, true)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSetTenantDoesNotUseOnConflict 钉住「不再依赖 (tenant_id, key) 唯一约束」：
//
// ★ 为什么要有这道形态门：pgxmock 只能证明【当前这条 SQL】能跑通，
//   证明不了「以后有人把它改回 ON CONFLICT 也不会立刻变红」——
//   ★ 而**生产库根本没有那条约束** ⇒ 改回去就是 42P10 ⇒
//   ⇒ 必须有一道形状门把这件事钉住（否则回归会静静地在生产上炸）。
func TestSetTenantDoesNotUseOnConflict(t *testing.T) {
	src, err := os.ReadFile("store_db.go")
	require.NoError(t, err)
	text := string(src)
	start := strings.Index(text, "func (s *StoreDB) SetTenant(")
	require.GreaterOrEqual(t, start, 0, "store_db.go 里找不到 SetTenant")
	body := text[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	require.NotContains(t, body, "ON CONFLICT",
		"★ 租户级写路径不得再依赖 (tenant_id, key) 唯一约束（生产库没有它 ⇒ 42P10）")
	require.Contains(t, body, "UPDATE tenant_settings_kv")
	require.Contains(t, body, "INSERT INTO tenant_settings_kv")
}

// TestSetTenantSerializesSameKeyWrites 钉住「并发同 key 写 ⇒ 串行化」。
//
// ★ 病：无唯一约束时，「先 UPDATE 未命中 ⇒ 再 INSERT」在**两个并发写者**下
//   会各插一行 ⇒ 同 key 两行 ⇒ 读侧取哪行取决于物理顺序 ⇒ 可能读到旧值。
//   ★ 本键控制的正是「会话正文录制同意」⇒ 读到旧值 ⇒ 正文又停止落库
//     ⇒ 本次修复的**孪生缺陷**（且更难诊断：写返回 200、读却是旧值）。
// ★ 钉法：① 形状门 —— 咨询锁语句必须存在且**出现在写语句之前**；
//   ② pgxmock 的**顺序断言**（见上面两条腿）—— 锁不在写之前就会红。
func TestSetTenantSerializesSameKeyWrites(t *testing.T) {
	src, err := os.ReadFile("store_db.go")
	require.NoError(t, err)
	text := string(src)
	start := strings.Index(text, "func (s *StoreDB) SetTenant(")
	require.GreaterOrEqual(t, start, 0, "store_db.go 里找不到 SetTenant")
	body := text[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}

	lockIdx := strings.Index(body, "pg_advisory_xact_lock")
	require.GreaterOrEqual(t, lockIdx, 0,
		"★ 租户级写路径必须取事务级咨询锁（无唯一约束 ⇒ 并发同 key 写会落重复行）")
	updateIdx := strings.Index(body, "UPDATE tenant_settings_kv")
	require.GreaterOrEqual(t, updateIdx, 0)
	require.Less(t, lockIdx, updateIdx,
		"★ 咨询锁必须在写语句**之前**取（否则并发写仍能各插一行）")
	require.Contains(t, body, "tx.Commit(ctx)",
		"★ 取了事务级咨询锁就必须提交事务（否则锁与写都不生效）")
}
