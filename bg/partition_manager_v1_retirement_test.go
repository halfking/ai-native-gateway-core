package bg

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// v1StopWriteFakeKV 是只读 settings_kv 桩：platform scope 对停写键返回固定值。
type v1StopWriteFakeKV struct{ enabled string }

func (f *v1StopWriteFakeKV) Get(scope settings.Scope, key string) ([]byte, error) {
	if scope == settings.ScopePlatform && key == settings.KeyRequestLogsWriteEnabled {
		return []byte(f.enabled), nil
	}
	return nil, nil
}
func (f *v1StopWriteFakeKV) Set(settings.Scope, string, any) ([]byte, error) { return nil, nil }
func (f *v1StopWriteFakeKV) GetTenant(string, string) ([]byte, error)        { return nil, nil }
func (f *v1StopWriteFakeKV) SetTenant(string, string, any) ([]byte, error)   { return nil, nil }

func withV1WriteEnabled(t *testing.T, enabled string) {
	t.Helper()
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &v1StopWriteFakeKV{enabled: enabled})
	for _, spec := range settings.StorageSpecs() {
		if err := registry.RegisterSpec(spec); err != nil {
			t.Fatalf("register spec %s: %v", spec.Key, err)
		}
	}
	settings.Global = registry
}

// v1LifecycleFnNames 是被退休门滤掉的四个 v1 族生命周期条目。
var v1LifecycleFnNames = map[string]bool{
	"ensure_request_logs_partition":                true,
	"ensure_request_logs_bodies_partition":         true,
	"promote_request_logs_hot_to_partition":        true,
	"promote_request_logs_bodies_hot_to_partition": true,
}

// TestV1LifecycleSpecsStandDownWhenWritesStopped 钉住停写后 v1 族生命周期
// 作业的退役行为（迁移 860 轮）：停写 = ensure/promote 不再维护一个已冻结、
// 已改名为 `_request_logs*` 的族（预建未来分区 / 排空恒空的 hot 表都是纯浪费，
// 且归档会往待删族里写新行）。重新开启写路径时门自动恢复（热加载设置）。
//
// 反向断言同样要钉：写开着时四条必须在场——否则「门恒滤」的变异
// （v1LifecycleRetired 恒 true）与「门没接上」的变异（filter 从未被调用）
// 都会红在同一侧。
func TestV1LifecycleSpecsStandDownWhenWritesStopped(t *testing.T) {
	withV1WriteEnabled(t, "false")
	for _, spec := range ensureSpecs() {
		if v1LifecycleFnNames[spec.fnName] {
			t.Errorf("停写下 ensureSpecs 仍含 v1 族条目 %q —— 待删族不应继续预建分区", spec.fnName)
		}
	}
	for _, spec := range promoteSpecs() {
		if v1LifecycleFnNames[spec.fnName] {
			t.Errorf("停写下 promoteSpecs 仍含 v1 族条目 %q —— 恒空 hot 表的排空是纯浪费", spec.fnName)
		}
	}

	withV1WriteEnabled(t, "true")
	gotEnsure := 0
	for _, spec := range ensureSpecs() {
		if v1LifecycleFnNames[spec.fnName] {
			gotEnsure++
		}
	}
	gotPromote := 0
	for _, spec := range promoteSpecs() {
		if v1LifecycleFnNames[spec.fnName] {
			gotPromote++
		}
	}
	// count==N 而非 Contains（判据纪律）：ensure 2 条 + promote 2 条，
	// 删掉其中一条的静默回退必须红。
	if gotEnsure != 2 {
		t.Errorf("写开启时 ensureSpecs 应含 2 条 v1 族条目，实得 %d", gotEnsure)
	}
	if gotPromote != 2 {
		t.Errorf("写开启时 promoteSpecs 应含 2 条 v1 族条目，实得 %d", gotPromote)
	}
}
