package sessionaudithook

import "context"

type approvedResumeContextKey struct{}

type approvedResume struct {
	approvalID string
}

// WithApprovedResume marks an internal request reconstructed from an approved
// snapshot. The marker is context-only and cannot be supplied by an HTTP client.
func WithApprovedResume(ctx context.Context, approvalID string) context.Context {
	if approvalID == "" {
		return ctx
	}
	return context.WithValue(ctx, approvedResumeContextKey{}, approvedResume{approvalID: approvalID})
}

// IsApprovedResume reports whether this request was reconstructed by the
// approval-resume path.
func IsApprovedResume(ctx context.Context) bool {
	marker, ok := ctx.Value(approvedResumeContextKey{}).(approvedResume)
	return ok && marker.approvalID != ""
}
