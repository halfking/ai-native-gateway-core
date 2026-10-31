package provider

// 可选信号降级（2026-10-31 实施包审计 · 两层路由可靠性）。
//
// 事故（211，2026-10-28 13:39–13:48）：prompt-cache 亲和信号 chr 以
// `CROSS JOIN LATERAL recent_cache_hit_rate(...)` 写进候选查询——它的默认权重为 0，
// 本应只是"没有信号则中性"的可选维度，却成了硬依赖。函数缺失（部署顺序竞态 / 新装
// 库 / 误删）时整条候选查询 42883，而 42883 被 isRetryableDBError 判为不可重试，于是
// 记为熔断失败，所有 /v1 请求 500。熔断器只把"全瘫"换成了"park 后快速全瘫"。
//
// 修法：可选维度必须 fail-open。函数缺失时改用一份**等价降级 SQL**——chr 投影为
// NULL/0，路由侧 cache 维度已把 NULL 视作中性（同 rsr 冷启动语义）。该错误**不计入**
// 熔断，并按间隔周期性重探测全量 SQL，函数补齐后自愈。

import (
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// candidateCacheSignalCallSQL 是 candidateQuerySQLBaseTemplate 中 chr 的唯一调用形态。
	// 降级变换只替换这一处；测试钉住其出现次数恰为 1，防止模板改写后降级静默失效。
	candidateCacheSignalCallSQL = "CROSS JOIN LATERAL recent_cache_hit_rate(c.id, mo.raw_model_name, 50) AS chr"
	// candidateCacheSignalAbsentSQL 与调用形态的语义等价前提：recent_cache_hit_rate 是
	// 无 GROUP BY 的聚合函数，恒返回且仅返回一行，因此 LEFT JOIN ... ON TRUE 在函数
	// 存在时结果逐行相同；函数缺失时投影为 NULL/0（中性）。
	candidateCacheSignalAbsentSQL = "LEFT JOIN LATERAL (SELECT NULL::double precision AS rate, 0::integer AS samples) AS chr ON TRUE"

	// candidateCacheSignalRecheck 是降级后重新探测全量 SQL 的间隔。函数补齐后至多这么久
	// 自愈；降级期间每个间隔只多付一次失败查询，而不是每个请求一次。
	candidateCacheSignalRecheck = 60 * time.Second

	// pgUndefinedFunction 是 PostgreSQL SQLSTATE 42883（undefined_function）。
	pgUndefinedFunction = "42883"
	cacheSignalFuncName = "recent_cache_hit_rate"
)

// candidateQuerySQLWithoutCacheSignal 返回去掉 chr 函数调用后的候选查询，
// 以及「降级是否真的生效」。
//
// ★ 若模板中找不到唯一的调用形态（模板被改写），返回完整 SQL 与 false：此时降级不生效，
// ★ 行为与修复前一致——宁可退化为旧行为，也不生成一条语义未知的 SQL。
// ★ ★★ **第二个返回值是 2026-10-31 12h 审计轮加的**：★★ ★ 旧调用点无论降级是否
// ★ ★ 真的生效都打点「已降级」⇒ ⇒ ⇒ **指标会谎报** ⇒ ⇒ ⇒ ★ 所以把「是否生效」
// ★ ★ **显式交回调用点**⇒ ⇒ ⇒ ★ 门自己也能直接钉这条。
//
// ★★★ **2026-10-31 审计轮修正：★ 降级 SQL 【不许】缓存成「进程级一次性」★★★
//
//	★ 旧实现用 `sync.Once` **算一次就永久缓存** ⇒ ⇒ ★★ **而 `candidateQuerySQL()`
//	★ 不是常量** —— 它按**运行时环境变量** `ModalityRoutingStrictEnv`
//	★ （`LLM_GATEWAY_MODALITY_ROUTING_STRICT`）在两套「模态判词闸门」之间切换
//	★ （★ 缺省档 / 严格档）⇒ ⇒ ★★ **闸门一旦在降级期间被切换 ⇒ ⇒ 降级路径会一直
//	★ ★ 用旧闸门** ⇒ ⇒ ⇒ ★★ ⇒ **路由策略与当前配置不一致** ⇒ ⇒ ★★ ⇒ **而且它完全
//	★ ★ 静默**（★ 没有报错、指标也看不出来）⇒ ⇒ ⇒ ★★ ⇒ 判据「降级 SQL 与完整 SQL
//	★ ★ 的差异不止 chr 一处」在切换后就会**反转为假**。
//
//	★★ **所以改成：★ 每次按【当前】SQL 现算** ⇒ ⇒ ★★ 口径永远跟着当前配置走。
//	★★ 代价：★ 模板 13.8 KB ⇒ ★ 一次 `strings.Count` + 一次 `strings.Replace`
//	★★ ⇒ ★ µs 级 ⇒ ⇒ ★★ 而降级窗口只有 60 s ⇒ ⇒ ⇒ ★ **代价可忽略，收益是口径不漂**。
func candidateQuerySQLWithoutCacheSignal() (string, bool) {
	full := candidateQuerySQL()
	if strings.Count(full, candidateCacheSignalCallSQL) != 1 {
		return full, false
	}
	return strings.Replace(full, candidateCacheSignalCallSQL, candidateCacheSignalAbsentSQL, 1), true
}

