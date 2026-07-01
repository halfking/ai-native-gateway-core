// Package transport 实现网络中转领域的统一接口。
//
// 核心理念：IR 和 Legacy 是 Transport 领域的两种实现，对外通过 TransportLayer 接口暴露。
// 外部调用方（如 streaming.Executor）只依赖此接口，不感知 IR 还是 Legacy。
package transformation

import (
	"context"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// TransportLayer 是网络中转领域的统一接口。
//
// 三种方法对应一个请求生命周期：
//   - Convert         请求方向：客户端协议 → 上游协议
//   - ConvertResponse 响应方向：上游协议 → 客户端协议
//   - ConvertStream   流式版本：SSE chunk 实时转换
type TransportLayer interface {
	// Convert 执行协议转换（请求方向）。
	// 输入：envelope.Transport.BodyBytes（客户端原始请求体）
	// 输出：upstreamBody（发给上游 API 的请求体）
	Convert(ctx context.Context, envelope *domain.RequestEnvelope) (upstreamBody []byte, err error)

	// ConvertResponse 执行协议转换（响应方向）。
	// 输入：upstreamBody（上游 API 返回的响应体）
	// 输出：clientBody（返回给客户端的响应体）
	ConvertResponse(ctx context.Context, envelope *domain.RequestEnvelope, upstreamBody []byte) (clientBody []byte, err error)

	// ConvertStream 执行流式转换（SSE）。
	// 输入：upstreamResp（上游 API 的 http.Response）
	// 输出：通过 envelope.Transport.W 写入客户端
	ConvertStream(ctx context.Context, envelope *domain.RequestEnvelope, upstreamResp *http.Response) error

	// Implementation 返回当前实现类型，用于监控/调试。
	// 取值："ir" | "legacy"
	Implementation() string
}

// ProtocolDetector 协议检测器（从 relay/detect.go 提取）。
type ProtocolDetector interface {
	Detect(bodyBytes []byte, headers http.Header) (protocol string, confidence float64)
}

// ExtensionExtractor 扩展属性提取器。
type ExtensionExtractor interface {
	Extract(bodyBytes []byte, headers http.Header) (*domain.ExtensionsBag, error)
}

// ExtensionRestorer 扩展属性还原器。
type ExtensionRestorer interface {
	Restore(bodyBytes []byte, extensions *domain.ExtensionsBag) ([]byte, error)
}
