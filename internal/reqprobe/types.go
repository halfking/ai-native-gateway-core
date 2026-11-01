// Package reqprobe: 请求侧异常探测与记录（2026-09-21）。
//
// 背景：上游供应商 4xx 里有一大类是"网关发出去的请求体/请求形态不被该
// 供应商模型接受"——典型如 grok-4.6 不认 reasoning_effort=x-high、
// /v1/responses 端点在某中转上实际不存在。这类错误是配置/兼容问题而非
// 凭据问题，重试同一凭据无效，换凭据也无效。本包提供三件事：
//
//  1. Diagnose：从 (状态码, 上游错误体, 出站请求体) 判断是否是参数被拒
//     （param_rejected）或请求模式不匹配（mode_mismatch）。
//  2. StripParams：按错误点名参数（能解析出名字时）或按"不常见参数"清单
//     剔除出站体中的可选参数，供 executor 做一次免费重试。
//  3. Store/Coordinator：把探测发现与最终结果按 (day, provider, model,
//     trigger, param, status) 指纹去重记录，供 /format-anomalies 管理页
//     查看、分类、解决（支持批量）；已被"剔除后重试成功"验证过的参数
//     规则可被 executor 前置应用（学习，默认开启，可用
//     LLM_GATEWAY_REQPROBE_LEARN=off 关闭）。
//
// 存储：Full 模式用 Redis（跨重启保留），lite 模式用进程内存（重启清零，
// 上限 MemoryMaxRecords 条 FIFO 淘汰）。两种实现满足同一 Store 接口，
// admin API 与 executor 不感知部署模式。
//
// 安全红线：Record 全部走 Coordinator 的异步队列（满则丢弃），绝不在请求
// 热路径上同步写存储；错误样例截断到 512 字节，不含请求正文。
package reqprobe

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// learnedTTL 是学习规则的存活窗口：参数剔除规则自最后一次"剔除后成功"
// 起在窗口内继续前置应用，过期重新探测确认。防止把一次巧合成功固化成
// 永久行为，也保证上游后来支持某参数后网关会自动重新试探。
//
// 2026-10-29（nvidia 直通事故轮）：默认从 24h 收到 1h（用户要求「时效
// 1 小时……每隔一段时间就失效，有利于不停探测」），并可用
// LLM_GATEWAY_REQPROBE_LEARNED_TTL 覆盖（Go duration，如 30m / 2h；非法
// 值回退默认）。过期的代价是每个 (provider, model, wire) 每窗口多吃一跳
// 400 剥参重试——换取参数兼容状态的持续再确认。
//
// 2026-11-01（参数兼容静态化轮 TTL 复核）：固定窗口对「低流量腿」的代价
// 不成比例——窗口一过，**下一个真实用户请求**就成了探测器（400+剥参重试，
// 时延翻倍并计入上游 4xx）。nvidia glm 生产实测每个固定窗口稳定复现
// （reqanom 台账 2026-10-31 param=reasoning occ=5）。分级裁决：**不引入
// 按 (provider, model) 的静态分级表**——LearnedKey 本就是
// (providerID, outboundModel, actualWire) 粒度，静态表需要人工维护且
// 无法自适应；改为**指数退避**：同一参数族每次「再探测后仍被拒且剥参
// 成功」（BackoffCount+1）就把下一窗口翻倍，上限
// LLM_GATEWAY_REQPROBE_LEARNED_TTL_BACKOFF_MAX（步数，默认 4 ⇒ 最大 16×；
// 0 = 关闭退避、恢复固定窗口语义）。被反复证实的拒绝探测频率指数下降，
// 而上游一旦开始接受该参数，首轮窗口（base TTL）内就会重新发现。
//
// ★ 2026-11-01（12h 审计 P1-3）：退避基数**不能**用 RecoveredCount ——
//   那是布尔语义（「学过没有」，store 层刻意取 max 而非累加），生产路径
//   上恒 ≤ 1 ⇒ 拿它当步数基数会让退避**永不生效**（实测：生产写入口
//   连写 5 次仍为 1 ⇒ 退避窗口恒等于 base）。改用独立的 BackoffCount，
//   由 store 在「再探测」时推导，见 mergeBackoffCount。
const defaultLearnedTTL = time.Hour

