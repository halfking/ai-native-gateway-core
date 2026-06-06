package relay

import (
	"net/url"
)

// chat.go — The Go version routes requests through its own routing executor,
// not through a Python upstream. The upstream variable is kept as a placeholder
// for backward compatibility with ChatCompletionsPhase3, but is not initialized
// from any Python endpoint.
var upstream *url.URL
