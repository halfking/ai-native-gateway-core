package ir

import (
	"sync"
	"testing"
)

// 协议转换层的性能基准与并发安全测试（2026-11-11 审计轮）。
//
// 性能目标（来自 docs/audit/2026-11-11-协议转换层完整测试计划.md）：
//   · Parser    < 100μs / request
//   · Serializer < 100μs / request
//   · Roundtrip < 500μs / request
//
// 纪律：基准数字**不写死**在断言里（机器差异会让写死的阈值变成假红）；
// 断言只做**相对回归**与**并发安全**两件可判定的事。

// ─────────────────────────────────────────────────────────────────────────────
// 基准
// ─────────────────────────────────────────────────────────────────────────────

// benchOpenAIBody 是一个含多轮对话 + 工具 + 多模态块的 realistic 请求体。
func benchOpenAIBody() []byte {
	return []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role":"system","content":"You are a helpful assistant."},
			{"role":"user","content":[{"type":"text","text":"weather in SF?"}]},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_1","content":"sunny, 22C"},
			{"role":"user","content":[{"type":"text","text":"and tomorrow?"}]}
		],
		"tools": [
			{"type":"function","function":{"name":"get_weather","description":"lookup","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}},
			{"type":"function","function":{"name":"get_time","description":"clock","parameters":{"type":"object"}}}
		],
		"tool_choice":"auto",
		"temperature":0.7,
		"top_p":0.9,
		"max_tokens":1024,
		"stream":true,
		"custom_ext_field":"keep-me"
	}`)
}

// benchAnthropicBody 是 Anthropic 侧的 realistic 请求体。
func benchAnthropicBody() []byte {
	return []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 2048,
		"system": [{"type":"text","text":"You are helpful."}],
		"messages": [
			{"role":"user","content":[{"type":"text","text":"weather?"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"SF"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny"}]}
		],
		"tools": [{"name":"get_weather","description":"lookup","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"temperature":0.7,
		"custom_ext_field":"keep-me"
	}`)
}

// BenchmarkIRConvRoundtripOpenAIToAnthropic 基准：**跨协议**完整往返
// （解析→校验→序列化）。
//
// ⚠️ 同协议 parse/serialize 基准已存在（parse_openai_test.go /
// parse_anthropic_test.go / serialize_openai_test.go /
// serialize_anthropic_test.go / detect_test.go）—— 本文件**不重复**它们，
// 只补「跨协议 + 带校验」这一层（既有基准不含 ValidateAndFixRequest）。
func BenchmarkIRConvRoundtripOpenAIToAnthropic(b *testing.B) {
	body := benchOpenAIBody()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, err := ParseOpenAI(body)
		if err != nil {
			b.Fatal(err)
		}
		req = ValidateAndFixRequest(req, "bench")
		if _, err := SerializeAnthropic(req); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkIRConvRoundtripAnthropicToOpenAI 基准：反方向往返。
func BenchmarkIRConvRoundtripAnthropicToOpenAI(b *testing.B) {
	body := benchAnthropicBody()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, err := ParseAnthropic(body)
		if err != nil {
			b.Fatal(err)
		}
		req = ValidateAndFixRequest(req, "bench")
		if _, err := SerializeOpenAI(req); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkIRConvValidateAndFix 基准：校验+修复层（纯 IR 变换）。
func BenchmarkIRConvValidateAndFix(b *testing.B) {
	req, err := ParseOpenAI(benchOpenAIBody())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ValidateAndFixRequest(req, "bench")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 并发安全
// ─────────────────────────────────────────────────────────────────────────────

// TestConcurrentConversionIsSafe 并发转换：解析 + 校验 + 序列化。
//
// 目的：抓数据竞争（必须配 `go test -race` 跑才有意义）与共享状态污染。
// 判定标准是「零错误 + 零 race 报告」，不是「跑得快」。
func TestConcurrentConversionIsSafe(t *testing.T) {
	const workers = 32
	const iterations = 200

	openaiBody := benchOpenAIBody()
	anthropicBody := benchAnthropicBody()

	var wg sync.WaitGroup
	errCh := make(chan error, workers*iterations*2)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// 偶数 worker 走 OpenAI，奇数走 Anthropic —— 两个方向都要覆盖
				if id%2 == 0 {
					req, err := ParseOpenAI(openaiBody)
					if err != nil {
						errCh <- err
						continue
					}
					req = ValidateAndFixRequest(req, "concurrent")
					if _, err := SerializeAnthropic(req); err != nil {
						errCh <- err
					}
				} else {
					req, err := ParseAnthropic(anthropicBody)
					if err != nil {
						errCh <- err
						continue
					}
					req = ValidateAndFixRequest(req, "concurrent")
					if _, err := SerializeOpenAI(req); err != nil {
						errCh <- err
					}
				}
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	errCount := 0
	for err := range errCh {
		t.Errorf("并发转换错误: %v", err)
		errCount++
	}
	if errCount > 0 {
		t.Fatalf("检测到 %d 个并发错误", errCount)
	}
}

// TestConcurrentConversionProducesIdenticalOutput 并发下**输出一致性**：
//
// 只断言「无错误」是不够的 —— 共享状态污染（如某个 goroutine 的 Extensions
// 泄漏到另一个）不会报错，但会让线上请求带上别人的字段。
// 本测试断：同一个输入，串行与并发的输出**逐字节相同**。
func TestConcurrentConversionProducesIdenticalOutput(t *testing.T) {
	body := benchOpenAIBody()

	// 基线：串行一次
	baseReq, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	base, err := SerializeAnthropic(ValidateAndFixRequest(baseReq, "identity"))
	if err != nil {
		t.Fatal(err)
	}

	const workers = 16
	const iterations = 50

	// ⚠️ 容量必须是 workers*iterations：主 goroutine 在 wg.Wait() 之后才读，
	// 容量不足 ⇒ 发送端阻塞 ⇒ 死锁（本轮第一版就踩了这个，跑了 10 分钟超时）。
	results := make(chan string, workers*iterations)
	errCh := make(chan error, workers)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				req, err := ParseOpenAI(body)
				if err != nil {
					errCh <- err
					return
				}
				out, err := SerializeAnthropic(ValidateAndFixRequest(req, "identity"))
				if err != nil {
					errCh <- err
					return
				}
				results <- string(out)
			}
		}()
	}

	wg.Wait()
	close(results)
	close(errCh)

	for err := range errCh {
		t.Errorf("并发错误: %v", err)
	}

	baseStr := string(base)
	count := 0
	for got := range results {
		count++
		if got != baseStr {
			t.Fatalf("并发输出与串行基线不一致（状态污染）\n并发: %s\n基线: %s", got, baseStr)
		}
	}
	if count != workers*iterations {
		t.Fatalf("并发产出数不对: %d != %d", count, workers*iterations)
	}
}

// TestConcurrentParseDoesNotMutateInput 并发下**输入不被改写**：
//
// Parser 收 []byte 但内部会 unmarshal 到 map；若某条路径把请求体原地改写
// （例如把 tool_calls 规范化写回原数组），第二个 goroutine 就会读到被改过的
// 输入 ⇒ 输出依赖执行顺序。本测试钉住「输入字节逐字不变」。
func TestConcurrentParseDoesNotMutateInput(t *testing.T) {
	body := benchOpenAIBody()
	original := make([]byte, len(body))
	copy(original, body)

	const workers = 16
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ParseOpenAI(body); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if string(body) != string(original) {
		t.Error("Parser 改写了输入请求体（并发下会产生顺序依赖）")
	}
}
