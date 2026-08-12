package bg

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
)

// stubTimeoutErr is the smallest type that satisfies the stdlib's informal
// `interface{ Timeout() bool }` contract used by net.OpError / url.Error to
// decide whether an error is deadline-shaped.
type stubTimeoutErr struct{ msg string }

func (e stubTimeoutErr) Error() string { return e.msg }
func (e stubTimeoutErr) Timeout() bool { return true }

// TestClassifyProbeNetworkError pins the 2026-08-12 transport-error
// taxonomy. Before this, every non-2xx transport failure collapsed to a
// single "network_error" string and probeGateway did not even detect
// timeouts; an operator could not distinguish a DNS outage from a refused
// dial from a slow upstream. Each case below mirrors the shape of error
// http.Client.Do actually returns (always wrapped in *url.Error on the
// real hot path) plus the bare-error paths future callers may hit.
func TestClassifyProbeNetworkError(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		wantCode     string
		wantTimedOut bool
	}{
		{"nil", nil, "none", false},
		{"context deadline", context.DeadlineExceeded, "timeout", true},

		// *url.Error paths — what http.Client.Do actually yields.
		{"url.Error read timeout",
			&url.Error{Op: "Post", URL: "https://up/v1", Err: stubTimeoutErr{"i/o timeout"}},
			"timeout", true},
		{"url.Error dns nohost",
			&url.Error{Op: "Post", URL: "https://up/v1", Err: &net.DNSError{Err: "no such host", Name: "up"}},
			"dns_error", false},
		{"url.Error dns timeout",
			&url.Error{Op: "Post", URL: "https://up/v1", Err: &net.DNSError{Err: "server misbehaving", Name: "up", IsTimeout: true}},
			"timeout", true},
		{"url.Error dial refused",
			&url.Error{Op: "Post", URL: "https://up/v1", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}},
			"connection_error", false},
		{"url.Error dial timeout",
			&url.Error{Op: "Post", URL: "https://up/v1", Err: &net.OpError{Op: "dial", Net: "tcp", Err: stubTimeoutErr{"i/o timeout"}}},
			"timeout", true},
		{"url.Error mid-stream read reset",
			&url.Error{Op: "Post", URL: "https://up/v1", Err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}},
			"network_error", false},

		// Bare-error paths (custom Transport / direct dialer use).
		{"bare dns nohost",
			&net.DNSError{Err: "no such host", Name: "up"},
			"dns_error", false},
		{"bare dial refused",
			&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
			"connection_error", false},
		{"bare dial timeout",
			&net.OpError{Op: "dial", Net: "tcp", Err: stubTimeoutErr{"i/o timeout"}},
			"timeout", true},

		// Fallback: TLS / body-read / anything unrecognised.
		{"generic error", errors.New("tls: handshake failure"), "network_error", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotCode, gotTimedOut := classifyProbeNetworkError(c.err)
			if gotCode != c.wantCode || gotTimedOut != c.wantTimedOut {
				t.Errorf("classifyProbeNetworkError(%v) = (%q, %v), want (%q, %v)",
					c.err, gotCode, gotTimedOut, c.wantCode, c.wantTimedOut)
			}
		})
	}
}
