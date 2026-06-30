package streaming

import (
	"net/http"
	"strings"
)

// routingErrorClass holds the classification of a routing/GetCandidates error.
type routingErrorClass struct {
	code       string
	message    string
	httpStatus int
}

// classifyRoutingError inspects an error returned by provider.GetCandidates
// and maps it to a transparent error class. Database or infrastructure errors
// must NOT be disguised as the business error "no_candidate" — they have
// different root causes and require different operational responses.
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
	case strings.Contains(errStr, "not configured"):
		c.code = "routing_not_configured"
		c.message = "Routing service temporarily unavailable"
		c.httpStatus = http.StatusServiceUnavailable
	case strings.Contains(errStr, "connection") || strings.Contains(errStr, "timeout"):
		c.code = "routing_connection_error"
		c.message = "Routing service temporarily unavailable"
		c.httpStatus = http.StatusServiceUnavailable
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
