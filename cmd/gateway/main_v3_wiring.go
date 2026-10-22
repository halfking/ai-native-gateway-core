package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/memory"                  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	memclient "github.com/kaixuan/llm-gateway-go/domains/memory/client" //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/session"                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// compressorSessionDisabled returns true when the v3 session compressor is
// explicitly turned off via env. Default = enabled; missing Redis/DB degrades
// the cache to the tiers that are available (at minimum the in-process L1).
func compressorSessionDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_SESSION_COMPRESSOR_DISABLE")))
	return v == "1" || v == "true" || v == "yes"
}

// ──────────────────────────────────────────────────────────────────────────────
// Redis backend adapter (session.RedisClient → compression.SessionCacheBackend)
// ──────────────────────────────────────────────────────────────────────────────

type redisBackendAdapter struct {
	c *session.RedisClient
}

func redisBackendFromClient(c *session.RedisClient) compression.SessionCacheBackend {
	if c == nil {
		return nil
	}
	return &redisBackendAdapter{c: c}
}

func (a *redisBackendAdapter) HSet(ctx context.Context, key string, values ...any) error {
	fields := map[string]any{}
	for i := 0; i+1 < len(values); i += 2 {
		k, ok := values[i].(string)
		if !ok {
			continue
		}
		fields[k] = values[i+1]
	}
	return a.c.HSet(ctx, key, fields)
}

func (a *redisBackendAdapter) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	// P1-14 fix (2026-08-28): Use SafeHGetAll to prevent WRONGTYPE errors
	data, err := redissafe.SafeHGetAll(ctx, a.c.Client(), key)
	if err != nil {
		// Preserve the go-redis HGETALL contract this adapter replaces:
		// a missing key is an empty map + nil, i.e. a cache miss. The typed
		// WRONGTYPE error still propagates.
		if errors.Is(err, redissafe.ErrKeyNotFound) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	return data, nil
}

func (a *redisBackendAdapter) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return a.c.Expire(ctx, key, ttl)
}

func (a *redisBackendAdapter) Del(ctx context.Context, key string) error {
	return a.c.Del(ctx, key)
}

// ──────────────────────────────────────────────────────────────────────────────
// Postgres backend adapter (*db.DB → compression.SessionCacheDB)
// ──────────────────────────────────────────────────────────────────────────────

type pgBackendAdapter struct {
	dbConn *db.DB
}

func dbBackendFromPool(dbConn *db.DB) compression.SessionCacheDB {
	if dbConn == nil || !dbConn.Enabled() {
		return nil
	}
	return &pgBackendAdapter{dbConn: dbConn}
}

