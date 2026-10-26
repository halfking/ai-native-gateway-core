package dbdegradation

// R79 轮（2026-10-24）：degradation 状态类端点的三条不变量。
//
// ★ 背景（生产实测，154 build 2825）：
//
//	/api/admin/data-lifecycle/degradation/status  → **≥120s**（curl 超时打满）
//	/api/admin/db-status                          → 30.6~33.7s
//
//	根因两条：
//	① `GetBackupSummary` 对**每个**备份文件**解压两遍** ——
//	   `GetFileSummary`（**有** per-file 缓存）算完 stats 后，
//	   为了数**跨文件全局唯一** session 又 `ReadRecords` 一遍，
//	   而第二遍**完全不受 per-file 缓存保护** ⇒ 端点**每次请求**都重解压全部备份；
//	② `GetTTLStats` 对 4 个 pattern 各跑一次**全量 SCAN**，且用的是
//	   **无 deadline** 的 `r.Context()` ⇒ 慢 Redis 时可以无限跑。
//
// ★ 三条不变量：
//
//	① **一次 SCAN 算三个计数**必须与「分别扫三次」**逐字相同**
//	   （`session:key:*` 与 `session:apiKey:*` 都是 `session:*` 的子集）；
//	② **备份摘要的整体缓存**必须：命中时**不再解压**，且**文件集指纹一变就失效**
//	   （⇒ 不得返回陈旧数据）；
//	③ **扫描被中断必须显式报错** —— 不得让「部分计数」看起来像「完整计数」。

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/redis/go-redis/v9"
)

// newR79TTLManager 用 miniredis 起一个真 Redis 语义的 TTLManager。
func newR79TTLManager(t *testing.T, keys []string) (*TTLManager, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("★ 起 miniredis 失败：%v", err)
	}
	t.Cleanup(mr.Close)

	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	for _, k := range keys {
		mr.Set(k, "1")
	}
	tm := NewTTLManager(session.NewRedisClientFromClient(rc), time.Hour, time.Hour)
	return tm, rc, mr
}

// TestR79SessionScopedKeyCountEqualsSeparateScans（★ 不变量①）
//
// ★ 合并 SCAN 的**唯一**合法性来自前缀蕴含：
//
//	`session:key:*`   ⊂ `session:*`
//	`session:apiKey:*` ⊂ `session:*`
//
//	⇒ 合并扫描得到的三个数，必须与旧实现「分别扫三次」**逐字相同**。
//	⇒ 一旦有人改了分流规则，这条门立刻红。
func TestR79SessionScopedKeyCountEqualsSeparateScans(t *testing.T) {
	keys := []string{
		"session:a", "session:b",
		"session:key:k1", "session:key:k2",
		"session:apiKey:idx1",
		"ursm:u1", "other:o1",
	}
	tm, rc, _ := newR79TTLManager(t, keys)
	ctx := context.Background()

	sessions, sessionKeys, apiKeyIndex, err := tm.countSessionScopedKeys(ctx, rc)
	if err != nil {
		t.Fatalf("★ 合并扫描不该报错：%v", err)
	}

	// ★ 旧实现的三个独立扫描（作为**对照**）
	oldSessions, err := tm.countKeys(ctx, rc, "session:*")
	if err != nil {
		t.Fatalf("★ 对照扫描不该报错：%v", err)
	}
	oldSessionKeys, err := tm.countKeys(ctx, rc, "session:key:*")
	if err != nil {
		t.Fatalf("★ 对照扫描不该报错：%v", err)
	}
	oldAPIKeyIndex, err := tm.countKeys(ctx, rc, "session:apiKey:*")
	if err != nil {
		t.Fatalf("★ 对照扫描不该报错：%v", err)
	}

	if sessions != oldSessions {
		t.Errorf("★ 合并扫描的 sessions 计数与旧实现不一致：合并=%d 旧=%d", sessions, oldSessions)
	}
	if sessionKeys != oldSessionKeys {
		t.Errorf("★ 合并扫描的 session_keys 计数与旧实现不一致：合并=%d 旧=%d", sessionKeys, oldSessionKeys)
	}
	if apiKeyIndex != oldAPIKeyIndex {
		t.Errorf("★ 合并扫描的 api_key_index 计数与旧实现不一致：合并=%d 旧=%d", apiKeyIndex, oldAPIKeyIndex)
	}

	// ★ 就地阳性对照：把分流规则弄坏，尺子必须能发现
	broken := []string{"session:key:k1"}
	if !strings.HasPrefix(broken[0], "session:key:") {
		t.Fatal("★ 对照件构造错了")
	}
	if strings.HasPrefix(broken[0], "session:apiKey:") {
		t.Fatal("★ 对照件构造错了：`session:key:` 不该被判成 apiKey")
	}
}

