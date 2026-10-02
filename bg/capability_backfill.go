// capability_backfill.go — 按 credential_model 绑定回填 native Responses 能力位
// （2026-10-02，第三轮遗留 #1）。
//
// 背景 —— 这张表从来没有代码写过：
//
//	cand.SupportsNativeResponses 由 provider/client.go 的 SELECT 从
//	credential_model_capabilities 读出；全树唯一的引用就是那一条 SELECT，
//	没有任何 INSERT/UPDATE。迁移 612/613 的注释自认 "until an external probe
//	populates it" —— 那个 probe 不存在。实测 vapEUR/cred 126：~101 个绑定里
//	只有 1 行能力位，30 分钟内 /v1/responses 出站 0 次、chat 54 次。
//
// 本任务把那张表变成有维护者的表：对每个 openai-responses 协议的绑定定期跑
// 一次**现有** responses 探测器（probe_http.go 的 singleResponsesPing +
// providercap.ResponsesUnsupportedError），把结论写回
// credential_model_capabilities。
//
// 三条不可让步的约束：
//
//  1. **不新造探测协议。** 复用 singleResponsesPing —— 与 node_probe /
//     probe_http / model_probe 同一条出站路径、同一个端点解析、同一套
//     判定。回填任务自己不构造请求体、不自己判状态码。
//  2. **无证据不写。** singleResponsesPing 只在两种情形给出
//     supportsResponses：2xx（正向）与 ResponsesUnsupportedError（负向）。
//     网络错、5xx、429、鉴权失败一律返回 nil —— 此时**不写**。把「没探到」
//     写成 supported=false 就是本任务最可能的失效形态：那等于用默认值
//     覆盖掉一个可能完全正常的绑定，直接关掉它的 native 腿。
//  3. **只作用于非流式。** 探测器发的是一次非流式 /v1/responses
//     单请求，能力键只有 native_responses_nonstream 一个。流式腿无证据，
//     永不外推 —— 迁移 613 为此专门把 stream 拆成独立键，本任务不碰它。
//
// 运行经济性（R33 审计 2026-10-03 补）：蓝绿单跑选举（SetDistLock）、
// 无证据绑定的进程内 attempt 退避 + 扫描窗口放大（反饥饿）、出网 body 用
// outbound 名（node_probe 同款口径）。
package bg

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/admin/distlock"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/provider/catalog"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// CapabilityNonstream 是本任务唯一写入的能力键。流式键
// (native_responses_stream) 刻意不在此处出现：探测器只发一次非流式请求，
// 对 SSE 腿没有任何证据。
const CapabilityNonstream = "native_responses_nonstream"

const (
	// capabilityBackfillInterval 是回填周期。能力位不是健康信号，出错时的
	// 代价（误关 native 腿）由 staleAfter 兜底而不是由频率兜底，所以周期
	// 可以比探针慢一个量级。
	capabilityBackfillInterval = 30 * time.Minute
	// capabilityBackfillStaleAfter 是一行能力位在多久之后重新探测。探针结论
	// 的 Redis 侧寿命是 3600s（nodeCapabilityTTLSec），SQL 侧没有 TTL，
	// 过期与否由本字段显式承担。
	capabilityBackfillStaleAfter = 6 * time.Hour
	// capabilityBackfillBatchLimit 限制单轮**探测**行数（SQL 扫描窗口是它的
	// scanLimit 倍），避免一次全表回填把上游打爆。绑定数远大于它时，靠排序
	// + staleAfter + attempt 退避自然分批。
	capabilityBackfillBatchLimit = 50
	// capabilityBackfillProbeTimeout 限制单次探测的墙钟时间。
	capabilityBackfillProbeTimeout = 20 * time.Second
	// capabilityBackfillScanFactor：单轮扫描窗口 = 探测预算 × 该倍数。
	// 「无证据不写」（约束 2）意味着无证据行在 SQL 侧永远处于 due 态且按
	// NULLS FIRST 排在最前——窗口等于预算时，一批永远不产证据的绑定会把
	// 每轮预算吃满，把已过期健康行的刷新永久饿死（R33 审计 #2 后果②）。
	// 窗口放大后由内存侧 attempt 台账（见 capabilityBackfillAttemptBackoff）
	// 把近期试过的行让位给同批后面的行。
	capabilityBackfillScanFactor = 4
	// capabilityBackfillAttemptBackoff 是内存台账里对一条绑定两次探测之间的
	// 最小间隔。没有这层退避，「无证据」绑定每 30min 都被探一次（48 探/天）
	// 而健康行只有 4 探/天——探测成本反转到失败侧（R33 审计 #2 后果①）。
	// 台账是进程内的：重启即清空，最坏情况是重启后多探一轮，可接受。
	capabilityBackfillAttemptBackoff = time.Hour
	// capabilityBackfillAttemptLedgerMax 是台账的容量上限（防 map 无界增长；
	// 绑定数远小于它，触顶说明台账里堆满了过期项，触发一次清扫）。
	capabilityBackfillAttemptLedgerMax = 16384
	// capabilityBackfillDistLockTTL 是蓝绿单跑选举的持锁时长：周期 30min、
	// 最坏一轮 ~50 探 × 20s ≈ 17min，25min 保证正常一轮内不换主。
	capabilityBackfillDistLockTTL = 25 * time.Minute
)

