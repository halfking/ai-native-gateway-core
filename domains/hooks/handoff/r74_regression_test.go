package handoff

import (
	"strings"
	"testing"
)

// R74 P1 回归：resume 摘要必须覆盖非 OpenAI-chat 的请求形态。
//
// 旧实现把 messages[].content 反序列化成 string，遇到 Anthropic 的 block
// 数组、Responses 的 input 数组、OpenAI 的数组 content 时 Unmarshal 失败
// 直接返回 ""，于是 resume_packet.summary 退化成一句纯计数话术——新会话
// 开局即失忆，而没有任何错误或指标。
//
// 判别力：把 extractConversation 退回旧的「只认字符串 content」实现，
// 本测试必须红。
func TestR74_ExtractConversationCoversAllProtocolShapes(t *testing.T) {
	const secretish = "SECRET_DECISION_MIGRATE_BILLING"

	cases := []struct {
		name     string
		protocol string
		body     string
		want     string
	}{
		{
			name:     "openai_chat_string_content",
			protocol: "openai-chat",
			body:     `{"messages":[{"role":"user","content":"` + secretish + `"}]}`,
			want:     secretish,
		},
		{
			name:     "anthropic_block_content",
			protocol: "anthropic-messages",
			body: `{"system":"You are Claude.","messages":[
				{"role":"user","content":[{"type":"text","text":"` + secretish + `"}]}]}`,
			want: secretish,
		},
		{
			name:     "openai_array_content",
			protocol: "openai-chat",
			body: `{"messages":[{"role":"user","content":[
				{"type":"text","text":"` + secretish + `"}]}]}`,
			want: secretish,
		},
		{
			name:     "responses_input",
			protocol: "openai-responses",
			body: `{"input":[{"role":"user","content":[
				{"type":"input_text","text":"` + secretish + `"}]}]}`,
			want: secretish,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractConversation([]byte(tc.body), tc.protocol)
			if !strings.Contains(got, tc.want) {
				t.Errorf("协议 %s 的对话内容丢失：期望包含 %q，实际 = %q", tc.protocol, tc.want, got)
			}
		})
	}
}

// 脱敏仍必须生效：摘要路径不得把凭据形态的串原样带给新会话。
func TestR74_ExtractConversationStillRedactsCredentials(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"my key is sk-abcdefghijklmnopqrstuvwxyz012345"}]}`
	got := extractConversation([]byte(body), "openai-chat")
	if strings.Contains(got, "sk-abcdefghijklmnopqrstuvwxyz012345") {
		t.Errorf("凭据原样进入 resume 摘要：%q", got)
	}
}
