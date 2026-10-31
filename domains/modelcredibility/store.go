// store.go — 可信度判定的持久化与参考数据读取。
//
// store.go — persistence for credibility verdicts + loading of the reference
// data (peer samples, external baseline) that the verdict engine needs.
//
// # 表
//
//   - provider_model_credibility        当前判定（每个 凭据×模型 一行）
//   - provider_model_credibility_runs   判定历史（append-only）
//
// # 一条口径纪律：只用 status='success' 的智商实测
//
// model_iq_runs 里 status='partial' 的行表示「测了，但有请求出错」。
// 而 domains/modelquality 的准确率口径是
// `CorrectCount / TotalQuestions`（分母含出错题）⇒ partial 行的准确率
// 被系统性压低 ⇒ 拿它做同伴对比会把「当时网络抖了一下」误判成渗水。
//
// ⇒ 本包一律只取 status='success' 的行做能力对比。没有 success 行的节点
// 就是「能力未测量」，按缺失维度处理（不按 0 分算）。
package modelcredibility

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store 是判定结果的读写入口。
type Store struct {
	db *pgxpool.Pool
}

// NewStore 创建存储层。
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// DB 返回底层连接池（供上层复用）。
func (s *Store) DB() *pgxpool.Pool { return s.db }

// NodeMeasurement 是一个节点最近一次**成功**的能力实测。
type NodeMeasurement struct {
	CredentialID int64
	RawModelName string
	Accuracy     float64
	TestedAt     time.Time
}

// LatestAccuracy 取某节点最近一次成功实测的准确率。
//
// ok=false 表示没有可用实测（没测过 / 只测崩过）⇒ 能力维度按缺失处理。
func (s *Store) LatestAccuracy(ctx context.Context, credentialID int64, rawModelName string) (NodeMeasurement, bool, error) {
	if s.db == nil {
		return NodeMeasurement{}, false, fmt.Errorf("store: db pool is nil")
	}
	const q = `
		SELECT DISTINCT ON (credential_id, lower(raw_model_name))
		       credential_id, raw_model_name, accuracy::float8, tested_at
		FROM model_iq_runs
		WHERE credential_id = $1
		  AND lower(raw_model_name) = lower($2)
		  AND status = 'success'
		  AND accuracy IS NOT NULL
		ORDER BY credential_id, lower(raw_model_name), tested_at DESC
		LIMIT 1`
	var m NodeMeasurement
	err := s.db.QueryRow(ctx, q, credentialID, rawModelName).
		Scan(&m.CredentialID, &m.RawModelName, &m.Accuracy, &m.TestedAt)
	if err != nil {
		if isNoRows(err) {
			return NodeMeasurement{}, false, nil
		}
		return NodeMeasurement{}, false, fmt.Errorf("query latest accuracy: %w", err)
	}
	return m, true, nil
}

// LoadPeerReference 取同款 canonical 模型在各节点上的成功实测分布。
//
// ★ 返回的样本包含**被测节点自己**（比率因此恒 ≤ 1）。
func (s *Store) LoadPeerReference(ctx context.Context, canonicalID int64) (PeerReference, error) {
	if s.db == nil {
		return PeerReference{}, fmt.Errorf("store: db pool is nil")
	}
	if canonicalID <= 0 {
		return PeerReference{}, nil
	}
	const q = `
		WITH latest AS (
		    SELECT DISTINCT ON (credential_id, lower(raw_model_name))
		           credential_id, raw_model_name, accuracy::float8, tested_at
		    FROM model_iq_runs
		    WHERE status = 'success' AND accuracy IS NOT NULL
		    ORDER BY credential_id, lower(raw_model_name), tested_at DESC
		)
		SELECT l.credential_id,
		       c.provider_id,
		       COALESCE(p.display_name, p.code, ''),
		       l.raw_model_name,
		       l.accuracy,
		       l.tested_at
		FROM latest l
		JOIN credentials c ON c.id = l.credential_id
		JOIN provider_models pm
		  ON pm.provider_id = c.provider_id
		 AND lower(pm.raw_model_name) = lower(l.raw_model_name)
		JOIN providers p ON p.id = c.provider_id
		WHERE pm.canonical_id = $1`
	rows, err := s.db.Query(ctx, q, canonicalID)
	if err != nil {
		return PeerReference{}, fmt.Errorf("query peer reference: %w", err)
	}
	defer rows.Close()
	ref := PeerReference{CanonicalID: canonicalID}
	for rows.Next() {
		var s2 PeerSample
		if err := rows.Scan(&s2.CredentialID, &s2.ProviderID, &s2.ProviderName,
			&s2.RawModelName, &s2.Accuracy, &s2.TestedAt); err != nil {
			return PeerReference{}, fmt.Errorf("scan peer reference: %w", err)
		}
		ref.Samples = append(ref.Samples, s2)
	}
	if err := rows.Err(); err != nil {
		return PeerReference{}, fmt.Errorf("iterate peer reference: %w", err)
	}
	return ref, nil
}

