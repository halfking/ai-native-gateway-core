// 2026-09-30 真机取证：GLM-5 家族经方舟 coding 端点出站时，
// reasoning_effort 的合法档位与网关能力表曾经严重不符。
//
// 实测端点：https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions
// 实测模型：glm-5-2-260617（= 网关 `glm-5.2` 的实际出站名 raw_model）
// 采样纪律：间隔 7s；none / low 各 n=3 复验，其余档 n=1 全扫。
//
//	none     → 400 `reasoning_effort 'none' is not supported by this model`  (3/3)
//	minimal  → 400 同上（方舟把 minimal 归一化为 none 后按 none 拒）
//	low      → 200，reasoning_content 长度 0  (3/3，即"关闭思考"的真实档)
//	medium   → 200，reasoning_content 长度 0
//	high     → 200，reasoning_content 长度 0
//	xhigh    → 200，reasoning_content 759 字符（真开启）
//	max      → 200，reasoning_content 937 字符（真开启）
//
// 危害链：能力表若声明 none/minimal，ClampEffort 会把客户端的关闭意图
// （disabled/off/none/no/false）改写成上游明确拒绝的值 → 上游 400。
// 这比修复前更糟：修复前是"原样透传给上游被拒"（责任在上游，网关没动手），
// 修复后是"网关主动改写成一个自己能力表宣称合法、实则被拒的值"——
// 错误被移交给网关，且 paramledger 会把它记成一次合法的 clamp 收窄。
//
// 本测试钉死该契约。若有人把 none/minimal 加回 glm-5 家族能力表，
// 或让关闭意图在 GLM 上落回 none/minimal，此处立刻红。
package paramguard

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
	"github.com/kaixuan/llm-gateway-go/internal/reasoncap"
	"github.com/kaixuan/llm-gateway-go/internal/reasonnorm"
)

// upstreamRejectedGLMEfforts 是真机实测被 glm-5-2-260617 拒绝的取值。
// 一旦出现在目标能力表里，关闭意图就会被改写成必 400 的值。
var upstreamRejectedGLMEfforts = []string{"none", "minimal"}

// TestReasoncap_GLM5FamilyOmitsUpstreamRejectedEfforts 守在能力表这一层：
// 即使有人绕过 paramguard 直接调 ClampEffort，落点也不会是被拒的值。
func TestReasoncap_GLM5FamilyOmitsUpstreamRejectedEfforts(t *testing.T) {
	for _, model := range []string{"glm-5", "glm-5.2", "glm-5.1", "glm-5-260617"} {
		caps := reasoncap.Resolve(t.Context(), model, nil)
		if !caps.Supported {
			t.Fatalf("%s: 期望 GLM 家族有思考能力，实际 Supported=false", model)
		}
		if caps.Dialect != reasoncap.DialectGLM {
			t.Errorf("%s: Dialect = %q, want %q", model, caps.Dialect, reasoncap.DialectGLM)
		}
		for _, rejected := range upstreamRejectedGLMEfforts {
			for _, e := range caps.Efforts {
				if e == rejected {
					t.Errorf("%s: 能力表包含真机被拒的档位 %q（上游报 400 "+
						"reasoning_effort '%s' is not supported by this model）",
						model, rejected, rejected)
				}
			}
		}
		if len(caps.Efforts) == 0 {
			t.Errorf("%s: Efforts 为空会使 fixReasoningEffort 早退，等于方言门空转", model)
		}
	}
}

// TestClampEffort_DisableIntentNeverLandsOnRejectedTier 是本条契约的
// 纯函数层断言：无论 supported 传入什么，关闭类写法都不得落到被拒档位。
// 这里直接喂真机测出的上游集合，而不是喂生产代码读出的集合——
// 后者会随能力表一起漂移，把"门与被测物同源"变成自证。
func TestClampEffort_DisableIntentNeverLandsOnRejectedTier(t *testing.T) {
	rejected := map[string]bool{}
	for _, e := range upstreamRejectedGLMEfforts {
		rejected[e] = true
	}
	// 真机测得的 glm-5 家族支持集（low 是关闭档，low/medium/high 均不产思考）。
	supported := []string{"max", "xhigh", "high", "medium", "low"}
	for _, in := range []string{"disabled", "disable", "off", "false", "none", "no", "0", "  DISABLED  "} {
		got := reasonnorm.ClampEffort(in, supported)
		if got == "" {
			t.Errorf("ClampEffort(%q, %v) = \"\"，关闭意图不应得到空串", in, supported)
			continue
		}
		if rejected[got] {
			t.Errorf("ClampEffort(%q, %v) = %q，真机证明该值会被上游 400 拒绝",
				in, supported, got)
		}
	}
	// 显式钉住落点：方舟侧关闭档是 low。
	if got := reasonnorm.ClampEffort("disabled", supported); got != "low" {
		t.Errorf("ClampEffort(%q, %v) = %q, want %q（真机：low 才是关闭档）",
			"disabled", supported, got, "low")
	}
}

// TestApplyReported_GLMDisableIntentSendsUpstreamAcceptedValue 端到端钉死出站体。
func TestApplyReported_GLMDisableIntentSendsUpstreamAcceptedValue(t *testing.T) {
	rejected := map[string]bool{}
	for _, e := range upstreamRejectedGLMEfforts {
		rejected[e] = true
	}
	for _, effort := range []string{"disabled", "off", "none", "no", "false"} {
		out, reports := ApplyReported(
			[]byte(`{"model":"glm-5.2","reasoning_effort":"`+effort+`","max_tokens":512}`),
			paramreg.DialectGLM,
		)
		var obj map[string]any
		if err := json.Unmarshal(out, &obj); err != nil {
			t.Fatalf("%s: unmarshal: %v", effort, err)
		}
		got, _ := obj["reasoning_effort"].(string)
		if got == "" {
			t.Errorf("effort=%q: 出站体丢失 reasoning_effort", effort)
			continue
		}
		if rejected[got] {
			t.Errorf("effort=%q → 出站 %q，该值被真机证实会 400", effort, got)
		}
		if len(reports) == 0 {
			t.Errorf("effort=%q: 关闭意图被改写却没有 clamp 报告，paramledger 会静默", effort)
		}
	}
}
