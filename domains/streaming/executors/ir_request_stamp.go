package executors

// ir_request_stamp.go —— IR 请求侧 requestID 载体 stamp（R61 收口，S2-F3 续）。
//
// 背景（R60 S2-F3 只接了 anthropic 面）：ir 层 format-anomaly 上报
// （serialize/parse 各 report* 经 irRequestID 读取 req.Metadata.RequestID）
// 需要 IR 载体带网关 request id，否则日志打 request_id=unknown、无法与
// request_logs/泳道对账。R61 把 stamp 扩展到全部有 *InternalRequest 载体的
// 序列化面（openai 主/兜底/断路器路径；anthropic 出站）。
//
// 线上语义见 ir.Metadata 注释：各序列化器只透出 user_id，RequestID 永不
// 进上游请求体。

import (
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// stampIRRequestID 把网关 request id 写入 IR Metadata 载体。requestID 为空
// 或载体为 nil 时原样返回（parse 失败等场景无载体可写）。
func stampIRRequestID(irReq *ir.InternalRequest, requestID string) *ir.InternalRequest {
	if irReq == nil || requestID == "" {
		return irReq
	}
	if irReq.Metadata == nil {
		irReq.Metadata = &ir.Metadata{}
	}
	irReq.Metadata.RequestID = requestID
	return irReq
}
