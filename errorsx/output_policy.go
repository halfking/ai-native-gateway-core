package errorsx

import "errors"

// ErrOutputPolicyBlocked is a gateway policy decision made before any response
// bytes are committed. It is terminal for routing, but is not provider failure
// evidence and does not imply a protocol error has already been rendered.
var ErrOutputPolicyBlocked = errors.New("response blocked by output policy")