// lastOutboundSessionSQL 是压缩会话缓存 L3 冷启动回退的会话族腿（S3 波 2
// 停写切换，见 LastOutboundForSession 注释）。占位符：$1 = 会话 ID（由
// SessionFamilyTurnsForSessionSQL 内推消费），$2 = 租户。提升为包级变量
// 是给形状钉桩用的。
//
// # ★★ 2026-10-21（§143.55，E1 读侧落地）：本式原来在 final_full 门下**读不到快照**
//
// 原式是 `JOIN <bodies 源> sb ON sb.request_id = rl.request_id` + `WHERE sb.outbound_body IS NOT NULL`。
// 而 E1（迁移 708 / 开关 `storage.session_final_full_enabled`）的写入形态是：
//
//	逐轮行：outbound_body = **jsonb `null`**（★ §143.56 实测订正，见下方横幅）
//	快照行：request_id = 'final_full:<session>'、turn_no = 0、kind = 'final_full'
//
// ⇒ 快照行的 request_id 与任何一轮的 request_id 都不相等 ⇒ 永远匹配不上 turns 侧
// 的任何一行 ⇒ 本式读不到快照。
//
// # ★★★ 订正横幅（2026-10-21，§143.56）—— §143.55 的根因表述与修法**都不准确**
//
// ★ **订正 1：逐轮行是 jsonb `null`，不是 SQL NULL。**
//
//	写侧 `domains/session/v2/bodies_writer.go` 的 `jsonTextOrNull(data)` 在空时返回
//	**文本 `"null"`**（不是 Go 的 nil）⇒ 入库形态是 `('null'::jsonb)`。
//	⇒ 211 生产实测（2026-10-21 17:55 开门后）：逐轮行
//	  `pg_column_size(outbound_body) = 9`（jsonb null 的字节数；SQL NULL 会是 0）。
//	⇒ ★ §143.55 写的「逐轮 outbound_body = SQL NULL」**是错的**。
//
// ★ **订正 2：原式不是「恒返 0 行」，而是「返出 1 行、正文是 jsonb `null`」。**
//
//	`sb.outbound_body IS NOT NULL` 对 **jsonb null 恒真**（jsonb null 不是 SQL NULL）
//	⇒ 原式把「这一轮没有 outbound 正文」当成了「有正文」。
//	⇒ 所以 L3 冷启动拿到的是 **4 字节的 `null`**，不是 0 行。
//	（`db.SessionBodiesHasOutboundPredicate` 的文件头早就记着这条：会话族的
//	  「没有正文」一律是 jsonb null，裸 `IS NOT NULL` 在会话族上是恒真 —— 实测
//	  20,643 行里 20,643 行通过，判据等于没写。）
//	⇒ ★ §143.55 的「恒返 0 行 / IS NOT NULL 全滤掉」表述**作废**。
//
// ★ **订正 3：§143.55 落仓的那一版修法只修了一半。**
//
//	那一版是 LEFT JOIN + LATERAL 快照腿 +
//	`WHERE (sb.outbound_body IS NOT NULL OR ff.outbound_body IS NOT NULL)`：
//	快照**存在**时它答对了；快照**缺失**时（门开之前就存在的历史会话、或
//	final_full 还没写成功），`sb` 腿的 jsonb null 行仍然通过那个恒真的
//	`IS NOT NULL` ⇒ 照样返出 jsonb null 正文。
//	⇒ ⇒ 本轮（§143.56）把「有正文」判据换成 jsonb 口径，这一半才补上。
//
// ★ **订正 4：「门关时逐行等价」不成立**，差别不在 final_full，而在
//
//	「**没有正文**的轮次」上：
//
//	旧式（裸 `IS NOT NULL`）把 jsonb-null 行算成「有正文」⇒ 命中的可能是
//	「**最新一轮没有正文**」那一行，而不是「最新一轮有正文」那一行；
//	新式只认真有正文的轮次 ⇒ 命中「最近一个**真的有正文**的轮次」。
//	⇒ 这与 `domains/hooks/compression/session_cache.go` 里 `SessionCacheDB` 的
//	接口合同（「the most recent row **that has an outbound_body**」）**同口径**
//	⇒ 新式才是合同要求的那个，旧式是过宽的那个。
//
// ★ 诚实边界（§143.56 实测得出，别外推）：
//
//	本式是 L3 冷启动的**兜底**腿。生产的主路径是
//	`domains/session/v2/turn_reader.go` 的 `LoadLatestOutbound`
//	（**已 final_full-first**，且自带 `string(raw) == "null"` 护栏）
//	⇒ 只有「既没有快照、也没有 delta chain」的会话才会走到本式。
//	⇒ 所以本式的缺陷是**真缺陷**（真库可复现），但**不是**「E1 门一开就必炸」。
//
// # 修法：三条腿（turns 元数据 + 逐轮正文 + final_full 快照），COALESCE 取优先级
//
//	final_full 优先、逐轮兜底 —— 与 `domains/session/v2/turn_reader.go`
//
// 的 `LoadLatestOutbound`（方案 D2「差集提取改读 final_full；未命中回退旧
//
//	outbound_body」）**同一口径**。
//
// ★ 为什么 final_full 腿用 `session_bodies_unified` 视图、而不是改
// `db.SessionFamilyBodiesSourceSQL()` 的投影：
//
//	那个 helper 的列集合是**合同**（§9.231），它**刻意不投影 ts**，而且它投影的是
//	`request_delta AS request_body` 这类 v1 同形别名 —— 要在它上面按
//	`(tenant_id, session_id, kind)` 定位快照行，就得把 `session_id`/`kind`/`ts`
//	加进它的投影 ⇒ **改的是合同本身**（会波及 admin 的 14 个消费点）。
//	⇒ 而 final_full 腿要的就是这三个列 ⇒ 直接读既有视图
//	（`public.session_bodies_unified` = hot ∪ parent，708 起带 kind 列），
//	与 `turn_reader.go` / `bodies_writer.go` 的既有读法一致。
//
// ★ 为什么用 LEFT JOIN LATERAL 而不是标量子查询：
//
//   - 标量子查询在 SELECT 与 WHERE 里各写一遍 ⇒ **同一子查询被算两次**；
//   - LATERAL 只算一次，且 `LIMIT 1` 保证**不放大行**（一个外层行最多配 1 行）；
//   - LATERAL 的结果列 `ff.outbound_body` 可以同时进 SELECT（COALESCE）与
//     WHERE（"有正文"过滤）⇒ 不用重复求值。
//
// ★ 为什么 WHERE 保留「有正文」过滤（而不是删掉）：
//
//	删掉 ⇒ LEFT JOIN 会把「本轮没有正文」的 turns 行也放进来 ⇒ **结果集被放大**
//	（LEFT JOIN 的右侧无匹配 ⇒ 该 turns 行照样成行）。
//	保留 ⇒ 无匹配的右行被 WHERE 滤掉 ⇒ 与内连接同形。
//
// ★★ 2026-10-21（§143.56）：三处「有正文」判据全部改用
//
//	`db.SessionBodiesHasOutboundPredicate(alias)`（`IS NOT NULL AND <> 'null'::jsonb`）
//	—— 与 `admin/data_lifecycle.go`、`cmd/compression-bench/main.go` 同一口径，
//	且**判据只有一个事实源**（抄一份进 SQL 就等于多了一个会漂移的副本）。
//
// ★★★★★★ FIXME-14377（2026-10-23 §143.77 轮，只登记、★ 零行为变更）：
//	★★★★★★ 【L3 兜底腿每次 ~150 ms、4 天烧掉 63 分钟 DB 时间、且【全部返 0 行】】
//	★★★★★★ 【根因不是 IO、不是数据量、不是规划 —— 是【并行 worker 的启动开销】】
//
//	★ 现象（252 共享库 2026-10-23 只读；口径见
//	★★ scripts/audit/verify_l3_fallback_parallel_cost.sql，★ 生产真跑 rc=0）：
//	  ① 本式在生产库里以【两种形态】并存：
//	     - queryid 3955058327330889760：calls = **8,068**，mean = **145.644 ms**
//	       （§143.55/§143.56 形态：COALESCE(ff,sb) + final_full LATERAL 腿）
//	     - queryid 8794675561828039885：calls = **17,424**，mean = **150.342 ms**
//	       （§143.55 之前的旧内 JOIN 形态）
//	     ⇒ 合计 **25,492 次 / 3,794,612 ms（≈ 63 分钟）/ rows 全部 = 0**；
//	  ② ★★ rows 全部为 0 ⇒ 这 63 分钟【什么也没换回来】。
//
//	★★★★★★ 根因（★ 执行计划自证，不是推测）：
//	  本式的计划顶层是 `Gather Merge`（**并行排序**）⇒ 每次调用都要付
//	  **并行 worker 的启动开销**（252 是 4 核 / load 14 的宿主 ⇒ 启动很贵）。
//	  ★★ 决定性对照（★ 同一 SQL、同一会话、同一缓存，**只切**
//	  ★★ `max_parallel_workers_per_gather` 0 ↔ 默认；取第二次运行 = 缓存已热）：
//	    - E1 会话（1 轮）：**170.136 ms**（并行）⇒ **0.748 ms**（关并行）= **227×**
//	    - 787 轮大会话：261.811 ms（并行）⇒ **629.980 ms**（关并行）★ **反而慢 2.4×**
//	    - 库里不存在的会话（**返 0 行**）：**508.773 ms**（并行）⇒ **0.969 ms**（关并行）= **525×**
//	  ⇒ ⇒ ★★ **「返 0 行也要照付并行钱」被实测坐实**
//	  ⇒ ⇒ ★★ 而 63 分钟 ≈ 25,492 × ~100~170 ms 的【纯启动开销】
//	  ⇒ ⇒ ★★ 这是「**问一次、什么都不拿、还付 100 ms**」的典型形态。
//
//	★★★ 诚实边界（★ 别把数字外推）：
//	  ★★ **并行启动成本是【环境相关】的**：它取决于核数与宿主负载
//	  ★★ （252 = 4 核 / load 14 ⇒ 100~170 ms；核多、闲的宿主上会小得多）。
//	  ★★ 本轮**没有**在别的宿主上测过 ⇒ 别把它当常数。
//
//	★★★★★★ 三条修法候选，**逐条都实测过**，其中两条被否：
//	  【A】全局关并行（`max_parallel_workers_per_gather = 0`）
//	    ⇒ ★★ 小会话 227× 提速，但 **787 轮大会话 2.4× 变慢** ⇒ ★★ **不是等价修法**；
//	  【B】把投影从 canonical 的 **115 列**收窄到外层真正用到的 **7 列**
//	    ⇒ ★★★ **实测不消除并行计划**（159.469 ms，仍是 Gather Merge）
//	    ⇒ ⇒ ★★ **推翻了本轮自己的假设**（「行宽 ⇒ 代价模型 ⇒ 选并行」不成立）
//	    ⇒ ⇒ 它单独只值 0.919 → 0.530 ms ⇒ ⇒ **不能作为主修法**；
//	  【C】把 `ORDER BY ... LIMIT 1` 推进 UNION 的各腿（想用「腿内 top-1」消掉顶层排序）
//	    ⇒ ★★★★★ **实测会让计划退化成【顺序扫描】**：
//	    ⇒ 在 4.4 GB 的 turns 族上跑了几分钟（`wait_event = DataFileRead`）
//	    ⇒ ⇒ ★★ **不可用**（★ 教训：★ 「消除顶层排序」不等于「消除扫描量」）；
//	  【D】换驱动：bodies 侧按 `(tenant_id, session_id)` 索引直达 ⇒ O(1)
//	    ⇒ ★★ outbound_body 在 **11 个真实会话上逐字 md5 全同**（11/11）
//	    ⇒ ★★★ **但元数据口径变了**（1/11 例不同）：OLD 取「turns.ts 最大的那一轮」的
//	    ⇒ 元数据，NEW 取「有正文那一行所对应的轮」的元数据
//	    ⇒ ⇒ ★★ **不是严格等价** ⇒ ⇒ **语义取舍要属主拍板**，本轮不落。
//
//	★★★★★★ 本轮【不改行为】：★ 改它要动 §9.231 的 bodies 列集合合同
//	（★ D 方案要按 `(tenant_id, session_id, kind)` 定位快照行 ⇒ 得往投影里加
//	★★ session_id/kind/ts ⇒ 那是合同本身，会波及 admin 的 14 个消费点）
//	⇒ ⇒ 且 D 的元数据口径变化需要属主先拍板。
//	⇒ ⇒ ⇒ **只登记**；⇒ ⇒ ⇒ 修法排序与前置条件见 runbook §143.77。
var lastOutboundSessionSQL = `
		SELECT COALESCE(ff.outbound_body, sb.outbound_body),
		       rl.outbound_msg_count, rl.outbound_token_est,
		       rl.outbound_msg_hashes, rl.compression_meta
		FROM ` + db.SessionFamilyTurnsForSessionSQL() + ` rl
		LEFT JOIN ` + db.SessionFamilyBodiesSourceSQL() + ` sb
		  ON sb.request_id = rl.request_id
		LEFT JOIN LATERAL (
		  SELECT f.outbound_body
		  FROM public.session_bodies_unified f
		  WHERE f.tenant_id = $2
		    AND f.session_id = $1
		    AND f.kind = 'final_full'
		    AND ` + db.SessionBodiesHasOutboundPredicate("f") + `
		  ORDER BY f.ts DESC
		  LIMIT 1
		) ff ON TRUE
		WHERE rl.tenant_id = $2
		  AND ((` + db.SessionBodiesHasOutboundPredicate("sb") + `)
		       OR (` + db.SessionBodiesHasOutboundPredicate("ff") + `))
		ORDER BY rl.ts DESC
		LIMIT 1
	`

