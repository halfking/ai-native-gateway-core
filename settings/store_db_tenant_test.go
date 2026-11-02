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
// ★ 本门钉住两件事：① 已有行 ⇒ 走 UPDATE 并回传旧值（prev_value 语义）；
//   ② 没有行 ⇒ 走 INSERT（不报错、不返回旧值）。

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

	mock.ExpectQuery(`UPDATE tenant_settings_kv`).
		WithArgs("default", KeySessionRecordingConsent, "true", "bool", "session").
		WillReturnRows(pgxmock.NewRows([]string{"prev_value"}).AddRow([]byte("false")))

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
	mock.ExpectQuery(`UPDATE tenant_settings_kv`).
		WithArgs("default", KeySessionRecordingConsent, "true", "bool", "session").
		WillReturnRows(pgxmock.NewRows([]string{"prev_value"}))
	mock.ExpectExec(`INSERT INTO tenant_settings_kv`).
		WithArgs("default", KeySessionRecordingConsent, "true", "bool", "session").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	store := newStoreDBForTest(mock)
	prev, err := store.SetTenant("default", KeySessionRecordingConsent, true)
	require.NoError(t, err)
	require.Empty(t, prev, "首次写入没有旧值")
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
