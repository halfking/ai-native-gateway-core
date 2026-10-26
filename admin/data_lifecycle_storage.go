// Package admin — data_lifecycle_storage.go
//
// 2026-07-01: 存储总览（Storage Overview）端点。
// 解决诉求：
//   - 数据库大小 vs 本机磁盘的对比（独立显示，避免误以为 DB 在本机）
//   - 表级 Top-N 占用排行（pg_stat_user_tables 视角）
//   - 本机日志目录大小（lumberjack 旋转的 .log / .log.gz）
//   - 列存（citus_columnar）单独展示：表数 / 字段数 / 占用
//   - 给运维一个"还剩多少、谁在涨"的总览面板
//
// 实现要点：
//   - 不引入新依赖；跨平台拿磁盘容量（diskusage_unix/windows.go）
//   - 复用 h.db 查询 pg_database_size（标准 PG / Citus 均可用）
//   - 2026-07-03: 移除 pg_total_database_size — Citus 不提供该函数
//     改为 SUM(pg_total_relation_size) 近似估算
//   - 列存统计使用 pg_class.relam = columnar AM 过滤

package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/dbx"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
	"golang.org/x/sync/singleflight"
)

// databaseStorageInfo Postgres 实例维度的存储信息
type databaseStorageInfo struct {
	// pg_database_size：当前连接的 database 自己的数据+索引
	DatabaseBytes int64  `json:"database_bytes"`
	DatabaseHuman string `json:"database_human"`
	// TotalBytes：**用户 schema** 的关系总占用（表 + 索引 + TOAST）。
	// 2026-07-03: Citus 不支持 pg_total_database_size(name)，改为
	//   SUM(pg_total_relation_size) 在所有用户 schema 上求和作为近似。
	//
	// ★ 2026-10-21（R67 / C6 订正）：原注释三处与实现不符，按事实重写 ——
	//   ① 「含 WAL」：**不成立**。pg_total_relation_size 只量关系存储，
	//      WAL / WAL 预留 / 统计信息都不在其中。
	//   ② 「pg_database_size 只算 heap+index」：**不成立**。它是**整库**
	//      占用（含 pg_catalog / information_schema / 空闲空间）。
	//   ③ 「通常 DatabaseBytes ≤ TotalBytes」：**不保证**。两者口径不同
	//      （整库 vs 仅用户 schema），在「系统目录占比大」的库上
	//      （新装库、空库）TotalBytes **必然更小**。
	//      实测（一次性库）：整库 44,996,275 / 用户 schema 42,360,832 /
	//      pg_catalog 单独 43,950,080。
	//   ⇒ 前端把本字段当「用户对象总量」用（StorageOverview.vue 的
	//     columnarPctOfDB 用它作分母）⇒ 这个口径是**自洽**的，别改成整库。
	TotalBytes int64  `json:"total_bytes"`
	TotalHuman string `json:"total_human"`
	// 表 / 索引 / TOAST 三段拆分（来自 pg_class 视角）
	TablesBytes   int64  `json:"tables_bytes"`
	IndexesBytes  int64  `json:"indexes_bytes"`
	ToastBytes    int64  `json:"toast_bytes"`
	FreeBytes     int64  `json:"free_bytes"` // 当前 connection 看不到 PG server 端 fs 剩余（仅客户端视角）
	FreeHuman     string `json:"free_human"`
	ServerVersion string `json:"server_version,omitempty"`
}

// columnarStorageInfo 列存（citus_columnar）统计
// 2026-07-03: 单独展示，方便运维识别压缩效果 + 监控列存合规
type columnarStorageInfo struct {
	// Available：citus_columnar 扩展是否安装
	Available bool `json:"available"`
	// TableCount：使用 columnar 访问方法的表（含分区）数量
	TableCount int `json:"table_count"`
	// TotalColumns：所有 columnar 表的字段数之和（粗粒度：用于了解数据形状）
	TotalColumns int `json:"total_columns"`
	// TotalBytes / TotalHuman：列存占用（含 chunk 元数据与压缩数据）
	TotalBytes int64  `json:"total_bytes"`
	TotalHuman string `json:"total_human"`
	// Note：若扩展未安装或查询失败，在此说明原因（前端可降级展示）
	Note string `json:"note,omitempty"`
}

// filesystemInfo 本机视角的磁盘容量（针对 gateway binary 所在 filesystem）
type filesystemInfo struct {
	Path        string `json:"path"` // 探测路径
	TotalBytes  int64  `json:"total_bytes"`
	TotalHuman  string `json:"total_human"`
	UsedBytes   int64  `json:"used_bytes"`
	UsedHuman   string `json:"used_human"`
	FreeBytes   int64  `json:"free_bytes"`
	FreeHuman   string `json:"free_human"`
	UsedPercent int    `json:"used_percent"` // 0-100
}

// directoryInfo 本机目录维度（lumberjack log dir / attachment dir）
type directoryInfo struct {
	Path        string `json:"path"`
	Exists      bool   `json:"exists"`
	Files       int    `json:"files"`
	SizeBytes   int64  `json:"size_bytes"`
	SizeHuman   string `json:"size_human"`
	OldestMtime int64  `json:"oldest_mtime"` // unix seconds
	NewestMtime int64  `json:"newest_mtime"`
}

// tableSizeInfo 单表大小（来自 pg_total_relation_size）
type tableSizeInfo struct {
	Table         string `json:"table"`
	Schema        string `json:"schema"`
	Rows          int64  `json:"rows"`
	TotalBytes    int64  `json:"total_bytes"`
	TotalHuman    string `json:"total_human"`
	IndexBytes    int64  `json:"index_bytes"`
	ToastBytes    int64  `json:"toast_bytes"`
	ToastHuman    string `json:"toast_human"`
	PercentOfDB   int    `json:"percent_of_db"` // 0-100
	IsPartitioned bool   `json:"is_partitioned"`
}

// storageOverview 总览响应
type storageOverview struct {
	Database    databaseStorageInfo `json:"database"`
	Columnar    columnarStorageInfo `json:"columnar"`
	Filesystem  filesystemInfo      `json:"filesystem"`
	LocalLogs   *directoryInfo      `json:"local_logs,omitempty"`
	Warnings    []string            `json:"warnings"`
	CollectedAt time.Time           `json:"collected_at"`
}

// tableSizesResponse 表级大小响应（Top-N）
type tableSizesResponse struct {
	Tables      []tableSizeInfo `json:"tables"`
	TotalBytes  int64           `json:"total_bytes"`
	TotalHuman  string          `json:"total_human"`
	CollectedAt time.Time       `json:"collected_at"`
}

// handleDataLifecycleStorage GET /api/admin/data-lifecycle/storage
//
// 2026-10-16 性能：本机 PG17.10 实测该端点是 416 个 admin GET 里唯一 >2s 的。
// 逐段计时（\\timing，冷/热各 3 次）：
//
//	pg_database_size + pretty + server_version        51–61ms
//	SUM(pg_total_relation_size) 全用户 schema（3362 关系）1622–1724ms  ← 85%
//	表/索引/TOAST 三段拆分（同一 JOIN 上 3× pg_relation_size）  245–364ms
//	列存扩展探测                                        1.2ms
//	queryFilesystem(".")                               <1ms（单次 statfs，不遍历）
//	queryDirectory(logDir) 回落 cwd 时                 ~250ms（22820 文件）
//
// 结论：**不存在不动缓存就能达标的路**。并发化只能把 2.24s 压到 max 段
// ~1.93s（省 13%，仍超 300ms 五倍以上）；"总览只留 pg_database_size" 会
// 掏空 total_human / tables_bytes / indexes_bytes / toast_bytes —— 这四个
// 字段 StorageOverview.vue 全部在渲染（web/src/views/data-lifecycle/
// StorageOverview.vue:13,22,26,30），属破坏响应契约；把两条 size 查询
// 合成一条也**实测更慢**（1.93s vs 1.87–2.08s，无收益，见
// data_lifecycle_storage_perf_test.go 的对照记录）。
//
// 因此走短 TTL 快照：物理尺寸变化极慢（一次 VACUUM FULL 单表回收通常
// 不超总量的 1%），30s 足以消掉重复聚合，又把最坏陈旧窗口压在 30s 内。
// 热调用直接命中快照，返回体与直查完全同构（同一 struct、同一 warnings）。
func (h *Handler) handleDataLifecycleStorage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), storageRequestTimeout)
	defer cancel()

	writeJSON(w, http.StatusOK, h.storageOverviewSnapshot(ctx))
}

