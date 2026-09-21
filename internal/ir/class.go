package ir

import "time"

// RequestClass is the gateway-internal request type (V6-W1.6 R8, docs/
// 架构优化v6/09-ir-class-journal-decoupling.md): immediate requests execute
// right away, scheduled requests carry a DueAt and park in the dispatch due
// heap until it passes.
//
// Class and DueAt are Go-struct metadata only — the four protocol serializers
// are explicitly fielded and never emit them, so the classification cannot
// leak into an upstream body (E10; pinned by TestSerializersDoNotLeakRequestClass).
// Injection seam: domain.TransportContext (set by the executors per attempt)
// → TransportIRConverter.Parse* stamps them onto the returned IR.
type RequestClass string

const (
	// ClassImmediate is the default: execute as soon as a lane is free.
	ClassImmediate RequestClass = "immediate"
	// ClassScheduled marks a request with a future DueAt (X-Gw-Due-At).
	ClassScheduled RequestClass = "scheduled"
)

// ClassOf derives the request class from a DueAt value: zero or past (<= now)
// → immediate, future → scheduled.
func ClassOf(dueAt time.Time) RequestClass {
	if dueAt.IsZero() || !dueAt.After(time.Now()) {
		return ClassImmediate
	}
	return ClassScheduled
}
