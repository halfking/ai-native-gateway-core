package largebody

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
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
		s.syncDir(dir)
		return
	}
	// 2026-11-23：暂存身份的目录链一并删掉。
	//
	// 暂存身份是每请求一个 uuid（gw_staging_<uuid>）；rebind 之后里面的两个
	// 文件都没了，只剩四层空目录。此前从不清理 ⇒ 每个大请求永久留下
	// `gw_staging_<uuid>/large-v1/requests/<reqid>/`（211 实测一个晚上 1800+
	// 个；`find` 扫整棵归档树要跑好几分钟）。
	//
	// 边界：
	//   - 只对暂存身份做。它的目录是这一个请求私有的，没有并发使用者；
	//     非暂存身份的会话目录可能有并发的 Capture，不动它（只同步）。
	//   - 只删暂存身份自己的四层（<reqid> ← requests ← large-v1 ←
	//     gw_staging_<uuid>），到此为止。分片目录（会话 id 前两位）与租户目录
	//     是所有会话共享的——即使此刻恰好是空的也不碰：并发的 Capture 正在
	//     往里建目录时把它们删掉，那一次 Capture 就失败了。
	//   - os.Root.Remove 只删得动**空**目录 ⇒ 链上任何一层还有别的东西
	//     （比如暂存会话目录里多出一个文件）就在那一层停下，不可能误删数据。
	if !strings.HasPrefix(source.SessionID, StagingSessionPrefix) {
		s.syncDir(dir)
		return
	}
	cur := dir
	for level := 0; level < stagingChainLevels; level++ {
		if err := s.root.Remove(cur); err != nil {
			break // 非空（或已不存在）⇒ 停在这一层
		}
		cur = filepath.Dir(cur)
	}
	if err := s.syncDir(cur); err != nil {
		slog.Warn("largebody: rebind source directory sync deferred", "request_id", source.RequestID, "error", err)
	}
}

const (
	// StagingSessionPrefix 是暂存身份的会话 id 前缀：大请求先以一个每请求唯一
	// 的 uuid 落盘（调用方生成），解析出真实会话之后再 Rebind 过去。
	StagingSessionPrefix = "gw_staging_"

	// stagingChainLevels 是暂存目录链的层数——与 requestDir 的布局对应：
	// <reqid> ← requests ← large-v1 ← <会话目录>。改 requestDir 的布局要同步改它
	//（TestRebindRemovesStagingChainOnly 有钉测）。
	stagingChainLevels = 4
)
