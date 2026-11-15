package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// handshakeCommandNames 是连接建立期的协议命令（HELLO / CLIENT / AUTH / SELECT）。
//
// ★ 为什么往返数**不**计它们：这些是**连接开销**，不是被测的「会话数据读取」往返。
//   go-redis v9.23 起，连接初始化里的 CLIENT SETINFO 改走**流水线**下发
//   （v9.20 是单命令），且连接池会不定时新建连接
//   ⇒ 每新建一次连接就凭空多 2 次往返（HELLO + CLIENT 流水线）
//   ⇒ 「量具自证：每会话恰好 4 次往返」被打断（实测 82 ≠ 80）。
//   —— 量的应该是**读会话数据**的往返，不是连了几次 Redis。
var handshakeCommandNames = map[string]bool{
	"hello":  true,
	"client": true,
	"auth":   true,
	"select": true,
}

// isHandshakeCommand 判断单条命令是否属于连接建立期协议命令。
func isHandshakeCommand(cmd redis.Cmder) bool {
	return handshakeCommandNames[strings.ToLower(cmd.Name())]
}

// isHandshakePipeline 判断这条流水线是否**只**由握手命令组成。
func isHandshakePipeline(cmds []redis.Cmder) bool {
	if len(cmds) == 0 {
		return false
	}
	for _, cmd := range cmds {
		if !isHandshakeCommand(cmd) {
			return false
		}
	}
	return true
}

// roundTripCounter 数的是**网络往返**，不是命令条数。
//
//	ProcessHook         → 单命令执行，一次往返
//	ProcessPipelineHook → 一批管道命令执行，一次往返
//
// ★ 为什么不能数「命令条数」：批量化前后 HGETALL 的**条数不变**（每会话仍是
// 1 条），变的只是「几条命令塞进一次往返」。数命令数会得出「优化无效」的
// 错误结论——量具必须对着被优化的那个维度。
type roundTripCounter struct {
	roundTrips int64
	commands   int64
}

func (c *roundTripCounter) DialHook(next redis.DialHook) redis.DialHook { return next }

func (c *roundTripCounter) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		// 握手命令不计往返（见 handshakeCommandNames 的说明）。
		if !isHandshakeCommand(cmd) {
			c.roundTrips++
			c.commands++
		}
		return next(ctx, cmd)
	}
}

func (c *roundTripCounter) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		// 纯握手流水线（CLIENT SETINFO 等）不计往返。
		if !isHandshakePipeline(cmds) {
			c.roundTrips++
			c.commands += int64(len(cmds))
		}
		return next(ctx, cmds)
	}
}

func (c *roundTripCounter) reset() {
	c.roundTrips = 0
	c.commands = 0
}

func newBatchSessionManager(t *testing.T) (*Manager, *miniredis.Miniredis, *roundTripCounter) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	counter := &roundTripCounter{}
	client.AddHook(counter)
	t.Cleanup(func() { _ = client.Close() })
	return NewManager(NewRedisClientFromClient(client), time.Hour), mr, counter
}

func seedSessions(t *testing.T, mgr *Manager, n int, tenantID string) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		sess, err := mgr.Create(ctx, 7, tenantID, "device-1")
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		ids = append(ids, sess.SessionID)
	}
	return ids
}

// TestGetBatchCollapsesPerSessionRoundTrips 是本轮优化的**主判据**：
// 批量读的往返数必须显著少于逐会话读的往返数。
//
// ★ 判据用「往返数」而不是「命令数」：批量化不改变命令条数，只改变往返次数。
func TestGetBatchCollapsesPerSessionRoundTrips(t *testing.T) {
	mgr, _, counter := newBatchSessionManager(t)
	ctx := context.Background()
	const n = 20
	ids := seedSessions(t, mgr, n, "tenant-1")

	// 旧路径：逐会话 Get + GetStats（两者读同一个 hash）
	counter.reset()
	for _, id := range ids {
		if _, err := mgr.Get(ctx, id); err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if _, err := mgr.GetStats(ctx, id); err != nil {
			t.Fatalf("GetStats failed: %v", err)
		}
	}
	perSession := counter.roundTrips
	// 量具自证：SafeHGetAll = TYPE + HGETALL = 2 次往返 ⇒ 每会话 4 次
	if want := int64(4 * n); perSession != want {
		t.Fatalf("per-session round trips = %d, want %d (4 per session: TYPE+HGETALL, twice)", perSession, want)
	}

	// 新路径：一次批量读
	counter.reset()
	sessions, stats, err := mgr.GetBatch(ctx, ids)
	if err != nil {
		t.Fatalf("GetBatch failed: %v", err)
	}
	batch := counter.roundTrips

	if len(sessions) != n {
		t.Fatalf("batch sessions = %d, want %d", len(sessions), n)
	}
	if len(stats) != n {
		t.Fatalf("batch stats = %d, want %d", len(stats), n)
	}
	if batch >= perSession {
		t.Fatalf("batch round trips = %d, per-session round trips = %d; batch must be strictly fewer", batch, perSession)
	}
	// 批量路径 = 一次 TYPE 流水线 + 一次 HGETALL 流水线 = 2 次往返
	if batch != 2 {
		t.Fatalf("batch round trips = %d, want 2 (one TYPE pipeline + one HGETALL pipeline)", batch)
	}
	t.Logf("round trips: per-session=%d batch=%d (n=%d)", perSession, batch, n)
}

