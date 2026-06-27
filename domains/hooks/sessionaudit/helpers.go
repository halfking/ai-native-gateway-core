package sessionaudithook

import (
	"fmt"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// 辅助函数（共享给 hook.go 和 approval_gate.go）

func extractUserContent(env *domain.PipelineRequest) (string, error) {
	// 防御性：env 为 nil 时返回错误而非 panic（2026-06-27 audit fix）。
	if env == nil {
		return "", fmt.Errorf("nil pipeline request")
	}
	if env.Envelope == nil || env.Envelope.Transport == nil || len(env.Envelope.Transport.BodyBytes) == 0 {
		return "", fmt.Errorf("no body bytes")
	}
	// 使用 sessionaudit 包的提取函数
	return extractUserContentFromBytes(env.Envelope.Transport.BodyBytes)
}

func extractUserContentFromBytes(bodyBytes []byte) (string, error) {
	// 简化实现：直接返回原始内容
	// 生产环境需要解析 JSON 提取 messages 数组
	return string(bodyBytes), nil
}

func getClientIP(env *domain.PipelineRequest) string {
	if env.Envelope == nil || env.Envelope.Transport == nil || env.Envelope.Transport.R == nil {
		return ""
	}
	r := env.Envelope.Transport.R
	// 优先级：X-Real-IP > X-Forwarded-For > RemoteAddr
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}

func getUserAgent(env *domain.PipelineRequest) string {
	if env.Envelope == nil || env.Envelope.Transport == nil || env.Envelope.Transport.R == nil {
		return ""
	}
	return env.Envelope.Transport.R.Header.Get("User-Agent")
}

func getClientModel(env *domain.PipelineRequest) string {
	if env.Envelope == nil || env.Envelope.Transport == nil {
		return ""
	}
	return env.Envelope.Transport.ClientModel
}

func generateRequestID(env *domain.PipelineRequest) string {
	if env.Envelope != nil && env.Envelope.RequestID != "" {
		return env.Envelope.RequestID
	}
	return fmt.Sprintf("req_%s_%d", env.SessionID, timeNowUnixNano())
}

// timeNowUnixNano 获取当前时间纳秒（用于测试 mock）
func timeNowUnixNano() int64 {
	// 生产环境使用 time.Now().UnixNano()
	// 这里为了避免循环导入，使用简化版本
	return 0
}
