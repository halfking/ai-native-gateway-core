package transformation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

// IRTransport 使用 IR（中间表示）实现协议转换。
type IRTransport struct {
	detector         ProtocolDetector
	extractor        ExtensionExtractor
	restorer         ExtensionRestorer
	cb               *StreamCircuitBreaker // 流式降级熔断器
	rawLogger        *logging.RawDataLogger
	anomalyReporter  *logging.LockFreeAnomalyReporter
	semanticAnalyzer *ir.SemanticAnalyzer
}

// NewIRTransport 构造默认配置的 IRTransport。
// 自动检测环境变量，按需启用诊断功能。
func NewIRTransport() *IRTransport {
	var rawLogger *logging.RawDataLogger
	var anomalyReporter *logging.LockFreeAnomalyReporter
	var semanticAnalyzer *ir.SemanticAnalyzer

	// 原始数据日志（opt-in）
	rawEnabled := os.Getenv("LLM_GATEWAY_RAW_LOG_ENABLED") == "true"
	if rawEnabled {
		logDir := os.Getenv("LLM_GATEWAY_RAW_LOG_DIR")
		if logDir == "" {
			logDir = "./logs/raw_data"
		}
		maxSizeStr := os.Getenv("LLM_GATEWAY_RAW_LOG_MAX_SIZE")
		maxSize := int64(100 * 1024 * 1024) // 100MB default
		if maxSizeStr != "" {
			if parsed, err := strconv.ParseInt(maxSizeStr, 10, 64); err == nil {
				maxSize = parsed
			}
		}

		logger, err := logging.NewRawDataLogger(logDir, maxSize, true)
		if err != nil {
			slog.Error("raw_data_logger: failed to initialize", "err", err)
		} else {
			rawLogger = logger
			slog.Info("raw_data_logger: initialized", "dir", logDir, "max_size", maxSize)
		}
	}

	// 异常报告器（opt-in）
	anomalyEnabled := os.Getenv("LLM_GATEWAY_ANOMALY_REPORTER_ENABLED") == "true"
	if anomalyEnabled {
		endpoint := os.Getenv("LLM_GATEWAY_ANOMALY_ENDPOINT")
		if endpoint == "" {
			endpoint = "https://llmgo.kxpms.cn/format-anomalies"
		}

		// 2026-07-28: switch from the legacy mutex-based
		// AnomalyReporter (deleted in this commit) to the lock-free
		// implementation. Endpoint default aligned with
		// cmd/gateway/main.go so the two paths converge on the same
		// receiver.
		var locator func() (string, int64)
		if rawLogger != nil {
			locator = rawLogger.CurrentLocation
		}
		reporter := logging.NewLockFreeAnomalyReporterWithRawLogLocator(endpoint, true, 1000, locator)
		anomalyReporter = reporter
		slog.Info("anomaly_reporter: initialized", "endpoint", endpoint)
	}

	// 语义分析器（opt-in）
	semanticEnabled := os.Getenv("LLM_GATEWAY_SEMANTIC_ANALYSIS_ENABLED") == "true"
	if semanticEnabled {
		semanticAnalyzer = ir.NewSemanticAnalyzer(true)
		slog.Info("semantic_analyzer: initialized")
	}

	return NewIRTransportWithLoggers(rawLogger, anomalyReporter, semanticAnalyzer)
}

// NewIRTransportWithLoggers 构造带日志和分析器的 IRTransport。
func NewIRTransportWithLoggers(
	rawLogger *logging.RawDataLogger,
	anomalyReporter *logging.LockFreeAnomalyReporter,
	semanticAnalyzer *ir.SemanticAnalyzer,
) *IRTransport {
	return &IRTransport{
		detector:         &IRProtocolDetector{},
		extractor:        &IRExtensionExtractor{},
		restorer:         &IRExtensionRestorer{},
		cb:               NewStreamCircuitBreaker(),
		rawLogger:        rawLogger,
		anomalyReporter:  anomalyReporter,
		semanticAnalyzer: semanticAnalyzer,
	}
}

