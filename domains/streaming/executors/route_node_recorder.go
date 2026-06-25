package executors

import (
	"context"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

type RouteNodeRecorder interface {
	RecordSuccess(ctx context.Context, credentialID int, model string)
	RecordFailure(ctx context.Context, credentialID int, model string, kind errorsx.ErrorKind)
}

type fpSlotRouteNodeRecorder struct {
	fpSlots interface {
		RecordNodeSuccess(ctx context.Context, credentialID int, model, requestID string) error
		RecordNodeFailure(ctx context.Context, credentialID int, model, requestID, errorKind string) error
	}
	requestIDFromContext func(context.Context) string
}

func NewRouteNodeRecorder(fpSlots interface {
	RecordNodeSuccess(ctx context.Context, credentialID int, model, requestID string) error
	RecordNodeFailure(ctx context.Context, credentialID int, model, requestID, errorKind string) error
}) RouteNodeRecorder {
	if fpSlots == nil {
		return nil
	}
	return &fpSlotRouteNodeRecorder{
		fpSlots: fpSlots,
		requestIDFromContext: func(ctx context.Context) string {
			return ""
		},
	}
}

func (r *fpSlotRouteNodeRecorder) RecordSuccess(ctx context.Context, credentialID int, model string) {
	if r == nil || r.fpSlots == nil {
		return
	}
	if err := r.fpSlots.RecordNodeSuccess(ctx, credentialID, model, r.requestIDFromContext(ctx)); err != nil {
		slog.Debug("route node success record failed",
			"error", err,
			"credential_id", credentialID,
			"model", model,
		)
	}
}

func (r *fpSlotRouteNodeRecorder) RecordFailure(ctx context.Context, credentialID int, model string, kind errorsx.ErrorKind) {
	if r == nil || r.fpSlots == nil || isTransientRouteNodeFailure(kind) {
		return
	}
	if err := r.fpSlots.RecordNodeFailure(ctx, credentialID, model, r.requestIDFromContext(ctx), string(kind)); err != nil {
		slog.Debug("route node failure record failed",
			"error", err,
			"credential_id", credentialID,
			"model", model,
			"error_kind", kind,
		)
	}
}

func isTransientRouteNodeFailure(kind errorsx.ErrorKind) bool {
	if kind == "" {
		return false
	}
	if kind == errorsx.KindCanceled ||
		kind == errorsx.KindNetwork ||
		kind == errorsx.KindTimeout ||
		kind == errorsx.KindUpstreamDown {
		return true
	}
	return errorsx.IsClientBug(kind)
}