// TestGetBatchMatchesPerSessionReads 是**对拍**：批量路径与逐键路径必须给出
// 同样的会话与同样的统计量。批量优化只许改往返数，不许改语义。
func TestGetBatchMatchesPerSessionReads(t *testing.T) {
	mgr, _, _ := newBatchSessionManager(t)
	ctx := context.Background()
	ids := seedSessions(t, mgr, 6, "tenant-1")

	sessions, stats, err := mgr.GetBatch(ctx, ids)
	if err != nil {
		t.Fatalf("GetBatch failed: %v", err)
	}
	for _, id := range ids {
		wantSess, err := mgr.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		wantStats, err := mgr.GetStats(ctx, id)
		if err != nil {
			t.Fatalf("GetStats failed: %v", err)
		}
		gotSess, ok := sessions[id]
		if !ok {
			t.Fatalf("batch missing session %s", id)
		}
		gotStats, ok := stats[id]
		if !ok {
			t.Fatalf("batch missing stats %s", id)
		}
		if gotSess.SessionID != wantSess.SessionID ||
			gotSess.TenantID != wantSess.TenantID ||
			gotSess.APIKeyID != wantSess.APIKeyID ||
			gotSess.Status != wantSess.Status ||
			gotSess.Title != wantSess.Title {
			t.Fatalf("session mismatch for %s: batch=%+v per-key=%+v", id, gotSess, wantSess)
		}
		if *gotStats != *wantStats {
			t.Fatalf("stats mismatch for %s: batch=%+v per-key=%+v", id, gotStats, wantStats)
		}
	}
}

// TestGetBatchSkipsWrongTypeKeyWithoutDroppingOthers 钉住**降级语义**：
// 一个坏键（类型不是 hash）不许打空整批——好键必须照常返回。
// 这正是批量路径必须与逐键路径对齐的地方（逐键路径是「坏键被跳过」）。
func TestGetBatchSkipsWrongTypeKeyWithoutDroppingOthers(t *testing.T) {
	mgr, mr, _ := newBatchSessionManager(t)
	ctx := context.Background()
	ids := seedSessions(t, mgr, 5, "tenant-1")

	badID := "bad-type-session"
	mr.Set("session:"+badID, "i-am-a-string")

	sessions, stats, err := mgr.GetBatch(ctx, ids)
	if err != nil {
		t.Fatalf("GetBatch must not fail on a wrong-type key: %v", err)
	}
	if len(sessions) != len(ids) {
		t.Fatalf("sessions = %d, want %d (wrong-type key must not drop the others)", len(sessions), len(ids))
	}
	if len(stats) != len(ids) {
		t.Fatalf("stats = %d, want %d", len(stats), len(ids))
	}
	if _, ok := sessions[badID]; ok {
		t.Fatalf("wrong-type key must not appear in results")
	}
}

// TestGetBatchSkipsMissingKey 钉住「键不存在 ⇒ 跳过，不报错」。
func TestGetBatchSkipsMissingKey(t *testing.T) {
	mgr, _, _ := newBatchSessionManager(t)
	ctx := context.Background()
	ids := seedSessions(t, mgr, 3, "tenant-1")
	ids = append(ids, "no-such-session")

	sessions, stats, err := mgr.GetBatch(ctx, ids)
	if err != nil {
		t.Fatalf("GetBatch failed: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("sessions = %d, want 3", len(sessions))
	}
	if len(stats) != 3 {
		t.Fatalf("stats = %d, want 3", len(stats))
	}
}

// TestGetBatchDeduplicatesIDs 钉住「同一个 id 重复出现只读一次」。
func TestGetBatchDeduplicatesIDs(t *testing.T) {
	mgr, _, counter := newBatchSessionManager(t)
	ctx := context.Background()
	ids := seedSessions(t, mgr, 2, "tenant-1")
	dup := append(append([]string{}, ids...), ids...)

	counter.reset()
	sessions, _, err := mgr.GetBatch(ctx, dup)
	if err != nil {
		t.Fatalf("GetBatch failed: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}
	// 去重后仍是一次 TYPE 流水线 + 一次 HGETALL 流水线
	if counter.roundTrips != 2 {
		t.Fatalf("round trips = %d, want 2 (duplicates must not add round trips)", counter.roundTrips)
	}
}