// CapabilityBackfillEnvKillSwitch 置为 0/false/off/no 即关闭本任务。
//
// 为什么必须有：这个任务会对**每一个** openai-responses 绑定周期性地发一次
// 真实上游请求并消耗 token。实测 vapEUR / cred 126 就有 ~101 个绑定；按默认
// staleAfter=6h 折算，那一个凭据就是 ~400 次/天。绑定数 × 凭据数一多，这笔
// 账不由任何人显式批准过，但它确实在花。运营方必须能不重新发版就掐掉它。
const CapabilityBackfillEnvKillSwitch = "LLM_GATEWAY_CAPABILITY_BACKFILL"

// CapabilityBackfill 定期回填 native_responses_nonstream 能力位。
//
// scan / persist 是接缝：它们是仅有的两个 DB 触点，把它们参数化后，
// 「拿到真实上游帧之后到底写不写、写什么」这条决策可以在没有网关 schema 的
// 环境里对着**真实探测器**测。探测器本身不是接缝 —— 那条路径必须是真的，
// 否则测的就是 mock。
type CapabilityBackfill struct {
	db         *pgxpool.Pool
	encKey     []byte
	keyring    *secret.Keyring
	sink       ResponsesCapabilitySink
	interval   time.Duration
	staleAfter time.Duration
	batchLimit int
	distLock   distlock.Manager

	// attemptsMu/attempts 是进程内「已试过」台账：BindingID → 最近一次真实
	// 出网探测时刻。只有真正出过网才记账（admission 拒绝的不记——它们没有
	// 出网成本，复活后应立刻可探）。
	attemptsMu sync.Mutex
	attempts   map[int64]time.Time

	// probe 缺省走 singleResponsesPing。测试可以换掉它，但换掉之后测的就不再
	// 是「真实上游帧 → 真实判定」这条链。
	probe   func(ctx context.Context, target probeTarget, desc providercap.Descriptor) httpProbeResult
	scan    func(ctx context.Context) ([]dueBinding, error)
	persist func(ctx context.Context, row dueBinding, supported bool, evidence []byte) error
}

