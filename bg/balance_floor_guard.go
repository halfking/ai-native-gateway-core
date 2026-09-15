package bg

// BalanceFloorGuard — proactive balance-floor enforcement for first-party
// vendor credentials (2026-09-13, migration 701).
//
// 目标：在原厂凭据（智谱 GLM、MiniMax 等）的额度被打光之前把它从路由池摘出，
// 保留一段缓冲（例如最后 50 万 token），避免上游账号被真实扣到 0 而触发风控
// /停用；充值或窗口重置后自动回到池子。全程不写 manual_disabled，恢复永远
// 是自动的。
//
// 两种额度语义（不能混为一谈，解析与下限字段一一对应）：
//
//  1. 货币余额（balance_floor_usd）— 有公开余额 API 的厂商（openai/deepseek/
//     siliconflow，经 providercap）。注意：credentials.balance_usd 的唯一常规
//     写入点是 cycleAll 的 probeBalance，而默认（LLM_GATEWAY_USE_NEW_PROBE_MODE，
//     2026-07-14 起）cycleAll 并不启动 —— 所以本 guard 自己负责刷新配置了
//     货币下限的凭据的余额（纯 GET，不烧 token，15 分钟新鲜度），摘出判定
//     只信新鲜余额。
//
//  2. 订阅套餐窗口（quota_floor_tokens / quota_floor_percent）—
//       zhipu:  GET {origin}/api/monitor/usage/quota/limit
//               （Authorization 不加 Bearer 前缀；limits[].unit 3=5 小时窗
//               6=7 天窗；percentage 方向=已用；HTTP 200 仍可能
//               success=false，必须单独判断。实测依据：cc-switch
//               coding_plan.rs parse_zhipu_token_tiers / TokenScope
//               proxy/balance.py，2026-08。）
//       minimax: GET {origin}/v1/api/openplatform/coding_plan/remains
//               （fallback /v1/token_plan/remains；Bearer；字段方向=剩余，
//               需 100-x 反转成已用；周窗仅 current_weekly_status==1 才算
//               激活；业务错误在 base_resp.status_code。）
//     MiniMax 只有百分比没有绝对量 → token 下限对它不可评估（fail-open 跳过）；
//     GLM 的 credit 计价套餐同理，操作员应改用百分比下限。
//
// 摘出机制：写 quota_state='balance_exhausted' + availability_state='suspended'
// + state_reason_code='balance_floor'。候选 SQL（provider/client.go）与
// v_routable_credential_models 本来就排除这些状态 = 立即出池；
// trg_notify_auto_route_creds 触发器自动广播缓存失效。恢复：额度回到
// floor*1.1（token/货币）或 floor-2pp（百分比）滞回带以上，且所有权校验
// （state_reason_code='balance_floor'）通过，才翻回 ok/ready —— 永远不会碰
// 反应式（writer.go）或其它路径写入的配额状态。
//
// 与 BalanceQuotaProbe 的协作：2 分钟探测循环本来会对 balance_exhausted 凭据
// 发真实 chat 探测；被 floor 摘出的凭据额度还剩缓冲，chat 探测必然成功并把
// 它写回 ok（writeHealth 硬配额守卫允许 $8='ok' 通过）→ 乒乓。所以
// balance_quota_probe.go 对 state_reason_code='balance_floor' 的行豁免，恢复
// 完全由本 guard 负责。admin 的 force-probe/reset-state 仍可人工越过
// （下一轮 sweep 会按 floor 重新摘出，除非操作员清掉下限）；下限全部清空
// 后，sweep 会自动释放仍被本 guard 摘出的凭据（releaseClearedFloorCredentials）。
//
// Env knobs:
//   LLM_GATEWAY_BALANCE_FLOOR_INTERVAL — sweep 周期，默认 5m（>=30s）
//   LLM_GATEWAY_BALANCE_FLOOR_GUARD    — "off"/"false"/"0" 关闭 sweep
//   LLM_GATEWAY_BALANCE_FLOOR_ESCAPE_HOURS — #4 逃生门（R28, 2026-09-14）：
//     plan 探测证据陈旧超过该小时数时释放 floor 摘出的行，默认 2（审计
//     B-E1, 2026-09-16：从 24h 收紧 —— 套餐 API 长期故障时凭据不应被
//     floor 卡死超过一个探测退避量级）；0=关闭逃生门（不推荐：floor 行
//     会永久卡死）。
//   LLM_GATEWAY_FLOOR_PLAN_CONCURRENCY — 套餐探测 worker pool 并发度
//     （E-B1, 2026-09-16），默认 10，钳制 1..100。
//
// 安全性：所有 floor 字段默认 NULL → 选点为空 → worker 空转；探测失败
// fail-open（保留上次状态，绝不用过期数据做摘出决策）；单凭据失败不影响
// 其它凭据。
//
// 配置示例（DB credentials 列）：
//   -- 货币下限（有公开余额 API 的厂商：openai/deepseek/siliconflow）
//   ALTER TABLE credentials ADD COLUMN IF NOT EXISTS balance_floor_usd numeric(12,2);
//   UPDATE credentials SET balance_floor_usd = 1.00 WHERE id = 123;
//
//   -- 套餐 token 下限（zhipu GLM：保最后 50 万 token）
//   ALTER TABLE credentials ADD COLUMN IF NOT EXISTS quota_floor_tokens bigint;
//   UPDATE credentials SET quota_floor_tokens = 500000 WHERE id = 456;
//
//   -- 套餐百分比下限（minimax 无绝对量，用百分比：已用 >= 95% 摘出）
//   ALTER TABLE credentials ADD COLUMN IF NOT EXISTS quota_floor_percent numeric(5,2);
//   UPDATE credentials SET quota_floor_percent = 95.0 WHERE id = 789;
//
// 恢复阈值：token/货币下限 * 1.1，百分比下限 - 2pp（滞回带防抖动）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/semaphore"

	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/secret"
)

const (
	// planFloorRestoreFactor: token/货币下限的恢复滞回 —— 额度 >= floor*1.1 才回池。
	planFloorRestoreFactor = 1.1
	// planFloorPercentHysteresis (百分点)：已用 <= floor-2pp 才回池。
	planFloorPercentHysteresis = 2.0
	// maxPlanWindowsPerCredential / 响应体上限：防御性，账单接口不该返回大响应。
	planRespBodyLimit = 64 * 1024
	// F-L2: 探测失败 Warn 限流窗口 —— 与 #12a 探测退避同窗（15 分钟），
	// 挂死的厂商套餐端点最多每刻钟刷一条 Warn。
	planWarnWindow = 15 * time.Minute
	// G-O1: 控制面 GET 重试 —— 1 次首发 + 至多 2 次重试，退避 500ms×attempt
	//（0.5s / 1s）。只重试网络错误与 5xx，4xx（坏 key / 坏 URL）不重试。
	planHTTPMaxAttempts = 3
	planHTTPRetryDelay  = 500 * time.Millisecond
	// D-L1: Stop 等待在途 sweep 的上限 —— sweep 的 cctx 派生自 Start 的
	// 调用方 ctx（main.go 传 Background），无界 join 可能拖住进程下线数分钟。
	stopWaitTimeout = 10 * time.Second
)