// LoadCalibrationPoints 取校准拟合所需的点集。
//
// 每个 canonical 模型一个点：标准智商 + 该模型各节点成功实测准确率的中位数。
func (s *Store) LoadCalibrationPoints(ctx context.Context) ([]CalibrationPoint, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: db pool is nil")
	}
	const q = `
		WITH latest AS (
		    SELECT DISTINCT ON (credential_id, lower(raw_model_name))
		           credential_id, raw_model_name, accuracy::float8, tested_at
		    FROM model_iq_runs
		    WHERE status = 'success' AND accuracy IS NOT NULL
		    ORDER BY credential_id, lower(raw_model_name), tested_at DESC
		),
		joined AS (
		    SELECT mc.id AS canonical_id, mc.canonical_name, mc.standard_iq::float8,
		           l.credential_id, l.accuracy, c.provider_id
		    FROM latest l
		    JOIN credentials c ON c.id = l.credential_id
		    JOIN provider_models pm
		      ON pm.provider_id = c.provider_id
		     AND lower(pm.raw_model_name) = lower(l.raw_model_name)
		    JOIN models_canonical mc ON mc.id = pm.canonical_id
		    WHERE mc.standard_iq IS NOT NULL AND mc.standard_iq > 0
		)
		SELECT canonical_id, canonical_name, standard_iq,
		       percentile_disc(0.5) WITHIN GROUP (ORDER BY accuracy) AS median_accuracy,
		       count(*) AS sample_count,
		       array_agg(DISTINCT provider_id) AS provider_ids
		FROM joined
		GROUP BY canonical_id, canonical_name, standard_iq`
	rows, err := s.db.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query calibration points: %w", err)
	}
	defer rows.Close()
	var pts []CalibrationPoint
	for rows.Next() {
		var p CalibrationPoint
		var providerIDs []int64
		if err := rows.Scan(&p.CanonicalID, &p.CanonicalName, &p.StandardIQ,
			&p.MedianAccuracy, &p.SampleCount, &providerIDs); err != nil {
			return nil, fmt.Errorf("scan calibration point: %w", err)
		}
		p.ProviderIDs = providerIDs
		pts = append(pts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate calibration points: %w", err)
	}
	return pts, nil
}

