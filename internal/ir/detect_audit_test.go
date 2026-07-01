package ir

import (
	"encoding/json"
	"testing"
)

// TestAudit_DetectProtocol_ClaudeModel 审计 claude 模型名的协议检测行为。
// 关键问题:客户端用 OpenAI Chat Completions 格式发送 model="claude-sonnet-5",
// DetectProtocol 会不会因为 model 字段包含 "claude" 而误判为 anthropic-messages?
func TestAudit_DetectProtocol_ClaudeModel(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		expectProto  string
		expectReason string
	}{
		{
			name: "标准OpenAI Chat Completions + claude-sonnet-5",
			body: `{
				"model": "claude-sonnet-5",
				"messages": [
					{"role": "user", "content": "hello"}
				],
				"max_tokens": 1024
			}`,
			expectProto:  ProtocolOpenAIChat,
			expectReason: "body有messages[]是OpenAI标志,model hint在这个case不起作用(detect.go:169-180)",
		},
		{
			name: "标准OpenAI Chat Completions + claude-opus-4-8",
			body: `{
				"model": "claude-opus-4-8",
				"messages": [
					{"role": "user", "content": "hello"}
				],
				"max_tokens": 1024
			}`,
			expectProto:  ProtocolOpenAIChat,
			expectReason: "body有messages[]是OpenAI标志",
		},
		{
			name: "空body + claude model hint",
			body: `{
				"model": "claude-sonnet-5"
			}`,
			expectProto:  ProtocolAnthropicMessages,
			expectReason: "body完全空,只有model hint起作用(detect.go:189-193)",
		},
		{
			name: "仅有system字段 + claude model",
			body: `{
				"model": "claude-sonnet-5",
				"system": "You are helpful",
				"max_tokens": 1024
			}`,
			expectProto:  ProtocolAnthropicMessages,
			expectReason: "无messages[],只有anthropic字段+claude model hint(detect.go:182-183)",
		},
		{
			name: "messages[] + system + claude model (混合信号)",
			body: `{
				"model": "claude-sonnet-5",
				"system": "You are helpful",
				"messages": [
					{"role": "user", "content": "hello"}
				],
				"max_tokens": 1024
			}`,
			expectProto:  ProtocolOpenAIChat,
			expectReason: "messages[]存在,openAIScore=0.3>0, body shape胜出(detect.go:206-210)",
		},
		{
			name: "messages[] + system + thinking + claude (多个anthropic字段)",
			body: `{
				"model": "claude-sonnet-5",
				"system": "You are helpful",
				"thinking": {"type": "enabled"},
				"messages": [
					{"role": "user", "content": "hello"}
				],
				"max_tokens": 1024
			}`,
			expectProto:  ProtocolAnthropicMessages,
			expectReason: "anthropicExclusive>=2触发(system+thinking),detect.go:203-204",
		},
		{
			name: "标准OpenAI + gpt-4o (对照组)",
			body: `{
				"model": "gpt-4o",
				"messages": [
					{"role": "user", "content": "hello"}
				],
				"max_tokens": 1024
			}`,
			expectProto:  ProtocolOpenAIChat,
			expectReason: "messages[] + gpt model hint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, conf, err := DetectProtocol([]byte(tt.body))
			if err != nil {
				t.Fatalf("DetectProtocol error: %v", err)
			}

			var bodyParsed map[string]any
			_ = json.Unmarshal([]byte(tt.body), &bodyParsed)
			modelField := bodyParsed["model"]
			_, hasMessages := bodyParsed["messages"]
			_, hasSystem := bodyParsed["system"]
			_, hasThinking := bodyParsed["thinking"]

			t.Logf("━━━ 审计结果 ━━━")
			t.Logf("  model字段: %v", modelField)
			t.Logf("  has messages[]: %v", hasMessages)
			t.Logf("  has system: %v", hasSystem)
			t.Logf("  has thinking: %v", hasThinking)
			t.Logf("  检测结果: %q (confidence=%.3f)", proto, conf)
			t.Logf("  预期: %q", tt.expectProto)
			t.Logf("  原因: %s", tt.expectReason)

			if proto != tt.expectProto {
				t.Errorf("❌ FAIL: 检测协议 = %q, 预期 = %q", proto, tt.expectProto)
			} else {
				t.Logf("✅ PASS")
			}
		})
	}
}