// warnGate rate-limits per-credential probe-failure Warn logs (F-L2): one Warn
// per credential per window, further failures inside the window stay at Debug
// so a dead vendor endpoint cannot flood the log. In-memory only — a restart
// re-warns once, which is acceptable.
type warnGate struct {
	ttl  time.Duration
	mu   sync.Mutex
	last map[int64]time.Time
}

func newWarnGate(ttl time.Duration) *warnGate {
	return &warnGate{ttl: ttl, last: make(map[int64]time.Time)}
}

// allow reports whether a Warn for id is due now, recording the attempt.
func (w *warnGate) allow(id int64, now time.Time) bool {
	if w == nil {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.last[id]; ok && now.Sub(t) < w.ttl {
		return false
	}
	// 防御性清理：凭据 ID 大规模翻新时避免 map 无界增长（正常规模到不了）。
	if len(w.last) >= 4096 {
		for k, t := range w.last {
			if now.Sub(t) >= w.ttl {
				delete(w.last, k)
			}
		}
	}
	w.last[id] = now
	return true
}

// Zhipu/MiniMax 套餐端点路径（相对 origin —— 两家 base_url 都带 API 前缀，
// 余额/套餐端点不在其下，必须从 scheme://host 重建，禁止字符串拼接 base）。
const (
	zhipuPlanQuotaPath = "/api/monitor/usage/quota/limit"
	// MiniMax 编程套餐（国内 api.minimaxi.com / 国际 api.minimax.io 同路径）。
	minimaxCodingPlanPath = "/v1/api/openplatform/coding_plan/remains"
	// MiniMax Token Plan（订阅 Key）—— 编程套餐端点业务失败时的 fallback。
	minimaxTokenPlanPath = "/v1/token_plan/remains"
)

// planState is the normalized probe result persisted to credentials.plan_quota_*.
type planState struct {
	Kind string `json:"kind"` // "zhipu_plan" | "minimax_plan"
	// Windows 按探测顺序（短窗在前）。zhipu 带 Remaining（原生单位，
	// TOKENS_LIMIT 即 token 数）；minimax 无绝对量。
	Windows []planWindow `json:"windows"`
	// MinTokensRemaining: TOKENS_LIMIT 条目中最小的 remaining；无 token 计价
	// 条目（纯 credit/百分比套餐）时为 nil —— token 下限对该凭据不可评估。
	MinTokensRemaining *int64 `json:"min_tokens_remaining,omitempty"`
	// MaxUsedPercent: 所有窗口已用百分比的最大值（0..100）。
	MaxUsedPercent float64 `json:"max_used_percent"`
}

type planWindow struct {
	Window      string     `json:"window"` // "5h" | "7d" | "unit_<n>"
	UsedPercent float64    `json:"used_percent"`
	Remaining   *int64     `json:"remaining,omitempty"`
	ResetAt     *time.Time `json:"reset_at,omitempty"`
}

// summary renders the operator-facing reason string for state_reason_detail.
func (s *planState) summary(floorTokens *int64, floorPercent *float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "balance_floor guard (%s)", s.Kind)
	if floorTokens != nil && s.MinTokensRemaining != nil {
		fmt.Fprintf(&b, ", remaining=%d tokens (floor=%d)", *s.MinTokensRemaining, *floorTokens)
	}
	if floorPercent != nil {
		fmt.Fprintf(&b, ", used=%.1f%% (floor=%.1f%%)", s.MaxUsedPercent, *floorPercent)
	}
	return b.String()
}

// flexNum accepts JSON numbers OR numeric strings — both vendors are
// inconsistent in this respect (DeepSeek-style amounts arrive quoted).
type flexNum float64

func (f *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("flexNum: %q is not numeric", s)
	}
	*f = flexNum(v)
	return nil
}

func (f *flexNum) ptr() *float64 {
	if f == nil {
		return nil
	}
	v := float64(*f)
	return &v
}

func millisToTime(ms *flexNum) *time.Time {
	if ms == nil || *ms <= 0 {
		return nil
	}
	t := time.UnixMilli(flexToInt64(*ms))
	return &t
}

// flexToInt64 clamps a vendor-supplied float into int64 range — out-of-range
// JSON numbers would otherwise convert to implementation-defined garbage, and
// garbage `remaining` could spuriously breach a token floor.
func flexToInt64(f flexNum) int64 {
	v := float64(f)
	if math.IsNaN(v) || v <= 0 {
		return 0
	}
	if v >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// ---------------------------------------------------------------- zhipu ----

type zhipuPlanResp struct {
	Success *bool  `json:"success"`
	Msg     string `json:"msg"`
	Data    *struct {
		Level  string           `json:"level"`
		Limits []zhipuPlanLimit `json:"limits"`
	} `json:"data"`
}

type zhipuPlanLimit struct {
	Type          string   `json:"type"`
	Unit          *flexNum `json:"unit"`
	Percentage    *flexNum `json:"percentage"`
	Remaining     *flexNum `json:"remaining"`
	NextResetTime *flexNum `json:"nextResetTime"`
}

// parseZhipuPlan parses GET /api/monitor/usage/quota/limit.
// 方向注意：percentage 是【已用】，直接存；remaining 是剩余绝对量
// （TOKENS_LIMIT 的单位是 token，CREDIT_LIMIT 是积分——后者不参与
// token 下限评估）。
func parseZhipuPlan(body []byte) (*planState, error) {
	var resp zhipuPlanResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("zhipu plan: bad json: %w", err)
	}
	if resp.Success != nil && !*resp.Success {
		return nil, fmt.Errorf("zhipu plan: business error: %s", resp.Msg)
	}
	if resp.Data == nil || len(resp.Data.Limits) == 0 {
		return nil, fmt.Errorf("zhipu plan: no data.limits")
	}

	st := &planState{Kind: "zhipu_plan"}
	for _, lim := range resp.Data.Limits {
		limType := strings.ToUpper(strings.TrimSpace(lim.Type))
		if limType != "TOKENS_LIMIT" && limType != "CREDIT_LIMIT" {
			continue
		}
		pct := lim.Percentage.ptr()
		if pct == nil {
			continue // 缺百分比没法诚实评估，宁缺勿假
		}
		w := planWindow{UsedPercent: *pct, ResetAt: millisToTime(lim.NextResetTime)}
		switch u := lim.Unit.ptr(); {
		case u != nil && *u == 3:
			w.Window = "5h"
		case u != nil && *u == 6:
			w.Window = "7d"
		default:
			// unit 缺失/不识别：只认字段判窗口，不用重置时间排序兜底
			// （周期末尾 7 天窗会比 5 小时窗先重置，时间排序必翻车）。
			w.Window = "unit_?"
		}
		if limType == "TOKENS_LIMIT" && lim.Remaining != nil {
			rem := flexToInt64(*lim.Remaining)
			w.Remaining = &rem
			if st.MinTokensRemaining == nil || rem < *st.MinTokensRemaining {
				st.MinTokensRemaining = &rem
			}
		}
		st.Windows = append(st.Windows, w)
		if *pct > st.MaxUsedPercent {
			st.MaxUsedPercent = *pct
		}
	}
	if len(st.Windows) == 0 {
		return nil, fmt.Errorf("zhipu plan: no TOKENS_LIMIT/CREDIT_LIMIT entries")
	}
	return st, nil
}