// StandardIQ 取某 canonical 模型的标准智商（0 表示无外部基线）。
func (s *Store) StandardIQ(ctx context.Context, canonicalID int64) (float64, error) {
	if s.db == nil || canonicalID <= 0 {
		return 0, nil
	}
	var iq *float64
	err := s.db.QueryRow(ctx, `SELECT standard_iq::float8 FROM models_canonical WHERE id = $1`, canonicalID).Scan(&iq)
	if err != nil {
		if isNoRows(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("query standard iq: %w", err)
	}
	if iq == nil {
		return 0, nil
	}
	return *iq, nil
}

// LoadPrevious 取该节点上一次判定的签名基线（nil = 首次测量）。
//
// ★ 顺带带出上一次的 verdict 与 verified 连击数 —— verified 的连续共识
// 需要它们（bg/model_probe RequiredConsensus 同思路）。
func (s *Store) LoadPrevious(ctx context.Context, credentialID int64, rawModelName string) (*Previous, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: db pool is nil")
	}
	const q = `
		SELECT prompt_token_signature, system_fingerprint, reported_model, measured_at,
		       COALESCE(verdict,''), COALESCE((evidence->>'consensus_streak')::int, 0)
		FROM provider_model_credibility
		WHERE tenant_id = 'default'
		  AND credential_id = $1
		  AND lower(raw_model_name) = lower($2)`
	var sig *int
	var fp, model *string
	var measuredAt *time.Time
	var prevVerdict string
	var streak int
	err := s.db.QueryRow(ctx, q, credentialID, rawModelName).
		Scan(&sig, &fp, &model, &measuredAt, &prevVerdict, &streak)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("query previous verdict: %w", err)
	}
	prev := &Previous{Verdict: Verdict(prevVerdict), VerifiedStreak: streak}
	if sig != nil {
		prev.PromptTokens = *sig
		prev.HasPromptTokens = true
	}
	if fp != nil {
		prev.SystemFingerprint = *fp
	}
	if model != nil {
		prev.ReportedModel = *model
	}
	if measuredAt != nil {
		prev.MeasuredAt = *measuredAt
	}
	return prev, nil
}

// LoadPassiveSignals 读该节点的被动完整性信号（真实流量侧证据）。
//
// 数据来源：model_integrity_events（domains/streaming/integrity 落），
// 未解决 + 近 7 天 + severity high/critical，按 (credential_id, 模型名) 匹配。
// 取 model_mismatch 与 fingerprint_drift 各自最新一条。
//
// ★ 查询失败（表权限不足 / 表不存在）**降级为 nil** 并返回 err —— 调用方
// 决定是否致命。211 实测网关连库角色对该表无读权限 ⇒ 生产上这条通道
// 可能整段降级，主动探测照常工作（scanner 打 WARN）。
// ★ 找不到事件返回 (&PassiveSignals{}, nil)：空结构 = 「读了，没有信号」，
// 与「没读到」区分（evidence 里可如实标注）。
func (s *Store) LoadPassiveSignals(ctx context.Context, credentialID int64, rawModelName string) (*PassiveSignals, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: db pool is nil")
	}
	const q = `
		SELECT DISTINCT ON (anomaly_type)
		       anomaly_type, COALESCE(expected_value,''), COALESCE(actual_value,'')
		FROM model_integrity_events
		WHERE resolved = FALSE
		  AND severity IN ('high','critical')
		  AND ts > now() - interval '7 days'
		  AND anomaly_type IN ('model_mismatch','fingerprint_drift')
		  AND credential_id = $1
		  AND (lower(COALESCE(raw_model_name,'')) = lower($2)
		       OR lower(COALESCE(client_model,'')) = lower($2))
		ORDER BY anomaly_type, ts DESC`
	rows, err := s.db.Query(ctx, q, credentialID, rawModelName)
	if err != nil {
		return nil, fmt.Errorf("query passive signals: %w", err)
	}
	defer rows.Close()
	out := &PassiveSignals{}
	for rows.Next() {
		var atype, expected, actual string
		if err := rows.Scan(&atype, &expected, &actual); err != nil {
			return nil, fmt.Errorf("scan passive signals: %w", err)
		}
		switch atype {
		case "model_mismatch":
			out.HasMismatch = true
			out.MismatchExpected = expected
			out.MismatchActual = actual
		case "fingerprint_drift":
			out.HasFingerprintDrift = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate passive signals: %w", err)
	}
	return out, nil
}

