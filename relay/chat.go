package relay

import (
	"net/url"
)

// chat.go — The Go version routes requests through its own routing executor.
// The upstream variable is kept for test compatibility but is not used
// in production routing (the executor handles upstream requests directly).
var upstream *url.URL
