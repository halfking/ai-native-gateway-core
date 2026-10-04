// provider/modality_verdict_gate_test.go — 候选集上的多模态判词闸门
//
// 这条闸门在**每一个请求的候选集构建**上，所以它的形状必须被钉住：
// 两个档位的 SQL 都要真的出现在 candidateQuerySQL() 里，而不只是
// 存在于一个没人调用的辅助函数里。
//
// 三条不可让步的语义：
//
//  1. carry_level 绝不进闸门。「上游收得下图片内容块」不是「模型看得见」，
//     把前者当前者正是本项目要修的缺陷。
//  2. default 档只由**语义负证据**排除模型；read_level='unknown'（探过但
//     没结论、例如上游超时）绝不能排除——一个间歇性超时的模型不该被摘掉。
//  3. strict 档额外要求 read_level='confirmed'，且对 $3=” 与 $3='text'
//     的请求完全不生效（纯文本请求没有多模态门）。
package provider

import (
	"strings"
	"testing"
)

func TestModalityVerdictGateSQL_DefaultShape(t *testing.T) {
	gate := modalityVerdictGateSQL(false)

	if strings.Contains(gate, "carry_level") {
		t.Error("gate references carry_level —— 结构级「可承载」不参与路由信任")
	}
	if !strings.Contains(gate, "read_level = 'negative'") {
		t.Error("gate must exclude on semantic NEGATIVE evidence")
	}
	if strings.Contains(gate, "read_level = 'confirmed'") {
		t.Error("default gate must not require positive evidence (that is strict mode)")
	}
	// 内层守卫：还有 unknown 就不下判词。
	if !strings.Contains(gate, "mmv2.read_level <> 'negative'") {
		t.Error("gate lost the inner guard — a single unknown binding must not veto a model")
	}
	if !strings.Contains(gate, "$3 IN ('vision', 'audio', 'video')") {
		t.Error("gate must be inert for text requests")
	}
}

func TestModalityVerdictGateSQL_StrictAddsConfirmation(t *testing.T) {
	gate := modalityVerdictGateSQL(true)
	if !strings.Contains(gate, "read_level = 'confirmed'") {
		t.Error("strict mode must require positive semantic evidence")
	}
	if strings.Contains(gate, "carry_level") {
		t.Error("gate references carry_level in strict mode too")
	}
	// strict 档是 default 档的超集：负证据排除仍须在。
	if !strings.Contains(gate, "read_level = 'negative'") {
		t.Error("strict mode dropped the negative-evidence exclusion")
	}
}

// 闸门必须真的被拼进候选 SQL，而不是只存在于辅助函数里。
func TestCandidateQueryContainsModalityGate(t *testing.T) {
	t.Setenv(ModalityRoutingStrictEnv, "")
	q := candidateQuerySQL()
	if strings.Contains(q, "/*MODALITY_VERDICT_GATE*/") {
		t.Fatal("gate marker survived substitution — the gate was never applied")
	}
	if !strings.Contains(q, "model_modality_verification") {
		t.Error("candidate SQL has no modality verdict gate")
	}
	if !strings.Contains(q, "read_level = 'negative'") {
		t.Error("default candidate SQL must exclude on semantic negative evidence")
	}
	if strings.Contains(q, "read_level = 'confirmed'") {
		t.Error("default candidate SQL must not require confirmation")
	}

	t.Setenv(ModalityRoutingStrictEnv, "on")
	strictQ := candidateQuerySQL()
	if !strings.Contains(strictQ, "read_level = 'confirmed'") {
		t.Error("strict env did not reach the candidate SQL")
	}
	// 拼进去之后整条语句仍须是完整 SQL：闸门替换不能吃掉右括号。
	if !strings.Contains(strictQ, "ORDER BY") {
		t.Error("gate substitution corrupted the candidate SQL")
	}
}

func TestModalityRoutingStrict(t *testing.T) {
	for _, v := range []string{"1", "true", "on", "yes", "ON"} {
		t.Setenv(ModalityRoutingStrictEnv, v)
		if !modalityRoutingStrict() {
			t.Errorf("strict switch %q did not enable strict mode", v)
		}
	}
	for _, v := range []string{"", "0", "false", "off", "no"} {
		t.Setenv(ModalityRoutingStrictEnv, v)
		if modalityRoutingStrict() {
			t.Errorf("strict switch %q enabled strict mode", v)
		}
	}
}
