package compression

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type memoReadCounter struct {
	net.Conn
	bytes *atomic.Int64
}

func (c memoReadCounter) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.bytes.Add(int64(n))
	return n, err
}

func TestStrictMemoRejectsBeforeRedisTransfersLargeValue(t *testing.T) {
	base := newSharedTestRedis(t)
	require.NoError(t, base.client.Set(context.Background(), "big-memo", strings.Repeat("x", 4<<20), 0).Err())
	var transferred atomic.Int64
	client := redis.NewClient(&redis.Options{Addr: base.server.Addr(), MaxRetries: -1, Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return memoReadCounter{Conn: conn, bytes: &transferred}, nil
	}})
	t.Cleanup(func() { _ = client.Close() })
	raw, found, err := (redisMemoStore{rdb: client}).get(strictCacheContext(), "big-memo")
	require.Error(t, err)
	require.False(t, found)
	require.Nil(t, raw)
	require.Less(t, transferred.Load(), int64(16<<10), "GET transferred the payload before rejecting it")
}

func TestStrictMemoKeepsSafeResultsAndRejectsOversizedBody(t *testing.T) {
	base := newSharedTestRedis(t)
	memo := NewResultMemo(base.client, DefaultMemoTTL)
	value := fullMemoValue()
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, memo.Set(strictCacheContext(), testMemoParts(), body, value))
	got, err := memo.Get(strictCacheContext(), testMemoParts(), body)
	require.NoError(t, err)
	require.NotNil(t, got)
	value.CompressedBody = []byte(strings.Repeat("x", (1<<20)+1))
	require.Error(t, memo.Set(strictCacheContext(), testMemoParts(), body, value))
}
