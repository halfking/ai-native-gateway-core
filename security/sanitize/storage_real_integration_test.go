//go:build integration

package sanitize

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func auditRealRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("SESSION_AUDIT_REDIS_ADDR")
	if addr == "" {
		t.Skip("set SESSION_AUDIT_REDIS_ADDR to a disposable loopback Redis")
	}
	host, _, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	ip := net.ParseIP(host)
	require.True(t, ip != nil && ip.IsLoopback(), "audit requires a disposable loopback Redis")
	r := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = r.Close() })
	require.NoError(t, r.Ping(context.Background()).Err())
	return r
}

// Separate OS processes exercise Redis Lua/leases without shared Go mutexes.
func TestStorageAuditSanitizeProcesses(t *testing.T) {
	r := auditRealRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tenant, session := "audit-process", fmt.Sprintf("audit-%d", time.Now().UnixNano())
	mapKey := sanitizeMapKey(tenant, session)
	offsetKey := sanitizeOffsetKey(tenant, session)
	t.Cleanup(func() {
		r.Del(context.Background(), mapKey, offsetKey, offsetKey+":lock", sanitizeGenerationKey(tenant, session))
	})
	exe, err := os.Executable()
	require.NoError(t, err)
	var wg sync.WaitGroup
	for worker := 0; worker < 2; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestStorageAuditSanitizeWorker$", "-test.v")
			cmd.Env = append(os.Environ(), "SESSION_AUDIT_WORKER="+fmt.Sprint(worker), "SESSION_AUDIT_SESSION="+session)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("worker %d: %v\n%s", worker, err, output)
			} else {
				t.Logf("worker %d: %s", worker, output)
			}
		}(worker)
	}
	wg.Wait()
	values, err := r.HGetAll(ctx, mapKey).Result()
	require.NoError(t, err)
	require.Len(t, values, 32)
	for i := 0; i < 32; i++ {
		value := fmt.Sprintf("138%08d", i)
		found := false
		for _, got := range values {
			if got == value {
				found = true
			}
		}
		require.True(t, found, "missing synthetic value %d", i)
	}
	require.Equal(t, "32", r.HGet(ctx, offsetKey, "phone").Val())
	require.NotEmpty(t, r.Get(ctx, sanitizeGenerationKey(tenant, session)).Val())
	t.Log("two independent processes: 32 unique values, 32 mappings, high-water 32")
}

func TestStorageAuditSanitizeWorker(t *testing.T) {
	worker := os.Getenv("SESSION_AUDIT_WORKER")
	if worker == "" {
		t.Skip("parent test starts this helper in a separate process")
	}
	r := auditRealRedis(t)
	mw := newOffsetTestMiddleware(t, r, NewPatternDetector())
	start := 0
	if worker == "1" {
		start = 16
	}
	retries := 0
	for i := start; i < start+16; i++ {
		value := fmt.Sprintf("138%08d", i)
		deadline := time.Now().Add(10 * time.Second)
		for {
			status, body, called := callOffsetTestMiddleware(mw, "audit-process", os.Getenv("SESSION_AUDIT_SESSION"), value)
			if status == 503 {
				// Contention is an intentional fail-closed admission response.
				// Retrying is safe only if no provider dispatch took place.
				require.False(t, called)
				require.Empty(t, body)
				require.True(t, time.Now().Before(deadline), "admission retry deadline")
				retries++
				time.Sleep(5 * time.Millisecond)
				continue
			}
			require.Equal(t, 200, status)
			require.True(t, called)
			require.False(t, strings.Contains(body, value))
			break
		}
	}
	t.Logf("accepted=16 fail_closed_admission_retries=%d", retries)
}

func TestStorageAuditRealRedisLeaseLoss(t *testing.T) {
	r := auditRealRedis(t)
	ctx := context.Background()
	tenant, session := "audit-lease", fmt.Sprintf("audit-%d", time.Now().UnixNano())
	entered, resume := make(chan struct{}), make(chan struct{})
	stale := newOffsetTestMiddleware(t, r, slowOffsetDetector{entered: entered, resume: resume})
	fresh := newOffsetTestMiddleware(t, r, NewPatternDetector())
	offsetKey := sanitizeOffsetKey(tenant, session)
	mapKey := sanitizeMapKey(tenant, session)
	t.Cleanup(func() { r.Del(ctx, mapKey, offsetKey, offsetKey+":lock", sanitizeGenerationKey(tenant, session)) })
	result := make(chan int, 1)
	go func() {
		status, _, called := callOffsetTestMiddleware(stale, tenant, session)
		if called {
			status = -status
		}
		result <- status
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("detector did not start")
	}
	// Simulate eviction/lease loss at the real Redis boundary.
	require.NoError(t, r.Del(ctx, offsetKey+":lock").Err())
	status, _, called := callOffsetTestMiddleware(fresh, tenant, session, "13912345678")
	close(resume)
	require.Equal(t, 200, status)
	require.True(t, called)
	require.Equal(t, 503, <-result)
	require.Equal(t, map[string]string{"{SENSITIVE:phone:1}": "13912345678"}, r.HGetAll(ctx, mapKey).Val())
}