// ── 快照缓存 ────────────────────────────────────────────────────────

// storageOverviewTTL 快照有效期。
//
// 上界取 30s：足以让"打开面板 → 轮询/切页返回"这类重复请求全部命中，
// 又保证最坏陈旧不超过 30s。VACUUM FULL / REINDEX 完成后会显式失效
// （见 runTableMaintenanceJob），不必等 TTL 自然过期。
//
// 声明为 var 而非 const：判据需要能把 TTL 调到毫秒级来证明"过期会重新
// 取数"（TestDataLifecycleStorageTTLExpiryRefetches）。把 TTL 改成极大值
// 会让数据永久陈旧 —— 那是本判据专门要拦的退化。
var storageOverviewTTL = 30 * time.Second

// storageRequestTimeout 单个存储端点的**请求侧**预算。
//
// 与 storageCollectTimeout（采集侧预算）是两件事：请求预算管"用户等多久"，
// 采集预算管"采集能跑多久"。两者的关系必须是 采集 > 请求，否则采集会在
// 跑完之前被掐掉，快照永远建不出来（判据见 TestStorageCollectBudgetOutlivesRequestBudget）。
const storageRequestTimeout = 15 * time.Second

// storageCollectTimeout 单次聚合的独立上限。
//
// 2026-10-20：从 15s 抬到 60s。
//
// 抬的理由不是"让慢查询多跑一会儿"，而是**聚合必须有跑完的机会**：
// 聚合的 ctx 是从 context.WithoutCancel 派生的（采集不挂在请求 ctx 上，
// 发起者随时可能断开），所以**请求 ctx 到期并不会掐死采集**——只有
// storageCollectTimeout 会。原先两者都是 15s，于是：冷页缓存下一次采集要
// 11–17s ⇒ 采集在 15s 被自己掐掉 ⇒ 快照**永远建不出来** ⇒
// /storage/tables 稳定 500（"context deadline exceeded"，duration_ms=15006），
// 而且**每次请求都重试一次、再掐死一次**，没有任何自愈路径。
//
// 抬到 60s 后：采集能跑完 ⇒ 快照建得出来 ⇒ 启动预热（main.go 调
// PrewarmStorageSnapshots）就能成功 ⇒ 请求路径直接命中快照，返回毫秒级。
// 请求自己的 15s 预算不变（用户侧不等待超过 15s），采集超了也只是
// 这次请求先超时返回，下次请求命中已建好的快照。
//
// 采集本身是纯读（pg_class / pg_stat_user_tables + stat 文件），
// 跑 60s 不会积压资源：单飞键保证同一时刻只有一次采集在飞。
const storageCollectTimeout = 60 * time.Second

type storageOverviewSnapshot struct {
	overview  storageOverview
	expiresAt time.Time
}

// storageSnapshotState 进程内快照状态。
//
// 2026-10-16：按 *pgxpool.Pool 分桶，而不是裸进程单例。LESSONS 2026-10-15
// 「probeOne 进程级 MemoryFlight + holdTimeout TTL → 包测串味」记的就是
// 进程级单例把状态漏到别的用例；按 pool 分桶让每个 Handler 实例（= 每个
// 独立连接的测试）各自持有快照，既避免串味，也不用改 handler.go 的结构体。
//
// 单飞键同样按 pool 区分：不同 pool 的并发 miss 不会共享一次聚合。
type storageSnapshotState struct {
	mu     sync.Mutex
	byPool map[*pgxpool.Pool]*storageOverviewSnapshot
	sf     singleflight.Group

	// collect / hits 是真实的生产可观测计数（聚合实打实跑了几次、快照
	// 命中了几次），不只为测试存在：判据靠它证明"聚合只发生了一次"。
	collect atomic.Int64
	hits    atomic.Int64
}

var storageSnapshots = &storageSnapshotState{byPool: make(map[*pgxpool.Pool]*storageOverviewSnapshot)}

// stale 取快照并忽略过期判断（仅供"采集超时但宁可给旧读数"的兜底）。
func (s *storageSnapshotState) stale(pool *pgxpool.Pool) *storageOverview {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap := s.byPool[pool]; snap != nil {
		return &snap.overview
	}
	return nil
}

func (s *storageSnapshotState) put(pool *pgxpool.Pool, ov *storageOverview) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byPool[pool] = &storageOverviewSnapshot{overview: *ov, expiresAt: time.Now().Add(storageOverviewTTL)}
}

// reset 清空全部快照（不碰 singleflight 与计数）。
func (s *storageSnapshotState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byPool = make(map[*pgxpool.Pool]*storageOverviewSnapshot)
}

// resetForTest 测试隔离：快照 + 计数全清。
//
// 刻意**不**重置 s.sf：singleflight.Group 内含互斥量，赋零值会与仍在飞行
// 中的 DoChan 竞争。运维侧的失效走 reset()，同样不碰 Group。
func (s *storageSnapshotState) resetForTest() {
	s.reset()
	s.collect.Store(0)
	s.hits.Store(0)
}

// get 返回快照与其是否仍在 TTL 内（第二个返回值）。
//
// 2026-10-17：与 tableSizesSnapshot 对齐成 stale-while-revalidate，需要一次
// 取出「快照 + 是否过期」而不能分两次加锁（分两次会出现
// 「判定为过期 → 期间被别人刷新 → 仍然拿旧值去触发无谓的后台刷新」）。
func (s *storageSnapshotState) get(pool *pgxpool.Pool) (*storageOverview, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.byPool[pool]
	if snap == nil {
		return nil, false
	}
	return &snap.overview, time.Now().Before(snap.expiresAt)
}

// refreshStorageOverviewAsync 过期后**后台**单飞聚合，不阻塞本次请求。
//
// 与 tableSizesSnapshot.refreshTableSizesAsync 同构：采集 ctx 必须从
// context.WithoutCancel 派生——发起者随时可能断开，不能连带取消共享聚合。
func (h *Handler) refreshStorageOverviewAsync(ctx context.Context) {
	base := context.WithoutCancel(ctx)
	go func() {
		ch := storageSnapshots.sf.DoChan(fmt.Sprintf("storage-overview:%p", h.db), func() (any, error) {
			collectCtx, cancel := context.WithTimeout(base, storageCollectTimeout)
			defer cancel()
			storageSnapshots.collect.Add(1)
			ov := collectStorageOverview(collectCtx, h)
			storageSnapshots.put(h.db, ov)
			return ov, nil
		})
		if res := <-ch; res.Err != nil {
			// 旧读数已经发出去了，这里失败不影响本次响应；但必须留痕，
			// 否则会静默地永远停在旧读数上。
			slog.Warn("storage: background overview refresh failed", "error", res.Err)
		}
	}()
}