// --------------------------------------------------------------- minimax ----

type minimaxPlanModel struct {
	ModelName string `json:"model_name"`
	// 方向注意：这两个是【剩余】百分比，落库前必须 100-x 反转。
	CurrentIntervalRemainingPercent *flexNum `json:"current_interval_remaining_percent"`
	EndTime                         *flexNum `json:"end_time"`
	// 周窗仅 status==1 才算激活；无周限的套餐 status==3 且剩余恒 100，
	// 照展示是假信息。
	CurrentWeeklyStatus           *flexNum `json:"current_weekly_status"`
	CurrentWeeklyRemainingPercent *flexNum `json:"current_weekly_remaining_percent"`
	WeeklyEndTime                 *flexNum `json:"weekly_end_time"`
}

type minimaxPlanResp struct {
	BaseResp *struct {
		StatusCode *flexNum `json:"status_code"`
		StatusMsg  string   `json:"status_msg"`
	} `json:"base_resp"`
	ModelRemains []minimaxPlanModel `json:"model_remains"`
}

// parseMiniMaxPlan parses coding_plan/remains（token_plan/remains 同形状，
// 由 fetch 层做端点 fallback）。
func parseMiniMaxPlan(body []byte) (*planState, error) {
	var resp minimaxPlanResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("minimax plan: bad json: %w", err)
	}
	if resp.BaseResp != nil && resp.BaseResp.StatusCode != nil && *resp.BaseResp.StatusCode != 0 {
		return nil, fmt.Errorf("minimax plan: business error (code %v): %s",
			*resp.BaseResp.StatusCode, resp.BaseResp.StatusMsg)
	}
	var general *minimaxPlanModel
	for i := range resp.ModelRemains {
		if resp.ModelRemains[i].ModelName == "general" {
			general = &resp.ModelRemains[i]
			break
		}
	}
	if general == nil {
		return nil, fmt.Errorf("minimax plan: no general entry in model_remains")
	}

	st := &planState{Kind: "minimax_plan"}
	if iv := general.CurrentIntervalRemainingPercent.ptr(); iv != nil {
		st.Windows = append(st.Windows, planWindow{
			Window:      "5h",
			UsedPercent: 100 - *iv,
			ResetAt:     millisToTime(general.EndTime),
		})
	}
	if general.CurrentWeeklyStatus.ptr() != nil && *general.CurrentWeeklyStatus.ptr() == 1 {
		if wk := general.CurrentWeeklyRemainingPercent.ptr(); wk != nil {
			st.Windows = append(st.Windows, planWindow{
				Window:      "7d",
				UsedPercent: 100 - *wk,
				ResetAt:     millisToTime(general.WeeklyEndTime),
			})
		}
	}
	if len(st.Windows) == 0 {
		return nil, fmt.Errorf("minimax plan: no usable percent windows")
	}
	for _, w := range st.Windows {
		if w.UsedPercent > st.MaxUsedPercent {
			st.MaxUsedPercent = w.UsedPercent
		}
	}
	return st, nil
}

// ----------------------------------------------------------------- guard ----

// floorAction is the outcome of a pure floor evaluation.
type floorAction int

const (
	floorNone floorAction = iota
	floorPull
	floorRestore
)

// evaluatePlanFloor decides pull/restore purely from floors + fresh probe data.
// 语义：
//   - 任一配置下限被击穿 → floorPull；
//   - 所有配置下限都回到滞回带以内 → floorRestore（对未被摘出的凭据无害，
//     恢复 UPDATE 的所有权 WHERE 会过滤成 0 行）；
//   - token 下限在套餐无 token 计价条目（MinTokensRemaining==nil）时不可评估，
//     fail-open 跳过，绝不因“不知道”而摘出。
func evaluatePlanFloor(floorTokens *int64, floorPercent *float64, st *planState) floorAction {
	if st == nil || (floorTokens == nil && floorPercent == nil) {
		return floorNone
	}
	breach := false
	recovered := true
	if floorTokens != nil {
		if st.MinTokensRemaining != nil {
			if *st.MinTokensRemaining <= *floorTokens {
				breach = true
			}
			if float64(*st.MinTokensRemaining) < float64(*floorTokens)*planFloorRestoreFactor {
				recovered = false
			}
		}
	}
	if floorPercent != nil {
		if st.MaxUsedPercent >= *floorPercent {
			breach = true
		}
		// 窗口重置后 used 归 0 必须可恢复：floor < 滞回带（如 floor=1%）
		// 时 floor-hysteresis 为负、永远不可达，会卡死在 suspended ——
		// 所以 used==0（窗口刚重置）无条件视为已恢复。
		if st.MaxUsedPercent > *floorPercent-planFloorPercentHysteresis && st.MaxUsedPercent > 0 {
			recovered = false
		}
	}
	if breach {
		return floorPull
	}
	if recovered {
		return floorRestore
	}
	return floorNone
}

// originQuotaURL rebuilds a vendor control-plane URL from the ORIGIN of the
// configured base_url. zhipu base_url ends with /api/paas/v4 (or the coding
// variant) and MiniMax's with /v1 — appending the quota path to those would
// 404; both quota endpoints live at scheme://host + fixed path.
func originQuotaURL(baseURL, path string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("balance floor guard: invalid base_url %q", baseURL)
	}
	return u.Scheme + "://" + u.Host + path, nil
}

type floorCandidate struct {
	ID           int64
	Ciphertext   []byte
	BaseURL      string
	Protocol     string
	Catalog      string
	FloorUSD     *float64
	FloorTokens  *int64
	FloorPercent *float64
	QuotaState   string
	ReasonCode   string
}

type BalanceFloorGuard struct {
	db       *pgxpool.Pool
	encKey   []byte
	keyring  *secret.Keyring
	interval time.Duration
	disabled bool
	// #4 逃生门（R28, 2026-09-14）：plan 探测证据陈旧阈值与总开关。
	escapeStale time.Duration
	escapeOff   bool
	// E-B1: 套餐探测 worker pool 并发度（LLM_GATEWAY_FLOOR_PLAN_CONCURRENCY）。
	planConcurrency int
	// F-L2: 探测失败 Warn 限流（每凭据 15 分钟至多 1 条）。
	warnGate *warnGate
	http     *http.Client
	stopCh   chan struct{}
	stopOnce sync.Once

	// A-C1: keyring 并发保护
	keyringMu sync.RWMutex
	// A-C2 & D-L1: 生命周期守卫
	lifecycleMu sync.Mutex
	started     bool
	workerDone  chan struct{}
}