// SaveResult 落盘一次判定：更新当前态 + 追加一条历史。
//
// ★ 两张表在**同一个事务**里写：当前态与历史必须同时成功或同时失败，
// 否则会出现「历史里有、当前态没有」或反之的不一致。
func (s *Store) SaveResult(ctx context.Context, res Result) error {
	if s.db == nil {
		return fmt.Errorf("store: db pool is nil")
	}
	evidence, err := json.Marshal(res.Evidence)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}
	anomalies, err := json.Marshal(res.Anomalies)
	if err != nil {
		return fmt.Errorf("marshal anomalies: %w", err)
	}
	tenant := res.TenantID
	if tenant == "" {
		tenant = "default"
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin credibility tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const insertCurrent = `
		INSERT INTO provider_model_credibility AS c (
		    tenant_id, credential_id, provider_id, raw_model_name, canonical_id, canonical_name,
		    credibility_score, identity_score, capability_score, consistency_score, reference_score,
		    verdict, status, reported_model, prompt_token_signature, system_fingerprint,
		    evidence, anomalies, measured_dimensions, measured_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,now(),now())
		ON CONFLICT (tenant_id, credential_id, raw_model_name) DO UPDATE SET
		    provider_id           = EXCLUDED.provider_id,
		    canonical_id          = EXCLUDED.canonical_id,
		    canonical_name        = EXCLUDED.canonical_name,
		    credibility_score     = EXCLUDED.credibility_score,
		    identity_score        = EXCLUDED.identity_score,
		    capability_score      = EXCLUDED.capability_score,
		    consistency_score     = EXCLUDED.consistency_score,
		    reference_score       = EXCLUDED.reference_score,
		    verdict               = EXCLUDED.verdict,
		    status                = EXCLUDED.status,
		    -- ★ 失败探测不覆盖指纹基线：空值写进去会让下一轮把
		    -- 「这次没测到」读成「指纹变了」。
		    reported_model        = COALESCE(EXCLUDED.reported_model, c.reported_model),
		    prompt_token_signature= COALESCE(EXCLUDED.prompt_token_signature, c.prompt_token_signature),
		    system_fingerprint    = COALESCE(EXCLUDED.system_fingerprint, c.system_fingerprint),
		    evidence              = EXCLUDED.evidence,
		    anomalies             = EXCLUDED.anomalies,
		    measured_dimensions   = EXCLUDED.measured_dimensions,
		    measured_at           = EXCLUDED.measured_at,
		    updated_at            = now()`

	var sig any
	if res.Observed.HasUsage {
		sig = res.Observed.PromptTokens
	}
	var fp any
	if res.Observed.SystemFingerprint != "" {
		fp = res.Observed.SystemFingerprint
	}
	var reported any
	if res.Observed.ReportedModel != "" {
		reported = res.Observed.ReportedModel
	}
	if _, err := tx.Exec(ctx, insertCurrent,
		tenant, res.CredentialID, res.ProviderID, res.RawModelName,
		nullableID(res.CanonicalID), nullableStr(res.CanonicalName),
		scoreOrNull(res.Scores, 0, res.Overall),
		scoreOrNull(res.Scores, DimIdentity, res.Scores.Identity),
		scoreOrNull(res.Scores, DimCapability, res.Scores.Capability),
		scoreOrNull(res.Scores, DimConsistency, res.Scores.Consistency),
		scoreOrNull(res.Scores, DimReference, res.Scores.Reference),
		string(res.Verdict), res.Status,
		reported, sig, fp,
		jsonTextParam(evidence), jsonTextParam(anomalies),
		res.Scores.Measured.DimensionCount(), res.MeasuredAt,
	); err != nil {
		return fmt.Errorf("upsert provider_model_credibility: %w", err)
	}

	const insertRun = `
		INSERT INTO provider_model_credibility_runs (
		    tenant_id, credential_id, provider_id, raw_model_name, canonical_id, canonical_name,
		    credibility_score, identity_score, capability_score, consistency_score, reference_score,
		    verdict, status, measured_dimensions, reported_model, prompt_token_signature,
		    system_fingerprint, http_status, latency_ms, evidence, anomalies, error, measured_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`
	var runErr any
	if res.Status == StatusFailed {
		runErr = "probe_failed"
	}
	if _, err := tx.Exec(ctx, insertRun,
		tenant, res.CredentialID, res.ProviderID, res.RawModelName,
		nullableID(res.CanonicalID), nullableStr(res.CanonicalName),
		scoreOrNull(res.Scores, 0, res.Overall),
		scoreOrNull(res.Scores, DimIdentity, res.Scores.Identity),
		scoreOrNull(res.Scores, DimCapability, res.Scores.Capability),
		scoreOrNull(res.Scores, DimConsistency, res.Scores.Consistency),
		scoreOrNull(res.Scores, DimReference, res.Scores.Reference),
		string(res.Verdict), res.Status,
		res.Scores.Measured.DimensionCount(),
		reported, sig, fp,
		res.Observed.HTTPStatus, res.Observed.LatencyMs,
		jsonTextParam(evidence), jsonTextParam(anomalies), runErr, res.MeasuredAt,
	); err != nil {
		return fmt.Errorf("insert provider_model_credibility_runs: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit credibility tx: %w", err)
	}
	return nil
}