// storageOverviewSnapshot 取快照。
//
// 2026-10-17 第二次性能修正（stale-while-revalidate）：
// TTL 快照解决了"重复请求都聚合"，但**过期时仍会同步重算**，
// 而聚合本身要 SUM(pg_total_relation_size) 扫全部 3362 个关系，实测
// 冷页缓存下实测 1.26–2.36s（热缓存下同一 SQL 只要 0.77–0.94s，差别全在
// pg_total_relation_size 要真去 stat 文件）。于是每 30s 仍有一次请求破 2 秒。
//
// 改为：过期不再同步重算，而是**立即返回旧读数 + 后台单飞聚合**。
// 除"进程启动后的第一次"外，任何请求都不再支付聚合成本。
//
// 代价：读数最坏陈旧到 TTL + 一次聚合时长（实测 30 + 0.9 ≈ 31s；冷页缓存
// 下聚合要 2.4s，则最坏约 32s），而非严格 30s。
// 要精确读数的运维用 VACUUM FULL / REINDEX，那会显式失效快照。
//
// 失败语义保持与优化前一致：聚合永远成功（内部各段失败只记 warning），
// 所以本函数不会返回 error，HTTP 状态恒为 200。
func (h *Handler) storageOverviewSnapshot(ctx context.Context) *storageOverview {
	if ov, fresh := storageSnapshots.get(h.db); ov != nil {
		storageSnapshots.hits.Add(1)
		if fresh {
			return ov
		}
		// 已过期：先把旧读数发出去，聚合在后台跑。
		h.refreshStorageOverviewAsync(ctx)
		return ov
	}

	// 无任何快照：必须同步聚合一次（进程启动后的第一个请求）。
	//
	// 聚合用 WithoutCancel 的独立 ctx：发起者被客户端断开/超时时，
	// 不能把其他等待者共享的那一次聚合一起取消掉。等待方仍各自受自己的
	// ctx 约束（见下面的 select）。
	ch := storageSnapshots.sf.DoChan(storageOverviewFlightKey(h.db), func() (any, error) {
		return collectStorageOverviewSnapshot(ctx, h), nil
	})

	select {
	case res := <-ch:
		if res.Err == nil {
			return res.Val.(*storageOverview)
		}
		slog.Warn("storage: snapshot collect failed", "error", res.Err)
	case <-ctx.Done():
		// 等待期间本请求超时。回落到最近一次快照（可能已过 TTL）：
		// 宁可给稍旧的读数，也不要让字段凭空消失。
	}

	if ov := storageSnapshots.stale(h.db); ov != nil {
		return ov
	}
	// 无任何历史快照：沿用优化前的降级形态（200 + warning + 零值字段），
	// 不引入新的 HTTP 状态码。
	return &storageOverview{
		Warnings:    []string{"存储快照采集未完成且无可用缓存，请稍后重试"},
		CollectedAt: time.Now().UTC(),
	}
}

// storageOverviewFlightKey 单飞键。按 pool 区分，与 tableSizesFlightKey 同构，
// 这样并发 miss 不会跨连接共享同一次聚合。
func storageOverviewFlightKey(pool *pgxpool.Pool) string {
	return fmt.Sprintf("storage-overview:%p", pool)
}

// collectStorageOverviewSnapshot 是单飞的采集函数体。
//
// 抽成具名函数而不是留在 storageOverviewSnapshot 里内联，是为了让启动预热
// （PrewarmStorageSnapshots）与请求路径**共用同一份实现与同一个单飞键**。
// 预热若复制一份逻辑，两边迟早漂移，而漂移的表现是"预热采到了、请求路径
// 仍然判定无快照"，也就是那个 >2s 的冷路径又回来了，且没有任何报错。
func collectStorageOverviewSnapshot(ctx context.Context, h *Handler) *storageOverview {
	collectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storageCollectTimeout)
	defer cancel()
	storageSnapshots.collect.Add(1)
	ov := collectStorageOverview(collectCtx, h)
	storageSnapshots.put(h.db, ov)
	return ov
}

// PrewarmStorageSnapshots 在进程启动后**后台**预热两份存储快照。
//
// 为什么需要它（2026-10-17 第四次性能修正）：
// 上面两次 stale-while-revalidate 改造把"每 30s 破一次 2 秒"消掉了，
// 但都留了同一个尾巴——**进程启动后的第一个请求**没有任何快照可读，仍要
// 同步付全额聚合成本。实测 /storage/tables 的进程首调 2.82s、/storage
// 1.75s，其中 /storage/tables 仍**超过任务规定的 2 秒门槛**。
//
// 启动时就把两份快照采好，首调就退化成纯读。代价是启动后多两次后台聚合
// （实测 0.06s / 0.9s CPU+IO，各只一次；冷页缓存下最慢一次测到 2.4s），
// 不阻塞启动、不阻塞任何请求。
//
// 语义要点：
//   - 用 context.WithoutCancel：启动 ctx 早结束也不该把预热连坐取消
//     （否则 DB 慢一点就等于没预热，首调照样破 2 秒）；
//   - 复用请求路径的单飞键：预热途中到来的首个请求会**加入**同一次采集，
//     而不是再打一次库；
//   - 失败只记 warning：预热是优化不是正确性前提，失败后请求路径自己会
//     同步采集（退回到改造前的行为），绝不能因此让端点 500。
func (h *Handler) PrewarmStorageSnapshots(ctx context.Context) {
	if h == nil || h.db == nil {
		return
	}
	base := context.WithoutCancel(ctx)

	go func() {
		ch := storageSnapshots.sf.DoChan(storageOverviewFlightKey(h.db), func() (any, error) {
			return collectStorageOverviewSnapshot(base, h), nil
		})
		if res := <-ch; res.Err != nil {
			slog.Warn("storage: startup prewarm of overview failed", "error", res.Err)
		}
	}()

	go func() {
		ch := tableSizesSnapshots.sf.DoChan(tableSizesFlightKey(h.db), func() (any, error) {
			return collectTableSizesSnapshot(base, h)
		})
		if res := <-ch; res.Err != nil {
			slog.Warn("storage: startup prewarm of table sizes failed", "error", res.Err)
		}
	}()
}

// InvalidateDataLifecycleStorageCache 清空存储总览快照。
//
// 运维入口：任何会改变物理占用的操作（VACUUM FULL / REINDEX）完成后都应
// 调用，否则 UI 最长会显示 storageOverviewTTL（30s）之前的读数。本仓已在内置
// 表级维护任务 runTableMaintenanceJob 里自动调用；本函数供**仓外**改动物理
// 尺寸的操作（如手工 psql 执行 VACUUM FULL、pg_restore、扩展安装）显式失效用。
//
// 只丢快照、不重置 singleflight.Group：本函数由后台维护任务 goroutine 调用，
// 此刻可能有 HTTP 请求正处在 sf.DoChan 里，赋新的 Group 会与之竞争。
func InvalidateDataLifecycleStorageCache() {
	storageSnapshots.reset()
	tableSizesSnapshots.reset()
}

// resetDataLifecycleStorageCacheForTest 测试隔离用：快照与计数全清。
func resetDataLifecycleStorageCacheForTest() {
	storageSnapshots.resetForTest()
	tableSizesSnapshots.resetForTest()
}

// dataLifecycleStorageCollectCount 快照聚合的真实执行次数（测试 + 观测）。
func dataLifecycleStorageCollectCount() int64 { return storageSnapshots.collect.Load() }

// dataLifecycleStorageCacheHitCount 快照命中次数（测试 + 观测）。
func dataLifecycleStorageCacheHitCount() int64 { return storageSnapshots.hits.Load() }

// ── Top-N 大表快照缓存 ───────────────────────────────────────────────
//
// 与上面的总览快照同构，但**状态与计数独立**：两个端点的慢路径成因不同
// （总览是全库 SUM，这里是 ORDER BY 必须对全部 440 张表逐个算
// pg_total_relation_size），耦合计数会让"聚合了几次"这条判据说不清是谁触发的。
//
// 失效是共享的：InvalidateDataLifecycleStorageCache 一并清掉，两者都由
// VACUUM FULL / REINDEX 改变物理占用。

// tableSizesMaxLimit /storage/tables 允许的最大 limit，同时也是快照一次
// 采集的行数。与 handleDataLifecycleTableSizes 里的 `n <= 200` 上界同源。
const tableSizesMaxLimit = 200

