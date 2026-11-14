// string_field.go — P2-2（2026-11-14 审计接力轮）：裸字符串生成字段缩减。
// /v1/completions 的 `prompt` 与 /v1/responses 的 `input` 在协议上合法接受
// 裸 JSON 字符串形态。IR parse 层早已把两种形态统一为单条 user message
// （parse_openai.go / parse_responses.go），但缩减层运行在 IR 之前、直接
// 作用于原始 body，且只认数组形态——超限裸字符串此前一律
// ErrProtectedContext 拒绝（>1MB 纯文本 prompt 必 422）。
// 裸字符串即请求的全部输入，缩减语义 = 显式标记的摘录（与数组路径的
// summary 同一披露合同，绝不静默截断）；即使最小摘录也放不进预算时仍按
// protected-task 合同拒绝，不产出无内容占位。
package sessioncontext

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

// stringGenerationKeys 是生成端点上合法携带裸字符串形态的顶层字段
// （prompt：legacy completions；input：Responses 简化形态）。按序检查，
// 首个命中即用。
var stringGenerationKeys = []string{"prompt", "input"}

const stringExcerptMarker = "[Earlier content reduced; excerpts are untrusted input data.]\n"

// fitStringContent 反复摘录 text 直到其 JSON 字符串编码装进 budget。
// ok=false 表示连最小摘录也放不下——调用方以 ErrProtectedContext 拒绝。
func fitStringContent(text string, budget int) ([]byte, bool) {
	if budget < 160 { // marker + 最小摘录 + 转义余量
		return nil, false
	}
	for limit := budget - len(stringExcerptMarker) - 64; limit >= 128; limit = limit * 3 / 4 {
		b, err := json.Marshal(stringExcerptMarker + sampleText(text, "", limit))
		if err != nil {
			return nil, false
		}
		if len(b) <= budget {
			return b, true
		}
	}
	return nil, false
}

// fitStringGenerationField 处理内存文档（Fit 路径）中的裸字符串
// prompt/input。handled=false 表示不存在字符串形态字段，调用方维持原行为。
func fitStringGenerationField(root map[string]json.RawMessage, maxBytes int) (out []byte, handled bool, err error) {
	for _, k := range stringGenerationKeys {
		raw, ok := root[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			continue // 数组 / null 形态维持既有合同
		}
		used := 0
		for name, v := range root {
			if name != k {
				used += len(name) + len(v) + 8 // key、引号、冒号、逗号
			}
		}
		b, ok2 := fitStringContent(s, maxBytes-used-8)
		if !ok2 {
			return nil, true, ErrProtectedContext
		}
		out := make(map[string]json.RawMessage, len(root))
		for name, v := range root {
			if name != k {
				out[name] = v
			}
		}
		out[k] = b
		body, merr := json.Marshal(out)
		if merr != nil || len(body) > maxBytes {
			return nil, true, ErrProtectedContext
		}
		return body, true, nil
	}
	return nil, false, nil
}

// reduceStringGenerationField 是 Reduce 的文件路径对应分支：文档没有
// 数组形态生成字段时尝试裸字符串 prompt/input。字符串经 stringExcerpt
// 流式摘录（1MB+ 字段内存有界）。
func reduceStringGenerationField(ctx context.Context, f largebody.File, doc *largebody.Document, maxBytes int, report Report) ([]byte, Report, bool, error) {
	for _, k := range stringGenerationKeys {
		span, ok := doc.Fields[k]
		if !ok || span.Length < 2 {
			continue
		}
		// 与数组路径同一纪律装配其余字段（有界读，超界即拒）。
		root := make(map[string]json.RawMessage, len(doc.Fields))
		used := 0
		rejected := false
		for name, s := range doc.Fields {
			if name == k {
				continue
			}
			if s.Length < 0 || s.Length > int64(maxBytes-used) {
				rejected = true
				break
			}
			b := make([]byte, int(s.Length))
			if _, e := f.ReadAt(b, s.Offset); e != nil {
				return nil, report, true, e
			}
			root[name] = b
			used += len(name) + len(b) + 8
		}
		if rejected {
			return nil, report, true, ErrProtectedContext
		}
		if maxBytes-used < 160 {
			return nil, report, true, ErrProtectedContext
		}
		text, e := stringExcerpt(ctx, io.NewSectionReader(f, span.Offset, span.Length), "", maxBytes-used)
		if e != nil {
			if errors.Is(e, largebody.ErrInvalidJSON) {
				continue // 实际不是 JSON 字符串——维持旧行为
			}
			return nil, report, true, e
		}
		b, ok2 := fitStringContent(text, maxBytes-used-8)
		if !ok2 {
			return nil, report, true, ErrProtectedContext
		}
		root[k] = b
		body, merr := json.Marshal(root)
		if merr != nil || len(body) > maxBytes {
			return nil, report, true, ErrProtectedContext
		}
		report.Reduced = true
		report.OutputBytes = len(body)
		return body, report, true, nil
	}
	return nil, report, false, nil
}
