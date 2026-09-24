package taskprofile

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// analyzer.go — 修正驱动的调参提案生成器（v2 闭环 P1，回路 C，
// docs/planning/AUTO_ROUTING_CLOSED_LOOP_V2_PLAN.md §4.4）。
//
// 数据流（全部只读，除最终 INSERT tuning_proposals）：
//
//	task_type_corrections(修正) ⨝ auto_route_selections_all(658 结构化特征)
//	  → 按 (auto→human) 对聚合判别特征（置信度带 / domain_hint / code 指示）
//
//	读面口径（R65）：全部走 auto_route_selections_all（含 hot heap），与
//	annotation 采样 / testbench 同口径——selections 先进 hot 表、promotion
//	滞后（settled 8h / unsettled 7 天）期间对裸父表不可见。
//	  → 过闸（对 ≥5 样本且类型修正率 ≥30%，规划 §七.1 防自激阈值）
//	  → 提案草稿（threshold_change / keyword_add，机器可执行形态与
//	    bg/feedback_analyzer、005 迁移的 JSONB 契约一致）
//	  → 每条草稿内联回放量化（would-fix/would-touch 计数，隐私口径：只有计数）
//	  → 待审去重后 INSERT status='pending'（不自动生效；批准走既有
//	    /api/admin/auto-route/tuning 热调参链路）
//
// 隐私红线（规划 §七.4）：只读 658 结构化特征列与 corrections 元数据，绝不
// 取 prompt 原文。keyword_add 的关键词证据来自 domain_hint（写入侧已归一化
// 的领域 token，见 658 迁移注释），不是 prompt 摘录。
//
// 与 bg/feedback_analyzer 的分工：那是 tuning_signals 质量驱动（发现"低质量
// 请求里的高频词"），本文件是人工修正驱动（发现"分类器判错的方向"）——
// 两条提案来源共用 tuning_proposals 表与审批链路。

// 可调闸门（规划 §七.1：≥5 样本、≥30% 修正率）。
const (
	// MinPairSamples: 一个 (auto→human) 修正对进入关键词提案的最少样本数。
	MinPairSamples = 5
	// MinTypeCorrectionRate: 任务类型修正率（该类型修正数/该类型请求总量）
	// 进入提案的类型级闸门。
	MinTypeCorrectionRate = 0.30
	// MinTypeCorrected: 类型级修正数下限（与比率闸门同时生效）。
	MinTypeCorrected = 5
	// MinHintShare: domain_hint token 成为关键词证据的最低占比。
	MinHintShare = 0.60
	// MaxProposalsPerRun: 单次生成的提案上限（防刷屏）。
	MaxProposalsPerRun = 5
	// thresholdFloor: 全局 LLM 兜底阈值提案的下限——低于此值启发式形同虚设。
	thresholdFloor = 0.50
	// minGlobalCorrected: 全局阈值提案所需的最低修正样本（全局改动影响面大，
	// 闸门比 per-pair 高一档）。
	minGlobalCorrected = 10
	// thresholdCoverage: 候选新阈值需覆盖 ≥80% 的被改判样本置信度。
	thresholdCoverage = 0.80
	// scanLimit: 单次收集的修正行上限（窗口内修正通常远小于此）。
	scanLimit = 5000
)

// AnalyzerWindowDaysBounds: generate 端点的窗口天数钳制范围。
const (
	AnalyzerWindowDaysMin     = 7
	AnalyzerWindowDaysMax     = 90
	AnalyzerWindowDaysDefault = 30
)

// thresholdCandidates 是全局阈值的候选落点（降档步进 0.05，从保守到激进）。
// 只降不升：修正驱动意味着分类器置信度过高，升档没有语义。
var thresholdCandidates = []float64{0.85, 0.80, 0.75, 0.70, 0.65, 0.60, 0.55, 0.50}

// genericHints 不允许成为关键词证据的 domain_hint 值。
//
// R64 P2：domain_hint 写入侧目前是三值枚举 {technical, business, general}
// （autoroute/structured_features.go inferDomainHint）——任一请求都必然命中
// 其中之一，枚举词在 658 特征列里零判别力，放行只会让 keyword_add 空转
// （生成即过闸，提案无信息量）。故把三个枚举值全部排除；待写入侧升级为
// 自由领域 token 后移除 technical/business/general 即自动恢复产出。
var genericHints = map[string]bool{
	"": true, "-": true, "unknown": true, "other": true, "none": true,
	"general": true, "technical": true, "business": true,
}