// tableSizesTTL 快照有效期，取值口径与 storageOverviewTTL 一致（30s）。
// 声明为 var：判据要把它压到毫秒级来证明"到期会重新取数"（与总览同理）。
var tableSizesTTL = 30 * time.Second

type tableSizesSnapshot struct {
	rows        []tableSizeInfo
	collectedAt time.Time
	expiresAt   time.Time
}

type tableSizesSnapshotState struct {
	mu     sync.Mutex
	byPool map[*pgxpool.Pool]*tableSizesSnapshot
	sf     singleflight.Group

	collect atomic.Int64
	hits    atomic.Int64
}

var tableSizesSnapshots = &tableSizesSnapshotState{byPool: make(map[*pgxpool.Pool]*tableSizesSnapshot)}

func (s *tableSizesSnapshotState) get(pool *pgxpool.Pool, fresh bool) *tableSizesSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.byPool[pool]
	if snap == nil {
		return nil
	}
	if fresh && !time.Now().Before(snap.expiresAt) {
		return nil
	}
	return snap
}

func (s *tableSizesSnapshotState) put(pool *pgxpool.Pool, snap *tableSizesSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byPool[pool] = snap
}

func (s *tableSizesSnapshotState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byPool = make(map[*pgxpool.Pool]*tableSizesSnapshot)
}

// resetTableSizesForTest 测试隔离：快照 + 计数全清（同样不碰 sf.Group，
// 理由见 storageSnapshotState.resetForTest）。
func (s *tableSizesSnapshotState) resetForTest() {
	s.reset()
	s.collect.Store(0)
	s.hits.Store(0)
}

// tableSizesCollectCount / tableSizesHitCount 真实计数（判据 + 观测）。
func tableSizesCollectCount() int64 { return tableSizesSnapshots.collect.Load() }
func tableSizesHitCount() int64     { return tableSizesSnapshots.hits.Load() }

// sliceTableSizes 从采满的 Top-N 快照里切出前 limit 行，并按"对 Top-limit
// 求和"的原口径重算 total / 占比。
//
// queryTableSizes 的后处理是对**本次 limit** 求和（totalBytes、PercentOfDB），
// 所以切片时必须重算，不能直接沿用采满时的数字——否则 limit=20 会显示
// "对 200 行求和的占比"，与优化前不同。
func sliceTableSizes(rows []tableSizeInfo, limit int) ([]tableSizeInfo, int64, string) {
	if limit > len(rows) {
		limit = len(rows)
	}
	out := make([]tableSizeInfo, limit)
	copy(out, rows[:limit])

	var totalBytes int64
	for i := range out {
		out[i].PercentOfDB = 0 // 必须清零：不能带着采满时的占比出去
		totalBytes += out[i].TotalBytes
	}
	for i := range out {
		if totalBytes > 0 {
			out[i].PercentOfDB = int(out[i].TotalBytes * 100 / totalBytes)
		}
	}
	return out, totalBytes, humanBytes(totalBytes)
}

// tableSizesSnapshot 取 Top-N 快照：命中直接切片，否则单飞采一次满量。
//
// 失败语义保持与优化前一致：采集出错即返回 error，由 handler 照旧写 500
// （这个端点优化前就是 500，不改成 200 降级，避免契约漂移）。
// tableSizesResponseFrom 把快照按 limit 切片成响应。
func tableSizesResponseFrom(snap *tableSizesSnapshot, limit int) tableSizesResponse {
	tables, total, human := sliceTableSizes(snap.rows, limit)
	return tableSizesResponse{
		Tables: tables, TotalBytes: total, TotalHuman: human,
		CollectedAt: snap.collectedAt,
	}
}

// collectTableSizesSnapshot 真正打库的一次采集（单飞的函数体）。
//
// 一次采满 tableSizesMaxLimit 行，所有 limit 都从它切片。
func collectTableSizesSnapshot(ctx context.Context, h *Handler) (*tableSizesSnapshot, error) {
	collectCtx, cancel := context.WithTimeout(ctx, storageCollectTimeout)
	defer cancel()
	tableSizesSnapshots.collect.Add(1)
	rows, _, _, err := queryTableSizes(collectCtx, h, tableSizesMaxLimit)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	snap := &tableSizesSnapshot{
		rows:        rows,
		collectedAt: now.UTC(),
		expiresAt:   now.Add(tableSizesTTL),
	}
	tableSizesSnapshots.put(h.db, snap)
	return snap, nil
}

// tableSizesFlightKey 单飞键，按 pool 区分（口径同 storageOverviewFlightKey）。
func tableSizesFlightKey(pool *pgxpool.Pool) string {
	return fmt.Sprintf("table-sizes:%p", pool)
}

// refreshTableSizesAsync 过期后**后台**单飞刷新，不阻塞本次请求。
//
// 关键点：采集 ctx 不能挂在调用方的请求 ctx 上——发起者随时可能超时/断开，
// 那样会连带取消这次共享采集，让其他等待者也拿不到新快照。
// 故用 context.WithoutCancel + 独立超时。
//
// 单飞键沿用同一个 Group，所以并发过期只会真正采集一次。
func (h *Handler) refreshTableSizesAsync(ctx context.Context) {
	base := context.WithoutCancel(ctx)
	go func() {
		ch := tableSizesSnapshots.sf.DoChan(tableSizesFlightKey(h.db), func() (any, error) {
			return collectTableSizesSnapshot(base, h)
		})
		res := <-ch
		if res.Err != nil {
			// 后台刷新失败不影响本次响应（旧快照已经发出去了），
			// 但要让运维看得见，否则会静默地永远停在旧读数上。
			slog.Warn("storage: background table sizes refresh failed", "error", res.Err)
		}
	}()
}

// tableSizesSnapshot 取 Top-N 快照。
//
// 2026-10-17 第三次性能修正（stale-while-revalidate）：
// TTL 快照解决了"重复请求都打库"，但**冷路径本身仍是 2.35s**
// （ORDER BY pg_total_relation_size 必须对全部 440 个关系逐个算大小才能排序，
//
//	LIMIT 省不掉），于是每 30s 仍有一次请求破 2 秒。
//
// 改为：过期不再同步重算，而是**立即返回旧读数 + 后台单飞刷新**。
// 于是除"进程启动后的第一次"外，任何请求都不再支付冷路径成本。
//
// 代价（已评估、属可接受）：读数最坏可陈旧到 TTL + 一次采集时长
// （实测热缓存 0.06s、冷缓存 2.4s，故最坏 30.1–32.4s），而非严格的 30s。
// 运维要精确读数时用 VACUUM FULL / REINDEX，那会显式失效快照。
//
// 失败语义不变：无任何历史快照时仍同步采集一次（返回 error → handler 照旧 500）；
// 采集出错且有旧快照时仍返回旧读数。
func (h *Handler) tableSizesSnapshot(ctx context.Context, limit int) (tableSizesResponse, error) {
	if snap := tableSizesSnapshots.get(h.db, false); snap != nil {
		tableSizesSnapshots.hits.Add(1)
		if time.Now().Before(snap.expiresAt) {
			return tableSizesResponseFrom(snap, limit), nil
		}
		h.refreshTableSizesAsync(ctx)
		return tableSizesResponseFrom(snap, limit), nil
	}

	// 无任何快照：必须同步采一次（进程启动后的第一个请求）。
	ch := tableSizesSnapshots.sf.DoChan(fmt.Sprintf("table-sizes:%p", h.db), func() (any, error) {
		return collectTableSizesSnapshot(context.WithoutCancel(ctx), h)
	})

	select {
	case res := <-ch:
		if res.Err == nil {
			return tableSizesResponseFrom(res.Val.(*tableSizesSnapshot), limit), nil
		}
		// 采集失败但期间有别的请求写进了快照：给新读数，不报错。
		if snap := tableSizesSnapshots.get(h.db, false); snap != nil {
			tableSizesSnapshots.hits.Add(1)
			return tableSizesResponseFrom(snap, limit), nil
		}
		return tableSizesResponse{}, res.Err
	case <-ctx.Done():
		if snap := tableSizesSnapshots.get(h.db, false); snap != nil {
			tableSizesSnapshots.hits.Add(1)
			return tableSizesResponseFrom(snap, limit), nil
		}
		return tableSizesResponse{}, ctx.Err()
	}
}