// SetCircuitBreaker 替换流式熔断器（测试/注入用）。
func (t *IRTransport) SetCircuitBreaker(cb *StreamCircuitBreaker) {
	if cb != nil {
		t.cb = cb
	}
}

// CircuitBreaker 返回流式熔断器（监控用）。
func (t *IRTransport) CircuitBreaker() *StreamCircuitBreaker { return t.cb }

// Implementation 返回 "ir"。
func (t *IRTransport) Implementation() string { return "ir" }

// Convert 实现 4 象限协议转换（请求方向）。
func (t *IRTransport) Convert(ctx context.Context, envelope *domain.RequestEnvelope) ([]byte, error) {
	if envelope == nil || envelope.Transport == nil {
		return nil, errors.New("ir_transport: nil envelope/transport")
	}
	tc := envelope.Transport
	requestID := envelope.RequestID
	startTime := time.Now()

	// 1. 检测客户端协议（如果未设置）
	if tc.ClientProtocol == "" {
		proto, confidence := t.detector.Detect(tc.BodyBytes, tc.R.Header)
		tc.ClientProtocol = proto
		slog.DebugContext(ctx, "ir_transport: detected client protocol",
			"request_id", requestID,
			"protocol", proto,
			"confidence", confidence)
	}

	// 2. 记录原始客户端请求（转换前）
	if t.rawLogger != nil {
		headers := extractHeaders(tc.R.Header)
		t.rawLogger.LogClientRequest(requestID, tc.ClientProtocol, tc.BodyBytes, headers, "pre_parse")
	}

	// 3. 提取扩展属性到 TransportContext.Extensions
	if tc.Extensions.IsZero() && t.extractor != nil {
		ext, err := t.extractor.Extract(tc.BodyBytes, tc.R.Header)
		if err != nil {
			slog.WarnContext(ctx, "ir_transport: extract extensions failed",
				"request_id", requestID,
				"err", err)
		} else if ext != nil {
			tc.Extensions = *ext
		}
	}

	// 4. Parse: ClientProtocol → IR
	slog.DebugContext(ctx, "ir_transport: parsing request",
		"request_id", requestID,
		"from_protocol", tc.ClientProtocol,
		"body_size", len(tc.BodyBytes))

	internalReq, err := parseRequest(tc.ClientProtocol, tc.BodyBytes)
	if err != nil {
		// 记录解析错误
		slog.ErrorContext(ctx, "ir_transport: parse request failed",
			"request_id", requestID,
			"protocol", tc.ClientProtocol,
			"body_size", len(tc.BodyBytes),
			"err", err)

		// 记录原始数据到日志
		if t.rawLogger != nil {
			t.rawLogger.LogConversionError(requestID, tc.ClientProtocol, "client_request", "parse", tc.BodyBytes, err)
		}

		// 报告异常
		if t.anomalyReporter != nil {
			t.anomalyReporter.ReportConversionError(ctx, requestID, tc.ClientProtocol, tc.UpstreamProtocol, "parse_request", tc.BodyBytes, err)
		}

		return nil, fmt.Errorf("ir_transport: parse %s: %w", tc.ClientProtocol, err)
	}

	parseElapsed := time.Since(startTime)
	slog.InfoContext(ctx, "ir_transport: parsed request successfully",
		"request_id", requestID,
		"protocol", tc.ClientProtocol,
		"message_count", len(internalReq.Messages),
		"tool_count", len(internalReq.Tools),
		"has_system", internalReq.System != nil,
		"stream", internalReq.Stream,
		"parse_duration_ms", parseElapsed.Milliseconds())

	// 5. Serialize: IR → UpstreamProtocol
	slog.DebugContext(ctx, "ir_transport: serializing request",
		"request_id", requestID,
		"to_protocol", tc.UpstreamProtocol)

	serializeStart := time.Now()
	upstreamBody, err := serializeRequest(tc.UpstreamProtocol, internalReq)
	if err != nil {
		// 记录序列化错误
		slog.ErrorContext(ctx, "ir_transport: serialize request failed",
			"request_id", requestID,
			"protocol", tc.UpstreamProtocol,
			"err", err)

		// 记录原始数据到日志（IR状态）
		if t.rawLogger != nil {
			t.rawLogger.LogConversionError(requestID, tc.ClientProtocol, "upstream_request", "serialize", tc.BodyBytes, err)
		}

		// 报告异常
		if t.anomalyReporter != nil {
			t.anomalyReporter.ReportConversionError(ctx, requestID, tc.ClientProtocol, tc.UpstreamProtocol, "serialize_request", tc.BodyBytes, err)
		}

		return nil, fmt.Errorf("ir_transport: serialize %s: %w", tc.UpstreamProtocol, err)
	}

	serializeElapsed := time.Since(serializeStart)
	slog.InfoContext(ctx, "ir_transport: serialized request successfully",
		"request_id", requestID,
		"protocol", tc.UpstreamProtocol,
		"output_size", len(upstreamBody),
		"serialize_duration_ms", serializeElapsed.Milliseconds(),
		"total_duration_ms", time.Since(startTime).Milliseconds())

	// 6. 记录上游请求（转换后）
	if t.rawLogger != nil {
		t.rawLogger.LogUpstreamRequest(requestID, tc.UpstreamProtocol, upstreamBody, "post_serialize")
	}

	conversionTotal.WithLabelValues("ir", "request").Inc()
	return upstreamBody, nil
}