// VerdictRow 是一行判定（admin API 的列表视图）。
type VerdictRow struct {
	TenantID          string   `json:"tenant_id"`
	CredentialID      int64    `json:"credential_id"`
	CredentialLabel   string   `json:"credential_label"`
	ProviderID        int64    `json:"provider_id"`
	ProviderName      string   `json:"provider_name"`
	RawModelName      string   `json:"raw_model_name"`
	CanonicalID       int64    `json:"canonical_id"`
	CanonicalName     string   `json:"canonical_name"`
	CredibilityScore  *float64 `json:"credibility_score"`
	IdentityScore     *float64 `json:"identity_score"`
	CapabilityScore   *float64 `json:"capability_score"`
	ConsistencyScore  *float64 `json:"consistency_score"`
	ReferenceScore    *float64 `json:"reference_score"`
	Verdict           string   `json:"verdict"`
	Status            string   `json:"status"`
	ReportedModel     string   `json:"reported_model"`
	SystemFingerprint string   `json:"system_fingerprint"`
	Anomalies         []string `json:"anomalies"`
	MeasuredDims      int      `json:"measured_dimensions"`
	MeasuredAt        *string  `json:"measured_at"`
}

// VerdictFilter 是列表查询的过滤条件。
type VerdictFilter struct {
	ProviderID   int64
	CredentialID int64
	CanonicalID  int64
	Verdict      string
	Limit        int
}

