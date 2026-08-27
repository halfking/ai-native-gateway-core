package requestdetail

import (
	"context"
	"encoding/json"
	"errors"
)

// BodyReader loads persisted bodies from a dual-write DB source.
type BodyReader interface {
	ReadRequestLogsBodies(ctx context.Context, requestID string) (Bodies, Meta, error)
	ReadSessionTurnsBodies(ctx context.Context, requestID string) (Bodies, Meta, error)
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
				if file, ok, err := l.Store.GetFile(requestID); err != nil {
					return nil, err
				} else if ok {
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
		if file, ok, err := l.Store.GetFile(requestID); err != nil {
			return nil, err
		} else if ok {
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

	bodies, meta, err := l.Bodies.ReadRequestLogsBodies(ctx, requestID)
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

	bodies, meta, err = l.Bodies.ReadSessionTurnsBodies(ctx, requestID)
	if err == nil {
		d := &Detail{
			Source:      SourceSessionTurns,
			Persistence: PersistencePersisted,
			Meta:        meta,
		}
		if !omitBody {
			b := bodies
			d.Bodies = &b
		}
		return d, nil
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
