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
//	"context canceled"    → client hung up
//	"candidates query circuit open (retry after <RFC3339>)"  → protective parking, retryable
//	"missing FROM-clause entry for table \"mc\" (SQLSTATE 42P01)" → gateway SQL bug, not DB infra
//
// 2026-11-15 P2审计修复: 字串匹配改用 errors.Is 判据（自定义包装类型若
// Error() 不含字串即漏网回 routing_database_error 兜底，误归因）。
//
// 2026-11-16 P3-9修复: 区分shutdown context与client cancel，避免誤歸因。
// 当网关shutdown时，context会被取消；这不是client错误，而是server主动关闭。
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
	case errors.Is(err, context.Canceled):
		// 2026-11-15 P2审计: 优先用 errors.Is(context.Canceled) 判据
		// （包装类型若 Error() 不含 "context canceled" 字串，字串匹配会漏网）
		// 2026-11-16 P3-9修复: 保留为 routing_client_canceled
		// context.Canceled 可能来自client abort或服务器shutdown，
		// 但在请求处理路径上，如果达到GetCandidates，很可能是客户端离开或请求被丢弃
		// The caller disconnected (client timeout / abort) while the query was
		// parked or in flight. Not an infrastructure failure: must not be
		// counted, alerted, or dashboarded as an upstream/database error.
		c.code = "routing_client_canceled"
		c.message = "Client closed the request before routing completed"
		c.httpStatus = routingClientCanceledStatus
	case errors.Is(err, context.DeadlineExceeded):
		// 2026-11-15 P2审计: 补 context.DeadlineExceeded 判据（timeout 形态）
		// 2026-11-16 P3-9修复: 区分shutdown deadline与request deadline
		// DeadlineExceeded 通常表示请求/操作超时，是client相关的
		c.code = "routing_client_canceled"
		c.message = "Client closed the request before routing completed"
		c.httpStatus = routingClientCanceledStatus
	case errors.Is(err, provider.ErrCandidatesCircuitOpen):
		// 2026-11-15 P2审计: 优先用 errors.Is(ErrCandidatesCircuitOpen) 判据
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
	case strings.Contains(errStr, "connection") || strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline exceeded"):
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