// ListVerdicts 列出判定结果。
func (s *Store) ListVerdicts(ctx context.Context, f VerdictFilter) ([]VerdictRow, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: db pool is nil")
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	args := []any{}
	where := "WHERE 1=1"
	if f.ProviderID > 0 {
		args = append(args, f.ProviderID)
		where += fmt.Sprintf(" AND c.provider_id = $%d", len(args))
	}
	if f.CredentialID > 0 {
		args = append(args, f.CredentialID)
		where += fmt.Sprintf(" AND c.credential_id = $%d", len(args))
	}
	if f.CanonicalID > 0 {
		args = append(args, f.CanonicalID)
		where += fmt.Sprintf(" AND c.canonical_id = $%d", len(args))
	}
	if f.Verdict != "" {
		args = append(args, f.Verdict)
		where += fmt.Sprintf(" AND c.verdict = $%d", len(args))
	}
	args = append(args, limit)

	q := `
		SELECT c.tenant_id, c.credential_id, COALESCE(cred.label,''), c.provider_id,
		       COALESCE(p.display_name, p.code,''), c.raw_model_name,
		       COALESCE(c.canonical_id,0), COALESCE(c.canonical_name,''),
		       c.credibility_score::float8,
		       c.identity_score::float8,
		       c.capability_score::float8,
		       c.consistency_score::float8,
		       c.reference_score::float8,
		       c.verdict, COALESCE(c.status,''),
		       COALESCE(c.reported_model,''), COALESCE(c.system_fingerprint,''),
		       COALESCE(c.anomalies,'[]'::jsonb), COALESCE(c.measured_dimensions,0),
		       to_char(c.measured_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM provider_model_credibility c
		LEFT JOIN credentials cred ON cred.id = c.credential_id
		LEFT JOIN providers p ON p.id = c.provider_id
		` + where + `
		ORDER BY c.credibility_score ASC NULLS LAST, c.measured_at DESC NULLS LAST
		LIMIT $` + fmt.Sprint(len(args))

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query verdicts: %w", err)
	}
	defer rows.Close()
	out := []VerdictRow{}
	for rows.Next() {
		var row VerdictRow
		var anomaliesRaw []byte
		var measuredAt string
		if err := rows.Scan(&row.TenantID, &row.CredentialID, &row.CredentialLabel, &row.ProviderID,
			&row.ProviderName, &row.RawModelName, &row.CanonicalID, &row.CanonicalName,
			&row.CredibilityScore, &row.IdentityScore, &row.CapabilityScore,
			&row.ConsistencyScore, &row.ReferenceScore,
			&row.Verdict, &row.Status, &row.ReportedModel, &row.SystemFingerprint,
			&anomaliesRaw, &row.MeasuredDims, &measuredAt); err != nil {
			return nil, fmt.Errorf("scan verdict row: %w", err)
		}
		_ = json.Unmarshal(anomaliesRaw, &row.Anomalies)
		if measuredAt != "" {
			row.MeasuredAt = &measuredAt
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verdicts: %w", err)
	}
	return out, nil
}

// RunRow 是一次判定历史。
type RunRow struct {
	MeasuredAt       string   `json:"measured_at"`
	CredibilityScore float64  `json:"credibility_score"`
	IdentityScore    float64  `json:"identity_score"`
	CapabilityScore  float64  `json:"capability_score"`
	ConsistencyScore float64  `json:"consistency_score"`
	ReferenceScore   float64  `json:"reference_score"`
	Verdict          string   `json:"verdict"`
	Status           string   `json:"status"`
	ReportedModel    string   `json:"reported_model"`
	Anomalies        []string `json:"anomalies"`
	MeasuredDims     int      `json:"measured_dimensions"`
}

// ListRuns 列出某节点的判定历史。
func (s *Store) ListRuns(ctx context.Context, credentialID int64, rawModelName string, limit int) ([]RunRow, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: db pool is nil")
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	const q = `
		SELECT to_char(measured_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       COALESCE(credibility_score,0)::float8,
		       COALESCE(identity_score,0)::float8,
		       COALESCE(capability_score,0)::float8,
		       COALESCE(consistency_score,0)::float8,
		       COALESCE(reference_score,0)::float8,
		       COALESCE(verdict,''), COALESCE(status,''),
		       COALESCE(reported_model,''),
		       COALESCE(anomalies,'[]'::jsonb),
		       COALESCE(measured_dimensions,0)
		FROM provider_model_credibility_runs
		WHERE tenant_id = 'default'
		  AND credential_id = $1
		  AND lower(raw_model_name) = lower($2)
		ORDER BY measured_at DESC
		LIMIT $3`
	rows, err := s.db.Query(ctx, q, credentialID, rawModelName, limit)
	if err != nil {
		return nil, fmt.Errorf("query credibility runs: %w", err)
	}
	defer rows.Close()
	out := []RunRow{}
	for rows.Next() {
		var row RunRow
		var anomaliesRaw []byte
		if err := rows.Scan(&row.MeasuredAt, &row.CredibilityScore, &row.IdentityScore,
			&row.CapabilityScore, &row.ConsistencyScore, &row.ReferenceScore,
			&row.Verdict, &row.Status, &row.ReportedModel,
			&anomaliesRaw, &row.MeasuredDims); err != nil {
			return nil, fmt.Errorf("scan credibility run: %w", err)
		}
		_ = json.Unmarshal(anomaliesRaw, &row.Anomalies)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate credibility runs: %w", err)
	}
	return out, nil
}

