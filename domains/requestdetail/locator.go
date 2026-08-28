package requestdetail

import (
	"context"
	"encoding/json"
	"errors"
)

type lookupScopeKey struct{}

type LookupScope struct {
	TenantID     string
	Unrestricted bool
}

func WithLookupScope(ctx context.Context, scope LookupScope) context.Context {
	return context.WithValue(ctx, lookupScopeKey{}, scope)
}

func LookupScopeFromContext(ctx context.Context) LookupScope {
	if scope, ok := ctx.Value(lookupScopeKey{}).(LookupScope); ok {
		return scope
	}
	return LookupScope{Unrestricted: true}
}

// BodyReader loads persisted bodies from a dual-write DB source.
type BodyReader interface {
	// ReadRequestLogsBodies returns ErrNotFound when metadata exists but no
	// request-log body row exists. In that case the returned Meta is still
	// populated so the locator can preserve the request identity while trying
	// the session-turns fallback.
	ReadRequestLogsBodies(ctx context.Context, requestID string, omitBody bool) (Bodies, Meta, error)
	ReadSessionTurnsBodies(ctx context.Context, requestID string, omitBody bool) (Bodies, Meta, error)
}

// ErrNotFound means no layer could supply the request.
var ErrNotFound = errors.New("requestdetail: not found")

// Locator resolves detail in order: memory → file → request_logs → session_turns.
type Locator struct {
	Store  *Store
	Bodies BodyReader
}

// Get resolves a request detail. When omitBody is true, only meta/source are filled.
func (l *Locator) Get(ctx context.Context, requestID string, omitBody bool) (*Detail, error) {
	if requestID == "" {
		return nil, ErrNotFound
	}
	if l.Store != nil {
		if meta, ok := l.Store.GetMeta(requestID); ok {
			d := &Detail{
				Source:      SourceMemory,
				Persistence: PersistenceInFlight,
				Meta:        meta,
			}
			if !omitBody {
				if file, ok, err := l.Store.GetFile(requestID); err != nil && !errors.Is(err, ErrBodyTooLarge) {
					return nil, err
				} else if err == nil && ok {
					bodies := file.Bodies
					d.Bodies = &bodies
					d.Source = SourceFile
					if file.Meta.RequestID != "" {
						d.Meta = mergeMeta(meta, file.Meta)
					}
				}
			}
			return d, nil
		}
		if file, ok, err := l.Store.GetFile(requestID); err != nil && !errors.Is(err, ErrBodyTooLarge) {
			return nil, err
		} else if err == nil && ok {
			d := &Detail{
				Source:      SourceFile,
				Persistence: PersistenceInFlight,
				Meta:        file.Meta,
			}
			if !omitBody {
				bodies := file.Bodies
				d.Bodies = &bodies
			}
			return d, nil
		}
	}

	if l.Bodies == nil {
		return nil, ErrNotFound
	}

	bodies, meta, err := l.Bodies.ReadRequestLogsBodies(ctx, requestID, omitBody)
	if err == nil {
		d := &Detail{
			Source:      SourceRequestLogs,
			Persistence: PersistencePersisted,
			Meta:        meta,
		}
		if !omitBody {
			b := bodies
			d.Bodies = &b
		}
		return d, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// A request-log row can outlive its body row (TTL, partial persistence, or
	// a historical migration). Try the session store before giving up, while
	// retaining the metadata returned by the request-log reader.
	requestLogsMeta := meta
	sessionLookupID := requestID
	if requestLogsMeta.RequestID != "" {
		sessionLookupID = requestLogsMeta.RequestID
	}
	bodies, sessionMeta, err := l.Bodies.ReadSessionTurnsBodies(ctx, sessionLookupID, omitBody)
	if err == nil {
		d := &Detail{
			Source:      SourceSessionTurns,
			Persistence: PersistencePersisted,
			Meta:        mergeMeta(requestLogsMeta, sessionMeta),
		}
		if !omitBody {
			b := bodies
			d.Bodies = &b
		}
		return d, nil
	}
	if errors.Is(err, ErrNotFound) && requestLogsMeta.RequestID != "" {
		// Metadata is still useful to the detail page even when both body stores
		// are empty. Return it as a successful metadata-only response instead of
		// misreporting an existing request as 404.
		return &Detail{
			Source:      SourceRequestLogs,
			Persistence: PersistencePersisted,
			Meta:        requestLogsMeta,
			Warning:     "request body row not found in request_logs or session_turns; metadata-only fallback",
		}, nil
	}
	return nil, err
}

func mergeMeta(base, overlay Meta) Meta {
	out := base
	if overlay.RequestID != "" {
		out.RequestID = overlay.RequestID
	}
	if overlay.TenantID != "" {
		out.TenantID = overlay.TenantID
	}
	if overlay.GwSessionID != nil {
		out.GwSessionID = overlay.GwSessionID
	}
	if overlay.GwTaskID != nil {
		out.GwTaskID = overlay.GwTaskID
	}
	if overlay.ClientModel != nil {
		out.ClientModel = overlay.ClientModel
	}
	if overlay.Status != nil {
		out.Status = overlay.Status
	}
	if overlay.Success != nil {
		out.Success = overlay.Success
	}
	if overlay.LatencyMs != nil {
		out.LatencyMs = overlay.LatencyMs
	}
	if overlay.TurnNumber != nil {
		out.TurnNumber = overlay.TurnNumber
	}
	return out
}

// PtrTo is a helper that returns a pointer to a copy of v.
// It reduces heap allocations when constructing pointer fields by enabling
// stack-to-heap escape analysis optimization in patterns like: field = PtrTo(value)
func PtrTo[T any](v T) *T {
	return &v
}

// DecodeRaw helpers for callers that hold string bodies.
func DecodeRaw(s *string) json.RawMessage {
	if s == nil || *s == "" {
		return nil
	}
	raw := json.RawMessage(*s)
	if !json.Valid(raw) {
		b, _ := json.Marshal(*s)
		return b
	}
	return raw
}