// isCacheSignalAbsent 判定错误是否为"chr 函数不存在"。只匹配 42883 且消息点名该函数，
// 其它函数缺失（如 rsr）不在此降级——rsr 是硬过滤信号，不能静默中性化。
func isCacheSignalAbsent(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == pgUndefinedFunction && strings.Contains(pgErr.Message, cacheSignalFuncName)
}

// cacheSignalDegrade 记录 chr 信号当前是否处于降级态。零值即"未降级"，无需构造。
type cacheSignalDegrade struct {
	// untilUnixNano 为 0 表示未降级；否则在该时刻之前全部走降级 SQL。
	untilUnixNano atomic.Int64
}

// degradedAt 报告 now 时刻是否处于降级态。
func (d *cacheSignalDegrade) degradedAt(now time.Time) bool {
	until := d.untilUnixNano.Load()
	return until != 0 && now.UnixNano() < until
}

// markDegraded 进入降级态，持续到 now+candidateCacheSignalRecheck。
//
// ★★★ **2026-10-31 12h 审计轮改成 CAS**：★★★ ★ 旧实现是无条件 Store ⇒ ⇒ ★★ 而
// ★★★ **并发同窗的每个请求**都会各打一次点、各告一次警 ⇒ ⇒ ⇒ ★★ ⇒ 「降级次数」
// ★★★ 这个口径被**并发放大**（★ 而它本该是【状态切换的次数】）⇒ ⇒ ⇒ ★
// ★★★ 所以改成 CAS：★ 只有**赢得状态切换**的那一个调用返回 true ⇒ ⇒ ⇒ ★
// ★★★ ⇒ 指标与告警只发生一次 ⇒ ⇒ ⇒ ★ 口径回到「次数」。
//
// ★★★ **窗口边界（★ 为什么过期后要重新抢）**：★★★ ★ 窗口过期 ⇒ ⇒ 重新探测全量
// ★★★ SQL ⇒ ⇒ **若函数仍未补齐**，★ 这一次的调用者就是新一轮的「首个撞上者」⇒ ⇒
// ★★★ ⇒ 它必须能**重新抢到** ⇒ ⇒ ⇒ ★ 所以「已过期」不算「已经在降级」。
func (d *cacheSignalDegrade) markDegraded(now time.Time) bool {
	want := now.Add(candidateCacheSignalRecheck).UnixNano()
	for {
		cur := d.untilUnixNano.Load()
		if cur != 0 && now.UnixNano() < cur {
			// 窗口内 ⇒ ⇒ 已经有别的请求完成了状态切换 ⇒ ⇒ 本次不是赢家。
			return false
		}
		if d.untilUnixNano.CompareAndSwap(cur, want) {
			return true
		}
	}
}