// keywordChannels: tuning_params 中可写的关键词通道（applyTuningParam 白名单）。
var keywordChannels = map[string]bool{"reasoning": true, "code": true, "creative": true}

// CorrectionRow is one misjudged sample with privacy-minimal features
// (658 structured columns only — no prompt text). Confidence is negative
// when NULL in the selections row (excluded from threshold math).
type CorrectionRow struct {
	AutoTaskType   string
	HumanTaskType  string
	Confidence     float64
	ConfidenceNull bool
	DomainHint     string
	HasCode        bool
}

// ProposalDraft is one machine-executable proposal awaiting insertion.
// Proposal/Evidence shapes follow the 005_tuning_proposals.sql contract;
// the JSON tags double as the generate-endpoint response shape.
type ProposalDraft struct {
	Category string         `json:"category"`
	TaskType string         `json:"task_type,omitempty"` // "" = global (threshold_change)
	Proposal map[string]any `json:"proposal"`
	Evidence map[string]any `json:"evidence"`
}

// pairStat aggregates the misjudged samples of one (auto→human) pair.
type pairStat struct {
	Auto      string
	Human     string
	Rows      []CorrectionRow
	Corrected int
	confBands map[string]int
	hints     map[string]int
}

func (p *pairStat) band(conf float64) string {
	switch {
	case conf < 0.50:
		return "<0.50"
	case conf < 0.70:
		return "0.50-0.70"
	case conf < 0.85:
		return "0.70-0.85"
	default:
		return ">=0.85"
	}
}

func (p *pairStat) add(r CorrectionRow) {
	p.Rows = append(p.Rows, r)
	p.Corrected++
	if !r.ConfidenceNull {
		p.confBands[p.band(r.Confidence)]++
	}
	if r.DomainHint != "" {
		p.hints[strings.ToLower(r.DomainHint)]++
	}
}

