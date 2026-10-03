package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestZeroValueResponsesSerializeArraysAsEmpty 锁住一类反复出现的契约缺陷：
// Go 的 nil 切片 json.Marshal 成 `null`，而前端普遍写 `resp.xxx.length` ——
// null.length 抛 TypeError，整页白屏。
//
// 已中过两次：
//   - RoutingAuditView（/routing/overrides/audit）：entries 为 null → 整页白屏
//   - CorrelationsView（/correlations）：by_model 等 5 个字段全 null → 整页白屏
//
// 为什么用「零值结构体 + 归一化函数」而不是打真实接口：
// 真实接口**只有在没有数据时**才复现——有数据时切片非 nil，接口完全正常。
// 也就是说线上「有流量就不出问题、没流量就白屏」，靠集成测试基本碰不到。
//
// ⚠ 第一版写成 `json.Marshal(AutoRouteCorrelationsResponse{})` 直接断言结构体
// 零值，判据落在**结构体**上而不在**归一化逻辑**上 —— 无论 handler 修没修，
// 结构体零值永远是 nil 切片，于是这是一扇恒红的门，比没有门更糟
// （会让人以为契约问题还没解决）。所以 handler 把归一化抽成
// normalizeCorrelationsResponse，这里测的是「过一遍归一化之后」的结果。
func TestZeroValueResponsesSerializeArraysAsEmpty(t *testing.T) {
	cases := []struct {
		name        string
		normalize   func() any
		arrayFields []string
	}{
		{
			name: "AutoRouteCorrelationsResponse（/correlations）",
			normalize: func() any {
				resp := AutoRouteCorrelationsResponse{}
				normalizeCorrelationsResponse(&resp)
				return resp
			},
			arrayFields: []string{"by_model", "by_strategy", "by_task_type", "by_model_task", "verdict"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(c.normalize())
			if err != nil {
				t.Fatalf("序列化失败: %v", err)
			}
			var generic map[string]any
			if err := json.Unmarshal(raw, &generic); err != nil {
				t.Fatalf("反序列化失败: %v", err)
			}
			for _, f := range c.arrayFields {
				v, ok := generic[f]
				if !ok {
					t.Errorf("字段 %s 在响应里不存在（JSON 键名可能改了，请同步前端契约）", f)
					continue
				}
				if v == nil {
					t.Errorf("字段 %s 零值序列化成 null，前端 `%s.length` 会抛 TypeError 导致整页白屏；"+
						"零值必须初始化为空切片（见 handler 里的收敛点）", f, f)
					continue
				}
				if _, isArr := v.([]any); !isArr {
					t.Errorf("字段 %s 零值不是数组而是 %T", f, v)
				}
			}
			// 兜底：整份 JSON 里不该出现 ":null"
			if s := string(raw); strings.Contains(s, ":null") {
				t.Errorf("响应里出现 :null，前端可能对其做 .length / .map 而炸掉：%s", s)
			}
		})
	}
}
