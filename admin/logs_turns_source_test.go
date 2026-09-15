package admin

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// S3 波1 样板（plan §4-S3）：FROM 源切换的形态守卫。
// 视图形态必须保持既有文本（存量行为零漂移）；原生形态必须只含
// session_turns 家族、携带 113 列契约的关键列名，且绝无 request_logs 引用。

// memSettingsBackend is a minimal in-memory settings Backend (jsonRawMessage
// is a []byte alias, so plain []byte satisfies the interface).
type memSettingsBackend struct {
	store map[string][]byte
}

func (f *memSettingsBackend) Get(_ settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *memSettingsBackend) Set(_ settings.Scope, _ string, _ any) ([]byte, error) {
	return nil, nil
}
func (f *memSettingsBackend) GetTenant(_, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *memSettingsBackend) SetTenant(_, _ string, _ any) ([]byte, error) {
	return nil, nil
}

// withNativeTurnsRead swaps settings.Global for a registry that resolves
// nativeTurnsReadSetting to the requested value; restored on cleanup.
func withNativeTurnsRead(t *testing.T, on bool) {
	t.Helper()
	old := settings.Global
	t.Cleanup(func() { settings.Global = old })

	r := settings.NewRegistry()
	r.MustRegisterSpec(&settings.Spec{
		Key:     nativeTurnsReadSetting,
		Type:    settings.TypeBool,
		Scope:   settings.ScopePlatform,
		Default: false,
	})
	if on {
		r.RegisterBackend(settings.ScopePlatform, &memSettingsBackend{
			store: map[string][]byte{nativeTurnsReadSetting: []byte("true")},
		})
	}
	settings.Global = r
}

func TestLogsSourceFromSQLDefaultIsView(t *testing.T) {
	withNativeTurnsRead(t, false)
	if got := logsSourceFromSQL(); got != "request_logs_with_current_month rl" {
		t.Fatalf("default source should be the compat view with alias rl, got %q", got)
	}
}

func TestLogsSourceFromSQLNativeTurns(t *testing.T) {
	withNativeTurnsRead(t, true)

	got := logsSourceFromSQL()
	for _, want := range []string{
		"FROM public.session_turns_hot t",
		"UNION ALL SELECT",
		"FROM public.session_turns t",
		" AS request_id",
		" AS ts",
		" AS credits_charged",
		" AS gw_session_id", // D4: sys:% → NULL 的派生列仍按契约命名
		" rl",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("native source missing %q", want)
		}
	}
	// 原生形态唯一不允许的字样：request_logs（v1 分支/反连接整体剔除）。
	if strings.Contains(got, "request_logs") {
		t.Errorf("native source must not reference request_logs at all\ngot: %s", got)
	}
}