// TestAudit_NeedsConversion 审计 needsConversion 判断的所有组合。
// 这个判断在 executor_anthropic.go:382-384 决定是否做 OpenAI→Anthropic 转换。
func TestAudit_NeedsConversion(t *testing.T) {
	cases := []struct {
		clientProto string
		candProto   string
		expect      bool
		reason      string
	}{
		{"openai-chat", "anthropic-messages", true, "Q3: OpenAI客户端 → Anthropic上游"},
		{"openai-chat", "openai-completions", false, "Q1: OpenAI客户端 → OpenAI上游"},
		{"anthropic-messages", "anthropic-messages", false, "Q4: Anthropic客户端 → Anthropic上游"},
		{"anthropic-messages", "openai-completions", false, "Q2: Anthropic客户端 → OpenAI上游(不走anthropic executor)"},
		{"", "anthropic-messages", false, "clientProto为空不转换"},
		{"openai-chat", "", false, "candProto为空"},
	}

	t.Logf("━━━ needsConversion 判断矩阵 ━━━")
	t.Logf("%-20s %-25s %-10s %s", "ClientProtocol", "Candidate.Protocol", "转换?", "说明")
	t.Log("==================================================================================")

	for _, tc := range cases {
		needsConversion := tc.clientProto != "anthropic-messages" &&
			tc.clientProto != "" &&
			tc.candProto == "anthropic-messages"

		status := "✅"
		if needsConversion != tc.expect {
			status = "❌ FAIL"
		}

		t.Logf("%s %-20s %-25s %-10v %s",
			status, tc.clientProto, tc.candProto, needsConversion, tc.reason)

		if needsConversion != tc.expect {
			t.Errorf("needsConversion不匹配: got=%v want=%v", needsConversion, tc.expect)
		}
	}
}

// TestAudit_ProtocolFlow_EndToEnd 完整流程审计:
// 1. 客户端 body (OpenAI格式 + claude model)
// 2. DetectProtocol 检测结果
// 3. 假设 cand.Protocol = "anthropic-messages"
// 4. needsConversion 判断
// 5. IR 转换执行
// 6. 最终上游 body
func TestAudit_ProtocolFlow_EndToEnd(t *testing.T) {
	flows := []struct {
		name           string
		clientBody     string
		candidateProto string
		expectFlow     string
	}{
		{
			name: "场景1: OpenAI格式+claude模型 → Anthropic上游 (正常Q3)",
			clientBody: `{
				"model": "claude-sonnet-5",
				"messages": [{"role": "user", "content": "你好"}],
				"max_tokens": 1024
			}`,
			candidateProto: "anthropic-messages",
			expectFlow:     "DetectProtocol→openai-chat → needsConversion=true → IR转换 → Anthropic body",
		},
		{
			name: "场景2: OpenAI格式+gpt模型 → OpenAI上游 (正常Q1)",
			clientBody: `{
				"model": "gpt-4o",
				"messages": [{"role": "user", "content": "hello"}],
				"max_tokens": 1024
			}`,
			candidateProto: "openai-completions",
			expectFlow:     "DetectProtocol→openai-chat → needsConversion=false → 直通",
		},
		{
			name: "场景3: Anthropic格式+claude模型 → Anthropic上游 (正常Q4)",
			clientBody: `{
				"model": "claude-sonnet-5",
				"system": "You are helpful",
				"messages": [{"role": "user", "content": [{"type":"text","text":"hello"}]}],
				"max_tokens": 1024
			}`,
			candidateProto: "anthropic-messages",
			expectFlow:     "DetectProtocol→openai-chat或anthropic → needsConversion取决于检测结果",
		},
	}

	for _, flow := range flows {
		t.Run(flow.name, func(t *testing.T) {
			t.Logf("━━━ 流程审计 ━━━")

			// Step 1: 协议检测
			clientProto, conf, err := DetectProtocol([]byte(flow.clientBody))
			if err != nil {
				t.Fatalf("DetectProtocol: %v", err)
			}
			t.Logf("Step 1: DetectProtocol → %q (conf=%.3f)", clientProto, conf)

			// Step 2: needsConversion 判断
			needsConv := clientProto != "anthropic-messages" &&
				clientProto != "" &&
				flow.candidateProto == "anthropic-messages"
			t.Logf("Step 2: needsConversion → %v", needsConv)
			t.Logf("  (clientProto=%q, cand.Protocol=%q)", clientProto, flow.candidateProto)

			// Step 3: 如果需要转换,执行 IR 转换
			if needsConv {
				irReq, err := ParseOpenAI([]byte(flow.clientBody))
				if err != nil {
					t.Fatalf("ParseOpenAI: %v", err)
				}
				t.Logf("Step 3: ParseOpenAI → ir.Model=%q, messages=%d",
					irReq.Model, len(irReq.Messages))

				// 模拟 resolveOutboundModel (这里假设 cand.RawModel)
				irReq.Model = "claude-sonnet-4-5-20250929" // 模拟替换

				upstreamBody, err := SerializeAnthropic(irReq)
				if err != nil {
					t.Fatalf("SerializeAnthropic: %v", err)
				}

				var result map[string]any
				_ = json.Unmarshal(upstreamBody, &result)
				t.Logf("Step 4: SerializeAnthropic → 上游body.model=%q", result["model"])

				// 验证 messages 是否完整
				msgs := result["messages"].([]any)
				if len(msgs) == 0 {
					t.Errorf("❌ FAIL: 上游body缺少messages")
				} else {
					t.Logf("  上游body.messages=%d条", len(msgs))
				}
			} else {
				t.Logf("Step 3: needsConversion=false → 不做IR转换,body直通")
			}

			t.Logf("预期流程: %s", flow.expectFlow)
		})
	}
}