// ConvertResponse 实现 4 象限协议转换（响应方向）。
func (t *IRTransport) ConvertResponse(ctx context.Context, envelope *domain.RequestEnvelope, upstreamBody []byte) ([]byte, error) {
	if envelope == nil || envelope.Transport == nil {
		return nil, errors.New("ir_transport: nil envelope/transport")
	}
	tc := envelope.Transport
	requestID := envelope.RequestID
	startTime := time.Now()

	// 1. 记录原始上游响应（转换前）
	if t.rawLogger != nil {
		t.rawLogger.LogUpstreamResponse(requestID, tc.UpstreamProtocol, upstreamBody, "pre_parse")
	}

	slog.DebugContext(ctx, "ir_transport: parsing response",
		"request_id", requestID,
		"from_protocol", tc.UpstreamProtocol,
		"body_size", len(upstreamBody))

	// 2. Parse: UpstreamProtocol → IR
	internalResp, err := parseResponse(tc.UpstreamProtocol, upstreamBody)
	if err != nil {
		// 记录解析错误
		slog.ErrorContext(ctx, "ir_transport: parse response failed",
			"request_id", requestID,
			"protocol", tc.UpstreamProtocol,
			"body_size", len(upstreamBody),
			"err", err)

		// 记录原始数据到日志
		if t.rawLogger != nil {
			t.rawLogger.LogConversionError(requestID, tc.UpstreamProtocol, "upstream_response", "parse", upstreamBody, err)
		}

		// 报告异常
		if t.anomalyReporter != nil {
			t.anomalyReporter.ReportConversionError(ctx, requestID, tc.UpstreamProtocol, tc.ClientProtocol, "parse_response", upstreamBody, err)
		}

		return nil, fmt.Errorf("ir_transport: parse response %s: %w", tc.UpstreamProtocol, err)
	}

	parseElapsed := time.Since(startTime)
	hasToolCalls := len(internalResp.ToolCalls) > 0

	slog.InfoContext(ctx, "ir_transport: parsed response successfully",
		"request_id", requestID,
		"protocol", tc.UpstreamProtocol,
		"content_blocks", len(internalResp.Content),
		"tool_calls", len(internalResp.ToolCalls),
		"finish_reason", internalResp.FinishReason,
		"parse_duration_ms", parseElapsed.Milliseconds())

	// 3. 语义分析：检测潜在的工具调用丢失
	if t.semanticAnalyzer != nil && !hasToolCalls {
		analysis := t.semanticAnalyzer.AnalyzeResponse(internalResp)
		if analysis.IsIncomplete {
			slog.WarnContext(ctx, "ir_transport: semantic analysis detected incomplete response",
				"request_id", requestID,
				"reason", analysis.Reason,
				"confidence", analysis.Confidence,
				"suspected_missing_tools", analysis.SuspectedMissingTools,
				"indicators", analysis.Indicators)

			// 对比原始日志，查找是否有工具调用丢失
			if analysis.SuspectedMissingTools {
				hasLoss, missingData := ir.CompareWithRawLog(upstreamBody, internalResp)
				if hasLoss {
					slog.ErrorContext(ctx, "ir_transport: TOOL_CALLS_LOST during conversion",
						"request_id", requestID,
						"missing_tool_calls", missingData)

					// 报告严重异常
					if t.anomalyReporter != nil {
						t.anomalyReporter.ReportToolCallsMissing(
							ctx, requestID,
							tc.UpstreamProtocol, tc.ClientProtocol,
							upstreamBody, nil, // clientBody will be populated below
							missingData,
							analysis.Confidence,
						)
					}
				}
			}

			// 报告语义不完整
			if t.anomalyReporter != nil {
				t.anomalyReporter.ReportSemanticIncomplete(
					ctx, requestID,
					tc.ClientProtocol,
					upstreamBody, // 暂时用上游数据，下面会更新为客户端数据
					analysis.Reason,
					analysis.Indicators,
					analysis.Confidence,
				)
			}
		}
	}

	// 4. Serialize: IR → ClientProtocol
	slog.DebugContext(ctx, "ir_transport: serializing response",
		"request_id", requestID,
		"to_protocol", tc.ClientProtocol)

	serializeStart := time.Now()
	clientBody, err := serializeResponse(tc.ClientProtocol, internalResp, tc.ClientModel)
	if err != nil {
		// 记录序列化错误
		slog.ErrorContext(ctx, "ir_transport: serialize response failed",
			"request_id", requestID,
			"protocol", tc.ClientProtocol,
			"err", err)

		// 记录原始数据到日志
		if t.rawLogger != nil {
			t.rawLogger.LogConversionError(requestID, tc.ClientProtocol, "client_response", "serialize", upstreamBody, err)
		}

		// 报告异常
		if t.anomalyReporter != nil {
			t.anomalyReporter.ReportConversionError(ctx, requestID, tc.UpstreamProtocol, tc.ClientProtocol, "serialize_response", upstreamBody, err)
		}

		return nil, fmt.Errorf("ir_transport: serialize response %s: %w", tc.ClientProtocol, err)
	}

	serializeElapsed := time.Since(serializeStart)
	slog.InfoContext(ctx, "ir_transport: serialized response successfully",
		"request_id", requestID,
		"protocol", tc.ClientProtocol,
		"output_size", len(clientBody),
		"serialize_duration_ms", serializeElapsed.Milliseconds(),
		"total_duration_ms", time.Since(startTime).Milliseconds())

	// 5. 还原扩展属性
	if !tc.Extensions.IsZero() && t.restorer != nil {
		restored, err := t.restorer.Restore(clientBody, &tc.Extensions)
		if err != nil {
			slog.WarnContext(ctx, "ir_transport: restore extensions failed",
				"request_id", requestID,
				"err", err)
		} else if restored != nil {
			clientBody = restored
		}
	}

	// 6. 记录客户端响应（转换后）
	if t.rawLogger != nil {
		t.rawLogger.LogClientResponse(requestID, tc.ClientProtocol, clientBody, "post_serialize")
	}

	conversionTotal.WithLabelValues("ir", "response").Inc()
	return clientBody, nil
}

