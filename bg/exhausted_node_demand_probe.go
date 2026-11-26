package bg

// exhausted_node_demand_probe.go —— 「额度耗尽节点」的**需求驱动**复探调度器。
//
// # 为什么要有这个组件（2026-11-26 · 211 充值事故）
//
// 凭据被判「永久配额耗尽」（quota_state='permanently_exhausted'）之后，恢复
// 只剩两条路：
//
//	① 时间轮询：bg/balance_quota_probe.go 的余额探针。它的 due gate 是**指数
//	   退避、上限 1 小时**（注释自认「充值后最迟 1h 自动恢复」）⇒ 充值后最长
//	   1 小时才被发现。
//	② 业务流量：真实请求成功 ⇒ writeHealth 翻绿。但候选 SQL（provider/client.go
//	   :1292 等五处）**整体排除** quota 红的凭据 ⇒ 零候选 ⇒ 没有业务流量 ⇒
//	   也没有健康证据 ⇒ 自锁。
//
// ⇒ 结果就是「充值了，但网关最长 1 小时不知道」。
//
// # 本组件做什么
//
// 把「发现充值」从**纯时间轮询**升级为**需求驱动**：
//
//	新请求命中「本模型的额度耗尽节点」 ⇒ 立即为这些凭据发起一次探活
//	（不等退避、不等 1 小时）⇒ 探到已恢复 ⇒ 凭据翻绿 ⇒ 候选恢复 ⇒ 模型立刻可用。
//
// # 防轰炸：探测间隔自动回退，封顶 5 分钟
//
// 同一个凭据被反复「撞到」（模型仍在用、节点仍在耗尽）时，探测间隔按
//
//	第 1 次：立即（不等）
//	第 2 次：30s → 第 3 次：60s → 第 4 次：120s → 第 5 次：240s → 第 6 次起：300s（封顶）
//
// 自动回退。封顶 5 分钟 ⇒ **最坏情况下凭据最多每 5 分钟被探一次**，既不会
// 因为上游还没到账而把上游打爆（720 次/天那种量级），也不会再出现「1 小时
// 才发现」的黑洞。
//
// 凭据恢复（不再出现在耗尽集合里）⇒ 它的回退计数清零 ⇒ 下次耗尽时又是
// 「立即探测」。
//
// # 与既有探测族的关系（谁负责什么）
//
//	· node_probe        —— 节点/模型级健康（直连 + 网关轮），负责「这个模型在
//	                       这个节点上还通不通」。
//	· credential_probe_v2 —— 凭据级探活（chat/embeddings 形态），负责凭据的
//	                       配额/鉴权/可达性判定，并负责把 quota_state 翻回 ok。
//	· balance_quota_probe —— **时间驱动**地驱动 credential_probe_v2 去复探
//	                       「永久/余额配额耗尽」的凭据（退避上限 1 小时）。
//	· 本组件              —— **需求驱动**地驱动同一套 credential_probe_v2：
//	                       「有请求 ⇒ 有人在等 ⇒ 立刻探一次」。
//
// 两条驱动路径共用同一个凭据级探针，因此恢复动作完全一致（writeHealth 链）。

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// dbQuerier 是本组件需要的**最小** DB 能力（*pgxpool.Pool 天然满足）。
//
// ★ 为什么不用 *pgxpool.Pool 直接做字段类型：那样单测就得连真库。
//
//	抽象成一行接口后，单测用 pgxmock（同样的 Query 形态）即可覆盖
//	「扫描 → 回退 → 提交」全链路，不必依赖生产库。
type dbQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const (
	// demandProbeFirstInterval 是「第二次及以后」两次探测之间的最小间隔。
	// 第一次是**立即**发起（不等这个间隔）。
	demandProbeFirstInterval = 30 * time.Second
	// demandProbeMaxInterval 是回退间隔的**上限**——需求驱动也不能把上游
	// 打爆（比余额探针的 1 小时上限密，但仍是有界的）。
	demandProbeMaxInterval = 5 * time.Minute
	// demandProbeScanTTL 是「该模型的额度耗尽凭据集合」的缓存时长：同一模型
	// 的连续请求不必每次都打库（请求热路径 ⇒ 必须有界）。
	demandProbeScanTTL = 60 * time.Second
	// demandProbeMaxCredentialsPerRequest 是单次请求最多触发的探测数（放大
	// 比上限）。一个模型可能挂着十几条耗尽的绑定；全探一遍会把上游打爆。
	demandProbeMaxCredentialsPerRequest = 8
)