var learnedTTL = sync.OnceValue(func() time.Duration {
	v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_REQPROBE_LEARNED_TTL"))
	if v == "" {
		return defaultLearnedTTL
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	return defaultLearnedTTL
})

// learnedBackoffMaxSteps 是退避步数上限（窗口 = base TTL × 2^步）。
// env LLM_GATEWAY_REQPROBE_LEARNED_TTL_BACKOFF_MAX；0 关闭退避。上限 10
// 硬封顶（2^10=1024×，防止 env 手滑把窗口推到年级）。
const learnedBackoffHardCap = 10

var learnedBackoffMaxSteps = sync.OnceValue(func() int {
	v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_REQPROBE_LEARNED_TTL_BACKOFF_MAX"))
	if v == "" {
		return 4
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 4
	}
	if n > learnedBackoffHardCap {
		return learnedBackoffHardCap
	}
	return n
})

// learnedWindow 返回一条记录当前的学习有效窗口：base TTL 指数退避
// （2026-11-01，见 learnedTTL 注释）。BackoffCount 是该参数族当日被
// 「再探测」的次数，第 k 次再探测后的窗口为 base × 2^(k-1)（首次学习
// 仍是 base，从第二次起翻倍）。退避关闭或 BackoffCount≤1 时恒等于
// base TTL。
func learnedWindow(rec *Record) time.Duration {
	return learnedWindowWithSteps(rec, learnedBackoffMaxSteps())
}

// learnedWindowWithSteps 是退避窗口的纯函数形态（steps=0 即固定窗口），
// 单测可不经 env 直接钉边界。
func learnedWindowWithSteps(rec *Record, steps int) time.Duration {
	base := learnedTTL()
	if steps <= 0 || rec == nil || rec.BackoffCount <= 1 {
		return base
	}
	shift := rec.BackoffCount - 1
	if shift > steps {
		shift = steps
	}
	window := base
	for i := 0; i < shift; i++ {
		window *= 2
	}
	return window
}

// mergeBackoffCount 合并「再探测次数」（退避窗口的基数）。
//
// 语义（2026-11-01 12h 审计 P1-3 修复）：
//
//   - 首次学习事件（剥参成功 / 拒绝即学习）⇒ 1（窗口 = base TTL）
//   - 旧记录距上次见到已超过**它自己的**窗口（= 这次是「再探测」）且本次
//     仍是学习事件 ⇒ +1（窗口 base × 2^(k-1)）
//   - 窗口内到达的事件 ⇒ 不变（窗口内的连续拒绝**不是**再探测）
//
// 为什么必须独立于 RecoveredCount：RecoveredCount 是布尔语义（「学过
// 没有」；store 层刻意取 max 而非累加，见 memory.go 的 P0-B 注释），
// 生产写路径上恒 ≤ 1 ⇒ 拿它当退避基数会让退避永不生效（实测：连写 5 次
// 仍为 1 ⇒ 窗口恒等于 base ⇒ 退避能力形同虚设）。
//
// 直写语义：调用方显式给了 BackoffCount ⇒ 以它为准（运维回填 / 单测钉
// 边界）；否则按上面的「再探测」规则推导。
func mergeBackoffCount(old *Record, incoming Record, now time.Time) int {
	if incoming.BackoffCount > 0 {
		return incoming.BackoffCount
	}
	learning := incoming.RecoveredCount > 0
	if old == nil || old.BackoffCount == 0 {
		if learning {
			return 1
		}
		return 0
	}
	if learning && old.LastSeen.Before(now.Add(-learnedWindow(old))) {
		return old.BackoffCount + 1
	}
	return old.BackoffCount
}

// MemoryMaxRecords 是内存实现的最大指纹数（lite 模式防膨胀上限）。
const MemoryMaxRecords = 5000

// maxErrorSample 是 ErrorSample 的截断长度。
const maxErrorSample = 512

// Trigger 是请求侧异常的归类。
type Trigger string

const (
	// TriggerParamRejected：上游拒绝某个（或某些）请求参数。
	// 例如 "Unrecognized request argument supplied: reasoning_effort"。
	TriggerParamRejected Trigger = "param_rejected"

	// TriggerModeMismatch：请求的 API 形态（responses vs chat/completions
	// vs anthropic-messages）与该供应商实际支持的不符。
	TriggerModeMismatch Trigger = "mode_mismatch"

	// TriggerUpstreamError：属于请求格式家族的其它 4xx（无法进一步归类，
	// 也没有可自动尝试的手段），仅记录供人工分类。
	TriggerUpstreamError Trigger = "upstream_error"
)

// Record 是一条按指纹去重后的请求侧异常记录（JSON 序列化用于 Redis 与
// admin API，字段名即 API 契约）。
type Record struct {
	ID          int64  `json:"id"`
	Fingerprint string `json:"fingerprint"`
	// Day 是 FirstSeen 所在本地日期（YYYY-MM-DD）。指纹含 Day，同一问题
	// 每天产生一行，满足"每天的错误信息形成一个列表"的查看诉求。
	Day           string `json:"day"`
	ProviderID    int    `json:"provider_id"`
	ProviderCode  string `json:"provider_code"`
	ClientModel   string `json:"client_model,omitempty"`
	OutboundModel string `json:"outbound_model,omitempty"`
	// Protocol 是失败时出站请求用的协议（cand.Protocol）。
	Protocol string  `json:"protocol,omitempty"`
	Trigger  Trigger `json:"trigger"`
	// Param 是被拒/被剔除的参数名（多个用逗号连接），mode_mismatch 时为空。
	Param string `json:"param,omitempty"`
	// SuggestMode 是建议切换到的请求形态（如 "chat"），param_rejected 时为空。
	SuggestMode string `json:"suggest_mode,omitempty"`
	HTTPStatus  int    `json:"http_status"`
	// ErrorKind 是 errorsx 的低基数分类（client_bug / unsupported_feature…）。
	ErrorKind string `json:"error_kind,omitempty"`
	// ErrorSample 是上游错误体截断样例（≤512 字节），不含请求正文。
	ErrorSample string `json:"error_sample,omitempty"`
	// Occurrences 是该指纹今天的出现次数。
	Occurrences int `json:"occurrences"`
	// RecoveredCount 是"剔除参数/切换模式后重试成功"的次数。
	RecoveredCount int `json:"recovered_count"`
	// BackoffCount 是该参数族当日被「再探测」的次数 —— 退避窗口的基数。
	//
	// ★ 2026-11-01（12h 审计 P1-3）：它**不能**复用 RecoveredCount ——
	//   后者是布尔语义（「学过没有」），生产写路径上恒 ≤ 1 ⇒ 拿它当退避
	//   步数基数会让退避永不生效。本字段由 store 在合并时按「再探测」
	//   规则推导（见 mergeBackoffCount），窗口 = base × 2^(k-1)。
	BackoffCount    int        `json:"backoff_count,omitempty"`
	FirstSeen       time.Time  `json:"first_seen"`
	LastSeen        time.Time  `json:"last_seen"`
	LastRequestID   string     `json:"last_request_id,omitempty"`
	Resolved        bool       `json:"resolved"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	ResolutionNotes string     `json:"resolution_notes,omitempty"`
}

// Counts 供导航栏徽标与页面统计。
type Counts struct {
	Unresolved int `json:"unresolved"`
	NewToday   int `json:"new_today"`
}

// Filter 是 List/ResolveFilter 的查询条件（字段为空表示不过滤）。
type Filter struct {
	Day            string
	ProviderCode   string
	Model          string // 匹配 client_model 或 outbound_model
	Trigger        string
	UnresolvedOnly bool
	Limit          int
	Offset         int
}

// matches 判断记录是否命中过滤条件（List 与 ResolveFilter 共用）。
func (f Filter) matches(r Record) bool {
	if f.Day != "" && r.Day != f.Day {
		return false
	}
	if f.ProviderCode != "" && !strings.EqualFold(r.ProviderCode, f.ProviderCode) {
		return false
	}
	if f.Model != "" &&
		!strings.EqualFold(r.ClientModel, f.Model) &&
		!strings.EqualFold(r.OutboundModel, f.Model) {
		return false
	}
	if f.Trigger != "" && string(r.Trigger) != f.Trigger {
		return false
	}
	if f.UnresolvedOnly && r.Resolved {
		return false
	}
	return true
}

// LearnedKey 是学习规则的精确作用域。ProviderCode 不是隔离键：同一 catalog
// 可挂多个行为不同的 provider 实例。Protocol 必须是实际出站线格式，而不是
// Candidate 上声明的静态协议。
type LearnedKey struct {
	ProviderID    int
	OutboundModel string
	ActualWire    string
}

func (k LearnedKey) normalized() LearnedKey {
	k.OutboundModel = strings.ToLower(strings.TrimSpace(k.OutboundModel))
	k.ActualWire = strings.ToLower(strings.TrimSpace(k.ActualWire))
	return k
}

func (k LearnedKey) valid() bool {
	k = k.normalized()
	return k.ProviderID > 0 && k.OutboundModel != "" && k.ActualWire != ""
}

func (k LearnedKey) cacheKey() string {
	k = k.normalized()
	return strconv.Itoa(k.ProviderID) + "|" + k.OutboundModel + "|" + k.ActualWire
}

// Store 是请求侧异常的持久化接口。实现必须并发安全；所有方法可被 admin
// handler 与 Coordinator worker 同时调用。
type Store interface {
	// Upsert 按 rec.Fingerprint 合并：已存在则 Occurrences/RecoveredCount
	// 累加、LastSeen/LastRequestID/ErrorSample 刷新；不存在则分配新 ID
	// 插入。返回合并后的记录。
	Upsert(ctx context.Context, rec Record) (Record, error)
	// List 返回按 LastSeen 倒序的记录与过滤后总数。
	List(ctx context.Context, f Filter) ([]Record, int, error)
	// Counts 返回未解决总数与今日新增（未解决）数。
	Counts(ctx context.Context) (Counts, error)
	// Resolve 将指定 ID 标记为已解决，返回受影响行数。
	Resolve(ctx context.Context, ids []int64, notes string) (int, error)
	// ResolveFilter 将匹配过滤条件的未解决记录全部标记为已解决（批量
	// "一键解决"），返回受影响行数。
	ResolveFilter(ctx context.Context, f Filter, notes string) (int, error)
	// LearnedParams 返回该精确 (providerID, outboundModel, actualWire) 上
	// "剔除后重试成功且未解决"的参数名集合（最近 learnedTTL 内成功过），
	// 供 executor 前置剔除。无效/历史空键不学习（fail-open）。
	// mode_mismatch 不参与学习（协议切换是每请求的廉价回退，无需缓存）。
	LearnedParams(ctx context.Context, key LearnedKey) []string
	Close() error
}

// Fingerprint 计算记录指纹：day|providerID|providerCode|model|protocol|trigger|param|mode|status|kind。
// 不含 ErrorSample（同一问题的上游文案抖动不应裂成多行）。
func Fingerprint(r *Record) string {
	h := sha1.New()
	for _, part := range []string{
		r.Day,
		strconv.Itoa(r.ProviderID),
		strings.ToLower(r.ProviderCode),
		strings.ToLower(r.OutboundModel),
		strings.ToLower(r.Protocol),
		string(r.Trigger),
		strings.ToLower(r.Param),
		strings.ToLower(r.SuggestMode),
		strconv.Itoa(r.HTTPStatus),
		r.ErrorKind,
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Today 返回本地时区的 YYYY-MM-DD。
func Today(t time.Time) string {
	return t.Local().Format("2006-01-02")
}

// truncateSample 截断错误样例。
func truncateSample(s string) string {
	if len(s) <= maxErrorSample {
		return s
	}
	return s[:maxErrorSample] + "...[truncated]"
}
