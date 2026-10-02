package bg

import (
	"context"
)

// ResponsesCapabilitySink persists protocol capability evidence for one
// credential and its raw model name. Implementations own TTL and storage
// semantics; probe paths only emit a verdict when they have direct evidence.
type ResponsesCapabilitySink interface {
	SetSupportsResponses(ctx context.Context, credentialID int, rawModel string, supported bool) error
}

func writeResponsesCapability(ctx context.Context, sink ResponsesCapabilitySink, credentialID int, rawModel string, supported *bool) error {
	if sink == nil || supported == nil {
		return nil
	}
	if credentialID <= 0 || rawModel == "" {
		return nil
	}
	return sink.SetSupportsResponses(ctx, credentialID, rawModel, *supported)
}

func boolEvidence(value bool) *bool { return &value }
