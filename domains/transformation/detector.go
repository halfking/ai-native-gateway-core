package transformation

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// IRProtocolDetector 基于 IR 包的协议检测器。
type IRProtocolDetector struct{}

// Detect 返回协议名（openai-chat / anthropic-messages / ""）和置信度。
func (d *IRProtocolDetector) Detect(bodyBytes []byte, headers http.Header) (string, float64) {
	proto, conf, err := ir.DetectProtocol(bodyBytes)
	if err != nil {
		return "", 0
	}
	return proto, conf
}
