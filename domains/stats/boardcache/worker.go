package boardcache

import (
	"context"
	"log/slog"
	"time"
)

// Start launches fold + rebuild background workers.
func (s *Service) Start(ctx context.Context) {
	if s == nil || s.rdb == nil {
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go s.run(cctx)
	slog.Info("boardcache worker started", "fold_interval", foldInterval().String())
}

// Stop stops background workers.
func (s *Service) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	<-s.done
}

func (s *Service) run(ctx context.Context) {
	defer close(s.done)
	foldTick := time.NewTicker(foldInterval())
	rebuildTick := time.NewTicker(15 * time.Minute)
	defer foldTick.Stop()
	defer rebuildTick.Stop()

	rebuildWindow := time.Now()
	go func() {
		initCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if s.build != nil {
			s.rebuildAll(initCtx)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-foldTick.C:
			s.foldAll(ctx)
		case <-rebuildTick.C:
			if !s.shouldRebuild(ctx, rebuildWindow) {
				continue
			}
			if !s.acquireRebuildLock(ctx) {
				continue
			}
			func() {
				defer s.releaseRebuildLock(ctx)
				rebuildCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
				defer cancel()
				s.rebuildAll(rebuildCtx)
				rebuildWindow = time.Now()
			}()
		}
	}
}

func (s *Service) foldAll(ctx context.Context) {
	timeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.foldScope(timeout, ScopeGlobal)
	tenants, err := s.rdb.SMembers(timeout, "llmgw:live:tenants").Result()
	if err != nil {
		return
	}
	for _, tid := range tenants {
		if tid == "" {
			continue
		}
		s.foldScope(timeout, ScopeTenant(tid))
	}
}