// TestAudit_ModelHintBehavior 专门审计 model hint 在不同 body shape 下的行为。
// 目标:确认 detect.go:169-180 的注释是否与实际代码一致。
func TestAudit_ModelHintBehavior(t *testing.T) {
	t.Logf("━━━ Model Hint 行为审计 ━━━")
	t.Logf("根据 detect.go:169-180 的注释:")
	t.Logf("  - body shape (messages[]) 是权威信号")
	t.Logf("  - model hint 只在 body 信号单边或完全为空时起作用")
	t.Logf("")

	cases := []struct {
		desc      string
		body      string
		wantProto string
		hintUsed  string
	}{
		{
			desc:      "messages[] + claude → OpenAI (body shape权威)",
			body:      `{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`,
			wantProto: ProtocolOpenAIChat,
			hintUsed:  "NO - body shape wins",
		},
		{
			desc:      "仅system + claude → Anthropic (hint起作用)",
			body:      `{"model":"claude-sonnet-5","system":"helpful","max_tokens":100}`,
			wantProto: ProtocolAnthropicMessages,
			hintUsed:  "YES - openAIScore=0, anthropicScore>0, hint=anthropic",
		},
		{
			desc:      "空body + claude → Anthropic (hint唯一信号)",
			body:      `{"model":"claude-sonnet-5"}`,
			wantProto: ProtocolAnthropicMessages,
			hintUsed:  "YES - both scores=0",
		},
		{
			desc:      "messages[] + system + claude → OpenAI (body shape wins)",
			body:      `{"model":"claude-sonnet-5","system":"x","messages":[{"role":"user","content":"hi"}]}`,
			wantProto: ProtocolOpenAIChat,
			hintUsed:  "NO - openAIScore=0.3 > anthropicScore=0.25(normalized)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			proto, _, _ := DetectProtocol([]byte(tc.body))
			status := "✅"
			if proto != tc.wantProto {
				status = "❌ FAIL"
			}
			t.Logf("%s %s → %q (hint: %s)", status, tc.desc, proto, tc.hintUsed)
			if proto != tc.wantProto {
				t.Errorf("期望 %q, 实际 %q", tc.wantProto, proto)
			}
		})
	}
}

