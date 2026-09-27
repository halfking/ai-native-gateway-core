package providerprofile

import (
	"encoding/json"
	"testing"
)

// R11 252 SQL 日志审计回归钉：jsonParamOrNULL 必须交出 string 而非 []byte。
// 网关全局 SimpleProtocol 下，pgx 把 []byte 内联为 bytea hex 字面量（'\x7b…'），
// 任何 jsonb/json 列解析必炸（生产实证：provider_profile_alerts.details
// 2026-09-27 252 日志 ×33/161min `Token "\" is invalid`，表内 805 行 details
// 全 NULL——凡带 Details 的告警自 2026-07-27 引入起全部丢失）。
func TestJSONParamOrNULL(t *testing.T) {
	b, err := marshalJSON(map[string]interface{}{"action": "advisory_only"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	v := jsonParamOrNULL(b)
	s, ok := v.(string)
	if !ok {
		t.Fatalf("jsonParamOrNULL must return string, got %T (pgx SimpleProtocol inlines []byte as bytea hex)", v)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("value is not valid JSON: %v (raw=%q)", err, s)
	}
	if m["action"] != "advisory_only" {
		t.Fatalf("roundtrip mismatch: %v", m)
	}
	if jsonParamOrNULL(nil) != nil {
		t.Fatal("nil slice must map to SQL NULL")
	}
	if jsonParamOrNULL([]byte{}) != nil {
		t.Fatal("empty slice must map to SQL NULL")
	}
}
