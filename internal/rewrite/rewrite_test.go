// AUDIT-GUARD: 本文件的门**必须随包在 CI 上执行** ⇒ 该包已登记进 Makefile 的 GUARD_PACKAGES
package rewrite

// rewrite_test.go —— P1-4 的门：**按候选改写的类型化契约**。
//
// # 三条验收（计划书 §6-P1-4）怎么落到判据上
//
//	① 「改写只动目标字段的**原字节对拍**」
//	    ⇒ 判据不是「重新序列化后 JSON 语义相等」（那太弱），而是
//	    **「结果的非目标字节与原 body 逐字相同」**。
//	② 「改写失败 ⇒ **原样发出**」
//	    ⇒ 判据是**返回的字节与入参逐字相同**（不是「语义相同」）。
//	③ 「同一请求不同候选**互不污染**」
//	    ⇒ 判据是对**同一个原始 body** 分别按两个候选应用两套规则，
//	    然后断言：两个结果各自正确、且**原始 body 一个字节都没变**。
//
// # ★ 被测集合与期望都**不来自**被测对象
//
// 期望值全部是本文件里的**字面量**（字节串、条数、字段名）⇒
// 改实现改不掉断言（与 LESSONS.md §355① 同族：门不能从被测对象取被测集合）。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// chatBody 是一个**真实形态**的 OpenAI 风格请求体（键序、缩进都不是规范化的）。
const chatBody = `{"model":"gpt-4o","messages":[{"role":"user","content":"say \"hi\""}],"temperature":0.7,"stream":true}`

// assertOnlyFieldChanged 断言 result 与 body 相比**只有** field 的值区间变了。
//
// ★ 这就是「原字节对拍」的实现：不是比较解析后的结构，
// 而是**比较字节**——非目标字节必须**逐字**相同。
func assertOnlyFieldChanged(t *testing.T, body, result []byte, field string) {
	t.Helper()
	start, end, ok := locateTopLevelField(body, field)
	if !ok {
		t.Fatalf("原 body 里找不到顶层字段 %q", field)
	}
	// 目标字段的**新**值区间（在结果里）。
	ns, ne, ok := locateTopLevelField(result, field)
	if !ok {
		t.Fatalf("结果里找不到顶层字段 %q", field)
	}
	// 前缀逐字相同。
	if string(result[:ns]) != string(body[:start]) {
		t.Fatalf("目标字段之前的字节变了：\n原: %q\n新: %q", body[:start], result[:ns])
	}
	// 后缀逐字相同（长度可能因新值长度不同而变 ⇒ 按尾部比）。
	oldTail := body[end:]
	newTail := result[ne:]
	if string(oldTail) != string(newTail) {
		t.Fatalf("目标字段之后的字节变了：\n原: %q\n新: %q", oldTail, newTail)
	}
}

// TestRewriteOnlyTouchesTargetFieldBytes 验收①：
// 改写**只**动目标字段，其余字节逐字不动。
func TestRewriteOnlyTouchesTargetFieldBytes(t *testing.T) {
	spec := Spec{
		RewritePoint: RewritePoint{Point: PointBeforeSend},
		Field:        "temperature",
		Value:        json.RawMessage(`1`),
	}
	out, err := Apply(context.Background(), PointBeforeSend, []byte(chatBody),
		Candidate{Provider: "openai", Model: "gpt-4o"}, []Spec{spec})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertOnlyFieldChanged(t, []byte(chatBody), out, "temperature")
	// ★ 其余字段的值必须**原样**在结果里（尤其是含转义引号的 messages）。
	if !strings.Contains(string(out), `{"role":"user","content":"say \"hi\""}`) {
		t.Fatalf("messages 字段被改动了：%s", out)
	}
	// ★ 目标字段确实变了。
	if !strings.Contains(string(out), `"temperature":1`) {
		t.Fatalf("temperature 未被改写：%s", out)
	}
}