// ExhaustedNodeDemandProbe 是「需求驱动复探」的调度器。
//
// 零值不可用：用 NewExhaustedNodeDemandProbe 构造。submit 为 nil 时组件保持
// 惰性（不查库、不探测）⇒ 未接线的部署行为与今天逐字节相同。
type ExhaustedNodeDemandProbe struct {
	db dbQuerier
	// submit 触发一次**立即**探活；返回 false 表示「没排上队」（已有同凭据的
	// 探针在跑）⇒ 调用方据此不把这次计入回退次数（否则回退虚增、间隔越拖
	// 越长，而实际一次探都没多发）。
	submit func(credID int) bool

	now func() time.Time

	mu    sync.Mutex
	state map[int]*demandProbeState

	scanMu       sync.Mutex
	scannedAt    map[string]time.Time
	scannedCreds map[string][]int
}

// demandProbeState 是单个凭据的回退状态。
type demandProbeState struct {
	// attempts 已经发起过的探测次数（连续未恢复 ⇒ 递增）。
	attempts int
	// lastProbeAt 上一次**真正排上队**的探测时刻。
	lastProbeAt time.Time
}

// NewExhaustedNodeDemandProbe 构造需求驱动复探调度器。
//
// submit 应当是 bg.CredentialProbeV2.ProbeNowAsyncIfIdle（立即执行、无 5
// 分钟延迟、且回报是否真的排上队）。
func NewExhaustedNodeDemandProbe(db dbQuerier, submit func(credID int) bool) *ExhaustedNodeDemandProbe {
	return &ExhaustedNodeDemandProbe{
		db:           db,
		submit:       submit,
		now:          time.Now,
		state:        make(map[int]*demandProbeState),
		scannedAt:    make(map[string]time.Time),
		scannedCreds: make(map[string][]int),
	}
}

// exhaustedNodeDemandProbeSQL 找出「本模型、额度已耗尽」的凭据。
//
// ★ 模型匹配用的是与候选 SQL **同一套 4 路矩阵**（canonical_raw_name /
//
//	standardized_name / models_canonical.canonical_name / model_aliases）——
//	否则会出现「诊断说模型没登记、实际登记在别名上」这类假阴性。
//
// ★ 租户过滤用 p.tenant_id（与候选 SQL 同口径）⇒ 不跨租户探别人的节点。
const exhaustedNodeDemandProbeSQL = `
SELECT DISTINCT c.id
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers  p ON p.id = c.provider_id
LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
WHERE COALESCE(c.quota_state, 'ok') IN ('permanently_exhausted','balance_exhausted','periodic_exhausted')
  AND (p.tenant_id = $2 OR $2 = '')
  AND (
        mo.canonical_raw_name = $1
     OR mo.standardized_name = $1
     OR mc.canonical_name = $1
     OR EXISTS (
         SELECT 1 FROM model_aliases ma
         WHERE ma.raw_name = $1
           AND COALESCE(ma.status, 'active') = 'active'
           AND mo.canonical_id IS NOT NULL
           AND ma.canonical_id = mo.canonical_id
       )
  )
ORDER BY c.id`

// exhaustedNodeDemandProbeRecoverySQL 问「这把凭据现在还耗尽吗」——**凭据级**
// 真值（只看 credentials.quota_state，与模型无关）。
//
// ★★ **为什么恢复判定必须走这条独立查询，而不能用「本次模型切片的缺席」
//
//	来推断**：
//
//	回退计数是**凭据级**的（一把凭据探了 N 次 ⇒ 间隔按 N 推进，封顶 5 分钟），
//	而「本模型的耗尽集合」只是**模型级切片** ⇒ 若用切片缺席判「已恢复」：
//
//		凭据 31 在模型 A 上耗尽（attempts=5 ⇒ 已封顶 300s）
//		⇒ 模型 B 的请求触发扫描 ⇒ B 的切片里没有 31
//		⇒ 若据此判「31 已恢复」⇒ 回退状态清零
//		⇒ 下次模型 A 的请求立即探测 ⇒ 5 分钟封顶被击穿
//		⇒ 高频请求下退化成「每请求探一次」⇒ 把上游打爆
//
//	⇒ 所以恢复判定必须问**凭据自己的 quota_state** ⇒ 跨模型口径天然成立。
//
// ★ 保守方向：宁可**多探一次**（安全），不可**漏探**（打爆上游）⇒
//
//	查询失败时**保留**回退状态（不清零）⇒ 只会多探，不会漏探。
const exhaustedNodeDemandProbeRecoverySQL = `
SELECT id,
       CASE WHEN COALESCE(quota_state, 'ok') IN ('permanently_exhausted','balance_exhausted','periodic_exhausted')
            THEN TRUE ELSE FALSE END AS still_exhausted
FROM credentials
WHERE id = ANY($1)`

