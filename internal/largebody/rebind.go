package largebody

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
)

// Fixed stripes serialize rebinds across Store handles without retaining an
// unbounded map keyed by request IDs. Waiting callers can honor cancellation.
var rebindStripes = func() [64]chan struct{} {
	var stripes [64]chan struct{}
	for i := range stripes {
		stripes[i] = make(chan struct{}, 1)
		stripes[i] <- struct{}{}
	}
	return stripes
}()

// Rebind associates an archive with the session discovered in the parsed body.
// Tenant, server request ID and original bytes cannot change. The destination
// is durably committed before source records are removed. Cancellation/failure
// before commit preserves the original; after commit the persisted destination
// is returned even if the caller cancels or source cleanup needs retry.
func (s *Store) Rebind(ctx context.Context, source Ref, sessionID string) (Ref, error) {
	if err := ctx.Err(); err != nil {
		return Ref{}, err
	}
	if err := validRef(source); err != nil {
		return Ref{}, err
	}
	target := source
	target.SessionID = sessionID
	if err := validIdentity(target.Identity); err != nil {
		return Ref{}, err
	}
	root, err := filepath.Abs(s.path)
	if err != nil {
		return Ref{}, err
	}
	hash := sha256.Sum256([]byte(root + "\x00" + source.TenantID + "\x00" + source.RequestID))
	stripe := rebindStripes[hash[0]%byte(len(rebindStripes))]
	select {
	case <-ctx.Done():
		return Ref{}, ctx.Err()
	case <-stripe:
	}
	defer func() { stripe <- struct{}{} }()
	current, sourceErr := s.Lookup(ctx, source.Identity)
	if sourceErr != nil {
		// A successful earlier move may have removed the original. Only a
		// verified destination with exactly the expected identity/hash is an
		// idempotent retry, never an arbitrary archive with the same request ID.
		if errors.Is(sourceErr, fs.ErrNotExist) {
			if existing, err := s.Lookup(ctx, target.Identity); err == nil {
				if existing == target {
					return existing, nil
				}
				return Ref{}, ErrExists
			} else if !errors.Is(err, fs.ErrNotExist) {
				return Ref{}, err
			}
		}
		return Ref{}, sourceErr
	}
	if current != source {
		return Ref{}, ErrIntegrity
	}
	if source == target {
		return target, nil
	}
	if existing, err := s.Lookup(ctx, target.Identity); err == nil {
		if existing != target {
			return Ref{}, ErrExists
		}
		s.cleanupReboundSource(source)
		return target, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Ref{}, err
	}
	if err := ctx.Err(); err != nil {
		return Ref{}, err
	}
	dir := requestDir(target.Identity)
	if err := s.ensureDirs(dir); err != nil {
		return Ref{}, err
	}
	if err := s.root.Link(source.RelativePath(), target.RelativePath()); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Ref{}, ErrExists
		}
		return Ref{}, err
	}
	committed, refPlaced := false, false
	defer func() {
		if !committed {
			if refPlaced {
				_ = s.root.Remove(filepath.Join(dir, "ref.json"))
			}
			_ = s.root.Remove(target.RelativePath())
		}
	}()
	// Validate the linked inode before making it recoverable by ref.json.
	if err := s.Verify(ctx, target); err != nil {
		return Ref{}, err
	}
	if err := s.writeRef(dir, target); err != nil {
		return Ref{}, err
	}
	refPlaced = true
	if err := ctx.Err(); err != nil {
		return Ref{}, err
	}
	if err := s.syncDir(dir); err != nil {
		return Ref{}, err
	}
	committed = true
	s.cleanupReboundSource(source)
	return target, nil
}

func (s *Store) cleanupReboundSource(source Ref) {
	dir := requestDir(source.Identity)
	// Removing the commit record first prevents recovery from acknowledging a
	// reference whose RAW is gone. A crash can leave orphan RAW, never data loss:
	// the destination is already synced before this routine runs.
	if err := s.root.Remove(filepath.Join(dir, "ref.json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("largebody: rebind source cleanup deferred", "request_id", source.RequestID, "error", err)
		return
	}
	if err := s.root.Remove(source.RelativePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("largebody: rebind source RAW cleanup deferred", "request_id", source.RequestID, "error", err)
	}
	if err := s.syncDir(dir); err != nil {
		slog.Warn("largebody: rebind source directory sync deferred", "request_id", source.RequestID, "error", err)
	}
}