// collectStorageOverview 真正跑四段聚合的慢路径（原 handler 主体，逻辑未改）。
func collectStorageOverview(ctx context.Context, h *Handler) *storageOverview {
	resp := &storageOverview{
		Warnings:    []string{},
		CollectedAt: time.Now().UTC(),
	}

	// 1. Database size
	dbInfo, dbErr := queryDatabaseStorage(ctx, h)
	if dbErr != nil {
		slog.Warn("storage: db query failed", "error", dbErr)
		resp.Warnings = append(resp.Warnings, "数据库大小查询失败: "+dbErr.Error())
	} else {
		resp.Database = *dbInfo
	}

	// 1b. 列存统计（独立展示）—— 失败不阻塞主流程，记 warning
	colInfo, colErr := queryColumnarStorageSafe(ctx, h)
	if colErr != nil {
		slog.Warn("storage: columnar query failed", "error", colErr)
		resp.Warnings = append(resp.Warnings, "列存统计查询失败: "+colErr.Error())
	} else {
		resp.Columnar = colInfo
	}

	// 2. Filesystem
	fsInfo, fsErr := queryFilesystem(".")
	if fsErr != nil {
		slog.Warn("storage: fs query failed", "error", fsErr)
		resp.Warnings = append(resp.Warnings, "本机磁盘查询失败: "+fsErr.Error())
	} else {
		resp.Filesystem = *fsInfo
	}

	// 3. 本机日志目录（来自 slog 包探测）
	logDir, logErr := resolveLogDir()
	if logErr == nil {
		dirInfo := queryDirectory(logDir)
		resp.LocalLogs = &dirInfo
	}

	// 4. 横向 warning：DB 占比过大、磁盘压力大、归档未做
	if resp.Database.TotalBytes > 0 && resp.Filesystem.TotalBytes > 0 {
		ratio := float64(resp.Database.TotalBytes) / float64(resp.Filesystem.TotalBytes)
		if ratio > 5.0 {
			resp.Warnings = append(resp.Warnings,
				"数据库总大小超过本机磁盘容量的 5 倍 — 表明 DB 部署在独立节点")
		}
	}
	if resp.Filesystem.UsedPercent >= 90 {
		resp.Warnings = append(resp.Warnings, "本机磁盘已用 ≥ 90%，请检查日志/缓存/附件")
	}
	if resp.LocalLogs != nil && resp.LocalLogs.Exists && resp.LocalLogs.SizeBytes > 5<<30 {
		// > 5GB
		resp.Warnings = append(resp.Warnings,
			"本机日志目录已超过 5GB，建议调小 log.max_size_mb / log.max_age_days")
	}

	return resp
}

// handleDataLifecycleTableSizes GET /api/admin/data-lifecycle/storage/tables?limit=20
func (h *Handler) handleDataLifecycleTableSizes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		// 上界用 tableSizesMaxLimit 而不是字面量：它同时是快照的采集行数，
		// 两处一旦漂移，"Top-N 是快照前缀"的前提会在 N 上限处悄悄失效。
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= tableSizesMaxLimit {
			limit = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), storageRequestTimeout)
	defer cancel()

	resp, err := h.tableSizesSnapshot(ctx, limit)
	if err != nil {
		slog.Warn("storage: table sizes query failed", "error", err)
		writeInternalErr(w, "查询表大小失败", err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// ── helpers ────────────────────────────────────────────────────────

// queryDatabaseStorage 查询 PG 当前 database 的三段大小 + server version
//
// 2026-07-03: Citus 不支持 pg_total_database_size(name)，报错：
//
//	ERROR: function pg_total_database_size(name) does not exist (SQLSTATE 42883)
//
// 改用 SUM(pg_total_relation_size) 在所有用户 schema 上求和作为 TotalBytes 近似。
// 这相当于"所有表(含索引、TOAST)占用的字节总和"，与原 pg_total_database_size
// 的主要差异是：不含 WAL / pg_xlog / 统计信息。运维角度够用。
func queryDatabaseStorage(ctx context.Context, h *Handler) (*databaseStorageInfo, error) {
	out := &databaseStorageInfo{}

	// 1. server_version + pg_database_size（PG / Citus 都支持）
	row := h.db.QueryRow(ctx, `
		SELECT
			pg_database_size(current_database()),
			pg_size_pretty(pg_database_size(current_database())),
			current_setting('server_version')
	`)
	if err := row.Scan(&out.DatabaseBytes, &out.DatabaseHuman, &out.ServerVersion); err != nil {
		return nil, err
	}

	// 2. TotalBytes：SUM(pg_total_relation_size) over user schemas
	//    包含表 + 索引 + TOAST。替代 pg_total_database_size。
	var totalBytes int64
	if err := h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(pg_total_relation_size(c.oid)), 0)::bigint
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog','information_schema')
	`).Scan(&totalBytes); err == nil {
		out.TotalBytes = totalBytes
	} else {
		// 兜底：若该查询也失败（Citus 偶发），至少保证有 pg_database_size 的值
		out.TotalBytes = out.DatabaseBytes
	}
	out.TotalHuman = humanBytes(out.TotalBytes)

	// 3. 表 / 索引 / TOAST 拆分（pg_class 视角）
	rows, err := h.db.Query(ctx, `
		SELECT
			COALESCE(SUM(pg_relation_size(c.oid)) FILTER (WHERE c.relkind IN ('r','p')), 0)::bigint,
			COALESCE(SUM(pg_relation_size(c.oid)) FILTER (WHERE c.relkind = 'i'), 0)::bigint,
			COALESCE(SUM(pg_relation_size(c.oid)) FILTER (WHERE c.relkind = 't'), 0)::bigint
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog','information_schema')
	`)
	if err == nil {
		defer rows.Close()
		if rows.Next() {
			_ = rows.Scan(&out.TablesBytes, &out.IndexesBytes, &out.ToastBytes)
		}
	}

	return out, nil
}

// queryColumnarStorageSafe 列存统计的安全包装（不阻塞主流程）
//
// 2026-07-03: 列存查询是 advisory 性质，失败应降级而非阻塞。
// 返回 (colInfo, err) 而非 panic，便于 handler 收 warning。
func queryColumnarStorageSafe(ctx context.Context, h *Handler) (columnarStorageInfo, error) {
	out := columnarStorageInfo{Available: false}
	if h == nil || h.db == nil {
		out.Note = "数据库连接未就绪"
		return out, nil
	}
	return queryColumnarStorage(ctx, h), nil
}

// queryColumnarStorage 查询列存（citus_columnar）表统计
//
// 2026-07-03: 单独统计列存，方便运维识别哪些表已转列存。
// 关键检测：c.relam = (SELECT oid FROM pg_am WHERE amname = 'columnar')
// 若 citus_columnar 扩展未安装，Available=false + Note 提示。
func queryColumnarStorage(ctx context.Context, h *Handler) columnarStorageInfo {
	out := columnarStorageInfo{Available: false}

	// 先确认扩展存在
	var extExists bool
	if err := h.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_extension WHERE extname = 'citus_columnar'
		)
	`).Scan(&extExists); err != nil || !extExists {
		out.Note = "citus_columnar 扩展未安装"
		return out
	}
	out.Available = true

	// 列存统计：表数 / 字段数 / 总占用
	row := h.db.QueryRow(ctx, `
		SELECT
			COUNT(*)::int AS table_count,
			COALESCE(SUM(c.relnatts), 0)::int AS total_columns,
			COALESCE(SUM(pg_total_relation_size(c.oid)), 0)::bigint AS total_bytes
		FROM pg_class c
		JOIN pg_am am ON am.oid = c.relam
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE am.amname = 'columnar'
		  AND n.nspname NOT IN ('pg_catalog','information_schema',
		                        'citus','citus_internal',
		                        'columnar','columnar_internal')
	`)
	if err := row.Scan(&out.TableCount, &out.TotalColumns, &out.TotalBytes); err != nil {
		out.Note = "列存统计查询失败: " + err.Error()
		return out
	}
	out.TotalHuman = humanBytes(out.TotalBytes)
	if out.TableCount == 0 {
		out.Note = "尚无表使用列存（citus_columnar 已加载）"
	}
	return out
}

