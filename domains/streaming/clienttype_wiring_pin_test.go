package streaming

import (
	"os"
	"strings"
	"testing"
)

// 原厂直通三巡（2026-10-28）：ExecParams.ClientType 的三个生产构造点
// 接线钉测。
//
// 背景：nativePairFor 的兜底链（header/UA → body 标记）让「不填
// ClientType」在功能上等价 ⇒ 接线被摘掉时**没有任何行为测试会红**
// （M8 变异臂实证）。但接线是「强名在请求边界一次确定、避免每候选
// 重复解析 body」的性能与语义锚点 ⇒ 用源码钉测守住（仓内先例：
// modality_mismatch_wiring_guard_test.go）。
//
// 若你改动了这三个构造点并因此红 here：ClientType 必须保持由
// extractClientType(r)（或更强的来源）填充——删掉它会让 executor
// 退化到「弱名才解析 body 标记」路径，且每个候选重复解析。
func TestExecParamsClientTypeWiredAtBoundary(t *testing.T) {
	sites := []struct {
		file string
		desc string
	}{
		{"messages.go", "/v1/messages（anthropic 入向 —— minimax-code 的主路径）"},
		{"responses.go", "/v1/responses"},
		{"handler.go", "chat 主路径"},
	}
	for _, s := range sites {
		t.Run(s.file, func(t *testing.T) {
			raw, err := os.ReadFile(s.file)
			if err != nil {
				t.Fatalf("read %s: %v", s.file, err)
			}
			src := string(raw)
			if !strings.Contains(src, "ClientType: ") {
				t.Fatalf("%s（%s）：ExecParams 构造不再填 ClientType —— 原厂直通的边界接线被摘掉（见 nativePairFor 注释）", s.file, s.desc)
			}
		})
	}
}
