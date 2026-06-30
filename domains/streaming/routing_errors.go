package streaming

import (
	"fmt"
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
	errStr := err.Error()
	c := routingErrorClass{
		code:       "routing_database_error",
		message:    fmt.Sprintf("Routing service error: %v", err),
		httpStatus: http.StatusInternalServerError,
	}
	switch {
	case strings.Contains(errStr, "not configured"):
		c.code = "routing_not_configured"
		c.message = "Routing service is not configured"
		c.httpStatus = http.StatusServiceUnavailable
	case strings.Contains(errStr, "connection") || strings.Contains(errStr, "timeout"):
		c.code = "routing_connection_error"
		c.message = "Database connection error"
		c.httpStatus = http.StatusServiceUnavailable
	case strings.Contains(errStr, "relation") || strings.Contains(errStr, "partition") ||
		strings.Contains(errStr, "function") || strings.Contains(errStr, "does not exist"):
		c.code = "routing_schema_error"
		c.message = fmt.Sprintf("Database schema error: %v", err)
	}
	return c
}
