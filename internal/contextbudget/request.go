package contextbudget

import (
	"bytes"
	"context"
	"io"
	"net/http"
)

type specKey struct{}

// WithSpec binds trusted bucket capabilities to this physical request attempt.
// Binding a zero Spec intentionally fails closed until a bucket is resolved.
func WithSpec(ctx context.Context, spec Spec) context.Context {
	return context.WithValue(ctx, specKey{}, spec)
}

func SpecFromContext(ctx context.Context) (Spec, bool) {
	if ctx == nil {
		return Spec{}, false
	}
	spec, ok := ctx.Value(specKey{}).(Spec)
	return spec, ok
}

// CheckRequest consumes the actual sending reader and replaces it with the
// checked, bounded bytes. GetBody alone is unsafe: serializers may replace Body
// without updating its old replay closure. Receipts are not cached across
// retries. Production assembly must bind a Spec before each LLM HTTP attempt.
func CheckRequest(req *http.Request) (Receipt, error) {
	if req == nil {
		return Receipt{}, invalid("nil HTTP request")
	}
	spec, bound := SpecFromContext(req.Context())
	if !bound {
		return Receipt{}, nil
	}
	if err := req.Context().Err(); err != nil {
		return Receipt{}, err
	}
	budget, err := Resolve(spec)
	if err != nil {
		return Receipt{}, err
	}
	if req.ContentLength > int64(budget.JSONBytes) {
		return Receipt{}, &Error{Kind: BytesExceeded, Limit: budget.JSONBytes, Actual: int(req.ContentLength), Detail: "final JSON exceeds byte limit"}
	}
	if req.GetBody == nil {
		return Receipt{}, &Error{Kind: Uncountable, Detail: "guarded HTTP body is not replayable"}
	}
	reader := req.Body
	if reader == nil {
		return Receipt{}, invalid("missing guarded HTTP body")
	}
	defer reader.Close()
	stopCancel := context.AfterFunc(req.Context(), func() { _ = reader.Close() })
	defer stopCancel()
	// The default counter cannot accept more wire bytes than input tokens;
	// avoid allocating the much larger JSON byte policy for an obvious reject.
	readLimit := min(budget.JSONBytes, budget.InputTokens)
	body, err := io.ReadAll(io.LimitReader(reader, int64(readLimit)+1))
	if err != nil {
		return Receipt{}, &Error{Kind: Uncountable, Detail: "cannot read guarded HTTP body"}
	}
	receipt, err := Check(body, spec)
	if err != nil {
		return Receipt{}, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	req.ContentLength = int64(len(body))
	return receipt, nil
}
