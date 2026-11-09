package v2

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
)

var errColdCacheCapacity = errors.New("cached session metadata exceeds bounded recovery capacity")

func readCacheFile(path string, limit int) ([]byte, error) {
	if limit == 0 {
		return os.ReadFile(path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat() // The opened inode, not a stale index/path Stat.
	if err != nil {
		return nil, err
	}
	if info.Size() > int64(limit) {
		return nil, errColdCacheCapacity
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if len(data) > limit {
		return nil, errColdCacheCapacity
	}
	return data, err
}

func (fc *FileCache) GetContext(ctx context.Context, tenantID, sessionID string) (*SessionStateV2, error) {
	limit := 0
	if strictColdRead(ctx) {
		limit = strictColdMetaBytes
	}
	return fc.get(tenantID, sessionID, limit)
}

func (c *CompressionMetaCache) GetContext(ctx context.Context, tenantID, sessionID string) (*SessionStateV2, error) {
	if !strictColdRead(ctx) {
		return c.Get(tenantID, sessionID), nil
	}
	key := cacheKey(tenantID, sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.items[key]
	if entry == nil {
		return nil, nil
	}
	if !entry.expiresAt.IsZero() && !time.Now().Before(entry.expiresAt) {
		c.lru.remove(entry.node)
		delete(c.items, key)
		return nil, nil
	}
	if !contextbudget.MetadataFits(entry.state, strictColdMetaBytes) {
		return nil, errColdCacheCapacity // Check before cloneMap's JSON allocation.
	}
	c.lru.moveToFront(entry.node)
	return cloneSessionStateV2(entry.state), nil
}

func boundedStateSQL(query string) string {
	return `WITH bounded_state(turn_no,ts,strategy,meta,prompt_tokens,completion_tokens,injection_verdict,output_verdict) AS (` + query + `)
SELECT turn_no,ts,strategy,
 CASE WHEN COALESCE(octet_length(meta::text),0) <= $3 THEN meta ELSE NULL END,
 prompt_tokens,completion_tokens,injection_verdict,output_verdict,
 COALESCE(octet_length(meta::text)>$3,FALSE) AS oversized
FROM bounded_state`
}