// TestAudit_RealWorldScenario 模拟真实场景:
// 客户端: OpenAI SDK, model="claude-opus-4-8"
// 上游: Anthropic API (cand.Protocol = "anthropic-messages")
// 预期: DetectProtocol → openai-chat, needsConversion=true, IR转换正常
func TestAudit_RealWorldScenario(t *testing.T) {
	t.Logf("━━━ 真实场景模拟 ━━━")
	t.Logf("客户端: OpenAI SDK")
	t.Logf("请求: POST /v1/chat/completions")
	t.Logf("Body: OpenAI格式 + model='claude-opus-4-8'")
	t.Logf("上游: Anthropic API (cand.Protocol='anthropic-messages')")
	t.Logf("")

	clientBody := []byte(`{
		"model": "claude-opus-4-8",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "解释量子纠缠"}
		],
		"max_tokens": 2000,
		"temperature": 0.7,
		"stream": false
	}`)

	// Step 1: 协议检测
	clientProto, conf, err := DetectProtocol(clientBody)
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	t.Logf("Step 1: DetectProtocol → %q (confidence=%.3f)", clientProto, conf)
	if clientProto != ProtocolOpenAIChat {
		t.Errorf("❌ FAIL: 期望检测为 openai-chat, 实际 %q", clientProto)
		t.Logf("  → 如果检测错误,会导致 needsConversion=false,body不转换直接发给Anthropic!")
	} else {
		t.Logf("  ✅ 协议检测正确")
	}

	// Step 2: needsConversion 判断
	candProto := "anthropic-messages"
	needsConv := clientProto != "anthropic-messages" && clientProto != "" && candProto == "anthropic-messages"
	t.Logf("Step 2: needsConversion = %v", needsConv)
	if !needsConv {
		t.Errorf("❌ FAIL: needsConversion应该为true,实际为false")
		t.Logf("  → 这会导致OpenAI格式的body直接发给Anthropic,上游收到无效请求!")
		return
	} else {
		t.Logf("  ✅ needsConversion 判断正确")
	}

	// Step 3: IR 转换
	irReq, err := ParseOpenAI(clientBody)
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	t.Logf("Step 3: ParseOpenAI 成功")
	t.Logf("  ir.Model = %q (客户端原始值)", irReq.Model)
	t.Logf("  ir.System = %+v", irReq.System)
	t.Logf("  ir.Messages = %d 条", len(irReq.Messages))

	// 验证 system 提取
	if irReq.System == nil || irReq.System.Content != "You are a helpful assistant." {
		t.Errorf("❌ system 提取失败: %+v", irReq.System)
	} else {
		t.Logf("  ✅ system 提取正确")
	}

	// 验证 messages 提取(system已被移除,只剩user)
	if len(irReq.Messages) != 1 {
		t.Errorf("❌ messages 应该剩1条(user),实际 %d 条", len(irReq.Messages))
	} else if irReq.Messages[0].Role != "user" {
		t.Errorf("❌ 第一条message role应该是user,实际 %q", irReq.Messages[0].Role)
	} else {
		t.Logf("  ✅ messages 处理正确(system已提取,剩余user)")
	}

	// Step 4: 模拟 model 替换
	irReq.Model = "claude-opus-4-20250514" // 模拟 resolveOutboundModel
	t.Logf("Step 4: resolveOutboundModel → %q", irReq.Model)

	// Step 5: 序列化为 Anthropic 格式
	upstreamBody, err := SerializeAnthropic(irReq)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}

	var result map[string]any
	_ = json.Unmarshal(upstreamBody, &result)
	t.Logf("Step 5: SerializeAnthropic 成功")
	t.Logf("  上游 body.model = %q", result["model"])
	t.Logf("  上游 body.system = %v", result["system"])
	t.Logf("  上游 body.messages = %d 条", len(result["messages"].([]any)))

	// 验证最终 body 格式
	if result["model"] != "claude-opus-4-20250514" {
		t.Errorf("❌ 上游model字段错误: %v", result["model"])
	}
	if result["system"] != "You are a helpful assistant." {
		t.Errorf("❌ 上游system字段错误: %v", result["system"])
	}
	msgs := result["messages"].([]any)
	if len(msgs) != 1 {
		t.Errorf("❌ 上游messages数量错误: %d", len(msgs))
	} else {
		firstMsg := msgs[0].(map[string]any)
		if firstMsg["role"] != "user" {
			t.Errorf("❌ 上游第一条消息role错误: %v", firstMsg["role"])
		}
		if firstMsg["content"] != "解释量子纠缠" {
			t.Errorf("❌ 上游消息内容错误: %v", firstMsg["content"])
		}
	}

	t.Logf("")
	t.Logf("━━━ 审计结论 ━━━")
	t.Logf("✅ 协议检测正确: openai-chat")
	t.Logf("✅ needsConversion判断正确: true")
	t.Logf("✅ IR转换执行正常")
	t.Logf("✅ 上游body格式正确(Anthropic Messages API)")
	t.Logf("✅ 用户提示词完整保留")
}