// TestRewriteDoesNotTouchNestedSameNameField 验收①的边界：
// **嵌套**对象里的同名字段**不算**顶层字段 ⇒ 不得被改。
func TestRewriteDoesNotTouchNestedSameNameField(t *testing.T) {
	// ★★ 嵌套的那个**故意放在前面** ⇒ 一个「朴素全文搜索第一个 `"model"`」的实现
	// 会命中嵌套那个 ⇒ 这条门就能抓到它（否则这条用例是白写的）。
	body := `{"options":{"model":"nested-model"},"model":"gpt-4o","stream":true}`
	spec := Spec{Field: "model", Value: json.RawMessage(`"rewritten"`)}
	out, err := Apply(context.Background(), PointBeforeSend, []byte(body),
		Candidate{Provider: "p", Model: "m"}, []Spec{spec})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(string(out), `"options":{"model":"nested-model"}`) {
		t.Fatalf("嵌套同名字段被改动了：%s", out)
	}
	if !strings.Contains(string(out), `"model":"rewritten"`) {
		t.Fatalf("顶层 model 未被改写：%s", out)
	}
}

// TestRewriteFailureKeepsOriginalBytes 验收②：
// 改写失败 ⇒ **原样发出**（字节逐字相同，不是「语义相同」）。
func TestRewriteFailureKeepsOriginalBytes(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
	}{
		{"字段不存在", Spec{Field: "not_there", Value: json.RawMessage(`1`)}},
		{"Field 为空", Spec{Value: json.RawMessage(`1`)}},
		{"Field 是路径", Spec{Field: "a.b", Value: json.RawMessage(`1`)}},
		{"Value 非法 JSON", Spec{Field: "temperature", Value: json.RawMessage(`{`)}},
		{"未知失败策略", Spec{Field: "temperature", Value: json.RawMessage(`1`), OnFailure: "weird"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Apply(context.Background(), PointBeforeSend, []byte(chatBody),
				Candidate{Provider: "p", Model: "m"}, []Spec{tc.spec})
			if err != nil {
				t.Fatalf("fail-open 路径不应返回错误：%v", err)
			}
			if string(out) != chatBody {
				t.Fatalf("失败后不是原样发出：\nwant: %s\ngot:  %s", chatBody, out)
			}
		})
	}
}

// TestRewriteFailClosedReturnsError 验收②的另一面：
// 显式声明 fail-closed ⇒ 返回错误（不发出）。
func TestRewriteFailClosedReturnsError(t *testing.T) {
	spec := Spec{
		Field:     "not_there",
		Value:     json.RawMessage(`1`),
		OnFailure: PolicyFailClosed,
	}
	out, err := Apply(context.Background(), PointBeforeSend, []byte(chatBody),
		Candidate{Provider: "p", Model: "m"}, []Spec{spec})
	if err == nil {
		t.Fatal("fail-closed 路径应返回错误")
	}
	if !errors.Is(err, ErrRewriteFailed) {
		t.Fatalf("err = %v，want ErrRewriteFailed", err)
	}
	if out != nil {
		t.Fatalf("fail-closed 路径不应返回 body：%q", out)
	}
}

// TestRewriteTimeoutKeepsOriginalBytes 验收②的第三种失败：
// **超时** ⇒ 按失败策略处理（默认 fail-open ⇒ 原样发出）。
//
// ★ 用注入点**确定性**地触发超时（否则只能用 1ns 赌运气 ⇒ 假绿的门）。
func TestRewriteTimeoutKeepsOriginalBytes(t *testing.T) {
	testDelayHook = func() { time.Sleep(300 * time.Millisecond) }
	defer func() { testDelayHook = nil }()

	spec := Spec{
		Field:   "temperature",
		Value:   json.RawMessage(`1`),
		Timeout: 20 * time.Millisecond,
	}
	out, err := Apply(context.Background(), PointBeforeSend, []byte(chatBody),
		Candidate{Provider: "p", Model: "m"}, []Spec{spec})
	if err != nil {
		t.Fatalf("fail-open 路径不应返回错误：%v", err)
	}
	if string(out) != chatBody {
		t.Fatalf("超时后不是原样发出：\nwant: %s\ngot:  %s", chatBody, out)
	}
}

