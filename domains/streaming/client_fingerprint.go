package streaming

import (
	"net/http"
	"strings"
)

// client_fingerprint.go — IDE 客户端指纹提取（需求 #1 的一部分）
//
// extractClientType 从 HTTP 请求头提取 IDE/client 类型指纹。
//
// 优先级：
//  1. X-Gw-Client-Type（网关自定义头，明确标识）
//  2. User-Agent（包含 IDE 特征字符串）
//  3. X-Stainless-Lang（OpenAI SDK 语言指纹，辅助判断）
//
// 返回值统一小写化，便于后续 switch case 匹配。
// 空字符串表示未识别或非 IDE 客户端。
func extractClientType(r *http.Request) string {
	// 1. 明确的客户端类型头（优先级最高）
	if ct := r.Header.Get("X-Gw-Client-Type"); ct != "" {
		return strings.ToLower(ct)
	}

	// 2. User-Agent 解析（IDE 特征字符串）
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	switch {
	case strings.Contains(ua, "cursor/"), strings.Contains(ua, "cursor-"):
		return "cursor"
	case strings.Contains(ua, "claude-code/"), strings.Contains(ua, "claude-code-"):
		return "claude-code"
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