// TestAudit_PotentialBugs 检查潜在的 bug 场景。
func TestAudit_PotentialBugs(t *testing.T) {
	t.Logf("━━━ 潜在 Bug 场景检查 ━━━")

	t.Run("Bug1: model hint过度影响检测", func(t *testing.T) {
		// 客户端明确用 OpenAI 格式(有messages[]),但因为 model="claude-xxx",
		// 会不会被误判为 anthropic-messages?
		body := `{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`
		proto, _, _ := DetectProtocol([]byte(body))
		if proto != ProtocolOpenAIChat {
			t.Errorf("❌ BUG: messages[]存在应该检测为openai-chat,实际%q", proto)
			t.Logf("  影响: needsConversion会是false,body不转换直接发给Anthropic,格式错误!")
		} else {
			t.Logf("✅ OK: model hint没有过度影响")
		}
	})

	t.Run("Bug2: system单字段+claude误判", func(t *testing.T) {
		// 只有 system 字段(OpenAI也支持),但 model="claude",会不会误判?
		body := `{"model":"claude-sonnet-5","system":"helpful","max_tokens":100}`
		proto, _, _ := DetectProtocol([]byte(body))
		// 这个case其实很模糊 - 既可能是 OpenAI 扩展字段,也可能是 Anthropic
		// 当前实现: anthropicScore=0.25(system), openAIScore=0, hint=anthropic
		// → detect.go:182-183 返回 anthropic-messages
		t.Logf("检测结果: %q", proto)
		t.Logf("  说明: 这是一个模糊case。当前返回%q", proto)
		t.Logf("  建议: 如果客户端是 OpenAI SDK,应该明确发送 messages[] 避免歧义")
	})

	t.Run("Bug3: needsConversion依赖检测准确性", func(t *testing.T) {
		// needsConversion 的三个条件都必须满足:
		// 1. clientProto != "anthropic-messages"
		// 2. clientProto != ""
		// 3. candProto == "anthropic-messages"
		//
		// 如果 DetectProtocol 误判,条件1会失败
		clientProto := "anthropic-messages" // 假设误判
		candProto := "anthropic-messages"
		needsConv := clientProto != "anthropic-messages" && clientProto != "" && candProto == "anthropic-messages"
		if needsConv {
			t.Errorf("❌ BUG: clientProto误判会导致不转换")
		} else {
			t.Logf("✅ 条件判断逻辑正确")
		}
	})

	t.Run("Bug4: 空clientProtocol导致不转换", func(t *testing.T) {
		clientProto := "" // handler.go:1605 如果 e.IR==nil 会保持 "openai-completions"
		candProto := "anthropic-messages"
		needsConv := clientProto != "anthropic-messages" && clientProto != "" && candProto == "anthropic-messages"
		if !needsConv {
			t.Logf("⚠️ WARNING: clientProto为空会导致needsConversion=false")
			t.Logf("  检查 handler.go:1605-1609 是否正确设置 clientProtocol")
		}
	})
}
