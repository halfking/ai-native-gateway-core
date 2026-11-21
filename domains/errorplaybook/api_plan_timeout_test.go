package errorplaybook

// apiPlanTimeoutMS 钉测（2026-11-22 第 62 增量审计轮 C-P2-3）：
// 12h 修订审计轮 F22 修复「timeout_ms 只认 float64 ⇒ 内存路径（归一化产出的
// int）永远落回默认 10s」——该修复此前零回归钉测，本文件把两种数值来源
// （归一化期 int / JSON 往返 float64）与非法形态一起钉死，防再漂移。

import (
	"encoding/json"
	"testing"
)

func TestAPIPlanTimeoutMSNumericShapes(t *testing.T) {
	cases := []struct {
		name   string
		in     interface{}
		want   int64
		wantOK bool
	}{
		{"归一化期 int（内存路径首修，F22 主缺陷形态）", 45000, 45000, true},
		{"JSON 往返 float64（从库读回）", float64(30000), 30000, true},
		{"float32", float32(20000), 20000, true},
		{"int32", int32(15000), 15000, true},
		{"int64", int64(60000), 60000, true},
		{"json.Number（useNumber 解码）", json.Number("12000"), 12000, true},
		{"json.Number 非数字", json.Number("abc"), 0, false},
		{"字符串（非法）", "45000", 0, false},
		{"nil（缺省 ⇒ 调用方落默认 10s）", nil, 0, false},
		{"bool（非法）", true, 0, false},
	}
	for _, tc := range cases {
		got, ok := apiPlanTimeoutMS(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Fatalf("%s: apiPlanTimeoutMS(%v) = (%d,%v), want (%d,%v)", tc.name, tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}