// NotifyRequest 是**需求驱动**的入口：新请求打到了 model 上 ⇒ 若该模型有
// 额度耗尽的节点 ⇒ 立刻为它们发起探活（受回退闸约束）。
//
// 返回 (submitted, skipped)：submitted = 本次真正排上队的探测数；
// skipped = 被回退闸挡下的（或被组件自己的上限裁掉的）数量。
//
// ★ 本方法**永不阻塞**调用方超过一次 DB 查询（带 2s 独立预算）；真正的探测
//
//	是异步的（submit → ProbeNowAsync → 后台 goroutine）。
func (p *ExhaustedNodeDemandProbe) NotifyRequest(ctx context.Context, model, tenantID string) (submitted, skipped int) {
	if p == nil || p.db == nil || p.submit == nil {
		return 0, 0
	}
	if model == "" {
		return 0, 0
	}
	creds, err := p.exhaustedCredentials(ctx, model, tenantID)
	if err != nil {
		slog.Warn("exhausted node demand probe: scan failed",
			"model", model, "tenant_id", tenantID, "error", err)
		return 0, 0
	}
	if len(creds) == 0 {
		return 0, 0
	}
	if len(creds) > demandProbeMaxCredentialsPerRequest {
		skipped += len(creds) - demandProbeMaxCredentialsPerRequest
		creds = creds[:demandProbeMaxCredentialsPerRequest]
	}
	for _, credID := range creds {
		if !p.due(credID) {
			skipped++
			continue
		}
		if !p.submit(credID) {
			// 已有同凭据的探针在跑 ⇒ 本次没发出去 ⇒ 不计入回退计数。
			skipped++
			continue
		}
		p.recordSubmission(credID)
		submitted++
	}
	if submitted > 0 {
		slog.Info("exhausted node demand probe: probes dispatched for exhausted nodes",
			"model", model, "tenant_id", tenantID,
			"submitted", submitted, "skipped", skipped)
	}
	return submitted, skipped
}