// ErrStreamCircuitOpen 报告 IR 流式路径已被熔断，应降级到 Legacy。
var ErrStreamCircuitOpen = errors.New("ir_transport: stream circuit open, fallback to legacy")

// ConvertStream 实现流式 SSE 转换。
//
// 行为：
//   - 入口检查 CircuitBreaker；若已 Open 返回 ErrStreamCircuitOpen（Factory 应降级 Legacy）
//   - 维护 pendingEvent 状态以正确配对 Anthropic SSE 的 `event:` + `data:` 双行
//   - 单个 chunk 解析错误：记录到熔断器 + slog.Warn，继续处理后续事件
//   - 流结束：记录成功到熔断器
func (t *IRTransport) ConvertStream(ctx context.Context, envelope *domain.RequestEnvelope, upstreamResp *http.Response) error {
	if envelope == nil || envelope.Transport == nil || upstreamResp == nil {
		return errors.New("ir_transport: nil envelope/transport/upstream")
	}
	tc := envelope.Transport

	// 入口熔断检查
	if t.cb != nil && t.cb.ShouldFallback() {
		return ErrStreamCircuitOpen
	}

	// 设置 SSE headers
	if tc.W != nil {
		tc.W.Header().Set("Content-Type", "text/event-stream")
		tc.W.Header().Set("Cache-Control", "no-cache")
		tc.W.Header().Set("Connection", "keep-alive")
	}

	flusher, ok := tc.W.(http.Flusher)
	if !ok {
		return errors.New("ir_transport: response writer does not support flushing")
	}

	br := bufioReader(upstreamResp.Body)
	defer func() { _ = upstreamResp.Body.Close() }()

	// pendingEvent 跟踪 Anthropic SSE 的 event: 行类型，等待对应的 data: 行
	pendingEvent := ""

	for {
		line, err := br.ReadBytes('\n')

		if len(line) > 0 {
			if writeErr := t.processStreamLine(tc, envelope, line, &pendingEvent); writeErr != nil {
				// 关键写入错误 → 立即终止
				if t.cb != nil {
					t.cb.RecordError()
				}
				slog.Error("ir_transport: stream write failed", "request_id", envelope.RequestID, "err", writeErr)
				return writeErr
			}
		}

		if err != nil {
			if err != io.EOF {
				// 非 EOF 的读取错误视为流失败
				if t.cb != nil {
					t.cb.RecordError()
				}
				slog.Error("ir_transport: stream read failed", "request_id", envelope.RequestID, "err", err)
				return fmt.Errorf("stream read error: %w", err)
			}
			break
		}
	}

	// 发送 [DONE]（仅 OpenAI Chat Completions 客户端需要；Responses API
	// 通过 response.completed 显式终止，不需要 [DONE] 哨兵）
	if tc.ClientProtocol == "openai-chat" || tc.ClientProtocol == "openai" {
		_, _ = fmt.Fprintf(tc.W, "data: [DONE]\n\n")
		flusher.Flush()
	}

	// 流成功完成
	if t.cb != nil {
		t.cb.RecordSuccess()
	}
	conversionTotal.WithLabelValues("ir", "stream").Inc()
	return nil
}

