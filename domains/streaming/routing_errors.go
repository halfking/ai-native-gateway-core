package streaming

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// routingErrorClass holds the classification of a routing/GetCandidates error.
type routingErrorClass struct {
	code       string
	message    string
	httpStatus int
}

// Status used when the caller already hung up. Matches nginx's "client closed
// request" convention; the response never reaches a client anyway, but the
// recorded status must not masquerade as a server-side 5xx.
const routingClientCanceledStatus = 499

// logCandidatesFailure emits the "failed to get candidates from provider" line
// at a severity matching the class: real infra/schema failures stay ERROR,
// client-canceled resolves are WARN (the caller left; nothing is broken).
func logCandidatesFailure(code string, err error, model, requestID string) {
	if code == "routing_client_canceled" {
		slog.Warn("failed to get candidates from provider", "error", err, "code", code, "model", model, "request_id", requestID)
		return
	}
	slog.Error("failed to get candidates from provider", "error", err, "code", code, "model", model, "request_id", requestID)
}

// classifyRoutingError inspects an error returned by provider.GetCandidates
// and maps it to a transparent error class. Database or infrastructure errors
// must NOT be disguised as the business error "no_candidate" — they have
// different root causes and require different operational responses.
//
// 2026-11-15 (211 incident attribution fix): three real production shapes were
// all collapsing into the routing_database_error fallback, polluting the
// failure dashboard as "upstream · routing_database_error" during what was
// actually a client-timeout retry storm parked behind an open circuit:
//
//	"context canceled"                                       → client hung up
//	"candidates query circuit open (retry after <RFC3339>)"  → protective parking, retryable
//	"missing FROM-clause entry for table \"mc\" (SQLSTATE 42P01)" → gateway SQL bug, not DB infra
//
// 2026-11-15 P2审计修复: 字串匹配改用 errors.Is 判据（自定义包装类型若
// Error() 不含字串即漏网回 routing_database_error 兜底，误归因）。
//
// 2026-11-16 巡检补正: errors.Is 与字串判据是**并集**，不是替代关系。
//
//	★ 生产形态有**两支**：被 %w 包过的哨兵形态，和裸文本形态
//	  （"context canceled" / "candidates query circuit open (retry after ...)"
//	   逐字取自生产日志，见 routing_errors_test.go 的 "production shape" 用例）。
//	★ 只留 errors.Is ⇒ 裸文本形态全部漏网 ⇒ 回落 routing_database_error
//	  ⇒ 211 事故归因修复被静默回退（实测：2 条既有门禁转红）。
//	★ 反过来也要求：**哨兵形态与文本形态必须落同一个桶**，
//	  否则同一个故障会因为构造方式不同被拆进两个看板桶 ——
//	  那正是这个分类器要消灭的误归因（故 context.DeadlineExceeded
//	  与 "timeout" 同归 routing_connection_error，而非 client_canceled）。
func classifyRoutingError(err error) routingErrorClass {
	if err == nil {
		return routingErrorClass{
			code:       "routing_unknown_error",
			message:    "Internal routing service error",
			httpStatus: http.StatusInternalServerError,
		}
	}

	errStr := err.Error()
	c := routingErrorClass{
		code:       "routing_database_error",
		message:    "Internal routing service error",
		httpStatus: http.StatusInternalServerError,
	}
	switch {
	case errors.Is(err, context.Canceled) || strings.Contains(errStr, "context canceled"):
		// 2026-11-15 P2审计: errors.Is 优先（包装类型 %w 能识别）；
		// 2026-11-16 巡检补正: **字串兜底必须保留**。
		//   ★ 为什么不能只留 errors.Is：生产形态就是**文本形态**
		//     （"context canceled" 这条文本，逐字取自生产日志，
		//      见 routing_errors_test.go 的 "production shape" 用例）。
		//哨兵判据只覆盖「被 %w 包过」的那一支 ⇒文本形态全部漏网
		//     ⇒ 回落 routing_database_error 兜底 ⇒ 211 事故归因修复被静默回退。
		//     （实测：改后2 条既有门禁转红，code=routing_database_error。）
		// The caller disconnected (client timeout / abort) while the query was
		// parked or in flight. Not an infrastructure failure: must not be
		// counted, alerted, or dashboarded as an upstream/database error.
		c.code = "routing_client_canceled"
		c.message = "Client closed the request before routing completed"
		c.httpStatus = routingClientCanceledStatus
	case errors.Is(err, provider.ErrCandidatesCircuitOpen) || strings.Contains(errStr, "circuit open"):
		// 2026-11-15 P2审计: errors.Is 优先；2026-11-16 巡检补正: 字串兜底保留
		// （理由同上：生产既有 %w 包装形态，也有裸文本形态）。
		// Protective circuit after repeated candidate-query failures. The
		// primary failure is logged separately; this shape is a retryable
		// temporary condition, so 503 (not 500) and its own dashboard code.
		c.code = "routing_circuit_open"
		c.message = "Routing service temporarily unavailable"
		c.httpStatus = http.StatusServiceUnavailable
	case strings.Contains(errStr, "not configured"):
		c.code = "routing_not_configured"
		c.message = "Routing service temporarily unavailable"
		c.httpStatus = http.StatusServiceUnavailable
	case errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(errStr, "connection") || strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline exceeded"):
		// 2026-11-15 P2审计: 补 context.DeadlineExceeded 判据；
		// 2026-11-16 巡检补正**归因**：超时是**服务端**超时，不是客户端断开
		//   ⇒ 与文本形态（"timeout" / "context deadline exceeded"
		//      ⇒ routing_connection_error 503）落同一个桶。
		//   ★ 哨兵形态与文本形态必须同桶：否则同一个故障会因为错误的构造方式
		//     被拆进两个看板桶 —— 那正是这个分类器要消灭的误归因。
		c.code = "routing_connection_error"
		c.message = "Routing service temporarily unavailable"
		c.httpStatus = http.StatusServiceUnavailable
	case strings.Contains(errStr, "missing FROM-clause entry") || strings.Contains(errStr, "SQLSTATE 42P01"):
		// "missing FROM-clause entry" and 42P01 (undefined_object/table class)
		// are gateway-side SQL defects: the DB is reachable and healthy, the
		// query itself is wrong. Attribute as configuration/schema, not infra.
		c.code = "routing_schema_error"
		c.message = "Internal routing configuration error"
		c.httpStatus = http.StatusInternalServerError
	case strings.Contains(errStr, "relation \"") && strings.Contains(errStr, "does not exist"):
		c.code = "routing_schema_error"
		c.message = "Internal routing configuration error"
		c.httpStatus = http.StatusInternalServerError
	case strings.Contains(errStr, "partition") && (strings.Contains(errStr, "does not exist") || strings.Contains(errStr, "found for row")):
		c.code = "routing_schema_error"
		c.message = "Internal routing configuration error"
		c.httpStatus = http.StatusInternalServerError
	case strings.Contains(errStr, "function") && strings.Contains(errStr, "does not exist"):
		c.code = "routing_schema_error"
		c.message = "Internal routing configuration error"
		c.httpStatus = http.StatusInternalServerError
	}
	return c
}
