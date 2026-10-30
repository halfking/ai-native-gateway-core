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
	"sync"
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

var (
	candidateQueryDegradedOnce sync.Once
	candidateQueryDegradedSQL  string
)

// candidateQuerySQLWithoutCacheSignal 返回去掉 chr 函数调用后的候选查询。
// 若模板中找不到唯一的调用形态（模板被改写），返回完整 SQL：此时降级不生效，行为与
// 修复前一致——宁可退化为旧行为，也不生成一条语义未知的 SQL。
func candidateQuerySQLWithoutCacheSignal() string {
	candidateQueryDegradedOnce.Do(func() {
		full := candidateQuerySQL()
		if strings.Count(full, candidateCacheSignalCallSQL) != 1 {
			candidateQueryDegradedSQL = full
			return
		}
		candidateQueryDegradedSQL = strings.Replace(full, candidateCacheSignalCallSQL, candidateCacheSignalAbsentSQL, 1)
	})
	return candidateQueryDegradedSQL
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
func (d *cacheSignalDegrade) markDegraded(now time.Time) {
	d.untilUnixNano.Store(now.Add(candidateCacheSignalRecheck).UnixNano())
}