// LastOutboundForSession runs the L3 cold-start fallback query.
//
// 2026-10-13 S3 波 2（停写切换）：gw_session_id 是会话内谓词（A 形态）。
// 原实现只读 v1 母表（outbound 自 Phase 1 起就大多在 bodies 表，
// 该查询本就接近恒空），S4 停写后新会话完全无行。会话族优先：turns 元数据
// 四列（113 列契约内）经 SessionFamilyTurnsForSessionSQL 按 session 内推，
// outbound_body 经 SessionFamilyBodiesSourceSQL 按 request_id 单键 JOIN
// （该源不投影 ts，ts 等值 JOIN 形态禁止）。
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：**v1 母表兜底腿已删除**。
// 那条腿只在会话族查不到行时触发，而它读的关系已被 DROP ⇒ 42P01 ⇒
// 「会话族没命中」会从「回退到 v1 取历史」变成「整条查询报错」。
// 镜像前的历史会话在会话族里本就没有对应轮次，回退腿取到的内容
// 属于「另一个存储面里的同一段历史」，退役后不可得 ⇒ 直接返回
// pgx.ErrNoRows（调用方按「无 outbound」处理），不制造 42P01。
func (a *pgBackendAdapter) LastOutboundForSession(ctx context.Context, tenantID, gwSessionID string) (*compression.LastOutboundRow, error) {
	if a.dbConn == nil {
		return nil, fmt.Errorf("db pool not configured")
	}
	pool := a.dbConn.Pool()
	if pool == nil {
		return nil, fmt.Errorf("db pool is nil")
	}
	sessionRow := pool.QueryRow(ctx, lastOutboundSessionSQL, gwSessionID, tenantID)
	return scanLastOutboundRow(sessionRow)
}

