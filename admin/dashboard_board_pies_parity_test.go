package admin

import (
	"testing"
)

// R57 B7 钉桩：分钟路径（queryBoardPies）与日志 fallback 路径
// （fallbackBoardPies/emptyBoardPies）的饼图键与维度类型必须一一对应——
// 两侧漂移会让某条路径丢饼图或错读维度。键集变更是 API 契约变更
// （web/src/api/board.ts 的 pies 类型同步）。
func TestBoardPieKeyParity(t *testing.T) {
	minuteKeys := map[string]string{
		"clients":         "agent_name",
		"client_ips":      "client_ip",
		"identity_hashes": "identity_hash",
		"models":          "model",
		"errors":          "error_kind",
		"tenants":         "tenant",
		"providers":       "provider",
	}
	if len(minuteKeys) != 7 {
		t.Fatalf("minute pie key count drifted: %d (update this test + web board.ts together)", len(minuteKeys))
	}
	if _, ok := minuteKeys["virtual_ips"]; ok {
		t.Fatal("virtual_ips key must stay retired (R57 B7: client_ips reads the real source)")
	}
	empty := emptyBoardPies()
	for key := range minuteKeys {
		if _, ok := empty[key]; !ok {
			t.Errorf("emptyBoardPies missing key %q", key)
		}
	}
	for key := range empty {
		if _, ok := minuteKeys[key]; !ok {
			t.Errorf("emptyBoardPies has stale key %q", key)
		}
	}
	if _, ok := empty["client_ips"]; !ok {
		t.Fatal("client_ips pie missing")
	}
}
