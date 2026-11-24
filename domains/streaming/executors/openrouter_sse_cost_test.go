package executors

import (
	"io"
	"strings"
	"testing"
)

type closeTrackingReader struct {
	*strings.Reader
	closed bool
}

func (r *closeTrackingReader) Close() error { r.closed = true; return nil }

func TestOpenRouterSSECostBodyPreservesSplitChunksAndZero(t *testing.T) {
	// The JSON data line crosses reads. Null is ignored, explicit zero is kept.
	input := "data: {\"usage\":{\"cost\":null}}\r\ndata: {\"usage\":{\"cost\":0}}\r\n\n"
	raw := &closeTrackingReader{Reader: strings.NewReader(input)}
	var amounts []string
	body := observeOpenRouterSSE("text/event-stream", "https://openrouter.ai/api/v1/chat/completions", raw, func(a string) { amounts = append(amounts, a) })
	var got strings.Builder
	buf := make([]byte, 7)
	for {
		n, err := body.Read(buf)
		got.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if got.String() != input {
		t.Fatalf("body changed: %q", got.String())
	}
	if len(amounts) != 1 || amounts[0] != "0" {
		t.Fatalf("unexpected cost evidence: %#v", amounts)
	}
	if err := body.Close(); err != nil || !raw.closed {
		t.Fatalf("Close not forwarded: err=%v closed=%v", err, raw.closed)
	}
}

func TestOpenRouterSSECostObserverIsProviderAndContentTypeScoped(t *testing.T) {
	for _, tc := range []struct{ endpoint, contentType string }{
		{"https://api.openrouter.ai/v1", "text/event-stream"},
		{"https://openrouter.ai/v1", "application/json"},
	} {
		raw := &closeTrackingReader{Reader: strings.NewReader("data: {\"usage\":{\"cost\":1}}\n")}
		called := false
		body := observeOpenRouterSSE(tc.contentType, tc.endpoint, raw, func(string) { called = true })
		_, _ = io.Copy(io.Discard, body)
		if called {
			t.Fatalf("unexpected observation for endpoint=%q content-type=%q", tc.endpoint, tc.contentType)
		}
	}
}
