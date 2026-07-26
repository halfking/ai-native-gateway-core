package transformation

import "testing"

// TestUnhandledOpenAIFieldsArePassthrough guards the 2026-07-27 fix (F-2).
//
// stream_options and metadata were previously listed in
// standardRequestFields even though internal/ir never parses or serializes
// them (zero references in parse_openai.go / serialize_openai.go). Listing
// them as "standard" caused the IRExtensionExtractor to skip them, so they
// were silently dropped on every IR-routed request — most visibly,
// stream_options.include_usage never reached the upstream, so the terminal
// {"choices":[],"usage":{...}} frame was never produced.
//
// Contract: these fields must NOT be in standardRequestFields so the
// Extensions mechanism captures and round-trips them on same-protocol routes.
func TestUnhandledOpenAIFieldsArePassthrough(t *testing.T) {
	// Fields IR does not handle but were once misclassified as "standard".
	// If IR later grows real parse/serialize for one of these, move it back
	// into standardRequestFields and delete it from this list.
	unhandled := []string{"stream_options", "metadata"}

	for _, field := range unhandled {
		if isStandardField(field) {
			t.Errorf(
				"field %q is listed as standard but internal/ir does not parse/serialize it; "+
					"it must be a passthrough extension or it gets silently dropped on IR routes (audit F-2)",
				field,
			)
		}
	}
}
