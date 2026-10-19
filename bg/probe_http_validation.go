package bg

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/internal/modelresponse"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

// probeResponseInconclusiveErrCode 是「拿到了响应但拿不出完整正向证据」的
// 唯一 errCode。
//
// 2026-10-20 12h 审计轮修复：executor 之前用 `category == probeCategorySkipped`
// 判定「观察」而被本文件之外的地方复用了同一个 category ——
// endpoint_id_required（确定性 404：模型必须配 endpoint id）也被算成
// 「不确定的观察」⇒ 观察分支不写 state manager ⇒ 旧行为里的 5 分钟摘流
// 被取消、该 pair 的重试链被终止、error_kind 被误报成 probe_direct_inconclusive。
// ⇒ 「观察」必须按 **errCode** 判定，不能按 category 判定。
const probeResponseInconclusiveErrCode = "probe_response_inconclusive"

func probeResponseProtocol(desc providercap.Descriptor) string {
	switch desc.ChatProbeEndpoint {
	case upstreamurl.EpMessages:
		return "messages"
	case upstreamurl.EpResponses:
		return "responses"
	default:
		return "chat"
	}
}

func probeModelsHaveEvidence(body []byte) bool {
	if !json.Valid(body) {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) == nil {
		if e := envelope["error"]; len(e) > 0 && string(e) != "null" {
			return false
		}
	}
	ids, err := modelresponse.ParseModelIDs(body)
	return err == nil && len(ids) > 0
}

// Native protocol support and successful generation are different evidence.
// A complete Responses token-budget terminal proves the former even if all
// of its output budget went into reasoning and no assistant answer exists.
func responsesProbeProtocolEvidence(body []byte) bool {
	if probeResponseHasOutput("responses", body) {
		return true
	}
	var response struct {
		ID                string          `json:"id"`
		Object            string          `json:"object"`
		Status            string          `json:"status"`
		Error             json.RawMessage `json:"error"`
		Output            json.RawMessage `json:"output"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	if json.Unmarshal(body, &response) != nil || response.ID == "" || response.Object != "response" ||
		response.Status != "incomplete" || response.IncompleteDetails.Reason != "max_output_tokens" ||
		(len(response.Error) > 0 && string(bytes.TrimSpace(response.Error)) != "null") {
		return false
	}
	var output []json.RawMessage
	return json.Unmarshal(response.Output, &output) == nil && output != nil
}

// HTTP status remains authoritative on failures. A successful HTTP exchange
// needs complete protocol evidence before it can recover routing state.
func classifyProbeHTTPResponse(status int, body []byte, readErr error, latencyMs int, protocol string) httpProbeResult {
	if readErr != nil {
		body = []byte("response body unavailable: " + readErr.Error())
	}
	result := classifyHTTPResponse(status, string(body), latencyMs)
	if status < 200 || status >= 300 {
		return result
	}
	valid := readErr == nil
	if protocol == "models" {
		valid = valid && probeModelsHaveEvidence(body)
	} else {
		valid = valid && probeResponseHasOutput(protocol, body)
	}
	if valid {
		return result
	}
	return inconclusiveHTTPProbe(status, latencyMs, protocol)
}

func inconclusiveHTTPProbe(status, latencyMs int, protocol string) httpProbeResult {
	return httpProbeResult{
		status: "inconclusive", category: probeCategorySkipped,
		httpStatus: status, latencyMs: latencyMs,
		errCode: probeResponseInconclusiveErrCode,
		errMsg:  fmt.Sprintf("%s probe returned no complete assistant/model evidence; routing state retained", protocol),
	}
}
