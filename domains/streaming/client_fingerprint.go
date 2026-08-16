package streaming

import (
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/clienttype"
	"github.com/kaixuan/llm-gateway-go/telemetry"
)

// client_fingerprint.go — IDE 客户端指纹提取（需求 #1 的一部分）
//
// extractClientType 从 HTTP 请求头提取 IDE/client 类型指纹。
//
// 优先级：
//  1. X-Gw-Client-Type（网关自定义头，明确标识）
//  2. User-Agent（包含 IDE/Agent 特征字符串）
//  3. 会话首条系统提示词语义匹配（智能体自报身份，如 "You are Claude Code…"）
//  4. X-Stainless-Lang（OpenAI SDK 语言指纹，辅助判断）
//
// 返回值统一小写化，便于后续 switch case 匹配。
// 空字符串表示未识别或非 IDE 客户端。
func extractClientType(r *http.Request) string {
	// 1. 明确的客户端类型头（优先级最高）
	if ct := r.Header.Get("X-Gw-Client-Type"); ct != "" {
		return clienttype.Normalize(ct)
	}

	// 2. User-Agent 解析（IDE/Agent 特征字符串）
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	switch {
	case strings.Contains(ua, "cursor/"), strings.Contains(ua, "cursor-"):
		return "cursor"
	case strings.Contains(ua, "claude-code/"), strings.Contains(ua, "claude-code-"):
		return "claude-code"
	case strings.Contains(ua, "opencode/"), strings.Contains(ua, "opencode-"):
		return "opencode"
	case strings.Contains(ua, "zcode/"), strings.Contains(ua, "zcode-"):
		return "zcode"
	case strings.Contains(ua, "codex/"), strings.Contains(ua, "codex-"):
		return "codex"
	case strings.Contains(ua, "roocode/"), strings.Contains(ua, "roo-code/"):
		return "roocode"
	case strings.Contains(ua, "vscode/"), strings.Contains(ua, "visual-studio-code/"):
		return "vscode"
	case strings.Contains(ua, "github-copilot/"), strings.Contains(ua, "copilot/"):
		return "copilot"
	case strings.Contains(ua, "windsurf/"):
		return "windsurf"
	case strings.Contains(ua, "zed/"):
		return "zed"
	case strings.Contains(ua, "jetbrains/"), strings.Contains(ua, "intellij/"),
		strings.Contains(ua, "pycharm/"), strings.Contains(ua, "webstorm/"):
		return "jetbrains"
	}

	// 3. SDK 指纹辅助判断（Stainless 生成的 SDK，语言特征间接暗示场景）
	// 目前仅记录，不直接返回 clientType，因为语言不等同于 IDE
	// 保留此逻辑供未来扩展（如 X-Stainless-Lang=python + 其他信号 → vscode）
	_ = r.Header.Get("X-Stainless-Lang")

	// 未识别
	return ""
}

// extractClientTypeWithPrompt 是 extractClientType 的增强版：先尝试
// HTTP 头检测，若未命中则通过系统提示词语义匹配补全客户端类型。
//
// 智能体通常在第一次会话的系统消息中自我介绍（如 "You are Claude Code
// by Anthropic"、"You are ZCode, an AI assistant"），语义检测可识别
// 这些自报身份。
//
// 优先级：
//  1. X-Gw-Client-Type 头（显式指定）
//  2. User-Agent 匹配
//  3. 系统提示词语义匹配
func extractClientTypeWithPrompt(r *http.Request, systemPrompt string) string {
	if ct := extractClientType(r); ct != "" {
		return ct
	}
	return telemetry.DetectAgentFromSystemPrompt(systemPrompt)
}

// ClientTokenOf 根据 userKey 与 clientType 拼接客户端 token。
//
// 设计原则（2026-07-27，Token 资源管理方案）：
//   - userKey 为空时统一为 "anon" — 兜底避免空 holder 进入 Redis
//   - clientType 为空时统一为 "unknown" — 隐式枚举，未识别客户端类型
//     不会进入精细配额表，但会被汇总到"未知类型"统计
//   - 拼接使用 ASCII "|" 分隔符，userKey/clientType 本身已规范化（extractClientType
//     输出小写 + 不含特殊字符；userKey 来源 upstream auth，已过滤）
//
// 这是拼接规则的规范实现（streaming 包内的调用方应使用它）。executors 包
// 出于避免跨包依赖的考虑内联了一份等价副本 clientTokenOf（见
// domains/streaming/executors/executor.go），FpSlot holder 实际由那份副本
// 生成；两处逻辑必须保持一致，修改任一处需同步另一处。
func ClientTokenOf(userKey, clientTypeValue string) string {
	if userKey == "" {
		userKey = "anon"
	}
	return userKey + "|" + clienttype.Normalize(clientTypeValue)
}