func NewBalanceFloorGuard(db *pgxpool.Pool, encKey []byte) *BalanceFloorGuard {
	interval := 5 * time.Minute
	if v := os.Getenv("LLM_GATEWAY_BALANCE_FLOOR_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 30*time.Second {
			interval = d
		} else if n, err := strconv.Atoi(v); err == nil && n > 0 {
			// 分钟数整数路径：大数乘 minute 会溢出为负 duration，
			// NewTicker(负值) 直接 panic → 顶层 recover 后 guard 静默死亡。
			// 溢出时回落默认 5min（对齐 scan_scheduler 的 parsed > 0 复检，
			// 2026-09-14 审计 A-P3-5）。
			if d := time.Duration(n) * time.Minute; d > 0 {
				interval = d
			}
		}
	}
	disabled := false
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_BALANCE_FLOOR_GUARD"))) {
	case "off", "false", "0":
		disabled = true
	}
	// #4 逃生门阈值（小时，默认 2）。0=关闭；解析后 d>0 复检防大数溢出，
	// 与上面 interval 钳制同一教训（time.Duration 乘法溢出为负会让陈旧判定
	// 永真/永假，2026-09-14 审计 A-P3-5 定式）。
	escapeStale := 2 * time.Hour
	escapeOff := false
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_BALANCE_FLOOR_ESCAPE_HOURS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			switch {
			case n == 0:
				escapeOff = true
			case n > 0:
				if d := time.Duration(n) * time.Hour; d > 0 {
					escapeStale = d
				}
			}
		}
	}
	// E-B1: 套餐探测并发度，默认 10；钳制 1..100 —— 0/负值会让 worker pool
	// 空转，过大会把厂商控制面 API 打出限流。
	planConcurrency := 10
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_FLOOR_PLAN_CONCURRENCY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n < 1 {
				n = 1
			}
			if n > 100 {
				n = 100
			}
			planConcurrency = n
		}
	}
	return &BalanceFloorGuard{
		db:              db,
		encKey:          encKey,
		interval:        interval,
		disabled:        disabled,
		escapeStale:     escapeStale,
		escapeOff:       escapeOff,
		planConcurrency: planConcurrency,
		warnGate:        newWarnGate(planWarnWindow),
		http:            &http.Client{Timeout: 10 * time.Second},
		stopCh:          make(chan struct{}),
	}
}

// SetKeyring wires the credential keyring (same as CredentialProbeV2);
// nil-safe — decrypt falls back to the Fernet key.
func (g *BalanceFloorGuard) SetKeyring(kr *secret.Keyring) {
	if g != nil {
		g.keyringMu.Lock()
		g.keyring = kr
		g.keyringMu.Unlock()
	}
}

func (g *BalanceFloorGuard) Start(ctx context.Context) {
	if g == nil {
		return
	}
	if g.disabled {
		slog.Info("balance_floor_guard disabled (LLM_GATEWAY_BALANCE_FLOOR_GUARD)")
		return
	}
	// A-C2: Start 不可重入 —— 第二次调用不得再起一个 ticker goroutine
	//（双倍探测、双倍摘出/恢复写竞争）。一次性生命周期：Stop 之后也不复活。
	g.lifecycleMu.Lock()
	if g.started {
		g.lifecycleMu.Unlock()
		slog.Warn("balance_floor_guard: Start ignored, already running")
		return
	}
	g.started = true
	g.workerDone = make(chan struct{})
	g.lifecycleMu.Unlock()
	slog.Info("balance_floor_guard started",
		"interval", g.interval,
		"vendors", []string{"zhipu", "minimax"},
		"floor_fields", []string{"balance_floor_usd", "quota_floor_tokens", "quota_floor_percent"})
	go func() {
		// D-L1: 关闭 workerDone 供 Stop() join；先注册（defer 后进先出，
		// 在 panic recover 之后执行）—— 即使 sweep panic 也能解除 Stop 阻塞。
		defer close(g.workerDone)
		// Audit R20 (2026-09-13): top-level panic guard. A panic on this
		// loop (pgx driver, JSON decode, providercap) previously took down
		// the whole gateway process — same shape the BaseWorker scaffold
		// already protects against.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("balance_floor_guard worker panicked", "panic", r, "stack", string(debug.Stack()))
			}
		}()
		ticker := time.NewTicker(g.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("balance_floor_guard stopping")
				return
			case <-g.stopCh:
				slog.Info("balance_floor_guard stopped")
				return
			case <-ticker.C:
				if err := g.safeCycle(ctx); err != nil {
					slog.Warn("balance_floor_guard cycle failed", "error", err)
				}
			}
		}
	}()
}

// safeCycle runs one sweep with a per-cycle panic guard so a bad credential
// payload skips this tick instead of killing the worker loop.
func (g *BalanceFloorGuard) safeCycle(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("balance_floor_guard cycle panicked", "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("balance_floor_guard cycle panic: %v", r)
		}
	}()
	return g.cycle(ctx)
}

// Stop closes the stop channel and joins the worker (D-L1) so a caller
// releasing shared resources (DB pool) cannot race a final sweep. The join is
// capped at stopWaitTimeout: the sweep ctx derives from Start's caller ctx
// (Background in main.go), so an unbounded wait could stall process shutdown
// for minutes on a mid-cycle vendor timeout — the worker still exits on its
// own via stopCh afterwards.
func (g *BalanceFloorGuard) Stop() {
	if g == nil {
		return
	}
	g.stopOnce.Do(func() {
		close(g.stopCh)
		g.lifecycleMu.Lock()
		done := g.workerDone
		g.lifecycleMu.Unlock()
		if done == nil {
			return // 从未 Start（或 disabled）：无 worker 可等
		}
		select {
		case <-done:
		case <-time.After(stopWaitTimeout):
			slog.Warn("balance_floor_guard: stop timeout waiting for worker",
				"timeout", stopWaitTimeout.String())
		}
	})
}

// CycleNow runs one sweep synchronously — test/ops entry point.
func (g *BalanceFloorGuard) CycleNow(ctx context.Context) error {
	if g == nil || g.disabled {
		return nil
	}
	return g.safeCycle(ctx)
}

func (g *BalanceFloorGuard) cycle(ctx context.Context) error {
	if g.db == nil {
		return nil
	}
	if err := g.sweepCurrencyFloors(ctx); err != nil {
		slog.Warn("balance_floor_guard: currency sweep failed", "error", err)
	}
	// 清下限释放放在套餐 sweep 之前：同周期内刚释放的 zhipu/minimax
	// 凭据能立刻被套餐探测感知到（floors 已 NULL，不会被重新摘出）。
	if err := g.releaseClearedFloorCredentials(ctx); err != nil {
		slog.Warn("balance_floor_guard: cleared-floor release failed", "error", err)
	}
	// #4 逃生门（R28, 2026-09-14）：陈旧 plan 证据释放，同样放在套餐 sweep
	// 之前 —— 释放后同 tick 的 plan sweep 立即重探测：成功且仍击穿则经
	// floorPull 重摘（checked_at 已刷新，2h 内不再触发逃生门），失败则保持
	// 释放态由 #12a 的 15 分钟退避节奏重试。
	if err := g.releaseStaleProbeFloorCredentials(ctx); err != nil {
		slog.Warn("balance_floor_guard: stale-probe escape release failed", "error", err)
	}
	return g.sweepPlanQuotas(ctx)
}

