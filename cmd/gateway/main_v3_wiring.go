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
//
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
//
// ★★★★★★ FIXME-14378（2026-10-23 §143.78 轮，只登记、★ 零行为变更）：
//
//	★★★★★★★ 【★ 追问「25,492 次为什么【全返 0 行】」的答案 ⇒ ⇒ ★★ 【处置方向改了】：
//	★★★★★★★ 【这条查询【结构性地查不到东西】⇒ ⇒ 优化它对【正确性】零收益】】
//
//	★★★ 完整因果链（★ 代码自证 + 生产只读实测，两处互相印证）：
//
//	  ① 【L3 是【兜底】】—— `domains/hooks/compression/session_cache.go` 的 `loadFromDB`
//	     **先**走 v2 主路径 `turnReader.LoadLatestOutbound`，再走 v2 delta 链
//	     `LoadChain(...,10)`，★ 两段都落空才 `return c.loadFromLegacyDB(...)`
//	     ⇒ ⇒ 那才是 L3（`c.db.LastOutboundForSession` = 本文件 `lastOutboundSessionSQL`）。
//	  ② 【0 行 ⇒ 只可能是「该 session_id 在 session_turns 里没有行」】——
//	     ★★ L3 的驱动腿是 `db.SessionFamilyTurnsForSessionSQL()`（turns 族），
//	     ★★ **bodies 侧没有行只会让结果 body 为 NULL、不会让行数变 0**。
//	     ★★ 而「父表看不到当月分区」这个假设**已被实测证伪**（10 月分区抽样：
//	     ★★ parent_rows == oct_rows == 787 ⇒ 父表看得见当月分区）。
//	  ③ 【那批 session_id 为什么在 turns 里没有行？】——
//	     ★★★★★ **`domains/streaming/session_assignment.go`：
//	     `shouldCreateFreshSessionForMessageCount(n) ⇒ n <= 1`，
//	     且 `assignGatewaySessionWithFinder` 里
//	     `if shouldCreateFreshSessionForMessageCount(...) { return createSession() }`
//	     ⇒ ⇒ **「客户端消息数 ≤ 1」的请求 ⇒ 每次都【新建一个会话】**
//	     ⇒ ⇒ **每次请求都换一个全新的 `gw_<uuid>`** ⇒ ⇒ **该 id 必然没有任何既往轮次**。
//	     ★★★ 而探针 ping 的请求体正是 1 条消息（`bg/probe_http.go` 的
//	     ★★★ `"messages":[{"role":"user","content":"."}]`）⇒ ⇒ **探针 100% 命中这一支**。
//	  ④ ⇒ ⇒ ★★★ **「25,492 次全 0 行」= 「每个请求都新建会话」的那批请求的首轮**。
//
//	★★★★★★ 生产实测（252 共享库，只读，口径见 `scripts/audit/verify_l3_zero_row_naming.sql`）：
//
//	★ `session_turns_2026_10`：总行数 279,446 / distinct 会话 253,393
//	★ ⇒ ★★★★★ **一次性会话（只 1 轮）252,384 = 99.60%**；多轮会话只有 1,009（0.40%）
//	★ ⇒ 一次性会话的前缀分布：`gw_` 252,383 / other 1
//	★ ⇒ 一次性会话的来源分桶（分桶之和 = 252,384，与总数自洽 ✔）：
//	★   business 164,054 + （origin_stage 空）42,068 + node_probe 31,851
//	★   + probe-service 13,217 + credential-selfcheck 1,194
//	★   ⇒ ⇒ **探针合计 46,262 = 18.3%；业务 + 无标记 206,122 = 81.7%**
//	★ ⇒ 抽样 5 个一次性 `gw_` id：★ **在父表里各只出现 1 次**（hot = 0）
//	★   ⇒ ⇒ 证实「每个请求一个新 id」：★ 这些 id 从不复用。
//
//	★★★★★★ 两条对既有结论的【订正】（★ 原文保留，订正见 runbook §143.78）：
//	  ① ★★★ §143.57.2 ② 说「`gt_gw_…` 在共享库【所有会话表】里都是 0 行」
//	     ⇒ ★★★ **不成立**（2026-10-23 实测）：`session_turns_2026_09` 里
//	     **有 11,801 行 `gt_gw_`**（时间窗 09-07 ~ 09-17）⇒ ⇒ **它不是「驱动侧无行」**，
//	     ★★ **而是「驱动侧有行、JOIN 侧无行」** ⇒ ⇒ §143.57 的【结论】仍成立，
//	     ★★ **但机制描述要精确化**；
//	  ② ★★★ §143.57.2 ⑥ 说「`gt_` 会话的【真身】在共享库是有行的（bodies_hot 2 / turns_hot 1）」
//	     ⇒ ★★★ **在本轮的 5 个样本上不成立**：真身 `gw_…` 的 **bodies 侧同样是 0 行**
//	     ⇒ ⇒ ★★ **「改成查真身」救不回这批会话** ⇒ ⇒ ★★ **两条读数来自【不同的会话集合】**
//	     ⇒ ⇒ ★★ **⇒ 这正是「同一组互相印证的数字必须来自同一个集合」这条纪律的又一次命中**。
//
//	★★★★★★ 处置方向的【修正】（★ 直接改写 §143.77.8 的 ⑱）：
//	  ★★ §143.77 把「优化这条查询」列为 ⑱ 的选项之一（①加索引 / ②换驱动 / ③不动）
//	  ⇒ ★★★ **本轮补上一条更前置的选项 ⓪**：
//	  ⇒ ⇒ **「让【注定查不到】的调用不要走到它」** ⇒ ⇒ ★★ **零 DB 成本**
//	  ⇒ ⇒ ★★ 比 ①（把 100 ms 变成 1 ms）强【一个数量级】。
//
//	  ★★★★ ★★ **判据必须订正**（★ 本轮第一版假设被自己的实测推翻 ★★★ 留痕）：
//	  ★★ 本轮第一版写的是「判据用 session_id 形态本身（`sys:` / `gt_` / `gs_` 前缀）」
//	  ⇒ ★★★ **这条被实测推翻**：一次性会话【全是 `gw_`】（252,383 / 252,384）
//	  ⇒ ⇒ ★★ **前缀判据一个都拦不住** ⇒ ⇒ ★★ **而 99.60% 的会话才是真正的大头**。
//	  ⇒ ⇒ ✅ **正确判据 = 「本次请求的会话是【自动新建】的」**
//	  ⇒ ⇒ ★★★ 事实源**现成、零新增状态**：
//	  ⇒ ⇒ ★★ `domains/streaming/session_assignment.go` 的 `sessionAssignment.AutoCreated`
//	  ⇒ ⇒ ★★ （`createSession()` 里置 `AutoCreated = true`；★ 且 `handler.go` 已经在
//	  ⇒ ⇒ ★★★ 往响应头写 `X-Gw-Session-Auto: true` ⇒ ⇒ **这个事实已经在生产流通**）
//	  ⇒ ⇒ ★★ 语义：**自动新建 ⇒ 该 id 全库从未出现过 ⇒ L3 必然返 0 行**
//	  ⇒ ⇒ ★★★ ⇒ ⇒ **短路它是【零语义风险】的**（不是「大概没用」，是「必然没用」）。
//	  ⇒ ⇒ ★★ 复用既有映射规则仍然有效（`admin/turns_sessions.go` 的 `deriveSessionParent`、
//	  ⇒ ⇒ ★★ `domains/streaming/handler.go` 的 `sanitizeGwSessionHeader` 白名单），
//	  ⇒ ⇒ ★★ 但它们**不是**判据本身，**只是做判据时的取材来源**。
//
//	★★★ 顺带查实的一个【放大器】（★ 与上面同源，★ 但独立）：
//	  ★★ `getLocal` / `loadSharedLocked` 在 `st == nil`（= L3 返 0 行）时
//	  ★★ **既不填 L1、也不提交 Redis** ⇒ ⇒ **下一次请求还会再查一次 L3**
//	  ⇒ ⇒ ★★ ⇒ **L3 的调用量与「请求数」同阶，而不是与「会话数」同阶**。
//
//	★★★ 诚实边界（★ 别外推）：
//	  ★★ 本轮**没有**用 154 的服务日志做交叉验证（★ 边界与 §143.77.6 相同）。
//	  ★★ ★★★ **一处自我修正（★ 本轮自己推翻自己的一句推断）**：
//	  ★★   本节第一版写的是「pg_stat_statements 的均值 145.644 / 150.342 ms，
//	  ★★   而本轮实测『库里查不到』那一档是 508.773 ms ⇒ ⇒ 说明生产上
//	  ★★   **有一部分调用是命中的**」—— ★★★ **这句推断被 `rows = 0` 直接否证**：
//	  ★★   ★★ 两个 queryid 的 `rows` **都是 0** ⇒ ⇒ **25,492 次【没有一次命中】**
//	  ★★   ⇒ ⇒ **不存在「一部分命中」这回事** ⇒ ⇒ ★★ 那 508.773 ms 的偏高
//	  ★★   ⇒ ⇒ 只能解释为【宿主负载差异】（★ 本轮复现跑在 4 核 / load 14 的宿主上，
//	  ★★   ⇒ ⇒ 生产 252 的均值 145~150 ms 才是生产口径）。
//	  ★★   ⇒ ⇒ ★★★ **收益估算必须用【生产均值 145~150 ms】，不能用本地那档**。
//	  ★★ ★★ 「25,492 次」是【pg_stat_statements 统计窗口】内的量，不是自然月量。
//	  ★★ ★★ 「一次性会话数」是 0 行调用数的【代理指标】，不是直接计数。
//	  ★★ 「探针请求是否真的走压缩器 hook」**已验**（★ 见 runbook §143.78.2）：
//	  ★★ ⇒ **直连上游的探针不走网关 HTTP 入口**（`bg/probe_http.go` 打的是
//	  ★★ ⇒ `t.BaseURL` + 供应商 key）⇒ ⇒ **不触达压缩器**；
//	  ★★ ⇒ **网关轮探针**（`bg/active_probe_executor.go` 的 `RunGateway`、
//	  ★★ ⇒ `bg/systemmonitor/executor.go` 的 `executeChatPing(gateway=true)`）
//	  ★★ ⇒ **会**走网关 HTTP 入口，但**不带任何会话头** ⇒ ⇒ 命中 ③ 的「新建会话」支。
//
//	★★★★★★ 本轮【不改行为】：★ 短路判据要落到【调用点】的哪一层（压缩器 hook 之前
//	还是 L3 适配器里）会改变【哪些会话仍走 v2 主路径】⇒ ⇒ **须与 ⑯/⑰ 一并拍板**。
//
// ★★★★★★ FIXME-14379（2026-10-23 §143.79 轮，只登记、★ 零行为变更）：
//
//	★★★★★★★ 【★ 把压缩器【冷启动读链】摊开成【四段瀑布】—— ★★★★ 并把
//	★★★★★★★ 「E1 门开着」的成本面从【一个】扩到【三个】，另附两条【订正】】
//
//	★★★★★★★ ★★★★★★ 【2026-10-23 轮末订正】：★ 本块的第一版数字【全部作废】，
//	★★★★★★★ 【被本轮自己的复算脚本 v2 推翻】—— ★ 原因是【腿指纹写窄】⇒ ⇒ 少算 66%。
//	★★★★★★★ 【作废的数字】（★ 保留留痕）：★ 「L3 26,241 次 / 命中率 26.8% /
//	★★★★★★★ 落空 73.2% / 合计 68.1 分钟 / 91,246 次无效往返」⇒ ⇒ ★★ 【它们是
//	★★★★★★★ 【跨快照相加】+【只认了 L3 的一个形态】的产物】⇒ ⇒ ★★ **不可再引用**。
//
//	★★★★★★ 一、读链的真实形状（★ 代码自证，★ 四段有顺序语义）：
//
//	  `domains/hooks/compression/session_cache.go` 的 `loadFromDB`：
//	     if c.turnReader != nil {
//	        msgs, err := c.turnReader.LoadLatestOutbound(...)   ← ① 快照腿
//	                                                              ★ 内含 ①b 回退腿
//	        if 空 → msgs, err = c.turnReader.LoadChain(..., 10)  ← ② delta 链腿
//	     }
//	     return c.loadFromLegacyDB(...)                          ← ③ L3 兜底腿
//
//	  `domains/session/v2/turn_reader.go` 的 `LoadLatestOutbound`：
//	     ①  kind='final_full' … ORDER BY ts DESC LIMIT 1
//	     ①b … ORDER BY turn_no DESC, ts DESC LIMIT 1   ← ★★ 回退腿（★ 无 kind 条件）
//
//	★★★★★★ 二、生产对账（252 共享库，只读，★ 统计窗口 4.1 天，
//	★★★★★★ ★★ 【全部读数来自同一条 SELECT 的单快照】⇒ ⇒ 数字自洽）：
//
//	★ 腿①  快照腿   ：36,228 次 / 73,730.1 ms / 均值 2.035 ms / rows 5,504
//	★ 腿①b 回退腿   ：**29,439 次 / 214,734.5 ms / 均值 7.294 ms** / rows 3,141
//	★ 腿②  delta 链 ：26,298 次 / 5,022.9 ms / 均值 0.191 ms / **rows 全 0**
//	★ 腿③  L3 兜底（★ 【三个形态】）：**26,299 次 / 3,929,073 ms / 均值 145~167 ms
//	★          / rows 全 0**（★ 形态 α 17,424 次 / 2,619,553.7 ms / 均值 150.342 ms
//	★          —— ★ 它是【单条最贵的腿】；形态 β 8,068 次 / 1,175,058.2 ms；
//	★          形态 γ 806 次 / 134,460.7 ms）
//	★ ⇒ ⇒ **读链合计 ≈ 4,463,233 ms ≈ 74.4 分钟** ⇒ ⇒ **L3 占 88.1%，
//	★ ⇒ ⇒ 而腿①b 占 4.8%（214.7 秒）—— ★★ 这条腿此前【完全无人知晓】**。
//
//	★★★★★★★ 【第五腿（★ 本轮新发现，此前完全无人知晓）】：
//	★ 写侧 `domains/session/v2/bodies_writer.go` 的 `getLatestBodies`
//	★ （★ 10 列投影，`ORDER BY turn_no DESC LIMIT 1`）
//	★ ⇒ **63,105 次 / 435,733.0 ms / 均值 6.905 ms / rows 7,111 ⇒ 命中率仅 11.3%**
//	★ ⇒ ⇒ **88.7% 的调用什么也没拿到**（★ 调用点：`session_writer_v2.go` 的
//	★ ⇒ ⇒ `getPreviousBody`（上一轮正文 + 附件 delta）与 `GetLatestBodiesInTx`（提交模式））
//	★ ⇒ ⇒ ★★ **它不在冷启动读链上（它在写路径上）⇒ ⇒ 但它读的是同一张表**
//	★ ⇒ ⇒ ★★ **⇒ 它的短路判据与读侧【不同】（它读的是「上一轮」⇒ 对非首轮
//	★ ⇒ ⇒ ★★ 的多轮会话是必要的）⇒ ⇒ ⇒ 属主拍板**。
//
//	★★★★★★ 三、落空瀑布【自洽】（★ 两条等式都【实测成立】）：
//
//	★ 按 `loadFromDB` 的【分支顺序】：腿① 未命中 ⇒ 腿①b ⇒ 腿①b 未命中 ⇒ 腿②
//	★ ⇒ 腿② 落空 ⇒ 腿③ ⇒ ⇒ 等式①：**腿①b 的未命中数 == 腿② 的调用数**
//	★ ⇒ ⇒ 生产实测 **26,316 == 26,316 ✔（差 0）**
//	★ ⇒ ⇒ 等式②：**腿② 的调用数 == L3 的调用数**（26,316 == 26,299，★ 差 17 在采样窗口内）
//	★ ⇒ ⇒ **⇒ 三段全落空的比例 = 72.59%**（★ 腿② 的 rows 全 0 是硬证据）
//	★ ⇒ ⇒ ★★ **⇒ 这个数是【下限】：见下面第五条（量具口径陷阱）**。
//
//	★★★★★★★ ★★ 【本轮自我推翻 #1】：★ 本块第一版写的等式是
//	★ 「腿①calls - 腿②calls == 腿①b 的 rows」⇒ ⇒ **实测差 4,426（不成立）**
//	★ ⇒ ⇒ ★★ **等式要按【代码里的分支顺序】写，不是按直觉写**。
//
//	★★★★★★ 四、★★ 订正一：「E1 门下 outbound_body 恒为 NULL」这句话【措辞不精确】
//
//	★ 实测（252 共享库，只读）：**整个 bodies 族的 `outbound_body` 里，
//	★ 真 SQL NULL = 0 行** ⇒ ⇒ 「没有正文」的行存的是 **jsonb `'null'`**，
//	★ 不是 SQL NULL ⇒ ⇒ ★★★ **因此 `outbound_body IS NOT NULL` 是【恒真判据】**
//	★ （294,839 / 294,839 全中）⇒ ⇒ **§143.76 的【结论】仍成立**（形态 A 确实没有真正文），
//	★ ★★ **但它的措辞要订正**：★ 「恒为 NULL」⇒ ⇒ **「恒为 jsonb 'null'」**。
//	★ ⇒ ⇒ ★★ **这个订正不是文字游戏，它直接改变了读侧的机制**：
//	★ ⇒ ⇒ **腿①b 的过滤 `outbound_body IS NOT NULL` 恒真 ⇒ ⇒ 它取到的是
//	★ ⇒ ⇒ 「该会话最新一轮的 outbound_body」—— 而 E1 门下那就是 jsonb `'null'`**
//	★ ⇒ ⇒ **实测坐实**：★ 1 轮会话取到的 payload = **`null`（4 字节）**
//	★ ⇒ ⇒ ★★★ ⇒ **腿①b 在 E1 门下【必然落空】**，只是它【返回了 1 行】。
//
//	★★★★★★ 五、★★★ 一个量具级的口径陷阱（★ 影响所有读数解读）：
//
//	★★ **`pg_stat_statements.rows` 对腿① / 腿①b 来说【高估了命中率】** ——
//	★★ 因为它们的过滤是恒真的 `outbound_body IS NOT NULL` ⇒ ⇒ 取到的可能是
//	★★ jsonb `'null'` ⇒ ⇒ **`rows` 记 1，但 Go 侧 `string(raw) == "null"` 判为空**
//	★★ ⇒ ⇒ **语义上仍然落空** ⇒ ⇒ ★★ **「72.59% 落空」是【下限】，真实值更高**
//	★★ （★ 实测抽样：腿①b 的 3,141 次命中里，★ 抽 5 个会话 ⇒ **3 个真正文 / 2 个 `null`**）。
//	★★ ⇒ ⇒ ★★ **通用式：★ `rows` 计的是【SQL 返回的行数】，不是【语义上的命中数】；
//	★★ 凡读侧有【恒真过滤】+【Go 侧二次判空】，量具的命中率就是【虚高】的**。
//
//	★★★★★★★ ★★ 【本轮自我推翻 #2】：★★ **复算脚本的【腿指纹写窄】⇒ 少算 66%**
//
//	★★ v1 脚本的腿③ 指纹是 `query LIKE '%COALESCE(ff.outbound_body%'` ⇒ ⇒ 它
//	★★ **只认到 §143.55/§143.56 之后的那一版** ⇒ ⇒ **完全漏掉** §143.55 之前的
//	★★ **旧内 JOIN 形态**（★ 那条恰是单条最贵的：17,424 次 / 2,619,553.7 ms）
//	★★ ⇒ ⇒ ★★ **L3 被读成 8,870 次（实际 26,299 次）⇒ ⇒ 「命中率 75.33%」
//	★★ ⇒ ⇒ 是错的（真实 27.41%）⇒ ⇒ 【结论的方向被带偏了】**。
//	★★ ⇒ ⇒ ★★★★ **通用式：★ 「读数变小」的第一问是【我认的形态全了吗】，
//	★★ 不是【数据变少了】—— ★ `calls` 是累计量、只增不减 ⇒ ⇒ 同一个脚本两次
//	★★ ⇒ ⇒ 读数变小 ⇒ ⇒ **一定是口径变了** ⇒ ⇒ 修法不是【把指纹写宽】，
//	★★ ⇒ ⇒ 而是【先普查、后认领、逐形态点名】**（★ 脚本 v2 已改，★ 形态 → queryid
//	★★ ⇒ ⇒ → 仓内归属 的映射表附在脚本头部）。
//
//	★★★★★★ 六、★★ 冷缓存才是腿成本的成因（★ 上一轮读数不单调 ⇒ 是量具问题）：
//
//	★ 取【第二次运行】（缓存已热）的读数：★ 腿①b 0.278 / 0.164 / 0.208 ms
//	★（1 / 64 / 787 轮）、★ 腿① 0.219 / 4.184 / 0.146 ms、★ 腿② 0.213 / 0.273 / 4.453 ms
//	★ ⇒ ⇒ **三条腿在热态全是【亚毫秒～4.5 ms】，且【与会话轮数几乎无关】**
//	★ ⇒ ⇒ ⇒ **生产的 7.294 ms 均值主要来自【冷缓存】**（★ 29,439 次访问的是
//	★ ⇒ ⇒ 大量不同会话 ⇒ 索引页常冷）⇒ ⇒ ★★ **⇒ 「腿①b 随轮数变贵」这个
//	★ ⇒ ⇒ 假设被否证**（★ 上一轮那条「64 轮 55.6 ms」是冷缓存的偶然）。
//
//	★★★★★★ 七、★★ 处置方向的【落点】修正（★ 改写 §143.78 留下的「落点待定」）：
//
//	★ §143.78 说「短路判据要落到【调用点】的哪一层 ⇒ ⇒ 须与 ⑯/⑰ 一并拍板」。
//	★ ⇒ ★★★★ **本轮把【读侧】的落点钉死了**：★ **落点 = 整个冷启动读链
//	★ ⇒ ⇒ （`loadFromDB` 的入口），不是 L3 那一腿。**
//	★ ⇒ ⇒ 理由（三条）：
//	★ ⇒ ⇒ ① L3 占读链成本 **88.1%** ⇒ ⇒ 短路它已拿到大部分收益（★ 但不是全部）；
//	★ ⇒ ⇒ ② 腿① / ①b / ② 合计 **91,965 次无效往返 + 293,487 ms（★ 6.6%）**，
//	★ ⇒ ⇒   **而那 72.59% 的三段全落空，在 Go 侧本来就返回 nil**
//	★ ⇒ ⇒   ⇒ ⇒ **⇒ 短路整条链【零语义风险】**（★ 不是「大概没用」，是「必然没用」）；
//	★ ⇒ ⇒ ③ 短路的判据【不变】：`sessionAssignment.AutoCreated`（★ 自动新建 ⇒
//	★ ⇒ ⇒   该 id 全库从未出现过 ⇒ ⇒ 四段必然全落空）。
//	★ ⇒ ⇒ ⇒ ⇒ **收益口径（★ 单快照）：从「63 分钟」升到
//	★ ⇒ ⇒ ⇒ ⇒ 「74.4 分钟 + 91,965 次无效往返」**（★ 读侧）。
//	★ ⇒ ⇒ ⇒ ⇒ ★★ **写侧第五腿另有 7.3 分钟 / 63,105 次 / 88.7% 落空**，
//	★ ⇒ ⇒ ⇒ ⇒ **但它的判据不同 ⇒ ⇒ ⇒ 属主拍板**（★ 动写路径，风险更高）。
//
//	★★★★★★ 八、★★ 「E1 门开着」的成本面：从【一个】扩到【三个】
//
//	★ ① 【写侧】形态 A（§143.76）★ ② 【读侧】v2 主路径 **72.59%** 落空（本轮）
//	★ ③ 【读侧】L3 恒 0 行（§143.78）
//	★ ⇒ ⇒ ★★★ ⇒ **⇒ ⑮（E1 关不关）这个决策多出了两个读侧成本面** ⇒ ⇒
//	★ ⇒ ⇒ ★★ **⇒ 且关 E1 会【让 v2 主路径重新变得有用】**（★ 库里还有
//	★ ⇒ ⇒ ★★ 88,756 行真正文 = 30.10%，★ 一旦 E1 停下、这部分继续增长 ⇒ ⇒
//	★ ⇒ ⇒ ★★ 腿①b 就会开始【有产出】）⇒ ⇒ ⇒ **⇒ 「关 E1」与「⓪ 短路」不是
//	★ ⇒ ⇒ ★★ 互斥项，而是【两个不同层面的东西】：★ ⓪ 消除【注定落空】的调用，
//	★ ⇒ ⇒ ★★ 关 E1 恢复【本该有】的正文 ⇒ ⇒ ★★ **两者都做才是最优**。
//
//	★★★★★★ 诚实边界（★ 别外推）：
//	  ★★ 本块的读数全部来自【同一条 SELECT 的单快照】（★ v1 那组是跨快照相加 ⇒ ⇒ 已作废）。
//	  ★★ 「72.59% 三段全落空」是【下限】：★ 腿①b 的 3,141 次 `rows` 里有一部分是
//	  ★★ jsonb `'null'` ⇒ ⇒ **语义落空比例更高**。
//	  ★★ 腿成本读数（§六）取自【本机复现】⇒ ⇒ **宿主负载与生产不同**
//	  ★★ ⇒ ⇒ 生产口径请用 `pg_stat_statements` 的均值。
//	  ★★ 第五腿的「88.7% 落空」**不等于「可以短路」**（★ 它读的是「上一轮」）。
//	  ★★ 本轮【不改行为】：★ 落点虽已钉死，★ 但改动会改变【哪些会话仍走 v2 主路径】
//	  ★★ ⇒ ⇒ **须与 ⑯/⑰ 一并拍板**。
//
// ★★★★★★ FIXME-14380（2026-10-23 §143.80 轮，只登记、★ 零行为变更）：
//
//	★★★★★★★ 【★ 补上连续三轮的「诚实边界未验项」—— ★★★★★★ 【首次用 154 的
//	★★★★★★★ 服务日志做交叉验证】⇒ ⇒ ★★★ 库侧与服务侧从此可以互相印证；★★★
//	★★★★★★★ 另发现【第五腿的落空不是单一原因，而是【两类性质完全不同】的】——
//	★★★★★★★ ★★ 其中【结构性】那一类（分支会话）【永久不写 bodies】⇒ ⇒
//	★★★★★★★ ★★ 它的短路判据与读侧【完全不同】⇒ ⇒ ⇒ 属主拍板】
//
//	★★★★★★ 一、★ 服务侧的直接证据（★ 本轮第一次有）
//
//	★ 154 上跑的是 `llm-gateway-go-canary@8782.service`（★ ★ `llm-gateway-go`
//	★ 这个服务名在 154 上是 **masked** ⇒ ★ 别找错）。
//	★ 黄金信号：`{"msg":"session_compressor: v2 cache loaded successfully",
//	★ ★ "session":"gw_…","body_size":N}`
//	⇒ ⇒ ★★ **`body_size = 0` 就是【语义落空】的直接证据** ——
//	★ ⇒ ⇒ ★ 它正是 §143.79 那条「量具 `rows` 记 1、但 Go 侧判空」
//	★ ⇒ ⇒ ★ 在【服务侧】的对应物 ⇒ ⇒ ★★ 库侧与服务侧从此可以互相印证。
//
//	★★★★★★ 二、★ 服务侧量化（154，只读，★ 三次采样，★ 结论稳定）
//
//	★ 采样一（80 分钟）：624 次冷启动 / 284 distinct 会话
//	★ 采样二（80 分钟，延后）：685 次 / 308 distinct
//	★ 采样三（40 分钟）：530 次 / 195 distinct
//	★ ┌────────────────────┬────────────┬────────────┬──────────┐
//	★ │ 类别               │ 次数       │ body=0     │ 占比     │
//	★ │                    │ ①/②/③     │ ①/②/③     │ ①/②/③   │
//	★ ├────────────────────┼────────────┼────────────┼──────────┤
//	★ │ 一次性 `gw_<uuid>` │ 256/277/163│ 256/277/163│ 100.00% │
//	★ │ 分支 `gt_gw_<uuid>` │ 26/29/30   │ 26/29/30   │ 100.00%  │
//	★ │ 固定长会话（omp）   │ 342/379/337│ 73/83/74   │ 21.4~21.9%│
//	★ │ 【排除 omp】       │ 282/306/193│ 282/306/193│ 100.00%  │
//	★ └────────────────────┴────────────┴────────────┴──────────┘
//	★ ⇒ ⇒ ★★★ **排除固定长会话后 = 282/282、306/306、193/193 = 100% 落空**
//	★ ⇒ ⇒ ★★ （★ 三次采样都是 100% ⇒ ⇒ 结论稳定 ⇒ ⇒ 不是窗口巧合）
//	★ ⇒ ⇒ ★★ **一次性会话：每个恰好冷启动 1 次，且 100% 落空**
//	★ ⇒ ⇒ ⇒ **与 §143.78 的机制推理（每个请求一个新 id ⇒ 首轮必然无行）完全吻合**
//
//	★★★★★★ ★ 三、★★ 【本轮推翻自己的一个说法】：★ 56.89% 这个数是【被稀释的】
//
//	★ 混合口径下算出来的「56.89% 落空」★ 【掩盖了真结论】——
//	★ 固定长会话（omp）379 次冷启动里 21.9% 落空 ⇒ ⇒ **把一次性会话的 100% 稀释了**
//	★ ⇒ ⇒ ★★ **通用式：★ 混合口径的比率会把子群的极端值稀释掉 ⇒ ⇒ "
//	★ ⇒ ⇒ ★★ ★ **凡是「某个子群 100% 落空」这种强结论，必须单独报，不能只报总体比率**。
//
//	★★★★★★ ★ 四、★★★★★ 【第五腿的落空 = 两类性质完全不同的机制】
//
//	★ 第五腿 = 写侧 `domains/session/v2/bodies_writer.go` 的 `getLatestBodies`
//	★ （★ §143.79 新发现：63,551 次 / 438,340 ms / 均值 6.897 ms / 命中率 11.50%）
//
//	★ 【时序性】★ 一次性会话的【首轮】⇒ 该 session 在 bodies 族【还没行】
//	★   ⇒ 实测：143,987 个会话（34.23%）属于此类
//	★ 【结构性】★★★ ★★ 分支会话（`gt_gw_` / `gs_gw_`）⇒ ★★ 【永久不写 bodies】
//	★   ⇒ 实测：**13,124 个会话，100.00% 无 bodies 行**（★ 分支会话的 turns 侧有 36,103 行）
//	★   ⇒ ⇒ ★★ **⇒ 这不是「还没写」，是「从来不写」⇒ ⇒ 它的落空是永久性的**
//
//	★★★★★★ ★ 五、★★★ 【结构性缺失的机制链】（★ 代码自证 + 生产实测，两处互证）
//
//	★ ① `admin/auto_title_generator.go` / `admin/auto_summary_generator.go`
//	★    ⇒ 它们用 `X-Gw-Session-Id: gt_<session_id>` / `gs_<session_id>`
//	★    ⇒ **loopback 自调用**（★ 实测 `origin_actor` = `auto-title-generator` /
//	★    ⇒ `auto-summary-generator`，`source_kind = 'live'` ⇒ ★ 走的是正常 live 写路径）
//	★ ② `domains/streaming/handler.go` 的 `serveWithExecutor` 里：
//	★    `if isBranchSessionID(sessionID) { …不自动创建 session… }`
//	★    ⇒ ★★ **`sessionInfo` 保持 nil** ⇒ ⇒ **后续 v2 写入路径不完整**
//	★    ⇒ ⇒ ★★ **turns 写了、bodies 没写**
//	★ ③ 生产实测坐实：★ `session_bodies_unified` 里【一条 branch 行都没有】
//	★    （★ 295,340 行 bodies 全部是 plain 会话）⇒ ⇒ ★ 与 ② 完全吻合
//
//	★★★★★★ ★ 六、★★ 【⇒ ⇒ 处置含义（★ 属主拍板的关键差异）】
//
//	★ 读侧（§143.79 的 ⓪）：★ 判据 = `sessionAssignment.AutoCreated`
//	★   ⇒ ★★ 【自动新建 ⇒ 该 id 全库从未出现过 ⇒ ⇒ 必然 0 行 ⇒ ⇒ 零语义风险】
//	★ 写侧（第五腿）：★ ★★ 【判据完全不同】
//	★   ⇒ ★★ 【结构性那批（分支会话）短路了也没用 —— 它们【本来就没有正文】，
//	★   ⇒ ⇒ ★★ 读了也只会读出 0 行 ⇒ ⇒ 但读它【确实浪费】⇒ ⇒ 仍值得省】
//	★   ⇒ ★★ 【时序性那批（一次性会话首轮）—— 判据是【本轮是不是该会话的首轮】，
//	★   ⇒ ⇒ ★★ 而不是【会话是否自动新建】⇒ ⇒ ★★ ⚠ 这两者不等价】
//	★ ⇒ ⇒ ⇒ ★★ **⇒ 「一次授权同时处理读侧与写侧」是错的 ⇒ ⇒ 必须分开拍板**
//
//	★★★★★★ ★ 七、★★ 【本轮抓到的最隐蔽的一处量具缺陷】
//
//	★★ ★★ 我一直用 `bash lgwp-prod.sh <sql> <timeout> | tail -N` 然后 `echo $?`
//	★★ ⇒ ⇒ ★★ **`$?` 拿到的是 `tail` 的退出码，不是 psql 的！**
//	★★ ⇒ ⇒ ★★ 所以「rc=0」这个判据一直是【假的】⇒ ⇒ 一个真的报错的脚本
//	★★ ⇒ ⇒ ★★ 也会被读成「跑通了」⇒ ⇒ ★★ 【本轮就有一个脚本真的报了
//	★★ ⇒ ⇒ ★★ `syntax error at or near "UNION"`，而当时的判据说它 rc=0】。
//	★★ ⇒ ⇒ ★★★ **通用式：★ 判据要读【被测对象】的退出码，不是【管道最后一环】的**
//	★★ ⇒ ⇒ ★★★ （★ 用 `set -o pipefail` 或显式捕获；★ 门
//	★★ ⇒ ⇒ ★★★ `cmd/gateway/section_14380_gateway_log_crosscheck_test.go` 的
//	★★ ⇒ ⇒ ★★★ `TestSection14380ProdExitCodeMustBeReal` 已钉住）
//	★★ ⇒ ⇒ ★★ 结论层面的影响：★ §143.79 的脚本用真退出码重验后【仍是 0】⇒ ⇒
//	★★ ⇒ ⇒ ★★ **那个结论侥幸成立，但【方法】是错的 ⇒ ⇒ 方法必须一起修**
//
//	★★★★★★ ★ 八、★★ 本轮【第二次】踩到同一个 UNION 坑（★ §143.77 已记录过）
//
//	★★ `FOR rec IN SELECT … LIMIT 3 UNION ALL SELECT … LIMIT 3`
//	★★ ⇒ ⇒ 报 `syntax error at or near "UNION"` ⇒ ⇒ 【UNION 分支里带
//	★★ ORDER BY / LIMIT 必须加括号】⇒ ⇒ ★★ 最稳的写法是【拆成两个独立循环】
//
//	★★★★★★ 诚实边界（★ 别外推）：
//	  ★★ 服务侧样本只有【一个网关实例】（★ 154 的 canary 槽 8782）⇒ ⇒
//	  ★★ 【245 的那一份没有覆盖】⇒ ⇒ 「100% 落空」这个结论的适用范围是 154 这一实例。
//	  ★★ 服务侧窗口是【分钟级】，库侧是【4.1 天】⇒ ⇒ ⇒ 两者**不是同一份快照**
//	  ★★ ⇒ ⇒ ★★ **它们互相印证的是【机制】，不是【同一个数】**。
//	  ★★ 「结构性 3.12% / 时序性 34.23%」是【按会话数】算的 ⇒ ⇒ ★
//	  ★★ 第五腿的【调用次数】落空占比没有单独拆到类里（★ 调用次数的分布与会话数不同）。
//	  ★★ 固定长会话（omp）为什么每会话冷启动 ~190 次：**未查实**（★ 已见它来自
//	  ★★ 本机 127.0.0.1 的 `/v1/chat/completions`，★ `user_agent: Go-http-client/1.1`）。
//	  ★★ 本轮【不改行为】：★ 写侧第五腿的处置动的是【写路径】⇒ ⇒ 风险等级高于读侧。
// FIXME-14381（§143.81 · 2026-10-23 · 累计 vs 现时口径分离轮 · ★★★ 本块是【纯注释】，零行为变更）
//
// ★★★★ 为什么这个块在这里（★ 它是 L3 冷启动式的旁边，不是别处）：
//
//	★ 这条 SQL 就是「压缩器冷启动读链」的第 ③ 段（L3 兜底）。
//	★ §143.79 / §143.80 把它的【累计耗时】当成【现时成本】（★「L3 占 88.1%」），
//	★ §143.81 实测发现：★ 三条同族形态里有两条 `delta_calls` **恒为 0** ⇒ 已死。
//	★ ⇒ ⇒ ★★ **判断一条 stats 记录是「历史形态」还是「当前形态」，
//	★ 要看【形态指纹】（queryid = hash(规范化文本)），不要看时间窗口。**
//
// ★★★ 本文件这条形态的【指纹特征】（★ 库里实测的全文差异，§143.81）：
//
//	L3_alpha（旧）: SELECT sb.outbound_body, …                          ⇒ 无 COALESCE / 无 LATERAL
//	L3_beta （中）: … IS NOT NULL OR … IS NOT NULL                       ⇒ §143.56 之前
//	L3_gamma（★现）: … IS NOT NULL AND … <> 'null'::jsonb              ⇒ §143.56 之后 = 本文件这条
//
//	⇒ ★★ 判别器 = 「query 文本里含 outbound_body <> $n::jsonb」
//	⇒ ⇒ ★★ **所以：只要这条 SQL 的「有正文」判据一改，形态指纹就换 ⇒ ⇒ 旧的 stats 读数立即作废。**
//
// ★★★ 与【现时成本】有关的三个量具事实（★ 全部本轮生产实测，★ 对生产的写入 = 0）：
//
//	① ★★ `pg_stat_statements` 的 calls/rows/total_exec_time **只增不减** ⇒ ⇒ 「累计大」≠「现在还在变大」
//	   ⇒ ⇒ ★★ 判定「此刻还在不在跑」必须用**两次采样取增量**（★ 工具：scripts/audit/diff_l3_snapshots.py）。
//	② ★★ `pg_stat_statements` **只记成功语句** ⇒ ⇒ 失败的读**在 stats 里完全隐形**
//	   ⇒ ⇒ ★★ 「现时成本」是【可见语句】的下界，★ 不是全部成本。
//	   ⇒ ⇒ ★★ **好消息**：★ 本文件这条读会话族（★ 不读 v1 族）⇒ ⇒ 它能成功执行 ⇒ ⇒ 它的成本是可见的。
//	③ ★★ v1 族的兜底腿已在迁移 890 整体退役 ⇒ ⇒ **本文件【必须不含 `_request_logs`】**
//	   ⇒ ⇒ ★★ 这条是**退役守卫**（★ 若它回来 ⇒ 必然 42P01 ⇒ 「会话族没命中」会从「回退」变成「整条查询报错」）。
//
// ★★ 门：cmd/gateway/section_14381_l3_shape_test.go（★ 含上述 ③ 的退役守卫）
// ★★ 复算：scripts/audit/verify_l3_leg_value.sql（★ Q7 自动判定「当前形态的 queryid」）
// ★★ 订正：runbook §143.81 + §143.79.3 的「订正横幅一（2026-10-23 §143.81 轮）」

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