// aggregateCorrections groups rows by (auto→human). Pure.
func aggregateCorrections(rows []CorrectionRow) []*pairStat {
	byKey := map[string]*pairStat{}
	order := []string{}
	for _, r := range rows {
		key := r.AutoTaskType + "→" + r.HumanTaskType
		p, ok := byKey[key]
		if !ok {
			p = &pairStat{Auto: r.AutoTaskType, Human: r.HumanTaskType,
				confBands: map[string]int{}, hints: map[string]int{}}
			byKey[key] = p
			order = append(order, key)
		}
		p.add(r)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return byKey[order[i]].Corrected > byKey[order[j]].Corrected
	})
	out := make([]*pairStat, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

// qualifyingPairs filters pairs whose auto task type passes the type-level
// correction-rate gate (≥MinTypeCorrected misjudged AND rate ≥
// MinTypeCorrectionRate over the window). Pure.
func qualifyingPairs(pairs []*pairStat, volumes map[string]int) []*pairStat {
	correctedForType := map[string]int{}
	for _, p := range pairs {
		correctedForType[p.Auto] += p.Corrected
	}
	out := []*pairStat{}
	for _, p := range pairs {
		total := volumes[p.Auto]
		if total <= 0 {
			continue
		}
		if correctedForType[p.Auto] < MinTypeCorrected {
			continue
		}
		if float64(correctedForType[p.Auto])/float64(total) < MinTypeCorrectionRate {
			continue
		}
		out = append(out, p)
	}
	return out
}

// draftThresholdProposal builds ONE global threshold_change draft from the
// qualifying pairs' confidence bands: the least-aggressive candidate below
// the current threshold that still covers ≥thresholdCoverage of the
// misjudged confidences. Pure. Returns nil when the gate is not met
// (insufficient samples / already covered / no candidate below current).
func draftThresholdProposal(qualifying []*pairStat, volumes map[string]int, oldThreshold float64, windowDays int) *ProposalDraft {
	correctedForType := map[string]int{}
	bands := map[string]int{}
	confs := []float64{}
	for _, p := range qualifying {
		correctedForType[p.Auto] += p.Corrected
		for b, n := range p.confBands {
			bands[b] += n
		}
		for _, r := range p.Rows {
			if !r.ConfidenceNull {
				confs = append(confs, r.Confidence)
			}
		}
	}
	// 覆盖率基于原始置信度值：带粒度（0.15/0.20）粗于 0.05 的候选步进，
	// 不能用于覆盖率判定；conf_bands 仅作 evidence 展示。
	total := len(confs)
	if total < minGlobalCorrected {
		return nil
	}
	// 已判对样本不受影响——覆盖率只对"被改判样本"统计；新阈值只需把这些
	// 低置信样本交给 LLM 兜底，因此取 ≥thresholdCoverage 的最大候选。
	newV := 0.0
	for _, cand := range thresholdCandidates {
		if cand >= oldThreshold || cand < thresholdFloor {
			continue
		}
		covered := 0
		for _, c := range confs {
			if c < cand {
				covered++
			}
		}
		if float64(covered)/float64(total) >= thresholdCoverage {
			newV = cand
			break
		}
	}
	if newV == 0.0 {
		return nil
	}
	typeQualifying := map[string]bool{}
	for _, p := range qualifying {
		typeQualifying[p.Auto] = true
	}
	rateMax := 0.0
	for t := range typeQualifying {
		if v := volumes[t]; v > 0 {
			if r := float64(correctedForType[t]) / float64(v); r > rateMax {
				rateMax = r
			}
		}
	}
	return &ProposalDraft{
		Category: "threshold_change",
		TaskType: "",
		Proposal: map[string]any{
			"key": "thresholds.llm_confidence",
			"old": round2(oldThreshold),
			"new": newV,
		},
		Evidence: map[string]any{
			"source":           "corrections",
			"window_days":      windowDays,
			"corrected_total":  total,
			"conf_bands":       bands,
			"coverage":         thresholdCoverage,
			"rate_max":         round2(rateMax),
			"qualifying_pairs": pairSummaries(qualifying),
			"rationale": fmt.Sprintf(
				"%d 个被改判样本中 %.0f%% 置信度 < %.2f——降档让该置信带交给 LLM 兜底重分类",
				total, thresholdCoverage*100, newV),
		},
	}
}

// draftKeywordProposals builds keyword_add drafts from qualifying pairs:
// the pair's dominant domain_hint token becomes a keyword candidate for the
// HUMAN task type's channel (channels restricted to the tunable whitelist).
// Pure.
func draftKeywordProposals(qualifying []*pairStat, currentKeywords map[string][]string, windowDays int) []*ProposalDraft {
	drafts := []*ProposalDraft{}
	for _, p := range qualifying {
		if p.Corrected < MinPairSamples {
			continue
		}
		if !keywordChannels[p.Human] {
			continue
		}
		token, share := topHint(p)
		if token == "" || share < MinHintShare {
			continue
		}
		if genericHints[token] || len([]rune(token)) < 2 {
			continue
		}
		existing := currentKeywords["keywords."+p.Human]
		if containsString(existing, token) {
			continue
		}
		codeShare := 0.0
		if p.Corrected > 0 {
			codeHits := 0
			for _, r := range p.Rows {
				if r.HasCode {
					codeHits++
				}
			}
			codeShare = float64(codeHits) / float64(p.Corrected)
		}
		drafts = append(drafts, &ProposalDraft{
			Category: "keyword_add",
			TaskType: p.Auto,
			Proposal: map[string]any{
				"key":     "keywords." + p.Human,
				"add":     []string{token},
				"channel": p.Human,
			},
			Evidence: map[string]any{
				"source":          "corrections",
				"window_days":     windowDays,
				"auto_task_type":  p.Auto,
				"human_task_type": p.Human,
				"corrected":       p.Corrected,
				"domain_hint":     token,
				"hint_share":      round2(share),
				"has_code_share":  round2(codeShare),
				"rationale": fmt.Sprintf(
					"auto=%s→human=%s 的 %d 个被改判样本中 %.0f%% 带 domain_hint='%s'",
					p.Auto, p.Human, p.Corrected, share*100, token),
			},
		})
	}
	return drafts
}

func topHint(p *pairStat) (string, float64) {
	best, bestN := "", 0
	for h, n := range p.hints {
		if n > bestN || (n == bestN && h < best) {
			best, bestN = h, n
		}
	}
	if bestN == 0 {
		return "", 0
	}
	return best, float64(bestN) / float64(p.Corrected)
}

func pairSummaries(pairs []*pairStat) []map[string]any {
	out := make([]map[string]any, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, map[string]any{
			"auto": p.Auto, "human": p.Human, "corrected": p.Corrected,
		})
	}
	return out
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ─── DB 侧（只读收集 + 回放 + 插入） ─────────────────────────────────────

// CollectCorrectionSignals reads misjudged samples (privacy-minimal feature
// projection) and per-task-type volumes for the window. Read-only.
//
// 口径声明（R64 P3）：修正样本与分母 volume 都只统计 classifier='heuristic'
// 的行，与 BacktestThresholdBand 的回放口径一致——LLM 兜底改判的
// 请求不归启发式调参管，混入会同时虚增分子与分母。
// 口径声明（R65）：读面走 auto_route_selections_all（含 hot heap），与
// annotation 采样/testbench 同口径（promotion 滞后期间裸父表不可见）。
func CollectCorrectionSignals(ctx context.Context, pool *pgxpool.Pool, windowDays int) ([]CorrectionRow, map[string]int, error) {
	rows, err := pool.Query(ctx, `
		SELECT c.auto_task_type, c.human_task_type, s.confidence, s.domain_hint,
		       COALESCE(s.has_code_indicator, false)
		FROM task_type_corrections c
		JOIN auto_route_selections_all s ON s.request_id = c.request_id
		WHERE c.agrees = false
		  AND s.classifier = 'heuristic'
		  AND c.created_at >= NOW() - make_interval(days => $1)
		ORDER BY c.created_at DESC
		LIMIT $2
	`, windowDays, scanLimit)
	if err != nil {
		return nil, nil, fmt.Errorf("query corrections: %w", err)
	}
	defer rows.Close()

	var out []CorrectionRow
	for rows.Next() {
		var r CorrectionRow
		var conf *float64
		var hint *string
		if err := rows.Scan(&r.AutoTaskType, &r.HumanTaskType, &conf, &hint, &r.HasCode); err != nil {
			return nil, nil, fmt.Errorf("scan corrections: %w", err)
		}
		if conf == nil {
			r.ConfidenceNull = true
		} else {
			r.Confidence = *conf
		}
		if hint != nil {
			r.DomainHint = strings.ToLower(*hint)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate corrections: %w", err)
	}

	volumes := map[string]int{}
	// 口径声明：分母与修正样本同口径，只数 heuristic 行（见函数头注释）。
	volRows, err := pool.Query(ctx, `
		SELECT task_type, count(*) FROM auto_route_selections_all
		WHERE ts >= NOW() - make_interval(days => $1)
		  AND classifier = 'heuristic'
		GROUP BY task_type
	`, windowDays)
	if err != nil {
		return nil, nil, fmt.Errorf("query volumes: %w", err)
	}
	defer volRows.Close()
	for volRows.Next() {
		var t string
		var n int
		if err := volRows.Scan(&t, &n); err != nil {
			return nil, nil, fmt.Errorf("scan volumes: %w", err)
		}
		volumes[t] = n
	}
	return out, volumes, volRows.Err()
}

// CurrentTuningValues reads the tuning_params rows the drafts must respect
// (current threshold + current keyword lists). Read-only.
func CurrentTuningValues(ctx context.Context, pool *pgxpool.Pool) (map[string]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT key, value FROM tuning_params
		WHERE key IN ('thresholds.llm_confidence',
		              'keywords.reasoning', 'keywords.code', 'keywords.creative')
	`)
	if err != nil {
		return nil, fmt.Errorf("query tuning_params: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan tuning_params: %w", err)
		}
		out[k] = v
	}
	return out, rows.Err()
}

// BacktestThresholdBand quantifies a hypothetical threshold drop: among
// heuristic-classified requests in the window whose confidence falls into
// the [newV, oldV) band (i.e. would newly go to LLM fallback), how many were
// later corrected (would-fix proxy) and how many total would be touched
// (LLM-fallback cost proxy). Read-only; counts only (privacy: no rows leave
// the database).
func BacktestThresholdBand(ctx context.Context, pool *pgxpool.Pool, windowDays int, newV, oldV float64) (wouldTouch, wouldFixProxy int, err error) {
	err = pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE EXISTS (
		           SELECT 1 FROM task_type_corrections c
		           WHERE c.request_id = ars.request_id AND c.agrees = false))
		FROM auto_route_selections_all ars
		WHERE ars.ts >= NOW() - make_interval(days => $1)
		  AND ars.classifier = 'heuristic'
		  AND ars.confidence >= $2 AND ars.confidence < $3
	`, windowDays, newV, oldV).Scan(&wouldTouch, &wouldFixProxy)
	if err != nil {
		return 0, 0, fmt.Errorf("backtest threshold band: %w", err)
	}
	return wouldTouch, wouldFixProxy, nil
}

// BacktestKeywordHint quantifies a hypothetical keyword sourced from a
// domain_hint: how many of the auto task type's requests in the window carry
// that hint, and how many of those were corrected (would-fix proxy).
// Read-only.
//
// 口径（R65）：读 auto_route_selections_all（与 collect/volume/threshold
// 回放同读面），且只统计 classifier='heuristic' 的行——collect/threshold
// 回放均限 heuristic，混入 llm/jev 行会使关键词证据计数虚高。
func BacktestKeywordHint(ctx context.Context, pool *pgxpool.Pool, windowDays int, taskType, hint string) (matched, matchedCorrected int, err error) {
	err = pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE EXISTS (
		           SELECT 1 FROM task_type_corrections c
		           WHERE c.request_id = ars.request_id AND c.agrees = false))
		FROM auto_route_selections_all ars
		WHERE ars.ts >= NOW() - make_interval(days => $1)
		  AND ars.classifier = 'heuristic'
		  AND ars.task_type = $2 AND lower(ars.domain_hint) = $3
	`, windowDays, taskType, strings.ToLower(hint)).Scan(&matched, &matchedCorrected)
	if err != nil {
		return 0, 0, fmt.Errorf("backtest keyword hint: %w", err)
	}
	return matched, matchedCorrected, nil
}

// GenerateCorrectionProposals runs the full corrections→proposals pipeline:
// collect → gate → draft → inline backtest (result merged into evidence) →
// dedupe against pending proposals → insert as status='pending'. Nothing is
// auto-applied; approval goes through the existing tuning endpoints.
// operator 是触发生成的管理员身份（写入每条 evidence，提案全程可归因）。
func GenerateCorrectionProposals(ctx context.Context, pool *pgxpool.Pool, windowDays int, operator string) ([]ProposalDraft, error) {
	rows, volumes, err := CollectCorrectionSignals(ctx, pool, windowDays)
	if err != nil {
		return nil, err
	}
	tuning, err := CurrentTuningValues(ctx, pool)
	if err != nil {
		return nil, err
	}

	oldThreshold := 0.70 // 02-seed 默认；tuning_params 缺行时按默认保守处理
	if raw, ok := tuning["thresholds.llm_confidence"]; ok {
		if v, perr := strconv.ParseFloat(strings.TrimSpace(raw), 64); perr == nil {
			oldThreshold = v
		}
	}
	currentKeywords := map[string][]string{}
	for _, ch := range []string{"reasoning", "code", "creative"} {
		if raw, ok := tuning["keywords."+ch]; ok {
			var kws []string
			if json.Unmarshal([]byte(raw), &kws) == nil {
				currentKeywords["keywords."+ch] = kws
			}
		}
	}

	pairs := aggregateCorrections(rows)
	qualifying := qualifyingPairs(pairs, volumes)

	drafts := []*ProposalDraft{}
	if d := draftThresholdProposal(qualifying, volumes, oldThreshold, windowDays); d != nil {
		drafts = append(drafts, d)
	}
	for _, d := range draftKeywordProposals(qualifying, currentKeywords, windowDays) {
		drafts = append(drafts, d)
	}
	if len(drafts) > MaxProposalsPerRun {
		drafts = drafts[:MaxProposalsPerRun]
	}

	// 回放量化是只读阶段，保持在锁外（R64 P1：临界区只覆盖「去重检查+
	// 插入」，collect/backtest 不参与串行化）。
	for _, d := range drafts {
		if err := backtestDraft(ctx, pool, windowDays, d); err != nil {
			return nil, err
		}
		d.Evidence["operator"] = operator
	}

	inserted, err := insertDraftsGuarded(ctx, pool, drafts)
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

// tuningProposalLockKey 是 tuning proposal generate 去重临界区专用的
// pg_advisory_xact_lock 键（R64 P1）。Two-constant 形式 (classid, objid)
// 均为固定编译期常量：classid 取 ASCII 'TUNP'（Tuning-proposal-Normalize
// 路径首字母），objid=1 预留同类扩展位。任何其他代码不得复用该键值对。
const (
	tuningProposalLockClassID = int32(0x54554E50) // 'TUNP'
	tuningProposalLockObjID   = int32(1)
)

// insertDraftsGuarded inserts drafts with the pending-duplicate guard, with
// the whole check-then-insert loop serialized under one advisory transaction
// lock. R64 P1：tuning_proposals 无 (category, task_type, proposal->>key,
// status='pending') 业务唯一约束，原先 EXISTS 探针与 INSERT 分离于池上执行，
// 两个并发 generate（定时 + admin 手动）可各自通过探针再各插一份 pending。
// 现在单事务内先取 pg_advisory_xact_lock 再逐条 EXISTS→INSERT：锁在事务
// 结束时自动释放，冲突方串行到锁后重读探针即能看到先行者插入的行。
// 失败时整体回滚（部分插入不再残留）。
func insertDraftsGuarded(ctx context.Context, pool *pgxpool.Pool, drafts []*ProposalDraft) ([]ProposalDraft, error) {
	if len(drafts) == 0 {
		return nil, nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin proposal insert tx: %w", err)
	}
	//nolint:errcheck // deferred rollback, best-effort; no-op after Commit
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, $2)`,
		tuningProposalLockClassID, tuningProposalLockObjID); err != nil {
		return nil, fmt.Errorf("acquire proposal dedup advisory lock: %w", err)
	}

	inserted := []ProposalDraft{}
	for _, d := range drafts {
		ok, err := insertDraft(ctx, tx, d)
		if err != nil {
			return nil, err // deferred rollback discards the whole batch
		}
		if ok {
			inserted = append(inserted, *d)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit proposals: %w", err)
	}
	return inserted, nil
}