// releaseClearedFloorCredentials releases floor-pulled credentials once the
// floor that pulled them is gone: currency floor NULL AND (both plan floors
// NULL, or the vendor is not a plan vendor whose plan floors would be inert
// config anyway — they have no probe data source, never participate in pull/
// restore). Until this pass existed such rows hung forever: currency pass C
// requires balance_floor_usd IS NOT NULL, the plan path no-ops on NULL floors
// (floorNone) and skips non-plan vendors outright, and BalanceQuotaProbe
// exempts balance_floor rows by design (anti ping-pong). A zhipu/minimax row
// with plan floors still set stays owned by the plan hysteresis path.
// Routability guards mirror pass C's candidate SELECT: a row disabled by
// other means is released once those disables lift.
func (g *BalanceFloorGuard) releaseClearedFloorCredentials(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tag, err := g.db.Exec(cctx, `
		UPDATE credentials
		SET quota_state = 'ok',
		    quota_recover_at = NULL,
		    availability_state = 'ready',
		    availability_recover_at = NULL,
		    state_reason_code = NULL,
		    state_reason_detail = 'balance_floor guard: floors cleared by operator, released to routing pool',
		    state_updated_at = now()
		WHERE COALESCE(quota_state, 'ok') = 'balance_exhausted'
		  AND COALESCE(state_reason_code, '') = 'balance_floor'
		  AND balance_floor_usd IS NULL
		  AND (
		      (quota_floor_tokens IS NULL AND quota_floor_percent IS NULL)
		      OR NOT EXISTS (
		          SELECT 1 FROM providers q
		          WHERE q.id = credentials.provider_id
		            AND COALESCE(q.catalog_code, '') IN ('zhipu', 'minimax')
		      )
		  )
		  AND status = 'active'
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND EXISTS (
		      SELECT 1 FROM providers p
		      WHERE p.id = credentials.provider_id
		        AND p.enabled = TRUE
		        AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  )
	`)
	if err != nil {
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("balance_floor_guard: released floor-pulled credentials (floors cleared)",
			"count", n)
	}
	return nil
}