// queryFilesystem 探测 path 所在 filesystem 容量（跨平台，见 diskusage_*.go）
func queryFilesystem(path string) (*filesystemInfo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	totalU, availU, freeU := statfsBytes(abs)
	if totalU == 0 {
		return nil, fmt.Errorf("statfs %s: 不可用", abs)
	}
	total := int64(totalU)
	free := int64(availU)
	used := total - int64(freeU)
	pct := 0
	if total > 0 {
		pct = int(used * 100 / total)
	}
	return &filesystemInfo{
		Path:        abs,
		TotalBytes:  total,
		TotalHuman:  humanBytes(total),
		UsedBytes:   used,
		UsedHuman:   humanBytes(used),
		FreeBytes:   free,
		FreeHuman:   humanBytes(free),
		UsedPercent: pct,
	}, nil
}

// queryDirectory 递归扫描目录，汇总文件数和总字节数
func queryDirectory(path string) directoryInfo {
	abs, _ := filepath.Abs(path)
	out := directoryInfo{Path: abs}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		out.Exists = false
		return out
	}
	out.Exists = true
	_ = filepath.Walk(path, func(p string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if fi.IsDir() {
			return nil
		}
		out.Files++
		out.SizeBytes += fi.Size()
		mt := fi.ModTime().Unix()
		if out.OldestMtime == 0 || mt < out.OldestMtime {
			out.OldestMtime = mt
		}
		if mt > out.NewestMtime {
			out.NewestMtime = mt
		}
		return nil
	})
	out.SizeHuman = humanBytes(out.SizeBytes)
	return out
}

// queryTableSizes Top-N 大表（含索引和 TOAST），带 DB 占比
//
// 2026-10-16 第二次性能修正（撤掉同一天第一次修正）：
//
//	第一次修正给这段 SQL 套了一层 `FROM ( ... ) sized` 派生表，理由是
//	「pg_total_relation_size 是按行函数，让它只算一次、外层按别名排序」。
//	这个理由成立，但**修法是错的**：派生表挡住了 planner 把 LIMIT 下推，
//	于是 toast 那个相关子查询必须对**内层的全部 440 行**求值，而不是只对
//	最终要返回的 20 行。该子查询每次都 `Seq Scan on pg_class t`（3881 行）。
//
//	真实 EXPLAIN ANALYZE（本机 PG17.10，pgcolumnar 已装）：
//	  带派生表：SubPlan 1 → Seq Scan on pg_class t (actual rows=0 loops=**440**)
//	            Rows Removed by Filter: 3881   Execution Time: **4571ms**
//	  无派生表：SubPlan 1 → Seq Scan on pg_class t (actual rows=0 loops=**20**)
//	            Rows Removed by Filter: 3881   Execution Time: **1104–1289ms**
//
//	即 440 × 3881 ≈ 170 万行的重复全表扫描，全部花在最终**不会返回**的行上。
//	第一次修正报的「1504ms → 527ms」是错的：它只测了派生表版的耗时，
//	没有对照「不套派生表」这个更简单的选项，于是把 4571ms 当成了改进。
//
//	现写法直接去掉派生表，并用 `ORDER BY 4`（位置引用）排序：
//	位置引用指向输出列，PG 复用 SELECT 已算出的值，**不会**重算
//	pg_total_relation_size —— 第一次修正想达成的「每行只算一次」照样成立，
//	同时 LIMIT 又能下推。语义与原查询逐列相同。
//
//	2026-10-16 第三次：加 `, 1` 作确定性 tie-break。
//	Top-N 快照一次采满 tableSizesMaxLimit 行、再按请求的 limit 切片，
//	这要求「Top-20 是 Top-200 的前缀」。字节数并列时 PG 的排序不稳定，
//	没有 tie-break 就只能算"大概率成立"，不能算严格成立。补 relname 作
//	次键后前缀性质精确，且并列时选谁从此固定（优化前是任意的）。
//	对耗时无影响（Top-N 的主项仍是 pg_total_relation_size）。
//
// 2026-10-20 第四次：TOAST 列的相关子查询是**纯浪费**，且取的是错值。
//
//	原表达式：
//	  COALESCE(pg_total_relation_size((
//	    SELECT t.oid FROM pg_class t WHERE t.reltoastrelid = c.oid)), 0)
//
//	两个独立的问题：
//
//	(1) 值恒为 0。子查询找的是「toast 表的宿主表」：t.reltoastrelid = c.oid
//	    意味着 c 自己是某张表的 TOAST 表。而本查询的 WHERE 限定
//	    c.relkind IN ('r','p')，TOAST 表的 relkind 是 't'，**永远匹配不上**。
//	    实测：536 张 r/p 表中可命中 0 张；而其中 435 张**确实有** TOAST 表。
//	    ⇒ toast_bytes 这个字段一直是 0，而 StorageOverview.vue:186 照着渲染它。
//
//	(2) 代价极高。相关子查询被 planner 展开成 SubPlan → Seq Scan on pg_class，
//	    对 LIMIT 下的每一行都重扫一遍全表（≈4000 行）。Top-200 就是
//	    200 × 4000 ≈ **80 万行重复扫描**，且每次还要过一遍
//	    relation_is_a_known_shard() 函数调用。全花在算一个恒为 NULL 的值上。
//
//	改法：直接用 c.reltoastrelid 这一列——它就是 c 自己的 TOAST 表 oid
//	（无 TOAST 时为 0，而 pg_total_relation_size(0) 返回 NULL，被 COALESCE
//	兜成 0）。零 join、零子扫描，且取到的是真值。
//
//	为什么上一轮（2026-10-16）记的是「LEFT JOIN 改写实测没有收益」：
//	那次的对照数字（1170/1197/3463ms vs 1696/1254/1455ms）是在
//	shared_buffers=400kB 的病态配置下测的，**规划**就要 ~1.2s，
//	规划耗时把子查询的差异整个盖住了 —— 结论方向对（当时没找到更优写法），
//	但「没有收益」这个判断本身不成立。直接用列比 LEFT JOIN 还更省：
//	后者仍要 join 一遍 pg_class。
//
//	另注：本查询剩余的耗时主体是 pg_total_relation_size 本身 —— 它要对每张表
//	真的去 stat 主 fork / fsm / vm / toast / 每个索引，共约 6000 次 stat。
//	本机 PG 数据目录挂在 Docker Desktop 的 **virtiofs** 上
//	（docker inspect: bind /Users/niuer/kaixuan/postgres-citus → /var/lib/postgresql/data），
//	每次 stat 是一次到宿主机的 RPC，实测 0.2–1.2ms/次 ⇒ 光 stat 就要 4.2s。
//	生产（Linux 原生文件系统）没有这层开销，该开销不成立。
//	本轮实测：改掉子查询后，采集从「>15s 打满超时」降到 4–6s，
//	落进 15s 预算内 ⇒ 启动预热成功 ⇒ 端点稳定 200。
//
// tableSizesSQL Top-N 大表查询的 SQL 正文。
//
// 抽成包级常量（而不是留在函数体里）：判据需要用**同一条**语句跑
// EXPLAIN 来证明「计划里没有 SubPlan」（那 80 万行重复全扫的形状），
// 复制一份 SQL 到测试里会让这道门在 SQL 漂移时静默失守 ——
// 门还在，验的却不是线上跑的那句。
const tableSizesSQL = `
	SELECT
		c.relname,
		n.nspname,
		COALESCE(s.n_live_tup, 0),
		pg_total_relation_size(c.oid),
		pg_indexes_size(c.oid),
		COALESCE(pg_total_relation_size(c.reltoastrelid), 0),
		(c.relkind = 'p')
	FROM pg_class c
	JOIN pg_namespace n ON n.oid = c.relnamespace
	LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
	WHERE n.nspname NOT IN ('pg_catalog','information_schema')
	  AND c.relkind IN ('r','p')
	ORDER BY 4 DESC, 1
	LIMIT $1
`