// processStreamLine 处理单行 SSE 输入并写入客户端。
//
// 维护 pendingEvent 状态以正确配对 Anthropic SSE：
//   - `event: <type>` 行：写入 pendingEvent
//   - `data: <json>` 行：用 pendingEvent 解析（若上游是 Anthropic）
//   - 空行（事件分隔）：清空 pendingEvent
//
// 解析错误时：slog.Warn + 记录到熔断器，但继续处理（不让单错杀全流）。
func (t *IRTransport) processStreamLine(tc *domain.TransportContext, env *domain.RequestEnvelope, line []byte, pendingEvent *string) error {
	trimmed := bytes.TrimRight(line, "\r\n")
	trimmedSpace := bytes.TrimSpace(trimmed)

	// 空行（事件分隔）→ 清空 pendingEvent
	if len(trimmedSpace) == 0 {
		*pendingEvent = ""
		return nil
	}

	// event: 行（Anthropic 协议）
	if bytes.HasPrefix(trimmedSpace, []byte("event:")) {
		*pendingEvent = string(bytes.TrimSpace(trimmedSpace[6:]))
		return nil
	}

	// data: 行（事件负载）
	if !bytes.HasPrefix(trimmedSpace, []byte("data:")) {
		// 其他行（如 OpenAI 的注释行 `:xxx`）跳过
		return nil
	}

	payload := bytes.TrimSpace(trimmedSpace[5:])
	if len(payload) == 0 {
		return nil
	}

	// 解析上游 chunk
	chunk, parseErr := t.parseUpstreamChunk(tc.UpstreamProtocol, trimmedSpace, *pendingEvent)
	if parseErr != nil {
		slog.Warn("ir_transport: parse stream chunk failed",
			"upstream", tc.UpstreamProtocol,
			"pending_event", *pendingEvent,
			"err", parseErr)
		// 审计修复 (2026-08-29)：IR P1-1 - 记录失败的 chunk 到 RawDataLogger
		if t.rawLogger != nil {
			t.rawLogger.LogConversionError(env.RequestID, tc.UpstreamProtocol, "upstream_response", "stream_parse", trimmedSpace, parseErr)
		}
		// 解析错误：记录到熔断器但继续
		if t.cb != nil {
			t.cb.RecordError()
		}
		*pendingEvent = ""
		return nil
	}
	if chunk == nil {
		// sentinel 帧（如 [DONE]）或空事件
		*pendingEvent = ""
		return nil
	}

	// 序列化为客户端协议
	clientData := t.serializeClientChunk(tc, env, chunk)
	*pendingEvent = ""

	if clientData == "" {
		return nil
	}

	// 写入客户端（Responses API 的 SerializeResponses 已返回完整 SSE 事件）
	if tc.ClientProtocol == "openai-responses" {
		if _, err := fmt.Fprintf(tc.W, "%s", clientData); err != nil {
			return err
		}
	} else {
		// OpenAI Chat / Anthropic Messages：只需 data: 包装
		if _, err := fmt.Fprintf(tc.W, "data: %s\n\n", clientData); err != nil {
			return err
		}
	}
	if f, ok := tc.W.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// parseUpstreamChunk 解析上游 SSE 数据行为 IR StreamChunk。
//
// line 应是已剥除 \r\n 但保留 `data: ` 前缀的完整行（让 IR 函数自己剥前缀）。
// 对于 Anthropic：使用 pendingEvent 作为 eventType（来自上一行 `event:`）。
// 对于 OpenAI：pendingEvent 被忽略（OpenAI 没有 event 行）。
func (t *IRTransport) parseUpstreamChunk(protocol string, line []byte, eventType string) (*ir.StreamChunk, error) {
	switch protocol {
	case "openai-chat", "openai":
		// IR 函数期望带 "data: " 前缀的输入
		return ir.ParseOpenAIStreamChunk(string(line))
	case "anthropic-messages", "anthropic":
		// Anthropic: data 行 payload 是纯 JSON（不需 "data: " 前缀）
		if bytes.HasPrefix(line, []byte("data:")) {
			payload := bytes.TrimSpace(line[5:])
			if bytes.Equal(payload, []byte("[DONE]")) {
				return nil, nil
			}
			return ir.ParseAnthropicStreamEvent(eventType, payload)
		}
		// 非 data 行（已被外层处理，不应到这里）
		return nil, nil
	default:
		return nil, fmt.Errorf("ir_transport: unsupported upstream protocol %s", protocol)
	}
}

// serializeClientChunk 将 IR StreamChunk 序列化为客户端协议 SSE 数据。
//
// Phase E (2026-07-01): added "openai-responses" branch. Responses API SSE
// uses named events (`event: response.output_text.delta` etc.) so we must
// NOT wrap the IR serializer output in `data: %s\n\n` like OpenAI Chat —
// the IR Responses serializer already emits the full `event:` + `data:` +
// blank-line structure itself.
//
// itemID is derived from requestID the same way domains/streaming/responses.go
// does (msg_<8..24 of requestID>), keeping wire IDs stable across reroutes.
func (t *IRTransport) serializeClientChunk(tc *domain.TransportContext, env *domain.RequestEnvelope, chunk *ir.StreamChunk) string {
	switch tc.ClientProtocol {
	case "openai-chat", "openai":
		return chunk.SerializeOpenAI(env.RequestID, tc.ClientModel, env.CreatedAt.Unix())
	case "anthropic-messages", "anthropic":
		return chunk.SerializeAnthropic(env.RequestID, tc.ClientModel)
	case "openai-responses", "responses":
		itemID := deriveResponsesMessageID(env.RequestID)
		return chunk.SerializeResponses(itemID)
	default:
		slog.Warn("ir_transport: unsupported client protocol", "protocol", tc.ClientProtocol)
		return ""
	}
}

// deriveResponsesMessageID produces the msg_xxx item_id used as
// response.output_text.delta's item_id. Mirrors the existing convention
// in domains/streaming/responses.go:msgID (requestID[8:24] when long).
func deriveResponsesMessageID(requestID string) string {
	if requestID == "" {
		return "msg_stream"
	}
	if len(requestID) > 24 {
		return "msg_" + requestID[8:24]
	}
	return "msg_" + requestID
}

// parseRequest 解析客户端请求到 IR。
func parseRequest(protocol string, body []byte) (*ir.InternalRequest, error) {
	switch protocol {
	case "openai-chat", "openai":
		return ir.ParseOpenAI(body)
	case "anthropic-messages", "anthropic":
		return ir.ParseAnthropic(body)
	case "gemini-generate", "gemini":
		return ir.ParseGemini(body)
	case "openai-responses", "responses":
		// Spec §7.1 IR main-path extension (2026-08-02): the Responses API
		// input direction. Reverses SerializeResponsesRequest. See
		// internal/ir/parse_responses.go.
		return ir.ParseResponses(body)
	default:
		return nil, fmt.Errorf("unsupported client protocol: %s", protocol)
	}
}

// serializeRequest 从 IR 序列化到上游协议。
func serializeRequest(protocol string, req *ir.InternalRequest) ([]byte, error) {
	switch protocol {
	case "openai-chat", "openai":
		return ir.SerializeOpenAI(req)
	case "anthropic-messages", "anthropic":
		return ir.SerializeAnthropic(req)
	case "gemini-generate", "gemini":
		return ir.SerializeGemini(req)
	case "openai-responses":
		// Spec §7.1 IR main-path extension (2026-08-02): the Responses API
		// request direction. Maps IR → {input[], instructions, max_output_tokens,
		// flat tools[]}. See internal/ir/serialize_responses.go.
		return ir.SerializeResponsesRequest(req)
	default:
		return nil, fmt.Errorf("unsupported upstream protocol: %s", protocol)
	}
}

// parseResponse 解析上游响应到 IR。
func parseResponse(protocol string, body []byte) (*ir.InternalResponse, error) {
	switch protocol {
	case "openai-chat", "openai":
		return ir.ParseOpenAIResponse(body)
	case "anthropic-messages", "anthropic":
		return ir.ParseAnthropicResponse(body)
	case "gemini-generate", "gemini":
		return ir.ParseGeminiResponse(body)
	case "openai-responses", "responses":
		return ir.ParseResponsesResponse(body)
	default:
		return nil, fmt.Errorf("unsupported upstream protocol: %s", protocol)
	}
}

// serializeResponse 从 IR 序列化到客户端协议。
//
// Phase E (2026-07-01): added "openai-responses" / "responses" branch for
// the Responses API client target. Used when /v1/responses client hits a
// non-Anthropic, non-OpenAI upstream (or when the transport-IR pipeline is
// preferred over the legacy hand-written convertChatResponseToResponses).
func serializeResponse(protocol string, resp *ir.InternalResponse, clientModel string) ([]byte, error) {
	switch protocol {
	case "openai-chat", "openai":
		return ir.SerializeOpenAIResponse(resp, clientModel)
	case "anthropic-messages", "anthropic":
		return ir.SerializeAnthropicResponse(resp, clientModel)
	case "openai-responses", "responses":
		return ir.SerializeResponsesResponse(resp, clientModel)
	default:
		return nil, fmt.Errorf("unsupported client protocol: %s", protocol)
	}
}

// extractHeaders 提取HTTP头部（用于日志记录）
func extractHeaders(headers http.Header) map[string]string {
	result := make(map[string]string)
	// 只记录关键头部，避免泄露敏感信息
	safeHeaders := []string{
		"Content-Type",
		"User-Agent",
		"X-Request-ID",
		"X-Session-ID",
		"X-Tenant-ID",
	}

	for _, key := range safeHeaders {
		if value := headers.Get(key); value != "" {
			result[key] = value
		}
	}

	return result
}