// releaseStaleProbeFloorCredentials is the plan-path escape hatch (#4, R28
// 2026-09-14). The plan sweep only restores from FRESH probe evidence
// (fail-open on error; since #12a a failing row isn't even re-probed for 15
// minutes), so once plan_quota_checked_at goes cold the hysteresis restore
// can never fire again and the row hangs in balance_exhausted/balance_floor
// forever — three dead ends at once: currency pass C requires
// balance_floor_usd, the plan restore requires a fresh probe, and
// BalanceQuotaProbe exempts balance_floor rows by design (anti ping-pong).
// The hatch releases such rows after
// LLM_GATEWAY_BALANCE_FLOOR_ESCAPE_HOURS (default 2h since audit B-E1
// 2026-09-16, 0=off) of stale/absent plan evidence. Single idempotent UPDATE;
// ownership invariants match
// releaseClearedFloorCredentials (only our own balance_floor rows, never
// manual_disabled rows, provider must be routable).
//
// 自愈性：释放后同 tick 的 plan sweep 会立刻重探测 —— 探测成功且仍击穿则经
// floorPull 立即重摘（persistPlanState 已刷新 checked_at，2h 内逃生门不再
// 触发）；探测仍失败则行保持 released（fail-open），#12a 的 15 分钟退避限制
// 重试节奏，探测恢复后按正常滞回工作。
func (g *BalanceFloorGuard) releaseStaleProbeFloorCredentials(ctx context.Context) error {
	if g.escapeOff {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tag, err := g.db.Exec(cctx, `
		UPDATE credentials
		SET quota_state = 'ok',
		    quota_recover_at = NULL,
		    availability_state = 'ready',
		    availability_recover_at = NULL,
		    state_reason_code = NULL,
		    state_reason_detail = 'balance_floor guard: escape hatch, plan probe stale',
		    state_updated_at = now()
		WHERE COALESCE(quota_state, 'ok') = 'balance_exhausted'
		  AND COALESCE(state_reason_code, '') = 'balance_floor'
		  AND (quota_floor_tokens IS NOT NULL OR quota_floor_percent IS NOT NULL)
		  AND (plan_quota_checked_at IS NULL
		       OR plan_quota_checked_at < now() - $1::interval)
		  AND status = 'active'
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  -- R30 P2-1（2026-09-16）：组合配置（货币下限+套餐下限同时存在）下，
		  -- pass B 先按新鲜货币证据摘出，本逃生门再以"plan 证据陈旧"释放，
		  -- 每 5 分钟永久乒乓且货币下限被绕过。货币证据仍新鲜且仍击穿时
		  -- 不释放——摘出交回 pass B 的货币语义。
		  AND NOT (
		      balance_floor_usd IS NOT NULL
		      AND balance_usd IS NOT NULL
		      AND balance_last_checked_at IS NOT NULL
		      AND balance_last_checked_at > now() - interval '30 minutes'
		      AND balance_usd <= balance_floor_usd
		  )
		  AND EXISTS (
		      SELECT 1 FROM providers p
		      WHERE p.id = credentials.provider_id
		        AND p.enabled = TRUE
		        AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  )
	`, g.escapeStale)
	if err != nil {
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("balance_floor_guard: escape hatch released floor-pulled credentials (plan probe stale)",
			"count", n, "stale_after", g.escapeStale.String())
	}
	return nil
}

// sweepCurrencyFloors enforces currency floors in three passes:
//
//	A refresh — the ONLY routine writer of credentials.balance_usd is
//	    cycleAll's probeBalance, and cycleAll does not run in the default
//	    new-probe mode (LLM_GATEWAY_USE_NEW_PROBE_MODE, true since
//	    2026-07-14). The guard therefore refreshes balance_usd itself for
//	    every floor-configured credential whose balance is >15min stale —
//	    a GET on the vendor balance API, no tokens burned. Without this
//	    pass the freshness-gated pull below would never fire on default
//	    deployments.
//	B pull — pure SQL, only from freshly-checked balances (stale/NULL
//	    balance is never a pull signal).
//	C restore — floor-pulled rows are invisible to the regular probes by
//	    design (BalanceQuotaProbe exemption + cycleAll exclusion), so pass
//	    A's refresh feeds the hysteresis restore here.
func (g *BalanceFloorGuard) sweepCurrencyFloors(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Pass A: refresh stale balances for floor-configured credentials.
	rows, err := g.db.Query(cctx, `
		SELECT c.id, c.secret_ciphertext,
		       COALESCE(p.base_url, ''), COALESCE(p.protocol, 'openai-completions'),
		       COALESCE(p.catalog_code, '')
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.balance_floor_usd IS NOT NULL
		  AND (c.balance_last_checked_at IS NULL
		       OR c.balance_last_checked_at < now() - interval '15 minutes')
		  AND c.status = 'active'
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
		ORDER BY c.balance_last_checked_at ASC NULLS FIRST
		LIMIT 100
	`)
	if err != nil {
		return err
	}
	type refreshRow struct {
		id         int64
		ciphertext []byte
		baseURL    string
		protocol   string
		catalog    string
	}
	var toRefresh []refreshRow
	for rows.Next() {
		var r refreshRow
		if err := rows.Scan(&r.id, &r.ciphertext, &r.baseURL, &r.protocol, &r.catalog); err != nil {
			slog.Warn("balance_floor_guard: currency refresh scan failed", "error", err)
			continue
		}
		toRefresh = append(toRefresh, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, r := range toRefresh {
		g.refreshBalance(cctx, r.id, r.ciphertext, r.baseURL, r.protocol, r.catalog)
	}

	// Pass B: pull credentials whose fresh balance breached the floor.
	tag, err := g.db.Exec(cctx, `
		UPDATE credentials
		SET quota_state = 'balance_exhausted',
		    availability_state = 'suspended',
		    availability_recover_at = NULL,
		    state_reason_code = 'balance_floor',
		    state_reason_detail = 'balance_floor guard: balance_usd <= balance_floor_usd',
		    state_updated_at = now()
		WHERE balance_floor_usd IS NOT NULL
		  AND balance_usd IS NOT NULL
		  AND balance_usd <= balance_floor_usd
		  AND balance_last_checked_at > now() - interval '30 minutes'
		  AND COALESCE(quota_state, 'ok') = 'ok'
		  AND COALESCE(availability_state, 'ready') NOT IN ('suspended', 'auth_failed')
		  AND status = 'active'
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  -- provider 守卫与 pass C 恢复选点对齐：disabled provider 下的
		  -- 凭据不得只被摘出却永远轮不到恢复（pass C 选不到它们）。
		  AND EXISTS (
		      SELECT 1 FROM providers p
		      WHERE p.id = credentials.provider_id
		        AND p.enabled = TRUE
		        AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  )
	`)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		slog.Info("balance_floor_guard: pulled credentials below currency floor",
			"count", tag.RowsAffected())
	}

	// Pass C: restore floor-pulled credentials that recharged above the
	// hysteresis band (fed by pass A's refresh).
	rows, err = g.db.Query(cctx, `
		SELECT c.id,
		       COALESCE(p.base_url, ''), COALESCE(p.protocol, 'openai-completions'),
		       COALESCE(p.catalog_code, '')
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.quota_state = 'balance_exhausted'
		  AND COALESCE(c.state_reason_code, '') = 'balance_floor'
		  AND c.balance_floor_usd IS NOT NULL
		  AND c.status = 'active'
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
		ORDER BY c.balance_last_checked_at ASC NULLS FIRST
		LIMIT 100
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var toRestore []refreshRow
	for rows.Next() {
		var r refreshRow
		if err := rows.Scan(&r.id, &r.baseURL, &r.protocol, &r.catalog); err != nil {
			slog.Warn("balance_floor_guard: currency restore scan failed", "error", err)
			continue
		}
		toRestore = append(toRestore, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range toRestore {
		g.restorePulledCurrency(cctx, r.id, r.baseURL, r.protocol, r.catalog)
	}
	return nil
}

// refreshBalance re-reads a credential's vendor balance and writes
// balance_usd/balance_last_checked_at. GET-only — never consumes tokens.
// Fail-open: on any error the previous values are kept.
func (g *BalanceFloorGuard) refreshBalance(ctx context.Context, id int64, ciphertext []byte, baseURL, protocol, catalog string) bool {
	desc := providercap.Resolve(protocol, catalog)
	balURL := providercap.BalanceURL(baseURL, desc)
	if balURL == "" {
		return false // vendor has no balance API; floors there are plan-floor territory
	}
	if blocked, reason := providercap.EgressBlocked(balURL); blocked {
		providercap.WarnBlocked("balance_floor_guard.balance", balURL, reason)
		return false
	}
	apiKey, err := decryptCiphertext(ciphertext, g.keyring, g.encKey)
	if err != nil || apiKey == "" {
		slog.Warn("balance_floor_guard: decrypt failed for currency refresh",
			"credential_id", id, "error", err)
		return false
	}
	balUSD, ok := providercap.FetchBalanceUSD(ctx, g.http, balURL, apiKey, desc)
	if !ok {
		return false // fail-open: keep previous balance, retry next cycle
	}
	if _, err := g.db.Exec(ctx, `
		UPDATE credentials SET balance_usd = $1, balance_last_checked_at = now()
		WHERE id = $2
	`, balUSD, id); err != nil {
		slog.Warn("balance_floor_guard: balance refresh write failed",
			"credential_id", id, "error", err)
		return false
	}
	return true
}

// restorePulledCurrency restores a floor-pulled credential once pass A's
// refreshed balance_usd clears the hysteresis band. This function never
// fetches the vendor balance itself — pass A (cycle head) owns balance
// refresh; the balURL/egress dance that used to live here was dead code from
// an earlier self-fetch design (R30 P3-2).
func (g *BalanceFloorGuard) restorePulledCurrency(ctx context.Context, id int64, baseURL, protocol, catalog string) {
	tag, err := g.db.Exec(ctx, `
		UPDATE credentials
		SET quota_state = 'ok',
		    quota_recover_at = NULL,
		    availability_state = 'ready',
		    availability_recover_at = NULL,
		    state_reason_code = NULL,
		    state_reason_detail = 'balance_floor guard: recharged above hysteresis band',
		    state_updated_at = now()
		WHERE id = $1
		  AND COALESCE(state_reason_code, '') = 'balance_floor'
		  AND balance_floor_usd IS NOT NULL
		  AND balance_usd IS NOT NULL
		  AND balance_usd >= balance_floor_usd * $2
	`, id, planFloorRestoreFactor)
	if err != nil {
		slog.Warn("balance_floor_guard: currency restore failed", "credential_id", id, "error", err)
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Info("balance_floor_guard: credential restored above currency floor",
			"credential_id", id)
	}
}

// sweepPlanQuotas probes zhipu/minimax subscription-plan quotas, persists the
// sensing columns, and enforces token/percent floors. Rows whose last probe
// FAILED are parked for 15 minutes per attempt (#12a, R28 2026-09-14): they
// sort by the failure stamp first (sinks to the front when eligible) but are
// skipped until the backoff expires, so a dead vendor plan API is not hit on
// every 5-min tick. Success clears the stamp (persistPlanState).
//
// E-B1 (2026-09-16)：候选探测改走 runPlanProbes 的有界 worker pool（默认
// 并发 10）—— 200 凭据串行、单请求最坏 10s 超时会远超 3 分钟 cctx；并发后
// 同一 cctx 仍兜底整体时限，fail-open 语义不变。周期结束输出 F-L1 汇总。
func (g *BalanceFloorGuard) sweepPlanQuotas(ctx context.Context) error {
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	rows, err := g.db.Query(cctx, `
		SELECT c.id, c.secret_ciphertext,
		       COALESCE(p.base_url, ''), COALESCE(p.protocol, 'openai-completions'),
		       COALESCE(p.catalog_code, ''),
		       c.balance_floor_usd, c.quota_floor_tokens, c.quota_floor_percent,
		       COALESCE(c.quota_state, 'ok'), COALESCE(c.state_reason_code, '')
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.status = 'active'
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
		  AND (
		      COALESCE(p.catalog_code, '') IN ('zhipu', 'minimax')
		      OR c.quota_floor_tokens IS NOT NULL
		      OR c.quota_floor_percent IS NOT NULL
		  )
		  -- #12a 失败退避：最近一次探测失败的行冷却 15 分钟后才再次入选。
		  AND COALESCE(c.plan_quota_probe_failed_at, to_timestamp(0)) < now() - interval '15 minutes'
		ORDER BY COALESCE(c.plan_quota_probe_failed_at, c.plan_quota_checked_at) ASC NULLS FIRST
		LIMIT 200
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var cands []floorCandidate
	for rows.Next() {
		var c floorCandidate
		var floorUSD *float64
		if err := rows.Scan(&c.ID, &c.Ciphertext, &c.BaseURL, &c.Protocol, &c.Catalog,
			&floorUSD, &c.FloorTokens, &c.FloorPercent, &c.QuotaState, &c.ReasonCode); err != nil {
			slog.Warn("balance_floor_guard: plan scan failed", "error", err)
			continue
		}
		c.FloorUSD = floorUSD
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// 只处理订阅套餐厂商；非套餐厂商的 token/percent 下限无数据源，
	// 由选点进来的仅是为了不被漏掉 —— 明确跳过。
	planCands := make([]floorCandidate, 0, len(cands))
	for _, c := range cands {
		if c.Catalog == "zhipu" || c.Catalog == "minimax" {
			planCands = append(planCands, c)
		}
	}

	var stats planSweepStats
	runPlanProbes(cctx, planCands, g.planConcurrency, &stats, func(ctx context.Context, c floorCandidate) {
		g.handlePlanCredential(ctx, c, &stats)
	})

	// F-L1: 周期级汇总 —— 探测成功率、摘出/恢复量与耗时，运营可观测。
	// probed=0（无套餐厂商的部署）不输出：每 5 分钟一条空行是纯噪音
	//（审计 2026-09-16 二轮 F-2）。probed 只计实际发起的探测；ctx 到点
	// 被放弃的候选计入 skipped（R30 P2-4：len(planCands) 会把未探测的
	// 也算进 probed，success+failed < probed 误导运营）。
	if len(planCands) > 0 {
		slog.Info("balance_floor_guard: plan sweep completed",
			"probed", stats.probed.Load(),
			"skipped", stats.skipped.Load(),
			"success", stats.success.Load(),
			"failed", stats.failed.Load(),
			"pulled", stats.pulled.Load(),
			"restored", stats.restored.Load(),
			"duration", time.Since(start).Round(time.Millisecond).String())
	}
	return nil
}

// planSweepStats aggregates per-cycle plan probe outcomes (F-L1) — atomics
// because probes run concurrently on the worker pool. probed/skipped split
// attempted vs abandoned candidates (R30 P2-4).
type planSweepStats struct {
	probed   atomic.Int64
	skipped  atomic.Int64
	success  atomic.Int64
	failed   atomic.Int64
	pulled   atomic.Int64
	restored atomic.Int64
}

// runPlanProbes probes candidates on a bounded worker pool (E-B1). The
// semaphore blocks submission until a slot frees, so at most `limit` probes
// are in flight; per-candidate panics are contained exactly like the old
// serial loop. errgroup 未进 vendor（modules.txt 只收了 semaphore/
// singleflight），semaphore + WaitGroup 在此语义完全等价。
func runPlanProbes(ctx context.Context, cands []floorCandidate, limit int, stats *planSweepStats, probe func(context.Context, floorCandidate)) {
	if limit < 1 {
		limit = 1
	}
	sem := semaphore.NewWeighted(int64(limit))
	var wg sync.WaitGroup
	for _, c := range cands {
		c := c
		// cctx 到点后 Acquire 报错：剩余候选放弃本轮（fail-open，下周期
		// 重来），已提交的探测随取消的 ctx 自然收尾。
		if err := sem.Acquire(ctx, 1); err != nil {
			stats.skipped.Add(1)
			continue
		}
		stats.probed.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer sem.Release(1)
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("balance_floor_guard: panic handling credential",
						"credential_id", c.ID, "panic", r)
				}
			}()
			probe(ctx, c)
		}()
	}
	wg.Wait()
}

func (g *BalanceFloorGuard) handlePlanCredential(ctx context.Context, c floorCandidate, stats *planSweepStats) {
	var st *planState
	var err error
	switch c.Catalog {
	case "zhipu":
		st, err = g.fetchZhipuPlan(ctx, c.BaseURL, c.Ciphertext)
	case "minimax":
		st, err = g.fetchMiniMaxPlan(ctx, c.BaseURL, c.Ciphertext)
	default:
		return
	}
	if err != nil {
		// fail-open：探测失败保留旧状态，绝不用过期数据做摘出决策。
		// F-L2: 失败日志升为 Warn，但每凭据 15 分钟窗口（与 #12a 退避同窗）
		// 至多 1 条，其余降级 Debug —— 挂死的厂商端点不能刷屏。
		if g.warnGate.allow(c.ID, time.Now()) {
			slog.Warn("balance_floor_guard: plan probe failed",
				"credential_id", c.ID, "catalog", c.Catalog, "error", err)
		} else {
			slog.Debug("balance_floor_guard: plan probe failed (rate-limited)",
				"credential_id", c.ID, "catalog", c.Catalog, "error", err)
		}
		if stats != nil {
			stats.failed.Add(1)
		}
		// #12a 失败退避记账（R28, 2026-09-14）：只写 plan_quota_probe_failed_at。
		// 绝不写 plan_quota_checked_at —— 它的新鲜度是 #4 逃生门的判据，失败时
		// 刷新它会让逃生门永远哑火。扫描侧凭 failed_at 让该行沉底 15 分钟，
		// 避免每个周期都打挂死的套餐端点。
		if _, uerr := g.db.Exec(ctx, `
			UPDATE credentials
			SET plan_quota_probe_failed_at = now()
			WHERE id = $1
		`, c.ID); uerr != nil {
			slog.Warn("balance_floor_guard: probe backoff stamp failed",
				"credential_id", c.ID, "error", uerr)
		}
		return
	}
	if stats != nil {
		stats.success.Add(1)
	}

	g.persistPlanState(ctx, c.ID, st)

	action := evaluatePlanFloor(c.FloorTokens, c.FloorPercent, st)
	switch action {
	case floorPull:
		// R21 (2026-09-13): the old `ReasonCode != "balance_floor"` guard here
		// permanently neutralized token/percent floors after an admin
		// reset-quota left an "ok + balance_floor reason" residue (the pull
		// was refused, while currency pass B — pure SQL — still pulled,
		// splitting the two paths). The SQL WHERE below (quota_state='ok' +
		// availability guards) already prevents overwriting our own pulled
		// rows, so re-evaluating a residue row is safe: below floor → re-pull,
		// recovered → the restore path clears the stale reason.
		if c.QuotaState == "ok" {
			detail := st.summary(c.FloorTokens, c.FloorPercent)
			tag, uerr := g.db.Exec(ctx, `
				UPDATE credentials
				SET quota_state = 'balance_exhausted',
				    availability_state = 'suspended',
				    availability_recover_at = NULL,
				    state_reason_code = 'balance_floor',
				    state_reason_detail = $1,
				    state_updated_at = now()
				WHERE id = $2
				  AND status = 'active'
				  AND lifecycle_status = 'active'
				  AND COALESCE(manual_disabled, FALSE) = FALSE
				  AND COALESCE(quota_state, 'ok') = 'ok'
				  AND COALESCE(availability_state, 'ready') NOT IN ('suspended', 'auth_failed')
			`, detail, c.ID)
			if uerr != nil {
				slog.Warn("balance_floor_guard: floor pull failed",
					"credential_id", c.ID, "error", uerr)
				return
			}
			if tag.RowsAffected() > 0 {
				if stats != nil {
					stats.pulled.Add(1)
				}
				slog.Info("balance_floor_guard: credential pulled below plan floor",
					"credential_id", c.ID, "detail", detail)
			}
		}
	case floorRestore:
		detail := st.summary(c.FloorTokens, c.FloorPercent)
		tag, uerr := g.db.Exec(ctx, `
			UPDATE credentials
			SET quota_state = 'ok',
			    quota_recover_at = NULL,
			    availability_state = 'ready',
			    availability_recover_at = NULL,
			    state_reason_code = NULL,
			    state_reason_detail = $1,
			    state_updated_at = now()
			WHERE id = $2
			  AND COALESCE(manual_disabled, FALSE) = FALSE
			  AND COALESCE(quota_state, 'ok') = 'balance_exhausted'
			  AND COALESCE(state_reason_code, '') = 'balance_floor'
		`, "balance_floor guard: recovered above hysteresis band ("+detail+")", c.ID)
		if uerr != nil {
			slog.Warn("balance_floor_guard: plan restore failed",
				"credential_id", c.ID, "error", uerr)
			return
		}
		if tag.RowsAffected() > 0 {
			if stats != nil {
				stats.restored.Add(1)
			}
			slog.Info("balance_floor_guard: credential restored above plan floor",
				"credential_id", c.ID, "detail", detail)
		}
	}
}

// persistPlanState writes the sensing columns. It deliberately does NOT touch
// state_updated_at — bumping it on every probe would keep auth-failure cycles
// forever "fresh" inside AutoRevoker's rolling window and cause false
// escalations. It also does NOT touch balance_last_checked_at — no currency
// balance was read here, and keeping it stale preserves pass A's 15-min
// refresh gate (and blocks pass B from treating a legacy balance_usd as fresh
// evidence on plan-only vendors). A successful probe clears
// plan_quota_probe_failed_at (#12a) — the backoff stamp only applies to
// consecutive failures.
func (g *BalanceFloorGuard) persistPlanState(ctx context.Context, credID int64, st *planState) {
	windowsJSON, err := json.Marshal(st.Windows)
	if err != nil {
		windowsJSON = []byte("[]")
	}
	if _, err := g.db.Exec(ctx, `
		UPDATE credentials
		SET plan_quota_kind = $2,
		    plan_quota_windows = $3::jsonb,
		    plan_quota_remaining_tokens = $4,
		    plan_quota_used_percent = $5,
		    plan_quota_checked_at = now(),
		    plan_quota_probe_failed_at = NULL
		WHERE id = $1
	`, credID, st.Kind, string(windowsJSON), st.MinTokensRemaining,
		roundPercent(st.MaxUsedPercent)); err != nil {
		slog.Warn("balance_floor_guard: plan state write failed",
			"credential_id", credID, "error", err)
	}
}

func roundPercent(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

func (g *BalanceFloorGuard) fetchZhipuPlan(ctx context.Context, baseURL string, ciphertext []byte) (*planState, error) {
	u, err := originQuotaURL(baseURL, zhipuPlanQuotaPath)
	if err != nil {
		return nil, err
	}
	// 智谱校验：Authorization 不加 Bearer 前缀（与 chat API 不同，实测钉实）。
	apiKey, err := g.decryptKey(ciphertext)
	if err != nil {
		return nil, err
	}
	if blocked, reason := providercap.EgressBlocked(u); blocked {
		providercap.WarnBlocked("balance_floor_guard.zhipu", u, reason)
		return nil, fmt.Errorf("egress blocked: %s", reason)
	}
	body, err := g.getJSON(ctx, u, "Authorization", apiKey)
	if err != nil {
		return nil, fmt.Errorf("zhipu plan fetch: %w", err)
	}
	return parseZhipuPlan(body)
}

func (g *BalanceFloorGuard) fetchMiniMaxPlan(ctx context.Context, baseURL string, ciphertext []byte) (*planState, error) {
	apiKey, err := g.decryptKey(ciphertext)
	if err != nil {
		return nil, err
	}
	codingURL, err := originQuotaURL(baseURL, minimaxCodingPlanPath)
	if err != nil {
		return nil, err
	}
	if blocked, reason := providercap.EgressBlocked(codingURL); blocked {
		providercap.WarnBlocked("balance_floor_guard.minimax", codingURL, reason)
		return nil, fmt.Errorf("egress blocked: %s", reason)
	}
	body, err := g.getJSON(ctx, codingURL, "Authorization", "Bearer "+apiKey)
	if err == nil {
		if st, perr := parseMiniMaxPlan(body); perr == nil {
			return st, nil
		}
	}
	// 编程套餐端点不可用/业务失败 → Token Plan 订阅 Key fallback（同响应形状）。
	tokenURL, uerr := originQuotaURL(baseURL, minimaxTokenPlanPath)
	if uerr != nil {
		return nil, uerr
	}
	body, err = g.getJSON(ctx, tokenURL, "Authorization", "Bearer "+apiKey)
	if err != nil {
		return nil, fmt.Errorf("minimax plan fetch (coding+token): %w", err)
	}
	return parseMiniMaxPlan(body)
}

func (g *BalanceFloorGuard) decryptKey(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", fmt.Errorf("empty secret_ciphertext")
	}
	// A-C1: keyring 可能被 SetKeyring 并发替换，读取必须持读锁。
	g.keyringMu.RLock()
	kr := g.keyring
	g.keyringMu.RUnlock()
	key, err := decryptCiphertext(ciphertext, kr, g.encKey)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	if key == "" {
		return "", fmt.Errorf("decrypted key is empty")
	}
	return key, nil
}

// httpStatusError marks a completed HTTP exchange whose status wasn't 200 —
// typed so the retry policy (G-O1) can distinguish transient 5xx from a
// definitive 4xx (bad key / bad URL).
type httpStatusError struct {
	code int
	url  string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d from %s", e.code, e.url)
}

// retryableHTTPErr reports whether a getJSON attempt is worth retrying
// (G-O1): transport errors and 5xx are treated as transient; 4xx answers and
// caller-context cancellation are not.
func retryableHTTPErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var se *httpStatusError
	if errors.As(err, &se) {
		return se.code >= http.StatusInternalServerError
	}
	return true
}

func (g *BalanceFloorGuard) getJSON(ctx context.Context, u, authHeader, authValue string) ([]byte, error) {
	// 请求只构造一次：URL/头是确定性的，构造失败重试也不会变好 —— 且构造
	// 错误会绕过 retryableHTTPErr 的分类被当传输错误白烧 2 次重试（审计
	// 2026-09-16 二轮 F-1）。GET 无 body，跨 attempt 复用同一 req 是安全的。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(authHeader, authValue)
	req.Header.Set("Accept", "application/json")
	// 智谱对 Accept-Language 敏感（英文响应字段稳定，实测 cc-switch 同款头）。
	req.Header.Set("Accept-Language", "en-US,en")

	var lastErr error
	for attempt := 1; attempt <= planHTTPMaxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(planHTTPRetryDelay * time.Duration(attempt-1)):
			}
		}
		body, err := g.getJSONOnce(req, u)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retryableHTTPErr(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (g *BalanceFloorGuard) getJSONOnce(req *http.Request, u string) ([]byte, error) {
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{code: resp.StatusCode, url: u}
	}
	return io.ReadAll(io.LimitReader(resp.Body, planRespBodyLimit))
}
