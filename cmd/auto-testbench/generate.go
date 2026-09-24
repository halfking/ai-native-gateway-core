package main

// generate.go — 套件候选样本生成器（"修正即测试"与分层抽样的落地通路）。
//
// v2 规划 §4.3 的两个扩充来源：
//   - source=corrections：人工修正回流。task_type_corrections 的
//     human_task_type 作为金标签（修正即造测试集），联结
//     auto_route_selections_all 的 658 结构化特征列。
//   - source=selections：按任务类型分层抽样 auto_route_selections_all，
//     label_source=auto（弱金标签，只作候选池）。
//
// 隐私红线（规划 §七.4）：只 SELECT 结构化特征列，绝不取 prompt 原文；
// 产出行的 prompt 是占位符，带 generated=true 标记——离线回归跳过这些行
// （候选文件不注册进 autoMatchingSuiteFiles），需人工脱敏复核后转正。
// 本工具对数据库只读（仅 Query）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// featureRow is the privacy-minimal projection shared by both sources
// (658 migration columns only — no prompt text). Columns from the LEFT
// JOINed selections side are nullable.
type featureRow struct {
	RequestID        string
	TaskType         string // corrections: auto_task_type; selections: task_type
	HumanTaskType    string // corrections only
	Classifier       *string
	Confidence       float64
	DetectedLanguage *string
	PromptLenBucket  *string
	ContextLenBucket *string
	HasCode          *bool
	HasMultimedia    *bool
	ComplexityBucket *string
	ContentHash      *string
}

// candidateFromRow maps one DB row to a suite-candidate JSONL row.
func candidateFromRow(src string, r featureRow) suiteCase {
	gold := r.HumanTaskType
	if gold == "" {
		gold = r.TaskType
	}
	note := "候选样本(label=" + srcLabel(src) + ")"
	if src == "corrections" && r.HumanTaskType != "" && r.TaskType != "" && r.HumanTaskType != r.TaskType {
		note = fmt.Sprintf("候选样本:人工改判 auto=%s -> human=%s", r.TaskType, r.HumanTaskType)
	}
	c := suiteCase{
		Name:         "gen_" + src + "_" + hash8(r.RequestID),
		Bucket:       "generated_" + src,
		ExpectedTask: gold,
		Generated:    true,
		LabelSource:  srcLabel(src),
		Prompt:       "[generated-candidate:replace-with-sanitized-repro]",
		ExpectNote:   note + ";人工脱敏复核并替换占位 prompt、移除 generated 标记后方可并入回归套件",
		SignalsNote:  describeFeatures(r),
	}
	// 可映射的结构化信号：多模态指示 → image。
	if r.HasMultimedia != nil && *r.HasMultimedia {
		c.Image = true
	}
	return c
}

func srcLabel(src string) string {
	switch src {
	case "corrections":
		return "human_correction"
	case "selections":
		return "auto_stratified"
	}
	return src
}

func describeFeatures(r featureRow) string {
	s := "classifier=" + defaultStr(derefStr(r.Classifier), "-")
	s += " conf=" + strconv.FormatFloat(r.Confidence, 'f', 2, 64)
	if r.DetectedLanguage != nil {
		s += " lang=" + *r.DetectedLanguage
	}
	if r.PromptLenBucket != nil {
		s += " len_bucket=" + *r.PromptLenBucket
	}
	if r.ContextLenBucket != nil {
		s += " ctx_bucket=" + *r.ContextLenBucket
	}
	if r.HasCode != nil {
		s += " code=" + b01(*r.HasCode)
	}
	if r.ComplexityBucket != nil {
		s += " cx=" + *r.ComplexityBucket
	}
	if r.ContentHash != nil {
		s += " content_hash=" + (*r.ContentHash)[:minInt(12, len(*r.ContentHash))]
	}
	return s
}

func defaultStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func b01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func hash8(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}

// runGenerate connects read-only and streams candidates to -out (or stdout).
func runGenerate(ctx context.Context, dsn, source, out string, days, limit int) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	var rows []featureRow
	switch source {
	case "corrections":
		rows, err = queryCorrectionFeatures(ctx, pool, days, limit)
	case "selections":
		rows, err = queryStratifiedSelections(ctx, pool, days, limit)
	default:
		return fmt.Errorf("unknown -source %q (want corrections|selections)", source)
	}
	if err != nil {
		return err
	}

	w := os.Stdout
	if out != "" {
		f, ferr := os.Create(out)
		if ferr != nil {
			return ferr
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	fmt.Fprintf(os.Stderr, "# generated candidates: source=%s rows=%d days=%d limit=%d — 隐私口径:仅结构化特征,无 prompt 原文\n",
		source, len(rows), days, limit)
	for _, r := range rows {
		if err := enc.Encode(candidateFromRow(source, r)); err != nil {
			return err
		}
	}
	return nil
}

const featureCols = `detected_language, prompt_length_bucket, context_length_bucket,
	has_code_indicator, has_multimedia_indicator, complexity_bucket, content_hash`

func queryCorrectionFeatures(ctx context.Context, pool *pgxpool.Pool, days, limit int) ([]featureRow, error) {
	q := `SELECT c.request_id, c.auto_task_type, c.human_task_type,
		s.classifier, COALESCE(s.confidence, 0), ` + featureCols + `
		FROM task_type_corrections c
		LEFT JOIN auto_route_selections_all s ON s.request_id = c.request_id
		WHERE c.created_at >= NOW() - make_interval(days => $1)
		ORDER BY c.created_at DESC
		LIMIT $2`
	rows, err := pool.Query(ctx, q, days, limit)
	if err != nil {
		return nil, fmt.Errorf("query corrections: %w", err)
	}
	defer rows.Close()

	var out []featureRow
	for rows.Next() {
		var r featureRow
		if err := rows.Scan(&r.RequestID, &r.TaskType, &r.HumanTaskType,
			&r.Classifier, &r.Confidence, &r.DetectedLanguage, &r.PromptLenBucket,
			&r.ContextLenBucket, &r.HasCode, &r.HasMultimedia, &r.ComplexityBucket,
			&r.ContentHash); err != nil {
			return nil, fmt.Errorf("scan corrections: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func queryStratifiedSelections(ctx context.Context, pool *pgxpool.Pool, days, limit int) ([]featureRow, error) {
	q := `SELECT request_id, task_type, '' AS human_task_type, classifier, confidence,
		` + featureCols + `
		FROM (
			SELECT request_id, task_type, classifier, confidence,
				detected_language, prompt_length_bucket, context_length_bucket,
				has_code_indicator, has_multimedia_indicator, complexity_bucket, content_hash,
				row_number() OVER (PARTITION BY task_type ORDER BY ts DESC) AS rn
			FROM auto_route_selections_all
			WHERE ts >= NOW() - make_interval(days => $1)
		) t
		WHERE rn <= $2
		ORDER BY task_type, rn`
	rows, err := pool.Query(ctx, q, days, limit)
	if err != nil {
		return nil, fmt.Errorf("query stratified selections: %w", err)
	}
	defer rows.Close()

	var out []featureRow
	for rows.Next() {
		var r featureRow
		if err := rows.Scan(&r.RequestID, &r.TaskType, &r.HumanTaskType,
			&r.Classifier, &r.Confidence, &r.DetectedLanguage, &r.PromptLenBucket,
			&r.ContextLenBucket, &r.HasCode, &r.HasMultimedia, &r.ComplexityBucket,
			&r.ContentHash); err != nil {
			return nil, fmt.Errorf("scan selections: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
