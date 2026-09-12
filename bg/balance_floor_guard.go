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
// trg_notify_auto_route_refresh 触发器自动广播缓存失效。恢复：额度回到
// floor*1.1（token/货币）或 floor-2pp（百分比）滞回带以上，且所有权校验
// （state_reason_code='balance_floor'）通过，才翻回 ok/ready —— 永远不会碰
// 反应式（writer.go）或其它路径写入的配额状态。
//
// 与 BalanceQuotaProbe 的协作：2 分钟探测循环本来会对 balance_exhausted 凭据
// 发真实 chat 探测；被 floor 摘出的凭据额度还剩缓冲，chat 探测必然成功并把
// 它写回 ok（writeHealth 硬配额守卫允许 $8='ok' 通过）→ 乒乓。所以
// balance_quota_probe.go 对 state_reason_code='balance_floor' 的行豁免，恢复
// 完全由本 guard 负责。admin 的 force-probe/reset-state 仍可人工越过
// （下一轮 sweep 会按 floor 重新摘出，除非操作员清掉下限）。
//
// Env knobs:
//   LLM_GATEWAY_BALANCE_FLOOR_INTERVAL — sweep 周期，默认 5m（>=30s）
//   LLM_GATEWAY_BALANCE_FLOOR_GUARD    — "off"/"false"/"0" 关闭 sweep
//
// 安全性：所有 floor 字段默认 NULL → 选点为空 → worker 空转；探测失败
// fail-open（保留上次状态，绝不用过期数据做摘出决策）；单凭据失败不影响
// 其它凭据。

import (
	"context"
	"encoding/json"
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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
)

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
	http     *http.Client
	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewBalanceFloorGuard(db *pgxpool.Pool, encKey []byte) *BalanceFloorGuard {
	interval := 5 * time.Minute
	if v := os.Getenv("LLM_GATEWAY_BALANCE_FLOOR_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 30*time.Second {
			interval = d
		} else if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = time.Duration(n) * time.Minute
		}
	}
	disabled := false
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_BALANCE_FLOOR_GUARD"))) {
	case "off", "false", "0":
		disabled = true
	}
	return &BalanceFloorGuard{
		db:       db,
		encKey:   encKey,
		interval: interval,
		disabled: disabled,
		http:     &http.Client{Timeout: 10 * time.Second},
		stopCh:   make(chan struct{}),
	}
}

// SetKeyring wires the credential keyring (same as CredentialProbeV2);
// nil-safe — decrypt falls back to the Fernet key.
func (g *BalanceFloorGuard) SetKeyring(kr *secret.Keyring) {
	if g != nil {
		g.keyring = kr
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
	slog.Info("balance_floor_guard started",
		"interval", g.interval,
		"vendors", []string{"zhipu", "minimax"},
		"floor_fields", []string{"balance_floor_usd", "quota_floor_tokens", "quota_floor_percent"})
	go func() {
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

func (g *BalanceFloorGuard) Stop() {
	if g == nil {
		return
	}
	g.stopOnce.Do(func() { close(g.stopCh) })
}

// CycleNow runs one sweep synchronously — test/ops entry point.
func (g *BalanceFloorGuard) CycleNow(ctx context.Context) error {
	if g == nil || g.disabled {
		return nil
	}
	return g.cycle(ctx)
}

func (g *BalanceFloorGuard) cycle(ctx context.Context) error {
	if g.db == nil {
		return nil
	}
	if err := g.sweepCurrencyFloors(ctx); err != nil {
		slog.Warn("balance_floor_guard: currency sweep failed", "error", err)
	}
	return g.sweepPlanQuotas(ctx)
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

// restorePulledCurrency re-reads the balance of a floor-pulled credential and
// restores it when the fresh balance clears the hysteresis band.
func (g *BalanceFloorGuard) restorePulledCurrency(ctx context.Context, id int64, baseURL, protocol, catalog string) {
	desc := providercap.Resolve(protocol, catalog)
	balURL := providercap.BalanceURL(baseURL, desc)
	if balURL == "" {
		return // vendor has no balance API; operator must clear the floor
	}
	if blocked, reason := providercap.EgressBlocked(balURL); blocked {
		providercap.WarnBlocked("balance_floor_guard.balance", balURL, reason)
		return
	}
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
// sensing columns, and enforces token/percent floors.
func (g *BalanceFloorGuard) sweepPlanQuotas(ctx context.Context) error {
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
		ORDER BY c.plan_quota_checked_at ASC NULLS FIRST
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

	for _, c := range cands {
		// 只处理订阅套餐厂商；非套餐厂商的 token/percent 下限无数据源，
		// 由选点进来的仅是为了不被漏掉 —— 明确跳过。
		if c.Catalog != "zhipu" && c.Catalog != "minimax" {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("balance_floor_guard: panic handling credential",
						"credential_id", c.ID, "panic", r)
				}
			}()
			g.handlePlanCredential(cctx, c)
		}()
	}
	return nil
}

func (g *BalanceFloorGuard) handlePlanCredential(ctx context.Context, c floorCandidate) {
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
		slog.Debug("balance_floor_guard: plan probe failed",
			"credential_id", c.ID, "catalog", c.Catalog, "error", err)
		return
	}

	g.persistPlanState(ctx, c.ID, st)

	action := evaluatePlanFloor(c.FloorTokens, c.FloorPercent, st)
	switch action {
	case floorPull:
		if c.QuotaState == "ok" && c.ReasonCode != "balance_floor" {
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
// evidence on plan-only vendors).
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
		    plan_quota_checked_at = now()
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
	key, err := decryptCiphertext(ciphertext, g.keyring, g.encKey)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	if key == "" {
		return "", fmt.Errorf("decrypted key is empty")
	}
	return key, nil
}

func (g *BalanceFloorGuard) getJSON(ctx context.Context, u, authHeader, authValue string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(authHeader, authValue)
	req.Header.Set("Accept", "application/json")
	// 智谱对 Accept-Language 敏感（英文响应字段稳定，实测 cc-switch 同款头）。
	req.Header.Set("Accept-Language", "en-US,en")
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, u)
	}
	return io.ReadAll(io.LimitReader(resp.Body, planRespBodyLimit))
}