// NewCapabilityBackfill 构造回填任务。sink 可为 nil：它只负责把同一结论
// 镜像进 Redis node-state 能力键（请求期闸门的读源），不是结论本身。
func NewCapabilityBackfill(
	db *pgxpool.Pool,
	encKey []byte,
	keyring *secret.Keyring,
	sink ResponsesCapabilitySink,
) *CapabilityBackfill {
	return &CapabilityBackfill{
		db:         db,
		encKey:     encKey,
		keyring:    keyring,
		sink:       sink,
		interval:   capabilityBackfillInterval,
		staleAfter: capabilityBackfillStaleAfter,
		batchLimit: capabilityBackfillBatchLimit,
		probe: func(ctx context.Context, target probeTarget, desc providercap.Descriptor) httpProbeResult {
			endpoint := resolveProbeEndpoint(target, desc, ProbeModeResponses)
			if endpoint == "" {
				return httpProbeResult{status: "skipped", category: probeCategorySkipped,
					errCode: "endpoint_unresolved", errMsg: "empty base_url"}
			}
			// 出网 body 用 outbound 名，raw 名只用于日志——node_probe.probeDirect
			// 的同款约定（"Use the outbound name for the upstream body; fall back
			// to the raw name"）。对 outbound≠raw 的映射型中转（vapeur 类），用
			// raw 名探测会 404 model_not_found，而 404 不含 "responses api" 字样
			// → 判不出 ResponsesUnsupportedError → 永远产不出证据（R33 审计 #3）。
			modelField := target.OutboundModel
			if modelField == "" {
				modelField = target.RawModel
			}
			return singleResponsesPing(ctx, endpoint, target.APIKey, modelField,
				desc, target.CredentialID, target.RawModel)
		},
		scan:    nil, // 构造后由 Run/BackfillOnce 绑到 b.dueBindings
		persist: nil,
	}
}

// SetDistLock 接线蓝绿单跑选举（与 CallHistoryAggregator.SetDistLock 同契约：
// Start/Run 前调用；字段被 sweep goroutine 无同步读）。nil / 未启用 / Redis
// 故障时 acquireSweepDistLock 返回 nil，行为与本接线之前完全一致（双实例各
// 跑各的）——这是既有 sweep 族的统一降级路径。没有它，回填是本任务特有的
// 出网成本项，双实例双跑意味着上游调用翻倍（R33 审计 #1）。
func (b *CapabilityBackfill) SetDistLock(mgr distlock.Manager) {
	b.distLock = mgr
}

