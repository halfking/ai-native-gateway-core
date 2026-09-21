package telemetry

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// withStopWriteGateSettings 把 settings.Global 换成携带真实 storage specs 的
// registry，并把 storage.request_logs_write_enabled 钉在给定值上（fake 平台
// 后端），恢复交由 t.Cleanup。复用 body_summary_test.go 的 fakeSettingsBackend
// 与 strconvFormatBool。
func withStopWriteGateSettings(t *testing.T, writeEnabled bool) {
	t.Helper()
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	store := map[string][]byte{
		"storage.request_logs_write_enabled": []byte(strconvFormatBool(writeEnabled)),
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: store})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	for _, spec := range settings.StorageSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
}

// TestRequestLogsWriteEnabled_DefaultsTrue：settings.Global 为 nil（单测未
// 初始化 settings）时门控必须回退为 true（继续双写），与 Lite sink 同语义。
func TestRequestLogsWriteEnabled_DefaultsTrue(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	settings.Global = nil
	require.True(t, requestLogsWriteEnabled())

	registry := settings.NewRegistry()
	for _, spec := range settings.StorageSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry // 已注册 spec 但无后端值 → default true
	require.True(t, requestLogsWriteEnabled())
}

// TestInsertRequestLogStopWriteGate：Full 链路 INSERT 路径的 S4 停写门控。
// 关停后 request_logs_hot 主行与 request_logs_bodies_hot 正文写入被整体
// 跳过，usage_ledger 计费行照常提交（pgxmock 对未预期的 Exec 直接报错，
// 因此期望序列本身就是"宽表零写入"的行为断言）。
func TestInsertRequestLogStopWriteGate(t *testing.T) {
	tests := []struct {
		name       string
		writeOn    bool
		expectWide bool
	}{
		{name: "gate on keeps wide-table dual write", writeOn: true, expectWide: true},
		{name: "gate off skips wide-table keeps usage ledger", writeOn: false, expectWide: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withStopWriteGateSettings(t, tc.writeOn)

			mock, err := pgxmock.NewConn()
			require.NoError(t, err)
			defer mock.Close(context.Background())

			mock.ExpectBegin()
			mock.ExpectExec("INSERT INTO usage_ledger_hot").
				WithArgs(anyArgs(18)...).
				WillReturnResult(pgxmock.NewResult("INSERT", 1))
			if tc.expectWide {
				mock.ExpectExec("INSERT INTO request_logs_hot").
					WithArgs(anyArgs(102)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectExec("INSERT INTO request_logs_bodies_hot").
					WithArgs(anyArgs(4)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
			}
			mock.ExpectCommit()

			c := NewClientWithRequestLogDB(mock)
			err = c.persistRequestLog(&RequestLogEntry{
				Op:        RequestLogInsert,
				RequestID: "req-stop-write-insert",
				TenantID:  "tenant-1",
				Success:   true,
			})
			require.NoError(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestUpdateRequestLogStopWriteGate：Full 链路 UPDATE（终态回填）路径的
// S4 停写门控。关停后 request_logs_hot UPDATE（含 RowsAffected==0 的回落
// INSERT）、bodies 正文写入整体跳过；usage_ledger 终态刷新照常。
func TestUpdateRequestLogStopWriteGate(t *testing.T) {
	tests := []struct {
		name       string
		writeOn    bool
		expectWide bool
	}{
		{name: "gate on keeps wide-table update", writeOn: true, expectWide: true},
		{name: "gate off skips wide-table keeps usage ledger", writeOn: false, expectWide: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withStopWriteGateSettings(t, tc.writeOn)

			mock, err := pgxmock.NewConn()
			require.NoError(t, err)
			defer mock.Close(context.Background())

			mock.ExpectBegin()
			mock.ExpectExec("UPDATE usage_ledger_hot").
				WithArgs(anyArgs(5)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			if tc.expectWide {
				mock.ExpectExec("UPDATE request_logs_hot").
					WithArgs(anyArgs(99)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec("INSERT INTO request_logs_bodies_hot").
					WithArgs(anyArgs(4)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
			}
			mock.ExpectCommit()

			c := NewClientWithRequestLogDB(mock)
			err = c.persistRequestLog(&RequestLogEntry{
				Op:        RequestLogUpdate,
				RequestID: "req-stop-write-update",
				TenantID:  "tenant-1",
				Success:   true,
			})
			require.NoError(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