// backtestDraft runs the category-specific inline replay and merges the
// result into evidence.backtest.
func backtestDraft(ctx context.Context, pool *pgxpool.Pool, windowDays int, d *ProposalDraft) error {
	switch d.Category {
	case "threshold_change":
		newV, _ := d.Proposal["new"].(float64)
		oldV, _ := d.Proposal["old"].(float64)
		touch, fix, err := BacktestThresholdBand(ctx, pool, windowDays, newV, oldV)
		if err != nil {
			return err
		}
		d.Evidence["backtest"] = map[string]any{
			"would_fix_proxy": fix,
			"would_touch":     touch,
		}
	case "keyword_add":
		tokenRaw, _ := d.Proposal["add"].([]string)
		if len(tokenRaw) == 0 {
			return nil
		}
		matched, fixed, err := BacktestKeywordHint(ctx, pool, windowDays, d.TaskType, tokenRaw[0])
		if err != nil {
			return err
		}
		d.Evidence["backtest"] = map[string]any{
			"matched":           matched,
			"matched_corrected": fixed,
		}
	}
	return nil
}

// escapeLikePattern escapes SQL LIKE wildcards in a keyword token so the
// dedup probe matches the literal token only. R64 P3：`%`/`_` 是 LIKE 通配
// 符、`\` 是转义符，逐个前置 `\`；SQL 侧必须配 `ESCAPE '\'`。必须先转义
// `\` 本身（strings.NewReplacer 单趟替换，不会二次转义）。
func escapeLikePattern(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// insertDraft inserts one draft with a pending-duplicate guard. Returns
// false when a pending proposal for the same change already exists (advisory
// dedup, mirroring bg/feedback_analyzer's semantics — last writer wins is
// acceptable because proposals are advisory artifacts).
//
// Must run inside the caller's transaction while the tuningProposalLockKey
// advisory xact lock is held (see insertDraftsGuarded, R64 P1).
func insertDraft(ctx context.Context, tx pgx.Tx, d *ProposalDraft) (bool, error) {
	token := ""
	if add, ok := d.Proposal["add"].([]string); ok && len(add) > 0 {
		token = add[0]
	}
	key, _ := d.Proposal["key"].(string)
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS(
		    SELECT 1 FROM tuning_proposals
		    WHERE status = 'pending'
		      AND category = $1
		      AND COALESCE(task_type, '') = $2
		      AND proposal->>'key' = $3
		      AND ($4 = '' OR proposal->>'add' LIKE '%' || $4 || '%' ESCAPE '\')
		)
	`, d.Category, d.TaskType, key, escapeLikePattern(token)).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("dedup check: %w", err)
	}
	if exists {
		return false, nil
	}

	proposalJSON, _ := json.Marshal(d.Proposal)
	evidenceJSON, _ := json.Marshal(d.Evidence)
	// string() + ::text::jsonb（非 []byte）：pool 强制 SimpleProtocol 时
	// []byte 绑定为 bytea hex，任何 jsonb 转换都会 22P02（doc §3.2）。
	if _, err := tx.Exec(ctx, `
		INSERT INTO tuning_proposals (category, task_type, proposal, evidence, status)
		VALUES ($1, NULLIF($2, ''), $3::text::jsonb, $4::text::jsonb, 'pending')
	`, d.Category, d.TaskType, string(proposalJSON), string(evidenceJSON)); err != nil {
		return false, fmt.Errorf("insert proposal: %w", err)
	}
	return true, nil
}