func queryTableSizes(ctx context.Context, h *Handler, limit int) ([]tableSizeInfo, int64, string, error) {
	rows, err := h.db.Query(ctx, tableSizesSQL, limit)
	if err != nil {
		return nil, 0, "", err
	}
	defer rows.Close()

	out := make([]tableSizeInfo, 0, limit)
	var totalBytes int64
	for rows.Next() {
		var t tableSizeInfo
		if err := rows.Scan(
			&t.Table, &t.Schema, &t.Rows,
			&t.TotalBytes, &t.IndexBytes, &t.ToastBytes,
			&t.IsPartitioned,
		); err != nil {
			warnRowSkip("dataLifecycle.storage.tableSizes", err)
			continue
		}
		t.TotalHuman = humanBytes(t.TotalBytes)
		t.ToastHuman = humanBytes(t.ToastBytes)
		totalBytes += t.TotalBytes
		out = append(out, t)
	}
	// 占比对 Top-N 求和，截断会让 Top-N 大表榜 + DB 占用同时偏小。
	if err := rows.Err(); err != nil {
		return nil, 0, "", fmt.Errorf("failed to iterate table size rows: %w", err)
	}

	// 占比（对 Top-N 求和，不严格等于 DB 总数，仅作参考）
	for i := range out {
		if totalBytes > 0 {
			out[i].PercentOfDB = int(out[i].TotalBytes * 100 / totalBytes)
		}
	}

	return out, totalBytes, humanBytes(totalBytes), nil
}

// resolveLogDir 探测 lumberjack 日志目录。
// 2026-07-02: 优先读 logging.ActiveConfig().File 的真实目录（不再猜测）。
// 兜底：探测常见候选路径 ./var/log、./logs、/var/log/llm-gateway-go。
// 永远返回绝对路径，调用方不应当 panic。
func resolveLogDir() (string, error) {
	// 优先：从 lumberjack 运行时配置读真实路径
	if cfg := logging.ActiveConfig(); cfg.File != "" {
		abs, err := filepath.Abs(filepath.Dir(cfg.File))
		if err == nil {
			return abs, nil
		}
	}
	candidates := []string{
		"./var/log",
		"./logs",
		"/var/log/llm-gateway-go",
	}
	for _, c := range candidates {
		if abs, err := filepath.Abs(c); err == nil {
			if info, err := os.Stat(abs); err == nil && info.IsDir() {
				return abs, nil
			}
		}
	}
	// fallback：cwd
	abs, err := filepath.Abs(".")
	if err != nil {
		return "", err
	}
	return abs, nil
}

// humanBytes 1024 进制 human-readable
func humanBytes(n int64) string {
	if n < 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return formatInt(int64(v)) + " " + units[i]
	}
	return strings.TrimRight(strings.TrimRight(
		formatFloat(v), "0"), ".") + " " + units[i]
}

// humanBytes exported alias for other files in this package
func humanBytesExport(n int64) string { return humanBytes(n) }

// formatInt / formatFloat 极简实现，避免再拉一个 fmt 包路径分支
func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func formatFloat(v float64) string {
	// 保留 1 位小数
	intPart := int64(v)
	frac := int64((v - float64(intPart)) * 10)
	if frac < 0 {
		frac = -frac
	}
	return formatInt(intPart) + "." + formatInt(frac)
}

// sortTablesDesc util：按 TotalBytes 倒序
func sortTablesDesc(tables []tableSizeInfo) {
	sort.Slice(tables, func(i, j int) bool {
		return tables[i].TotalBytes > tables[j].TotalBytes
	})
}

// ──────────────────────────────────────────────────────────────────
// 2026-07-08: 表级维护操作 (VACUUM / VACUUM FULL / REINDEX)
//
// 目的：data-lifecycle/storage/tables 页面"数据库表 Top N" 卡片
// 只读显示表大小，但运维常常需要"清掉 dead tuples / 回收磁盘"。
// 这些 handler 提供 per-table 操作能力，对应 UI 上的按钮。
//
// 约束：
//   - 全部需要 superAdmin 权限（路由注册时用 h.superAdmin）
//   - 禁止系统表（pg_catalog / information_schema / citus / columnar）
//   - 禁止 partitioned parent（VACUUM FULL on parent 会锁所有子表）
//   - VACUUM FULL / REINDEX 有锁表风险，handler 内部自动 lock_timeout
//   - 全部操作写 audit log
//   - 全部操作有 statement_timeout 兜底（防止 hang）
// ──────────────────────────────────────────────────────────────────

// tableMaintenanceRequest 通用请求体
type tableMaintenanceRequest struct {
	Schema string `json:"schema"` // 默认为 "public"
	Table  string `json:"table"`  // 必填
}

// tableMaintenanceResponse 通用响应
//
// 2026-07-13：vacuum/reindex/vacuum-full 改为异步任务，POST 后立即返回
// { run_id, polling_url, async: true }，前端通过 GET /jobs/:run_id 轮询。
type tableMaintenanceResponse struct {
	Schema       string `json:"schema"`
	Table        string `json:"table"`
	Operation    string `json:"operation"`
	RunID        string `json:"run_id,omitempty"`
	Async        bool   `json:"async,omitempty"`
	PollingURL   string `json:"polling_url,omitempty"`
	Success      bool   `json:"success"`
	Message      string `json:"message"`
	DurationMS   int64  `json:"duration_ms"`
	SizeBefore   int64  `json:"size_before_bytes"`
	SizeAfter    int64  `json:"size_after_bytes"`
	SizeSaved    int64  `json:"size_saved_bytes"`
	ReclaimedPct int    `json:"reclaimed_pct"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at"`
}

// validateTableForMaintenance 统一校验：白名单 schema + 拒绝系统表 + 拒绝 parent
func validateTableForMaintenance(schema, table string) (fullName string, err error) {
	if table == "" {
		return "", errors.New("table is required")
	}
	if schema == "" {
		schema = "public"
	}
	// 白名单：只允许 public
	if schema != "public" {
		return "", fmt.Errorf("schema %q not allowed (only 'public')", schema)
	}
	fullName = schema + "." + table

	// 拒绝系统 schema
	switch schema {
	case "pg_catalog", "information_schema", "citus", "citus_internal",
		"columnar", "columnar_internal", "pg_toast":
		return "", fmt.Errorf("schema %q is system schema, not allowed", schema)
	}
	if strings.HasPrefix(table, "pg_") {
		return "", fmt.Errorf("table %q is system table, not allowed", table)
	}
	return fullName, nil
}

// getTableSizeBytes 取表当前总大小（含索引 + TOAST）
func getTableSizeBytes(ctx context.Context, h *Handler, fullName string) (int64, error) {
	var size int64
	row := h.db.QueryRow(ctx, fmt.Sprintf("SELECT pg_total_relation_size('%s')", fullName))
	if err := row.Scan(&size); err != nil {
		return 0, err
	}
	return size, nil
}

