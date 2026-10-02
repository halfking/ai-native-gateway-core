// Package bg — probe_root_cause.go — 探测失败根因分类（2026-09-25）。
//
// 需求：一次探测失败后，系统必须回答"是协议问题还是节点的问题"，而不是
// 把所有失败一视同仁地压进 5s→6h 重试梯。三类根因：
//
//	node     —— 上游/凭据自身异常（超时、DNS、连接拒绝、401/403 认证、
//	            402/429、5xx、配额/余额形 400）。重试梯有意义：节点可能恢复。
//	protocol —— 探测契约与上游不匹配（404 model/endpoint not found、
//	            405/415 方法与媒体类型、其余配额形以外的 400/422）。
//	            重试永远不可能成功 —— 只有修正 provider 协议/端点/目录配置
//	            才能治愈；按 model-not-served 同款 6h 长回看停放。
//	gateway  —— 网关自身问题（endpoint/request build、解密、gateway 未配置、
//	            pin 不支持）。错误属于本实例或网关能力，不是节点的错；
//	            共享可用性面拒写（isGatewaySideProbeError 的既有语义）。
//
// 分类是纯函数：输入 (errCode, httpStatus, responseBody 前 512 字节)，
// 无 IO、无接收者，便于表驱动单测。响应体仅用于把 400 家族里
// "配额/余额形"（节点问题）与"契约形"（协议问题）分开 —— OpenAI 系上游
// 对欠费返回 400 insufficient_quota，若按状态码一刀切会误判成协议问题。
package bg

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ProbeRootCause is the three-way root cause verdict for a failed probe round.
type ProbeRootCause string

const (
	// ProbeRootCauseNode — the upstream/credential itself is at fault.
	ProbeRootCauseNode ProbeRootCause = "node"
	// ProbeRootCauseProtocol — the probe contract (protocol path, body
	// shape, model catalog entry) does not match the upstream. Retrying the
	// same probe cannot succeed; only a config fix heals it.
	ProbeRootCauseProtocol ProbeRootCause = "protocol"
	// ProbeRootCauseGateway — this gateway instance (or the gateway round's
	// capability) is at fault; the node may be perfectly healthy.
	ProbeRootCauseGateway ProbeRootCause = "gateway"
)

// nodeProbeRootCauseTotal counts failed probe rounds by root cause. Protocol
// share trending up means provider protocol/endpoint/catalog config drift —
// an operator action item, not upstream weather.
var nodeProbeRootCauseTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llmgw_node_probe_root_cause_total",
		Help: "Failed node-probe rounds by root cause (node/protocol/gateway), round and err code.",
	},
	[]string{"round", "cause", "err_code"},
)

// recordProbeRootCause increments the root-cause counter for one failed round.
func recordProbeRootCause(round string, r nodeProbeRoundResult) {
	if r.ok {
		return
	}
	cause := r.rootCause
	if cause == "" {
		cause = ProbeRootCauseNode
	}
	nodeProbeRootCauseTotal.WithLabelValues(round, string(cause), r.errCode).Inc()
}

// nodeShapedQuotaBodyKeywords match the "this credential is out of money"
// family of 4xx bodies. A 400 carrying one of these is a NODE (billing)
// problem wearing an HTTP-400 costume, not a protocol mismatch. Both the
// OpenAI ("insufficient_quota") and aggregate-vendor ("余额不足"/"已欠费")
// spellings are covered; matching is lowercase substring.
var nodeShapedQuotaBodyKeywords = []string{
	"insufficient_quota", "insufficient balance", "quota exceeded",
	"exceeded your current quota", "billing", "arrears", "unpaid",
	"balance is not enough", "not enough balance", "余额不足", "欠费",
	"额度不足", "余额已不足", "账户余额",
}

// nodeShapedQuotaBody reports whether a 4xx response body carries the
// billing/quota verdict that reclassifies a contract-shaped status back to
// a node (credential) problem.
func nodeShapedQuotaBody(body string) bool {
	if body == "" {
		return false
	}
	haystack := strings.ToLower(body)
	for _, kw := range nodeShapedQuotaBodyKeywords {
		if strings.Contains(haystack, kw) {
			return true
		}
	}
	return false
}

