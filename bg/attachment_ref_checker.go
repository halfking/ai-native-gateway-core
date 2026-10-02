// Package bg — attachment_ref_checker.go
//
// §三#2（2026-09-30 三十七轮续）：附件 LRU 悬挂删除守卫的 DB 反查实现。
// cleanupAttachmentsLRU 此前纯 mtime 删文件、零引用检查——内容寻址存储
// 按 hash 去重共享同一文件，删除仍被 request_attachments 引用的文件会
// 打断多轮回放旧媒体并断取证链。
//
// 依赖面说明：接口（storage_interface.go AttachmentReferenceChecker）隔离
// 了 bg 对 domains/attachments 的依赖；本实现直连 pool 裸 SQL，与
// partition_manager/state 系 worker 的既有形态一致（request_attachments
// 的读写方 Repository 在 domains/attachments，其 InsertOne 为 best-effort
// 契约——插入失败的文件会成为无引用孤儿，反而可被安全回收）。
package bg

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// attachmentRefQueryPool 是引用反查所需的最小查询面（pgxpool.Pool 与
// pgxmock.Pool 均满足），沿用 credentialSelfcheckConnPool 的窄接口定式。
type attachmentRefQueryPool interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// PGAttachmentRefChecker 基于 request_attachments 的引用反查。
type PGAttachmentRefChecker struct {
	pool attachmentRefQueryPool
}

// NewAttachmentRefChecker 构造引用反查器。db 为 nil 时返回 nil
// （worker 侧对 nil checker fail-closed：不删任何附件）。
func NewAttachmentRefChecker(db *pgxpool.Pool) *PGAttachmentRefChecker {
	if db == nil {
		return nil
	}
	return &PGAttachmentRefChecker{pool: db}
}

// ReferencedPaths 反查候选附件的存活引用，返回仍被引用的 RelPath 集合。
//
// 两臂查询（§三#2 修法 "path/hash 反查 request_attachments"）：
//   - storage_path 等值臂：精确引用判定。storage_path 无索引（401 迁移
//     仅建 created_at/hash/request_id/status_time 四索引），本臂顺序扫描——
//     可接受：调用点受磁盘水位门控（默认 auto_cleanup 关闭 + >85% 才触发，
//     每小时至多一轮），非热路径；
//   - hash 等值臂：走 idx_request_attachments_hash 部分索引。同内容跨月
//     会重复落盘（路径含 YYYY/MM，ir_transformer 既有注释），哈希命中是
//     过保护——把跨月复本也保活，宁可少删。
//
// legacy req_<requestID>/ 布局的行可能 hash 为 NULL（nullableString 落库），
// 故路径臂不可省。
func (c *PGAttachmentRefChecker) ReferencedPaths(ctx context.Context, candidates []AttachmentRef) (map[string]bool, error) {
	referenced := make(map[string]bool)
	if c == nil || c.pool == nil || len(candidates) == 0 {
		return referenced, nil
	}

	paths := make([]string, 0, len(candidates))
	hashSet := make(map[string]bool, len(candidates))
	for _, cand := range candidates {
		if cand.RelPath != "" {
			paths = append(paths, cand.RelPath)
		}
		if cand.Hash != "" {
			hashSet[cand.Hash] = true
		}
	}

	if len(paths) > 0 {
		rows, err := c.pool.Query(ctx,
			"SELECT storage_path FROM public.request_attachments WHERE storage_path = ANY($1)", paths)
		if err != nil {
			return nil, err
		}
		seenPath := make(map[string]bool, len(paths))
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, err
			}
			seenPath[p] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		// DB 侧路径以 / 分隔（写侧 filepath.Join 产出于 linux 生产），
		// 候选侧已统一 ToSlash 后比对。
		for _, cand := range candidates {
			if seenPath[cand.RelPath] {
				referenced[cand.RelPath] = true
			}
		}
	}

	if len(hashSet) > 0 {
		hashes := make([]string, 0, len(hashSet))
		for h := range hashSet {
			hashes = append(hashes, h)
		}
		rows, err := c.pool.Query(ctx,
			"SELECT hash FROM public.request_attachments WHERE hash = ANY($1)", hashes)
		if err != nil {
			return nil, err
		}
		liveHash := make(map[string]bool, len(hashes))
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return nil, err
			}
			liveHash[h] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		for _, cand := range candidates {
			if cand.Hash != "" && liveHash[cand.Hash] {
				referenced[cand.RelPath] = true
			}
		}
	}

	return referenced, nil
}