// exhaustedCredentials 返回该模型的额度耗尽凭据（带 TTL 缓存）。
func (p *ExhaustedNodeDemandProbe) exhaustedCredentials(ctx context.Context, model, tenantID string) ([]int, error) {
	key := tenantID + "|" + model
	now := p.now()

	p.scanMu.Lock()
	if at, ok := p.scannedAt[key]; ok && now.Sub(at) < demandProbeScanTTL {
		creds := p.scannedCreds[key]
		p.scanMu.Unlock()
		return creds, nil
	}
	p.scanMu.Unlock()

	// ★ 独立 2s 预算：这是**请求热路径**上的旁路查询，DB 抖动时不能让请求
	// 跟着等（宁可这次不探，下一次请求再探）。
	scanCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := p.db.Query(scanCtx, exhaustedNodeDemandProbeSQL, model, tenantID)
	if err != nil {
		// ★ stale-while-error：扫描失败时**继续用上一次扫到的耗尽集合**。
		//   否则一次 DB 抖动就把「需求驱动复探」整个关掉 ⇒ 又回到「等 1 小时
		//   退避」的老坑（而 DB 抖动往往正好发生在上游大面积异常的时刻）。
		if cached := p.cachedCredentials(key); len(cached) > 0 {
			slog.Warn("exhausted node demand probe: scan failed, serving cached exhausted set",
				"model", model, "tenant_id", tenantID, "cached", len(cached), "error", err)
			return cached, nil
		}
		return nil, err
	}
	defer rows.Close()
	creds := make([]int, 0, 8)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		creds = append(creds, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	p.scanMu.Lock()
	p.scannedAt[key] = now
	p.scannedCreds[key] = creds
	p.scanMu.Unlock()

	// ★ 恢复检测：按**凭据级**真值（credentials.quota_state）判断哪些凭据
	//   已经恢复 ⇒ 它们的回退计数清零 ⇒ 下次再耗尽时又是「立即探测」。
	//   ★ 走独立的凭据级查询（不是「本次模型切片里缺席」）⇒ 跨模型口径
	//     成立 ⇒ 不会击穿 5 分钟封顶。详见 forgetRecovered 的注释。
	p.forgetRecovered(ctx)
	return creds, nil
}

// cachedCredentials 返回上一次扫到的耗尽凭据集合（stale-while-error 用）。
func (p *ExhaustedNodeDemandProbe) cachedCredentials(key string) []int {
	p.scanMu.Lock()
	defer p.scanMu.Unlock()
	return p.scannedCreds[key]
}

// due 判断该凭据此刻是否该探测（回退闸）。
func (p *ExhaustedNodeDemandProbe) due(credID int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state[credID]
	if st == nil || st.attempts == 0 {
		return true // 首次：立即
	}
	return p.now().Sub(st.lastProbeAt) >= demandProbeInterval(st.attempts)
}

// recordSubmission 记录一次**真正排上队**的探测 ⇒ 回退计数 +1。
func (p *ExhaustedNodeDemandProbe) recordSubmission(credID int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state[credID]
	if st == nil {
		st = &demandProbeState{}
		p.state[credID] = st
	}
	st.attempts++
	st.lastProbeAt = p.now()
}

// forgetRecovered 清掉已恢复凭据的回退状态。
//
// ★★ **口径（本函数的全部要点）：恢复判定问「凭据自己的 quota_state」，
//
//	而不是问「它有没有出现在本次模型切片里」**。
//
//	原因：回退计数是**凭据级**的（一把凭据探了 N 次 ⇒ 间隔按 N 推进，封顶
//	5 分钟），而「本模型的耗尽集合」只是**模型级切片** ⇒ 若用切片缺席判
//	「已恢复」，模型 B 的一次扫描就会把「模型 A 上仍耗尽」的凭据 31 的回退
//	状态清零 ⇒ 下次模型 A 的请求立即探测 ⇒ **5 分钟封顶被击穿** ⇒ 高频
//	请求下退化成「每请求探一次」⇒ 把上游打爆。
//
//	⇒ 这里改为直接查 credentials.quota_state（凭据级真值，与模型无关）
//	  ⇒ 跨模型口径天然成立。
//
// ★ 保守方向：宁可**多探一次**（安全），不可**漏探**（打爆上游）⇒
//
//	查询失败时**保留**回退状态（不清零）。
func (p *ExhaustedNodeDemandProbe) forgetRecovered(ctx context.Context) {
	p.mu.Lock()
	tracked := make([]int, 0, len(p.state))
	for id := range p.state {
		tracked = append(tracked, id)
	}
	p.mu.Unlock()
	if len(tracked) == 0 {
		return
	}

	recovered, err := p.recoveredCredentials(ctx, tracked)
	if err != nil {
		// ★ 查询失败 ⇒ 保守：不清零（宁可多探一次）。
		slog.Warn("exhausted node demand probe: recovery check failed, keeping backoff state",
			"tracked", len(tracked), "error", err)
		return
	}
	if len(recovered) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range recovered {
		delete(p.state, id)
	}
	slog.Info("exhausted node demand probe: backoff state reset for recovered credentials",
		"recovered", len(recovered))
}

// recoveredCredentials 问「这些凭据里，哪些已经不再耗尽了」（凭据级真值）。
//
// ★ 「查无此行」也算恢复（凭据被删除了 ⇒ 不该再对它保留回退状态）。
func (p *ExhaustedNodeDemandProbe) recoveredCredentials(ctx context.Context, ids []int) ([]int, error) {
	scanCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// ★ pgx v5 对 int4[] 的原生编码是 []int32 ⇒ 这里显式转换（[]int 会被
	//   拒或被按 unknown 推断，行为不稳）。
	arg := make([]int32, 0, len(ids))
	for _, id := range ids {
		arg = append(arg, int32(id))
	}
	rows, err := p.db.Query(scanCtx, exhaustedNodeDemandProbeRecoverySQL, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stillExhausted := make(map[int]struct{}, len(ids))
	for rows.Next() {
		var id int
		var exhausted bool
		if err := rows.Scan(&id, &exhausted); err != nil {
			return nil, err
		}
		if exhausted {
			stillExhausted[id] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// ★ 仍然耗尽的留着；其余（不再耗尽 **或** 已被删除）⇒ 恢复。
	recovered := make([]int, 0, len(ids))
	for _, id := range ids {
		if _, still := stillExhausted[id]; !still {
			recovered = append(recovered, id)
		}
	}
	return recovered, nil
}

// demandProbeInterval 是回退曲线：30s → 60s → 120s → 240s → 300s（封顶）。
//
// attempts 是**已经发起过**的探测次数：attempts=1 ⇒ 刚探过 ⇒ 再等 30s；
// attempts=5 及以上 ⇒ 封顶 5 分钟。
func demandProbeInterval(attempts int) time.Duration {
	if attempts <= 0 {
		return 0
	}
	d := demandProbeFirstInterval
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= demandProbeMaxInterval {
			return demandProbeMaxInterval
		}
	}
	if d > demandProbeMaxInterval {
		return demandProbeMaxInterval
	}
	return d
}