func scanLastOutboundRow(row interface{ Scan(dest ...any) error }) (*compression.LastOutboundRow, error) {
	var (
		ob     []byte
		mc     *int
		te     *int
		hashes []byte
		meta   []byte
	)
	if err := row.Scan(&ob, &mc, &te, &hashes, &meta); err != nil {
		// ★★ 2026-10-21（§143.56）：**0 行不是错误** ⇒ 映射成 (nil, nil)。
		//
		// 理由（两条调用路径的实测差）：
		//
		//	`domains/hooks/compression/session_cache.go` 的 `getLocal`
		//	→ `loadFromDB` 出错 ⇒ **warn + 返回 (nil,nil,nil)** ⇒ 吞掉；
		//	`domains/hooks/compression/session_cache_shared.go` 的 `loadSharedLocked`
		//	→ `loadFromDB` 出错 ⇒ **直接 return err** ⇒ **往上抛**。
		//
		// ⇒ ⇒ 「没有 outbound」与「数据库出错」必须分开表达，否则
		// ⇒ ⇒ **Redis 共享路径会把「这个会话没有历史」当成硬错误**。
		//
		// ★ 触发面：① 迁移 890 之后，会话族里查不到轮次的**历史会话**
		//　（既有形态，见本文件 LastOutboundForSession 的注释）；
		//　② §143.56 把「有正文」判据换成 jsonb 口径之后，**没有正文**的
		//　　会话（252 共享库实测 **24,229 个 = 9.68%**）会正确地返 0 行
		//　　⇒ ⇒ **不映射 ⇒ 共享路径会开始报错** ⇒ ⇒ 必须在这里收口。
		//
		// ★ 真库门判据 ⑧ 钉住这一条（去掉本映射 ⇒ 判据 ⑧ 立刻红）。
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	out := &compression.LastOutboundRow{}
	if ob != nil {
		out.OutboundBody = json.RawMessage(ob)
	}
	if mc != nil {
		out.OutboundMsgCount = *mc
	}
	if te != nil {
		out.OutboundTokenEst = *te
	}
	if hashes != nil {
		out.OutboundMsgHashes = json.RawMessage(hashes)
	}
	if meta != nil {
		out.CompressionMeta = json.RawMessage(meta)
	}
	return out, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Executor → compression.Dependencies
// ──────────────────────────────────────────────────────────────────────────────

// NewDependenciesFromExecutor wires the existing executor's Memora and
// Provider clients into the compression.Dependencies struct. Returns nil
// when either client is missing (LLM summary then degrades to mechanical
// trim — see compression.SessionCompressor).
func NewDependenciesFromExecutor(exec *executors.Executor) *compression.Dependencies {
	if exec == nil {
		return nil
	}
	deps := &compression.Dependencies{}
	if exec.Memora != nil {
		deps.Memora = memoraClientAdapter{c: exec.Memora}
	}
	if exec.Provider != nil {
		deps.Provider = providerClientAdapter{r: exec.Provider}
	}
	return deps
}

// memoraClientAdapter bridges *memclient.Client to the compression.MemoraClient
// interface (Disabled + Search).
type memoraClientAdapter struct {
	c memory.Reader
}

func (m memoraClientAdapter) Disabled() bool {
	if m.c == nil {
		return true
	}
	return m.c.Disabled()
}

func (m memoraClientAdapter) Search(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	items, err := m.c.Search(ctx, userID, query, topK)
	if err != nil {
		return nil, err
	}
	return convertMemoraItems(items), nil
}

func (m memoraClientAdapter) SmartSearch(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	items, err := m.c.SmartSearch(ctx, userID, query, topK)
	if err != nil {
		return nil, err
	}
	return convertMemoraItems(items), nil
}

func convertMemoraItems(items []memory.Memory) []memory.Memory { return items }

type legacyMemoraReader struct {
	c *memclient.Client
}

type legacyMemoraWriter struct {
	s *memclient.Sink
}

func (m legacyMemoraReader) Disabled() bool {
	if m.c == nil {
		return true
	}
	return m.c.Disabled()
}

func (m legacyMemoraReader) Ping(ctx context.Context) error {
	if m.c == nil {
		return nil
	}
	return m.c.Ping(ctx)
}

func (m legacyMemoraReader) BaseURL() string {
	if m.c == nil {
		return ""
	}
	return m.c.BaseURL()
}

func (m legacyMemoraReader) AddMessage(ctx context.Context, userID string, messages []memory.Message, info map[string]any) error {
	if m.c == nil {
		return nil
	}
	legacyMessages := make([]memory.Message, 0, len(messages))
	for i := range messages {
		legacyMessages = append(legacyMessages, memory.Message{
			Role:    messages[i].Role,
			Content: messages[i].Content,
		})
	}
	return m.c.AddMessage(ctx, userID, legacyMessages, info)
}

func (m legacyMemoraReader) Search(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	if m.c == nil {
		return nil, nil
	}
	items, err := m.c.Search(ctx, userID, query, topK)
	if err != nil {
		return nil, err
	}
	out := make([]memory.Memory, 0, len(items))
	for i := range items {
		out = append(out, memory.Memory{
			ID:     items[i].ID,
			Text:   items[i].Text,
			Tags:   items[i].Tags,
			Score:  items[i].Score,
			CubeID: items[i].CubeID,
		})
	}
	return out, nil
}

func (m legacyMemoraReader) SmartSearch(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	if m.c == nil {
		return nil, nil
	}
	items, err := m.c.SmartSearch(ctx, userID, query, topK)
	if err != nil {
		return nil, err
	}
	out := make([]memory.Memory, 0, len(items))
	for i := range items {
		out = append(out, memory.Memory{
			ID:     items[i].ID,
			Text:   items[i].Text,
			Tags:   items[i].Tags,
			Score:  items[i].Score,
			CubeID: items[i].CubeID,
		})
	}
	return out, nil
}

func (w legacyMemoraWriter) Enqueue(op memory.WriteOp) {
	if w.s == nil {
		return
	}
	msgs := make([]memory.Message, 0, len(op.Messages))
	for i := range op.Messages {
		msgs = append(msgs, memory.Message{
			Role:    op.Messages[i].Role,
			Content: op.Messages[i].Content,
		})
	}
	w.s.Enqueue(memory.WriteOp{
		UserID:   op.UserID,
		Info:     op.Info,
		Source:   op.Source,
		Messages: msgs,
	})
}

func (w legacyMemoraWriter) Stats() memory.Stats {
	if w.s == nil {
		return memory.Stats{}
	}
	stats := w.s.Stats()
	return memory.Stats{
		Enqueued:          stats.Enqueued,
		Dropped:           stats.Dropped,
		Processed:         stats.Processed,
		Errored:           stats.Errored,
		QueueLen:          stats.QueueLen,
		QueueCap:          stats.QueueCap,
		ConsecutiveErrors: stats.ConsecutiveErrors,
		LastError:         stats.LastError,
		LastErrorAt:       stats.LastErrorAt,
		Paused:            stats.Paused,
	}
}

func (w legacyMemoraWriter) Pause() {
	if w.s != nil {
		w.s.Pause()
	}
}

func (w legacyMemoraWriter) Resume() {
	if w.s != nil {
		w.s.Resume()
	}
}

// providerClientAdapter bridges executors providerResolver to compression.ProviderClient.
// The adapter discards the *provider.Policy return value because the
// compression only needs the candidate list.
type providerClientAdapter struct {
	r routingProviderResolver
}

// routingProviderResolver is the minimal subset of executors providerResolver
// we depend on. Re-declared here to avoid importing executors into the
// interface signature (which would create a circular type reference).
type routingProviderResolver interface {
	Enabled() bool
	GetCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, *provider.Policy, error)
	ModelKnown(ctx context.Context, model string) bool
}

func (p providerClientAdapter) Enabled() bool {
	if p.r == nil {
		return false
	}
	return p.r.Enabled()
}

func (p providerClientAdapter) ModelKnown(ctx context.Context, model string) bool {
	if p.r == nil {
		return false
	}
	return p.r.ModelKnown(ctx, model)
}

func (p providerClientAdapter) GetCandidates(ctx context.Context, model, profile string) ([]compression.ProviderCandidate, error) {
	return p.GetCandidatesForTenant(ctx, model, profile, "")
}

// GetCandidatesForTenant preserves tenant-specific routing policy for callers
// that can propagate the request tenant through the compression dependency.
func (p providerClientAdapter) GetCandidatesForTenant(ctx context.Context, model, profile, tenantID string) ([]compression.ProviderCandidate, error) {
	if p.r == nil {
		return nil, fmt.Errorf("provider resolver nil")
	}
	raw, _, err := p.r.GetCandidates(ctx, model, profile, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]compression.ProviderCandidate, 0, len(raw))
	for i := range raw {
		out = append(out, compression.ProviderCandidate{
			CredentialID:  raw[i].CredentialID,
			ProviderID:    raw[i].ProviderID,
			RawModel:      raw[i].RawModel,
			BaseURL:       raw[i].BaseURL,
			APIKey:        raw[i].APIKey,
			Protocol:      raw[i].Protocol,
			ContextWindow: nil, // provider.Candidate does not carry context window; compression falls back to default heuristic
			Available:     raw[i].Routable,
		})
	}
	return out, nil
}
