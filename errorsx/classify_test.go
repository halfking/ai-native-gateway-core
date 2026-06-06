package errorsx

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestClassifyError_ContextCanceled(t *testing.T) {
	kind := ClassifyError(context.Canceled, nil)
	if kind != KindCanceled {
		t.Errorf("expected KindCanceled for context.Canceled, got %q", kind)
	}
}

func TestClassifyError_Timeout(t *testing.T) {
	kind := ClassifyError(context.DeadlineExceeded, nil)
	if kind != KindTimeout {
		t.Errorf("expected KindTimeout, got %q", kind)
	}
}

func TestClassifyError_ConnectionRefused(t *testing.T) {
	err := errors.New("dial tcp 127.0.0.1:443: connection refused")
	kind := ClassifyError(err, nil)
	if kind != KindNetwork {
		t.Errorf("expected KindNetwork for connection refused, got %q", kind)
	}
}

func TestClassifyError_ConnectionReset(t *testing.T) {
	err := errors.New("read tcp: connection reset by peer")
	kind := ClassifyError(err, nil)
	if kind != KindNetwork {
		t.Errorf("expected KindNetwork for connection reset, got %q", kind)
	}
}

func TestClassifyError_DNSFailure(t *testing.T) {
	err := errors.New("lookup nonexistent.example.com: no such host")
	kind := ClassifyError(err, nil)
	if kind != KindNetwork {
		t.Errorf("expected KindNetwork for DNS failure, got %q", kind)
	}
}

func TestClassifyError_GenericError(t *testing.T) {
	err := errors.New("some random error")
	kind := ClassifyError(err, nil)
	if kind != KindTransient {
		t.Errorf("expected KindTransient for generic error, got %q", kind)
	}
}

func TestClassifyError_NilError_NilResponse(t *testing.T) {
	kind := ClassifyError(nil, nil)
	if kind != KindUpstreamDown {
		t.Errorf("expected KindUpstreamDown for nil err and nil resp, got %q", kind)
	}
}

func TestClassifyError_429(t *testing.T) {
	resp := &http.Response{StatusCode: 429}
	kind := ClassifyError(nil, resp)
	if kind != KindRateLimit {
		t.Errorf("expected KindRateLimit for 429, got %q", kind)
	}
}

func TestClassifyError_401(t *testing.T) {
	resp := &http.Response{StatusCode: 401}
	kind := ClassifyError(nil, resp)
	if kind != KindAuth {
		t.Errorf("expected KindAuth for 401, got %q", kind)
	}
}

func TestClassifyError_403(t *testing.T) {
	resp := &http.Response{StatusCode: 403}
	kind := ClassifyError(nil, resp)
	if kind != KindAuth {
		t.Errorf("expected KindAuth for 403, got %q", kind)
	}
}

func TestClassifyError_402(t *testing.T) {
	resp := &http.Response{StatusCode: 402}
	kind := ClassifyError(nil, resp)
	if kind != KindQuota {
		t.Errorf("expected KindQuota for 402, got %q", kind)
	}
}

func TestClassifyError_500(t *testing.T) {
	resp := &http.Response{StatusCode: 500}
	kind := ClassifyError(nil, resp)
	if kind != KindUpstreamDown {
		t.Errorf("expected KindUpstreamDown for 500, got %q", kind)
	}
}

func TestClassifyError_200(t *testing.T) {
	resp := &http.Response{StatusCode: 200}
	kind := ClassifyError(nil, resp)
	if kind != KindTransient {
		t.Errorf("expected KindTransient for 200, got %q", kind)
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		kind     ErrorKind
		expected bool
	}{
		{KindTransient, true},
		{KindTimeout, true},
		{KindNetwork, true},
		{KindUpstreamDown, true},
		{KindRateLimit, false},
		{KindAuth, false},
		{KindQuota, false},
		{ErrorKind(""), false},
	}
	for _, tt := range tests {
		got := IsRetryable(tt.kind)
		if got != tt.expected {
			t.Errorf("IsRetryable(%q) = %v, want %v", tt.kind, got, tt.expected)
		}
	}
}