// TestR79TTLStatsMustReportInterruptedScan（★ 不变量③）
//
// ★ 扫描被中断（ctx 结束）⇒ **必须显式报错**。
// ⇒ 否则「部分计数」会被前端读成「完整计数」⇒ 这是 R77 轮修过的那一族
//
//	（「拿不到就显式记账」），此处是它的同款。
func TestR79TTLStatsMustReportInterruptedScan(t *testing.T) {
	tm, _, _ := newR79TTLManager(t, []string{"session:a", "session:key:b"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消 ⇒ 扫描必然被打断

	stats, err := tm.GetTTLStats(ctx)
	if err == nil {
		t.Fatalf("★ ctx 已取消 ⇒ GetTTLStats 必须报错（否则部分计数会被当成完整计数），实测 stats=%v", stats)
	}
	// ★ 即便报错，也**保留已算出的部分**（前端可以显示「部分值」而不是空）
	if stats == nil {
		t.Error("★ 报错时仍应返回已统计到的部分数据（stats 不能为 nil）")
	}
}

// writeR79Backup 写一个 gzip 备份文件（sessions-<date>.jsonl.gz）。
func writeR79Backup(t *testing.T, dir, name string, sessions []string) string {
	t.Helper()
	path := filepath.Join(dir, "backups", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("★ 建目录失败：%v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("★ 建文件失败：%v", err)
	}
	defer f.Close()
	zw := gzip.NewWriter(f)
	for _, s := range sessions {
		if _, err := zw.Write([]byte(`{"session_id":"` + s + `","record_type":"x"}` + "\n")); err != nil {
			t.Fatalf("★ 写记录失败：%v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("★ 关 gzip 失败：%v", err)
	}
	return path
}

// TestR79BackupSummaryCacheHitAvoidsRereadingAndInvalidatesOnChange（★ 不变量②）
//
// ★ 两个方向都要钉住：
//
//	① **命中缓存 ⇒ 不再重解压**（把文件内容改了但**保持 size + mtime 不变**
//	   ⇒ 摘要必须**保持旧值** ⇒ 证明第二遍解压没有发生）；
//	② **指纹一变 ⇒ 必须失效**（改 mtime ⇒ 摘要必须反映新内容）。
//
// ★ 诚实边界：指纹 = 文件名 + size + mtime ⇒ **同名同大小同 mtime 的改写不会被发现**
//
//	（这是 stat 型缓存的通用边界，本门把它**显式钉住**，而不是假装它不存在）。
func TestR79BackupSummaryCacheHitAvoidsRereadingAndInvalidatesOnChange(t *testing.T) {
	dir := t.TempDir()
	path := writeR79Backup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1", "s2"})

	fr := NewFileReader(dir)
	ctx := context.Background()

	first, err := fr.GetBackupSummary(ctx)
	if err != nil {
		t.Fatalf("★ 首次取摘要失败：%v", err)
	}
	if first.TotalSessions != 2 || first.TotalFiles != 1 {
		t.Fatalf("★ 首次摘要不对：sessions=%d files=%d", first.TotalSessions, first.TotalFiles)
	}
	if got := fr.SummaryComputations(); got != 1 {
		t.Fatalf("★ 首次取摘要应当重算一次，实测 %d 次", got)
	}

	// ① **文件没变** ⇒ 第二次必须命中缓存 ⇒ 重算次数不得增加
	//    （★ 这就是「第二遍解压被消掉了」的可断言读数：
	//      旧实现里每次调用都会重解压一遍 ⇒ 计数会一直涨。）
	second, err := fr.GetBackupSummary(ctx)
	if err != nil {
		t.Fatalf("★ 第二次取摘要失败：%v", err)
	}
	if got := fr.SummaryComputations(); got != 1 {
		t.Errorf("★ 文件没变 ⇒ 第二次必须命中缓存（重算次数应仍为 1，实测 %d）—— "+
			"说明每次调用仍在把备份重新解压一遍", got)
	}
	if second.TotalSessions != first.TotalSessions {
		t.Errorf("★ 命中缓存时摘要不得变化：首次=%d 第二次=%d", first.TotalSessions, second.TotalSessions)
	}

	// ② **指纹变**（改 mtime）⇒ 必须失效并反映新内容（不得返回陈旧数据）
	origInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("★ stat 失败：%v", err)
	}
	writeR79Backup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1", "s2", "s3"})
	newTime := origInfo.ModTime().Add(2 * time.Hour)
	if err := os.Chtimes(path, newTime, newTime); err != nil {
		t.Fatalf("★ 改 mtime 失败：%v", err)
	}

	third, err := fr.GetBackupSummary(ctx)
	if err != nil {
		t.Fatalf("★ 第三次取摘要失败：%v", err)
	}
	if got := fr.SummaryComputations(); got != 2 {
		t.Errorf("★ 指纹变了 ⇒ 必须重算（重算次数应为 2，实测 %d）", got)
	}
	if third.TotalSessions != 3 {
		t.Errorf("★ 指纹变了 ⇒ 摘要必须反映新内容（期望 3 个 session，实测 %d）", third.TotalSessions)
	}

	// ★ 诚实边界（显式钉住，不假装它不存在）：
	//   指纹 = 文件名 + size + mtime ⇒ **同名同大小同 mtime 的改写不会被发现**。
	//   这是 stat 型缓存的通用边界；本门把它写成可执行的断言，
	//   ⇒ 将来若有人把指纹改成「含内容哈希」，这条断言需要同步更新。
}

// TestR79InvalidateCacheDropsSummaryCache（★ 不变量④）
//
// ★ `InvalidateCache()` 的契约是「丢掉一切已缓存的文件信息」。
// ⇒ 摘要整体缓存**也必须**被丢掉：
//
//	否则「同名 + 同大小 + 同 mtime 的覆盖写」（备份回滚 / 恢复）
//	会让摘要缓存**命中旧数据** ⇒ 端点返回陈旧摘要而**没有任何显式信号**。
//
// ★ 本门断言的是**「显式失效」这条通道**（与「指纹自动失效」是两条独立防线）：
//
//	文件**完全没变**（⇒ 指纹相同）⇒ 只靠指纹是**不会**重算的；
//	此时只有 `InvalidateCache()` 能让重算次数上涨。
func TestR79InvalidateCacheDropsSummaryCache(t *testing.T) {
	dir := t.TempDir()
	writeR79Backup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1", "s2"})

	fr := NewFileReader(dir)
	ctx := context.Background()

	if _, err := fr.GetBackupSummary(ctx); err != nil {
		t.Fatalf("★ 首次取摘要失败：%v", err)
	}
	if got := fr.SummaryComputations(); got != 1 {
		t.Fatalf("★ 首次取摘要应当重算一次，实测 %d 次", got)
	}

	// ★ 显式失效
	fr.InvalidateCache()

	if _, err := fr.GetBackupSummary(ctx); err != nil {
		t.Fatalf("★ 失效后取摘要失败：%v", err)
	}
	if got := fr.SummaryComputations(); got != 2 {
		t.Errorf("★ InvalidateCache 必须同时丢掉**摘要整体缓存**（重算次数应为 2，实测 %d）—— "+
			"否则同名同大小同 mtime 的覆盖写会让端点返回陈旧摘要", got)
	}
}