// handleDataLifecycleTableVacuum POST /api/admin/data-lifecycle/storage/tables/vacuum
//
// 执行 VACUUM (ANALYZE) — 不锁表，立即 mark free space 供本表复用。
// 安全等级：🟢 低（推荐定期执行）。
//
// 2026-07-13：所有表级维护操作改为异步模式（10~30 分钟级），POST 后立即返回
// run_id，前端通过 GET /api/admin/data-lifecycle/jobs/:run_id 轮询。
func (h *Handler) handleDataLifecycleTableVacuum(w http.ResponseWriter, r *http.Request) {
	h.handleTableMaintenanceDispatch(w, r, "VACUUM", JobTypeVacuum)
}

// handleDataLifecycleTableVacuumFull POST /api/admin/data-lifecycle/storage/tables/vacuum-full
//
// 执行 VACUUM FULL — ACCESS EXCLUSIVE 锁表，把磁盘还给 OS。
// 安全等级：🟡 中（业务低峰期）。
func (h *Handler) handleDataLifecycleTableVacuumFull(w http.ResponseWriter, r *http.Request) {
	h.handleTableMaintenanceDispatch(w, r, "VACUUM FULL", JobTypeVacuumFull)
}

// handleDataLifecycleTableReindex POST /api/admin/data-lifecycle/storage/tables/reindex
//
// 执行 REINDEX INDEX（所有索引）— 锁表但短，回收索引 bloat。
// 安全等级：🟡 中（业务低峰期）。
func (h *Handler) handleDataLifecycleTableReindex(w http.ResponseWriter, r *http.Request) {
	h.handleTableMaintenanceDispatch(w, r, "REINDEX", JobTypeReindex)
}

// handleTableMaintenanceDispatch 异步分发入口
func (h *Handler) handleTableMaintenanceDispatch(w http.ResponseWriter, r *http.Request, op string, jobType JobType) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req tableMaintenanceRequest
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	fullName, err := validateTableForMaintenance(req.Schema, req.Table)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	params := map[string]any{
		"schema": req.Schema,
		"table":  req.Table,
		"op":     op,
	}

	operator := r.Header.Get("X-Admin-User")
	run := h.StartJob(jobType, params, operator, func(ctx context.Context, run *JobRun) {
		h.runTableMaintenanceJob(ctx, run, req.Schema, req.Table, op, fullName)
	})

	writeJSON(w, http.StatusAccepted, tableMaintenanceResponse{
		Schema:     req.Schema,
		Table:      req.Table,
		Operation:  op,
		RunID:      run.RunID,
		Async:      true,
		PollingURL: "/api/admin/data-lifecycle/jobs/" + run.RunID,
		StartedAt:  run.StartedAt.UTC().Format(time.RFC3339),
		Message:    fmt.Sprintf("%s 已调度到后台执行", op),
	})
}

// runTableMaintenanceJob 异步执行表级维护
func (h *Handler) runTableMaintenanceJob(ctx context.Context, run *JobRun, schema, table, op, fullName string) {
	startedAt := time.Now()
	h.TouchHeartbeat(run, "正在执行 "+op)

	timeout := 30 * time.Minute
	lockTimeout := "5s"
	switch op {
	case "VACUUM":
		timeout = 10 * time.Minute
		lockTimeout = "10s"
	case "VACUUM FULL":
		timeout = 60 * time.Minute
		lockTimeout = "5s"
	case "REINDEX":
		timeout = 30 * time.Minute
		lockTimeout = "5s"
	}

	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var sizeBefore int64
	if size, err := getTableSizeBytes(opCtx, h, fullName); err == nil {
		sizeBefore = size
	}
	h.UpdateProgress(run, 0, sizeBefore, 0, fmt.Sprintf("%s 前表大小 %s", op, humanBytes(sizeBefore)))

	conn, err := h.db.Acquire(opCtx)
	if err != nil {
		h.failJob(run, "failed to acquire conn: "+err.Error())
		return
	}
	defer conn.Release()

	// SET LOCAL outside a transaction block is silently ignored by
	// PostgreSQL (only a WARNING), so the 5s cap below never applied and a
	// wedged ACCESS EXCLUSIVE queue would block until the 30-60min opCtx
	// fired. VACUUM/REINDEX cannot run inside a tx block, so use a
	// session-level SET on this dedicated connection and RESET before the
	// conn goes back to the pool (the deferred RESET runs before Release).
	if _, lerr := conn.Exec(opCtx, fmt.Sprintf("SET lock_timeout = '%s'", lockTimeout)); lerr != nil {
		h.failJob(run, "failed to set lock_timeout: "+lerr.Error())
		return
	}
	defer func() {
		resetCtx, resetCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer resetCancel()
		//nolint:errcheck // best-effort cleanup; conn is discarded on failure
		conn.Exec(resetCtx, "RESET lock_timeout")
	}()

	var sql string
	switch op {
	case "VACUUM":
		sql = fmt.Sprintf("VACUUM (ANALYZE) %s", fullName)
	case "VACUUM FULL":
		sql = fmt.Sprintf("VACUUM FULL %s", fullName)
	case "REINDEX":
		sql = fmt.Sprintf("REINDEX TABLE %s", fullName)
	}

	// 2026-09-01: cluster-wide VACUUM FULL mutex. Two gateway
	// replicas firing on the same Sunday-02:00 cron tick would
	// otherwise race for ACCESS EXCLUSIVE; the loser would fail
	// with a generic "vacuum failed" from lock_timeout=5s. The
	// advisory lock below serializes all replicas on a single key
	// so only one runs at a time. Other operations (VACUUM, REINDEX)
	// are not serialized — they don't take ACCESS EXCLUSIVE.
	//
	// acquireTimeout = lockTimeout for consistency with the
	// ACCESS EXCLUSIVE timeout: if we can't grab the cluster mutex
	// within 5s, another replica is already mid-VACUUM FULL, and
	// we'd be racing it anyway.
	if op == "VACUUM FULL" {
		acquireTimeout, perr := time.ParseDuration(lockTimeout)
		if perr != nil {
			acquireTimeout = 5 * time.Second
		}
		err = dbx.VacuumFullMutex(opCtx, h.db, conn, acquireTimeout,
			func(ctx context.Context, c *pgxpool.Conn) error {
				_, ierr := c.Exec(ctx, sql)
				return ierr
			})
		if err != nil {
			h.failJob(run, fmt.Sprintf("%s failed: %s", op, err.Error()))
			return
		}
	} else if _, err := conn.Exec(opCtx, sql); err != nil {
		h.failJob(run, fmt.Sprintf("%s failed: %s", op, err.Error()))
		return
	}

	var sizeAfter int64
	if size, err := getTableSizeBytes(opCtx, h, fullName); err == nil {
		sizeAfter = size
	}

	duration := time.Since(startedAt)
	sizeSaved := sizeBefore - sizeAfter
	reclaimedPct := 0
	if sizeBefore > 0 {
		reclaimedPct = int(sizeSaved * 100 / sizeBefore)
	}

	h.SetResult(run, map[string]any{
		"size_before_bytes": sizeBefore,
		"size_after_bytes":  sizeAfter,
		"size_saved_bytes":  sizeSaved,
		"reclaimed_pct":     reclaimedPct,
		"duration_seconds":  int64(duration.Seconds()),
	}, "已完成")

	h.succeedJob(run, fmt.Sprintf("%s 完成", op))

	h.UpdateProgress(run, sizeSaved, sizeBefore, 1, "完成")

	// 2026-10-16：VACUUM FULL / REINDEX 会真实重写表与索引，物理占用已经变了。
	// 立刻失效存储总览快照，别让 UI 继续显示动手之前的读数（不必等 30s TTL）。
	InvalidateDataLifecycleStorageCache()
}

// opToJobType 把字符串 op 映射到对应的 JobType 枚举
func opToJobType(op string) JobType {
	switch op {
	case "VACUUM":
		return JobTypeVacuum
	case "VACUUM FULL":
		return JobTypeVacuumFull
	case "REINDEX":
		return JobTypeReindex
	default:
		return JobType(op)
	}
}
