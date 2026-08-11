package dispatch

import "errors"

// Sentinel errors produced by the dispatch pipeline. The executor maps these
// to HTTP responses (e.g. ErrNoRoute → 503 + Retry-After).

// errPaceTimeout is returned by a governor when the pacing wait exceeds the
// request's queue-wait budget. The forwarder routes the request to the
// failover mover (try another credential / model).
var errPaceTimeout = errors.New("dispatch: pacing wait exceeded queue budget")

// ErrNoRoute is returned to the Submit caller when no model/credential path
// is available (all tried, or no candidates and model-change disabled).
var ErrNoRoute = errors.New("dispatch: no routable credential/model available")

// ErrOverflow is returned when the entry (Tier-1 model) queue is full and the
// request cannot be admitted.
var ErrOverflow = errors.New("dispatch: entry queue full")

// ErrShutdown is returned when Submit is called after the pipeline stopped.
var ErrShutdown = errors.New("dispatch: pipeline shut down")

// IsPaceTimeout reports whether err is the governor pacing-timeout sentinel.
func IsPaceTimeout(err error) bool { return errors.Is(err, errPaceTimeout) }