// TestRewriteCandidatesDoNotPolluteEachOther 验收③：
// 同一请求的不同候选互不污染。
func TestRewriteCandidatesDoNotPolluteEachOther(t *testing.T) {
	original := []byte(chatBody)
	before := string(original)

	specs := []Spec{
		{
			RewritePoint: RewritePoint{Provider: "openai", Model: "gpt-4o"},
			Field:        "temperature",
			Value:        json.RawMessage(`0.1`),
		},
		{
			RewritePoint: RewritePoint{Provider: "anthropic"},
			Field:        "temperature",
			Value:        json.RawMessage(`0.9`),
		},
	}

	outOpenAI, err := Apply(context.Background(), PointBeforeSend, original,
		Candidate{Provider: "openai", Model: "gpt-4o"}, specs)
	if err != nil {
		t.Fatalf("openai 候选: %v", err)
	}
	outAnthropic, err := Apply(context.Background(), PointBeforeSend, original,
		Candidate{Provider: "anthropic", Model: "claude"}, specs)
	if err != nil {
		t.Fatalf("anthropic 候选: %v", err)
	}

	// ① 各自命中自己的规则。
	if !strings.Contains(string(outOpenAI), `"temperature":0.1`) {
		t.Fatalf("openai 候选未命中自己的规则：%s", outOpenAI)
	}
	if !strings.Contains(string(outAnthropic), `"temperature":0.9`) {
		t.Fatalf("anthropic 候选未命中自己的规则：%s", outAnthropic)
	}
	// ② ★ 原始 body 一个字节都没被改（纯函数）。
	if string(original) != before {
		t.Fatalf("原始 body 被就地改动了：\nwant: %s\ngot:  %s", before, original)
	}
	// ③ 两个结果彼此独立（不是同一个切片/同一份内容）。
	if string(outOpenAI) == string(outAnthropic) {
		t.Fatal("两个候选的结果相同 ⇒ 规则选择器失效")
	}
	// ④ 两个结果都只动了目标字段。
	assertOnlyFieldChanged(t, original, outOpenAI, "temperature")
	assertOnlyFieldChanged(t, original, outAnthropic, "temperature")
}

// TestRewritePointIsPerDimension 验收③的补充：
// 选择器是**逐维收窄**的：Provider / Model 留空 = 匹配任意。
func TestRewritePointIsPerDimension(t *testing.T) {
	specs := []Spec{{
		RewritePoint: RewritePoint{Provider: "openai"}, // 只填 Provider ⇒ 匹配任意模型
		Field:        "temperature",
		Value:        json.RawMessage(`0.2`),
	}}
	out, err := Apply(context.Background(), PointBeforeSend, []byte(chatBody),
		Candidate{Provider: "openai", Model: "any-model-at-all"}, specs)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(string(out), `"temperature":0.2`) {
		t.Fatalf("只填 Provider 应匹配任意模型：%s", out)
	}
	// Provider 不匹配 ⇒ 不生效（原样）。
	out2, err := Apply(context.Background(), PointBeforeSend, []byte(chatBody),
		Candidate{Provider: "other", Model: "gpt-4o"}, specs)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if string(out2) != chatBody {
		t.Fatalf("Provider 不匹配时不应改写：%s", out2)
	}
}

// TestRewritePointMustMatch 验收③的补充：
// 位置不匹配 ⇒ 不生效（Point 是选择器的一维）。
func TestRewritePointMustMatch(t *testing.T) {
	specs := []Spec{{
		RewritePoint: RewritePoint{Point: PointAfterModel},
		Field:        "temperature",
		Value:        json.RawMessage(`0.3`),
	}}
	out, err := Apply(context.Background(), PointBeforeSend, []byte(chatBody),
		Candidate{Provider: "p", Model: "m"}, specs)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if string(out) != chatBody {
		t.Fatalf("位置不匹配时不应改写：%s", out)
	}
}

// TestApplyIsPureFunction 契约的根性质：
// Apply 不改入参、不共享可变状态 ⇒ 这是「候选不污染」的**根**保证。
func TestApplyIsPureFunction(t *testing.T) {
	original := []byte(chatBody)
	specs := []Spec{{Field: "temperature", Value: json.RawMessage(`0.5`)}}
	first, err := Apply(context.Background(), PointBeforeSend, original,
		Candidate{Provider: "p", Model: "m"}, specs)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if string(original) != chatBody {
		t.Fatalf("Apply 改动了入参：%s", original)
	}
	// ★ 再跑一次，结果必须**逐字相同**（无隐藏状态、无顺序依赖）。
	second, err := Apply(context.Background(), PointBeforeSend, original,
		Candidate{Provider: "p", Model: "m"}, specs)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("两次 Apply 结果不同：\n1: %s\n2: %s", first, second)
	}
}