// WriteProviderQualityProfiles 把供应商×模型粒度的可信度汇总写到
// provider_quality_profiles 的 model_credibility_* 三列。
//
// ★ targets 为 nil 时写全量（旧行为）；传 targets 时只写本轮扫到的
// (provider, model) —— 防止已下线模型的历史残留行被误写。
//
// ★ 为什么写这三列而不是 reliability_score：
// reliability_score 在仓内已被 internal/quality/scorer_reliability.go 占用，
// 含义是「服务可靠性（在线率/一致性/趋势）」⇒ 与「模型是否被偷换」不是
// 一件事 ⇒ 一列两义会让读方无法分辨 ⇒ 另起三列。
//
// ★ 取「最差」而不是平均：掺水是「这家供应商在卖这个模型时至少有一条线
// 是假的」⇒ 平均会把掺水的那条线稀释掉（3 条好线 + 1 条渗水线 ⇒ 平均分
// 仍很高 ⇒ 看板上这家仍然「正常」）。
func (s *Store) WriteProviderQualityProfiles(ctx context.Context, targets []NodeTarget) (int, error) {
	if s.db == nil {
		return 0, fmt.Errorf("store: db pool is nil")
	}
	var rows []ProviderModelScore
	var err error
	if targets == nil {
		rows, err = s.RollupProviderModel(ctx)
	} else {
		rows, err = s.RollupProviderModelFiltered(ctx, targets)
	}
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	const upsert = `
		INSERT INTO provider_quality_profiles
		    (provider_id, model_name, model_credibility_score, model_credibility_verdict,
		     model_credibility_sample_count, calculated_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,now(),now())
		ON CONFLICT (provider_id, model_name) DO UPDATE SET
		    model_credibility_score       = EXCLUDED.model_credibility_score,
		    model_credibility_verdict     = EXCLUDED.model_credibility_verdict,
		    model_credibility_sample_count= EXCLUDED.model_credibility_sample_count,
		    calculated_at                 = EXCLUDED.calculated_at,
		    updated_at                    = EXCLUDED.updated_at`
	written := 0
	for _, r := range rows {
		if r.ProviderID <= 0 || r.RawModelName == "" {
			continue
		}
		// model_name 列宽 100，超长截断（避免整条写入失败）。
		name := r.RawModelName
		if len(name) > 100 {
			name = name[:100]
		}
		if _, err := s.db.Exec(ctx, upsert, r.ProviderID, name,
			round2(r.Credibility), r.Verdict, r.SampleCount); err != nil {
			return written, fmt.Errorf("upsert provider_quality_profiles (provider=%d model=%s): %w",
				r.ProviderID, name, err)
		}
		written++
	}
	return written, nil
}

// ProviderModelScore 是供应商×模型粒度的可信度汇总（写 provider_quality_profiles 用）。
type ProviderModelScore struct {
	ProviderID   int64
	ModelName    string
	RawModelName string
	Credibility  float64
	SampleCount  int
	Verdict      string
}