// Run 是周期循环。停机由 ctx 取消驱动，与其它 bg worker 一致。
// capabilityBackfillEnabled 读 kill switch。
//
// 每次轮询都重读，不在构造时定死：改环境变量需要重启才生效本来也是常态，但把
// 判据放在每次 tick 上可以让测试直接验证这条关闭路径，而不必起一个真 worker。
func capabilityBackfillEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(CapabilityBackfillEnvKillSwitch)))
	switch v {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

func (b *CapabilityBackfill) Run(ctx context.Context) {
	if b == nil || b.db == nil {
		return
	}
	if !capabilityBackfillEnabled() {
		slog.Info("capability_backfill: disabled by env kill switch",
			"env", CapabilityBackfillEnvKillSwitch)
		return
	}
	t := time.NewTicker(b.interval)
	defer t.Stop()
	// 启动即跑一轮：进程重启是能力位最可能过期的一刻。
	if _, err := b.BackfillOnce(ctx); err != nil {
		slog.Warn("capability_backfill: startup cycle failed", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !capabilityBackfillEnabled() {
				slog.Info("capability_backfill: disabled mid-run by env kill switch",
					"env", CapabilityBackfillEnvKillSwitch)
				return
			}
			if _, err := b.BackfillOnce(ctx); err != nil {
				slog.Warn("capability_backfill: cycle failed", "error", err)
			}
		}
	}
}

// dueBinding 是一个待探测的绑定。RawModel 是 provider_models.raw_model_name ——
// 与 node-state 能力键（llmgw:cred_fp_node:<cred>:<raw_model>）同一个命名面，
// 两个存储面才能对上同一条结论。
//
// 下面 6 个闸门字段刻意随行取回：dueBindings 的 SQL 里也筛了同样几条，但
// **那一份只是少取行的预筛，正确性来源在这里**。原因很实际——本任务的判据必须
// 能在没有网关 schema 的环境里被测到（见 capabilityBackfillAdmit）；只写在
// SQL 里的闸门一条也测不到，删掉也不会红。
type dueBinding struct {
	BindingID     int64
	CredentialID  int
	Ciphertext    []byte
	OutboundModel string
	RawModel      string
	BaseURL       string
	Protocol      string
	CatalogCode   string

	CredentialStatus   string
	LifecycleStatus    string
	CredentialDisabled bool
	ProviderEnabled    bool
	ProviderDisabled   bool
	BindingAvailable   bool
}

// probeableCredentialStatuses 是允许被主动探测的凭据状态。
//
// 与 bg/node_probe.go resolveDirectTarget 首轮查询同一份集合，不是我自己拟的：
// active / cooling / degraded 是「还在服务」的三态，其余（quarantine /
// quota_expired / disabled / **deleted**）都是系统已放弃或运营已停用的凭据。
// 探它们既浪费上游调用，也是对一条软删除记录发请求。域值见
// sql/migrations/startup/631_provider_credential_soft_delete.sql 的
// credentials_status_check。
var probeableCredentialStatuses = map[string]bool{
	"active":   true,
	"cooling":  true,
	"degraded": true,
}

// capabilityBackfillAdmit 决定这条绑定该不该被探。返回 (是否放行, 跳过原因)。
//
// 纯函数，所以每一条闸门都能在没有数据库的情况下被测到。这是本轮审计修掉的
// 最大覆盖漏洞：原先这些条件全在 SQL 里，一条判据都测不到。
func capabilityBackfillAdmit(b dueBinding) (bool, string) {
	if !b.BindingAvailable {
		return false, "binding_unavailable"
	}
	if b.CredentialDisabled {
		return false, "credential_manual_disabled"
	}
	if b.ProviderDisabled {
		return false, "provider_manual_disabled"
	}
	if !b.ProviderEnabled {
		return false, "provider_disabled"
	}
	if !strings.EqualFold(strings.TrimSpace(b.LifecycleStatus), "active") {
		return false, "lifecycle_" + strings.ToLower(strings.TrimSpace(b.LifecycleStatus))
	}
	status := strings.ToLower(strings.TrimSpace(b.CredentialStatus))
	if !probeableCredentialStatuses[status] {
		return false, "credential_status_" + status
	}
	normed, err := catalog.NormalizeProviderProtocol(b.Protocol)
	if err != nil {
		return false, "protocol_unparseable"
	}
	if normed != catalog.ProtocolOpenAIResponses {
		// 别家协议没有 /v1/responses，探它没有意义。
		return false, "protocol_" + normed
	}
	return true, ""
}

// BackfillOnce 跑一轮回填，返回写入了多少行能力位。
//
// 返回值只数**写成功**的行：单条绑定探测失败不终止整轮 —— 一条绑定解密失败
// 或上游不可达不应该让另外 49 条拿不到结论。
func (b *CapabilityBackfill) BackfillOnce(ctx context.Context) (int, error) {
	if b == nil {
		return 0, nil
	}
	if h := acquireSweepDistLock(ctx, b.distLock, "capability_backfill", capabilityBackfillDistLockTTL, "capability_backfill"); h != nil {
		defer h.Release(context.WithoutCancel(ctx))
		if !h.IsLeader() {
			slog.Debug("capability_backfill: follower instance skips cycle")
			return 0, nil
		}
	}
	scan := b.scan
	if scan == nil {
		if b.db == nil {
			return 0, nil
		}
		scan = b.dueBindings
	}
	rows, err := scan(ctx)
	if err != nil {
		return 0, err
	}
	written := 0
	probed := 0
	backedOff := 0
	for _, row := range rows {
		if probed >= b.batchLimit {
			break
		}
		if b.attemptedRecently(row.BindingID) {
			// 台账退避：这行近期真的出过网（多半没拿到证据）。不消耗本轮
			// 预算，让同批后面的行顶上——这正是扫描窗口取 batchLimit×4 的
			// 意义（反饥饿）。
			backedOff++
			continue
		}
		ok, didProbe, err := b.probeAndPersist(ctx, row)
		if err != nil {
			slog.Warn("capability_backfill: binding skipped",
				"binding_id", row.BindingID,
				"credential_id", row.CredentialID,
				"raw_model", row.RawModel,
				"error", err)
			continue
		}
		// admission 拒绝的行既不出网也不耗预算；预算只数真实探测。
		if didProbe {
			probed++
		}
		if ok {
			written++
		}
	}
	slog.Info("capability_backfill: cycle done",
		"scanned", len(rows), "probed", probed, "written", written,
		"backed_off", backedOff, "budget", b.batchLimit)
	return written, nil
}

// attemptedRecently 报告这条绑定是否处于出网退避期内。
func (b *CapabilityBackfill) attemptedRecently(id int64) bool {
	b.attemptsMu.Lock()
	defer b.attemptsMu.Unlock()
	t, ok := b.attempts[id]
	return ok && time.Since(t) < capabilityBackfillAttemptBackoff
}

// recordAttempt 记一次真实出网探测。
func (b *CapabilityBackfill) recordAttempt(id int64) {
	b.attemptsMu.Lock()
	defer b.attemptsMu.Unlock()
	if b.attempts == nil {
		b.attempts = make(map[int64]time.Time)
	}
	if len(b.attempts) >= capabilityBackfillAttemptLedgerMax {
		for bid, t := range b.attempts {
			if time.Since(t) >= capabilityBackfillAttemptBackoff {
				delete(b.attempts, bid)
			}
		}
		if len(b.attempts) >= capabilityBackfillAttemptLedgerMax {
			// 仍然触顶（绑定数异常庞大）：整体重置。最坏代价是退避态丢失、
			// 多探一轮，绝不让它变成内存泄漏面。
			b.attempts = make(map[int64]time.Time)
		}
	}
	b.attempts[id] = time.Now()
}

// scanLimit 是单轮 SQL 扫描窗口：探测预算 × capabilityBackfillScanFactor。
// 预算在 BackfillOnce 的探测循环里花，窗口放大是为了让 attempt 退避跳过的
// 行后面还有行可取（反饥饿）。溢出防御：batchLimit 异常大时退回原值。
func (b *CapabilityBackfill) scanLimit() int {
	n := b.batchLimit * capabilityBackfillScanFactor
	if n <= b.batchLimit || n < 0 {
		return b.batchLimit
	}
	return n
}

// dueBindings 扫出到期绑定。
//
// 三个条件缺一不可：
//   - 协议是 openai-responses 家族（别的协议没有 /v1/responses，探它没有意义）；
//   - 绑定与凭据/供应商都是活的（与 resolveDirectTarget 同一套人工意图闸门）；
//   - 能力行缺失，或 last_tested_at 已过 staleAfter。
func (b *CapabilityBackfill) dueBindings(ctx context.Context) ([]dueBinding, error) {
	qCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := b.db.Query(qCtx, `
		SELECT cmb.id,
		       c.id,
		       c.secret_ciphertext,
		       COALESCE(NULLIF(pm.outbound_model_name, ''), pm.raw_model_name, ''),
		       pm.raw_model_name,
		       p.base_url,
		       COALESCE(p.protocol, 'openai-completions'),
		       COALESCE(p.catalog_code, ''),
		       COALESCE(c.status, 'active'),
		       COALESCE(c.lifecycle_status, 'active'),
		       COALESCE(c.manual_disabled, FALSE),
		       COALESCE(p.enabled, TRUE),
		       COALESCE(p.manual_disabled, FALSE),
		       COALESCE(cmb.available, TRUE)
		FROM credential_model_bindings cmb
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		LEFT JOIN credential_model_capabilities cap
		       ON cap.credential_model_binding_id = cmb.id
		      AND cap.capability = $1
		WHERE cmb.available = TRUE
		  AND p.enabled = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND c.lifecycle_status = 'active'
		  -- 凭据状态闸门与 capabilityBackfillAdmit 同一份集合。这一条纯属
		  -- 预筛（少取行）；正确性来源在 Go 侧那条纯函数。
		  AND c.status IN ('active', 'cooling', 'degraded')
		  -- 协议别名在 Go 侧由 catalog.NormalizeProviderProtocol 归一；这里
		  -- 预筛同一组拼写（含 2026-09-23 vapeur 事故里的 "openai-response"
		  -- 单数错拼），Go 侧仍会再归一一次并丢弃非 responses 的行。
		  AND lower(replace(trim(p.protocol), '_', '-')) IN
		      ('openai-responses','openai-response','responses','response','openai-response-api')
		  AND (cap.id IS NULL OR cap.last_tested_at IS NULL
		       OR cap.last_tested_at < now() - make_interval(secs => $2))
		ORDER BY cap.last_tested_at ASC NULLS FIRST, cmb.id
		LIMIT $3
	`, CapabilityNonstream, int(b.staleAfter.Seconds()), b.scanLimit())
	if err != nil {
		return nil, fmt.Errorf("capability_backfill scan: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only scan

	var out []dueBinding
	for rows.Next() {
		var d dueBinding
		if err := rows.Scan(&d.BindingID, &d.CredentialID, &d.Ciphertext, &d.OutboundModel,
			&d.RawModel, &d.BaseURL, &d.Protocol, &d.CatalogCode,
			&d.CredentialStatus, &d.LifecycleStatus, &d.CredentialDisabled,
			&d.ProviderEnabled, &d.ProviderDisabled, &d.BindingAvailable); err != nil {
			return nil, fmt.Errorf("capability_backfill scan row: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("capability_backfill scan rows: %w", err)
	}
	return out, nil
}

// probeAndPersist 探测单条绑定并写回结论。返回 (是否写了一行, 是否真实出网
// 探测过, 错误)：admission 拒绝/端点未解析都是零出网路径，didProbe=false。
func (b *CapabilityBackfill) probeAndPersist(ctx context.Context, row dueBinding) (bool, bool, error) {
	// 闸门先走，且必须在解密与出网之前：一条软删除的凭据不该被解密，更不该
	// 被发请求。
	if admit, why := capabilityBackfillAdmit(row); !admit {
		slog.Debug("capability_backfill: binding skipped by admission gate",
			"binding_id", row.BindingID,
			"credential_id", row.CredentialID,
			"raw_model", row.RawModel,
			"reason", why)
		return false, false, nil
	}
	if b.probe == nil {
		return false, false, fmt.Errorf("capability_backfill: probe seam is not wired")
	}
	apiKey, err := b.decrypt(row)
	if err != nil {
		return false, false, err
	}
	// 闸门已归一过协议，这里直接复用其结果。
	normed, err := catalog.NormalizeProviderProtocol(row.Protocol)
	if err != nil {
		return false, false, fmt.Errorf("protocol %q: %w", row.Protocol, err)
	}
	desc := providercap.Resolve(normed, row.CatalogCode)
	target := probeTarget{
		CredentialID:  row.CredentialID,
		RawModel:      row.RawModel,
		OutboundModel: row.OutboundModel,
		BaseURL:       row.BaseURL,
		Protocol:      normed,
		APIKey:        apiKey,
	}
	if resolveProbeEndpoint(target, desc, ProbeModeResponses) == "" {
		return false, false, nil
	}

	pCtx, cancel := context.WithTimeout(ctx, capabilityBackfillProbeTimeout)
	defer cancel()
	// 复用现有探测器。与 probeWithRetry 的差别是**单次尝试**：那条重试阶梯
	// 只服务于「这一发失败要不要再试」的故障判定，而本任务要的判定只有
	// 「拿到能力位证据没有」—— 网络错/5xx/429 都不产生证据，重试它们只是
	// 把整轮拖慢；下一轮自然会重来。
	//
	// 记账在出网之前：从这一发起，这条绑定进入 attempt 退避（无论本轮拿到
	// 证据与否），防止无证据绑定每 30min 都被探一遍（R33 审计 #2）。
	b.recordAttempt(row.BindingID)
	result := b.probe(pCtx, target, desc)

	if result.supportsResponses == nil {
		// 约束 2：无证据不写。记一条日志说明为什么这一行没动 —— 静默跳过
		// 会让「为什么这批模型还是没能力位」变成不可查的问题。
		slog.Info("capability_backfill: no capability evidence, row left untouched",
			"binding_id", row.BindingID,
			"credential_id", row.CredentialID,
			"raw_model", row.RawModel,
			"probe_status", result.status,
			"http_status", result.httpStatus,
			"err_code", result.errCode)
		return false, true, nil
	}

	persist := b.persist
	if persist == nil {
		if b.db == nil {
			return false, true, nil
		}
		persist = b.persistRow
	}
	evidence := buildCapabilityEvidence(resolveProbeEndpoint(target, desc, ProbeModeResponses), result)
	if err := persist(ctx, row, *result.supportsResponses, evidence); err != nil {
		return false, true, err
	}
	// 同一结论镜像进 Redis：请求期 durable 闸门读的是 node-state 能力键，
	// 而它的 TTL 独立于本任务。两边不互写的话，SQL 侧修好了、请求期仍要等
	// 一次探针才开闸。
	_ = writeResponsesCapability(ctx, b.sink, row.CredentialID, row.RawModel, result.supportsResponses)
	return true, true, nil
}

// buildCapabilityEvidence 把**真实上游帧**装进 evidence_json。
//
// 用的是探测器已经拿到的响应（状态码 + 截断原文），不是本任务自己拼的
// 形状 —— 审计里那些「证据字段是装饰」的腐烂正是从「凭印象构造」开始的。
func buildCapabilityEvidence(endpoint string, result httpProbeResult) []byte {
	payload := map[string]any{
		"probe_mode":   "responses_nonstream",
		"endpoint":     endpoint,
		"http_status":  result.httpStatus,
		"latency_ms":   result.latencyMs,
		"probe_status": result.status,
		"err_code":     result.errCode,
		// bodySample 是探测器收到的**原始上游帧**（2xx 也有——errMsg 在成功
		// 路径上恒为空，拿它当证据等于给每条正向结论写一个空字符串）。
		// truncateProbeBody 走 errorsx.SanitizeErrorText：截断 + 脱敏凭据回显。
		"body_sample": firstNonEmpty(result.bodySample, truncateProbeBody(result.errMsg, 500)),
		"observed_at": time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		// json.Marshal 对这个纯 map[string]any 不会失败；真失败就退化成空
		// 对象，绝不因为序列化问题丢掉一整条能力结论。
		slog.Error("capability_backfill: evidence marshal failed", "error", err)
		return []byte(`{}`)
	}
	return raw
}

// persistRow 写回 credential_model_capabilities。
func (b *CapabilityBackfill) persistRow(ctx context.Context, row dueBinding, supported bool, evidence []byte) error {
	pCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tag, err := b.db.Exec(pCtx, `
		INSERT INTO credential_model_capabilities
		       (credential_model_binding_id, capability, supported, last_tested_at, evidence_json, updated_at)
		VALUES ($1, $2, $3, now(), $4, now())
		ON CONFLICT (credential_model_binding_id, capability)
		DO UPDATE SET supported      = EXCLUDED.supported,
		              last_tested_at = now(),
		              evidence_json  = EXCLUDED.evidence_json,
		              updated_at     = now()
	`, row.BindingID, CapabilityNonstream, supported, evidence)
	if err != nil {
		return fmt.Errorf("capability_backfill upsert binding_id=%d: %w", row.BindingID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("capability_backfill upsert binding_id=%d: 0 rows affected", row.BindingID)
	}
	slog.Info("capability_backfill: capability written",
		"binding_id", row.BindingID,
		"credential_id", row.CredentialID,
		"raw_model", row.RawModel,
		"capability", CapabilityNonstream,
		"supported", supported)
	return nil
}

// decrypt 复用 node_probe 的解密路径（secret.DecryptAny：AES-GCM 优先、
// v1:legacy 回落 Fernet），不新造解密。
func (b *CapabilityBackfill) decrypt(row dueBinding) (string, error) {
	s := string(row.Ciphertext)
	if !secret.IsV1Envelope(s) {
		return "", fmt.Errorf("unsupported secret format")
	}
	if b.keyring == nil {
		return "", fmt.Errorf("keyring not configured")
	}
	pt, _, err := secret.DecryptAny(s, b.keyring, b.encKey)
	if err != nil {
		// 只记 key id 与长度，与 node_probe 的既有纪律一致：ciphertext 字节
		// 绝不能进日志。
		slog.Error("capability_backfill: decrypt failed",
			"credential_id", row.CredentialID,
			"raw_model", row.RawModel,
			"envelope_kid", decryptEnvelopeKid(s),
			"envelope_len", len(s),
			"error", err.Error())
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), nil
}

// firstNonEmpty 返回第一个非空串。用于「优先取真实帧，退回错误文案」这种
// 二选一的证据来源。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