// classifyProbeRootCause maps one failed round onto the node/protocol/gateway
// taxonomy. Classification order:
//
//  1. gateway build/capability codes (incl. the probe_-prefixed defensive
//     forms updateBindingAvailability persists) → gateway;
//  2. transport failures (timeout/dns/connection/network) → node — the
//     request never got a contract-level answer;
//  3. HTTP status: auth (401/403), payment (402), rate (429), 408, 5xx →
//     node; 404/405/410/415 → protocol (wrong path/method/capability — the
//     endpoint exists but not for this probe's contract); 400/422 →
//     protocol unless the body carries the quota/balance verdict;
//  4. anything else (unknown codes, empty_response, …) → node — the
//     conservative default keeps the generic ladder, which is the correct
//     treatment for a fault we cannot attribute.
//
// ok rounds are not classified (empty cause).
func classifyProbeRootCause(errCode string, httpStatus int, responseBody string) ProbeRootCause {
	switch errCode {
	case "endpoint_build", "request_build",
		"probe_endpoint_build", "probe_request_build",
		"gateway_not_configured", "gateway_pin_unsupported", "gateway_probe_failed":
		return ProbeRootCauseGateway
	case "timeout", "dns_error", "connection_error", "network_error", "stream_timeout":
		return ProbeRootCauseNode
	}
	if httpStatus >= 500 {
		return ProbeRootCauseNode
	}
	switch httpStatus {
	case 401, 402, 403, 408, 429:
		return ProbeRootCauseNode
	case 404, 405, 410, 415:
		// Path/method/capability mismatch. A 404 here is the same verdict
		// isModelNotServedProbeError keys on (model/endpoint not served);
		// the protocol label makes the *reasoning* explicit and extends the
		// same treatment to 405/410/415.
		return ProbeRootCauseProtocol
	case 400, 422:
		if nodeShapedQuotaBody(responseBody) {
			return ProbeRootCauseNode // 欠费形的 400/422：节点（计费）问题
		}
		return ProbeRootCauseProtocol
	}
	if httpStatus >= 400 {
		return ProbeRootCauseNode
	}
	if httpStatus != 0 {
		// 1xx/2xx/3xx with ok=false: an empty/undecodable response. Not a
		// contract rejection the upstream admitted to — treat as node.
		return ProbeRootCauseNode
	}
	// No status and no known code: empty_response and friends.
	if errCode == "" || errCode == "none" {
		return ""
	}
	return ProbeRootCauseNode
}

// classifyGatewayRoundRootCause classifies a failed GATEWAY round. The
// gateway round dials this gateway's own loopback endpoint, so a
// connection-level transport failure (dial refused / DNS on the loopback
// host) means the gateway itself was unreachable — a GATEWAY fault, not the
// node's (2026-09-26 data: 36h 内 gateway 轮 transport 失败 126/126 均为
// dial tcp 127.0.0.1:878x，集中在部署/重启窗). timeout/stream_timeout stay
// on the node classification: a loopback timeout usually means the request
// waited on the full gateway chain including the upstream leg, so the node
// remains the conservative default.
func classifyGatewayRoundRootCause(errCode string, httpStatus int, responseBody string) ProbeRootCause {
	switch errCode {
	case "network_error", "connection_error", "dns_error":
		return ProbeRootCauseGateway
	}
	return classifyProbeRootCause(errCode, httpStatus, responseBody)
}

// logProbeRoundRootCause emits the operator-facing "协议还是节点" verdict for
// one failed round. Counting does NOT happen here — emitProbe is the single
// counter choke point per round — so callers may log a round once while the
// counter still fires exactly once.
func logProbeRoundRootCause(round string, r nodeProbeRoundResult) {
	if r.ok {
		return
	}
	cause := r.rootCause
	if cause == "" {
		cause = ProbeRootCauseNode
	}
	switch cause {
	case ProbeRootCauseProtocol:
		slog.Warn("node probe failed: protocol mismatch, NOT a node fault — "+
			"re-probing cannot heal it; fix the provider protocol/endpoint/model-catalog config (recheck parked at 6h)",
			"round", round, "err_code", r.errCode, "http_status", r.httpStatus, "detail", r.errDetail)
	case ProbeRootCauseGateway:
		slog.Warn("node probe failed: gateway-side fault, NOT a node fault — "+
			"shared availability surfaces stay untouched; fix this instance's config/capability",
			"round", round, "err_code", r.errCode, "http_status", r.httpStatus, "detail", r.errDetail)
	default:
		slog.Warn("node probe failed: node/upstream fault (ladder applies)",
			"round", round, "err_code", r.errCode, "http_status", r.httpStatus, "detail", r.errDetail)
	}
}

// annotateRootCause appends the machine-readable root cause to the round's
// err detail so dashboards and node_probe_runs readers can answer
// "协议还是节点" without re-deriving it from the status code. The detail
// stays a plain string on purpose (no schema change): suffix matching
// "(root_cause=protocol)" is queryable, and contains-checks on the detail
// (isDecryptShapedProbeDetail's "decrypt: ", the missing-binding
// "no rows in result set") are prefix matches and unaffected.
func annotateRootCause(r *nodeProbeRoundResult) {
	if r == nil || r.ok || r.rootCause == "" {
		return
	}
	tag := fmt.Sprintf("(root_cause=%s)", r.rootCause)
	if strings.Contains(r.errDetail, tag) {
		return
	}
	r.errDetail = strings.TrimSpace(r.errDetail + " " + tag)
}