// RollupProviderModel 把凭据级判定汇总到供应商×模型粒度。
//
// ★ 取「该供应商该模型下**最差**的凭据」而不是平均：
// 掺水是「这家供应商在卖这个模型时至少有一条线是假的」——
// 平均会把掺水的那条线稀释掉（3 条好线 + 1 条掺水线 ⇒ 平均看不出问题）。
// 但也要给「样本数」与「结论」一起写下去，让消费方知道这是最差值。
func (s *Store) RollupProviderModel(ctx context.Context) ([]ProviderModelScore, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: db pool is nil")
	}
	const q = `
		SELECT provider_id, raw_model_name,
		       MIN(credibility_score)::float8,
		       COUNT(*),
		       MIN(verdict)
		FROM provider_model_credibility
		WHERE tenant_id = 'default' AND credibility_score IS NOT NULL
		GROUP BY provider_id, raw_model_name`
	rows, err := s.db.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query provider model rollup: %w", err)
	}
	defer rows.Close()
	out := []ProviderModelScore{}
	for rows.Next() {
		var row ProviderModelScore
		if err := rows.Scan(&row.ProviderID, &row.RawModelName, &row.Credibility,
			&row.SampleCount, &row.Verdict); err != nil {
			return nil, fmt.Errorf("scan provider model rollup: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider model rollup: %w", err)
	}
	return out, nil
}

// RollupProviderModelFiltered 按**本轮扫到的目标**汇总到供应商×模型粒度。
//
// ★ 与 RollupProviderModel 的区别：只统计 targets 里出现过的
// (provider_id, raw_model_name)。没有这个过滤，provider_quality_profiles 会
// 被历史残留行（模型已下线 / 已移出特色清单）覆盖出与现状无关的行。
//
// ★ duplicate 保留：同一 provider+model 有多个凭据时都进结果，
// 由 upsert 的 ON CONFLICT 收敛到最后一行。
func (s *Store) RollupProviderModelFiltered(ctx context.Context, targets []NodeTarget) ([]ProviderModelScore, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: db pool is nil")
	}
	if len(targets) == 0 {
		return nil, nil
	}
	all, err := s.RollupProviderModel(ctx)
	if err != nil {
		return nil, err
	}
	want := make(map[rollupKey]struct{}, len(targets))
	for _, t := range targets {
		if t.RawModelName == "" {
			continue
		}
		want[rollupKey{t.ProviderID, strings.ToLower(t.RawModelName)}] = struct{}{}
	}
	return filterRollup(all, want), nil
}

// rollupKey 是 (provider_id, 归一化模型名) 的过滤键。
type rollupKey struct {
	p int64
	m string
}

// filterRollup 是纯过滤函数：只保留 want 里的 (provider, model)。
// 抽出来是为了直接单测（不碰 DB）。
func filterRollup(all []ProviderModelScore, want map[rollupKey]struct{}) []ProviderModelScore {
	out := make([]ProviderModelScore, 0, len(want))
	for _, r := range all {
		if _, ok := want[rollupKey{r.ProviderID, strings.ToLower(r.RawModelName)}]; ok {
			out = append(out, r)
		}
	}
	return out
}

// scoreOrNull 把「没测到的维度」写成 SQL NULL，而不是 0。
//
// ★ dim=0 表示综合分：一个维度都没测到时综合分的 0 是「没测过」，
// 不是「测了但极差」。写成 0 会让列表按 credibility_score ASC 把
// 「没测」排到「真渗水」前面，运维先看到的是没数据的行。
func scoreOrNull(s SubScores, dim Dimension, score float64) any {
	if dim == 0 {
		if s.Measured.DimensionCount() == 0 {
			return nil
		}
		return round2(score)
	}
	if !s.Measured.Has(dim) {
		return nil
	}
	return round2(score)
}

// nullableID 把 0 转成 SQL NULL。
func nullableID(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// nullableStr 把空串转成 SQL NULL。
func nullableStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// jsonTextParam 把 JSON 字节转成 string 再交给 pgx。
//
// ★ 裸 []byte 在 SimpleProtocol 下被内联为 bytea hex 字面量 ⇒ jsonb 解析
// 必炸（SQLSTATE 22P02）。211 实测 2026-10-30：trigger 落库即炸
// （"invalid input syntax for type json"）。与 bg/capability_backfill.go
// 的 capabilityEvidenceParam 同坑同修（仓内 R24 记录过 6 次真库失败）。
func jsonTextParam(b []byte) interface{} {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
