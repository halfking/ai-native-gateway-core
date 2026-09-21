package admin

import (
	"context"
	"strconv"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// gateFakeSettingsBackend 是 admin 侧的最小 settings Backend（真实 StoreDB
// 需要 pgxpool；此处按 settings.Backend 接口伪造平台 KV）。
type gateFakeSettingsBackend struct {
	store map[string][]byte
}

func (f *gateFakeSettingsBackend) Get(_ settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *gateFakeSettingsBackend) Set(_ settings.Scope, _ string, _ any) ([]byte, error) {
	return nil, nil
}
func (f *gateFakeSettingsBackend) GetTenant(_, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *gateFakeSettingsBackend) SetTenant(_ string, _ string, _ any) ([]byte, error) {
	return nil, nil
}

// withRequestLogsWriteSetting 把 settings.Global 换成携带真实 storage specs 的
// registry，并把 storage.request_logs_write_enabled 钉在给定值上。
func withRequestLogsWriteSetting(t *testing.T, writeEnabled bool) {
	t.Helper()
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &gateFakeSettingsBackend{
		store: map[string][]byte{
			"storage.request_logs_write_enabled": []byte(strconv.FormatBool(writeEnabled)),
		},
	})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	for _, spec := range settings.StorageSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
}

// TestTelemetryIngestRequestLogStopWriteGate：admin ingest（POST
// /api/telemetry/request-log 消费端）的 S4 停写门控。关停后仅保留
// usage_ledger 计费行；request_logs_hot 主行与 request_logs_bodies_hot
// 正文（UPDATE → INSERT..SELECT → UPDATE 三段）整体跳过，事务照常提交。
func TestTelemetryIngestRequestLogStopWriteGate(t *testing.T) {
	tests := []struct {
		name       string
		writeOn    bool
		expectWide bool
	}{
		{name: "gate on persists request_logs and bodies", writeOn: true, expectWide: true},
		{name: "gate off keeps only usage_ledger", writeOn: false, expectWide: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withRequestLogsWriteSetting(t, tc.writeOn)

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			// anyArgSlice 构造 n 个 AnyArg（pgxmock v4 未显式 WithArgs 时
			// 按 0 参数匹配，必须逐条绑定）。
			anyArgSlice := func(n int) []interface{} {
				args := make([]interface{}, n)
				for i := range args {
					args[i] = pgxmock.AnyArg()
				}
				return args
			}

			ing := &telemetryIngester{db: mock}
			mock.ExpectBegin()
			mock.ExpectExec("INSERT INTO usage_ledger_hot").
				WithArgs(anyArgSlice(18)...).
				WillReturnResult(pgxmock.NewResult("INSERT", 1))
			if tc.expectWide {
				mock.ExpectExec("INSERT INTO request_logs_hot").
					WithArgs(anyArgSlice(36)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectExec("UPDATE request_logs_bodies_hot").
					WithArgs(anyArgSlice(3)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 0))
				mock.ExpectExec("INSERT INTO request_logs_bodies_hot").
					WithArgs(anyArgSlice(3)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectExec("UPDATE request_logs_bodies_hot").
					WithArgs(anyArgSlice(3)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}
			mock.ExpectCommit()

			ing.persistRequestLog(context.Background(), &requestLogInput{
				RequestID: "ingest-stop-write-1",
				TenantID:  "tenant-1",
				Success:   true,
			})
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
