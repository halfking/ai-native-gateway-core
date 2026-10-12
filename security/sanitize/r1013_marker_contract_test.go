package sanitize

// r1013_marker_contract_test.go — 2026-10-10 遮蔽标记跨包对齐。
//
// 遮蔽标记现在有两处实现：
//
//   - security/sanitize.RedactionMarker（输入守卫、还原兜底）
//   - domains/outputcompliance.RedactionMarker（输出合规，见 redaction_marker.go）
//
// 后者不能直接复用前者：sanitize 依赖 outputcompliance（output_sensitive.go 里的
// outputSensitiveChecker 用它的类型），反向 import 会成环。两份实现因此靠这个
// 测试对齐——形状漂移时客户端会在同一次响应里看到两种写法。
//
// 钉住的东西只有形状（`[REDACTED:<type>]`）。**不**承诺二者共用同一套类型名：
// 输出侧的 type 来自 DB 里的 pii_patterns.pattern_type，运维可以自己起名，
// sanitize 侧的 type 来自检测规则；强行统一会把两个命名空间耦在一起。

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

func TestRedactionMarkerShapesAgree(t *testing.T) {
	for _, typ := range []SensitiveType{TypePhone, TypeEmail, TypeSecret, TypeInternalIP} {
		want := RedactionMarker(typ)
		got := outputcompliance.RedactionMarker(string(typ))
		if got != want {
			t.Errorf("type %q: sanitize=%q outputcompliance=%q", typ, want, got)
		}
	}
	// 空类型退回裸标记：两处都必须如此，且不带冒号。
	if got := RedactionMarker(""); got != "[REDACTED]" {
		t.Errorf("sanitize 空类型 = %q", got)
	}
	if got := outputcompliance.RedactionMarker(""); got != "[REDACTED]" {
		t.Errorf("outputcompliance 空类型 = %q", got)
	}
}

// TestRedactionMarkerIsNotAStarRun 呼应用户的硬要求：遮蔽物不得是星号串。
func TestRedactionMarkerIsNotAStarRun(t *testing.T) {
	for _, typ := range []SensitiveType{TypePhone, TypeEmail, TypeSecret, TypeInternalIP, TypeServerIP, ""} {
		marker := RedactionMarker(typ)
		for i := 0; i < len(marker); i++ {
			if marker[i] == '*' {
				t.Fatalf("type %q 的标记含星号: %q", typ, marker)
			}
		}
	}
}
